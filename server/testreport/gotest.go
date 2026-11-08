// Copyright 2026 Woodpecker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package testreport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// PackageTestName names the result recorded for a Go package that failed
// without a failing test, e.g. because it did not build or TestMain panicked.
const PackageTestName = "(package)"

// gotestEvent is one line of `go test -json` (see `go doc test2json`).
type gotestEvent struct {
	Action      string  `json:"Action"`
	Package     string  `json:"Package"`
	ImportPath  string  `json:"ImportPath"`
	Test        string  `json:"Test"`
	Elapsed     float64 `json:"Elapsed"`
	Output      string  `json:"Output"`
	FailedBuild string  `json:"FailedBuild"`
}

type gotestKey struct {
	pkg, test string
}

type gotestState struct {
	order    int
	action   string
	elapsed  float64
	output   strings.Builder
	finished bool
}

// gotestOutputMax bounds the output kept per test while reading, before the
// message is truncated to model.TestMessageMax.
const gotestOutputMax = 4 * model.TestMessageMax

// framing lines test2json repeats in the output events, which say nothing a
// result does not
var gotestFraming = regexp.MustCompile(`^(\s*=== (RUN|PAUSE|CONT|NAME)\s.*|\s*--- (PASS|FAIL|SKIP): .*|PASS|FAIL|(ok|FAIL)\s+\S+\s+(\d+(\.\d+)?s|\(cached\)).*|\s*)$`)

// gotestFileLine finds the file:line a failing test's output points at.
var gotestFileLine = regexp.MustCompile(`(?m)^\s+([\w.\-]+_test\.go):(\d+):`)

// ParseGoTest reads a `go test -json` event stream: one result per test and
// subtest (TestX/sub) with its pass, fail or skip action and elapsed time, the
// output of failed and skipped tests as their message, and the package import
// path as their suite. Lines that are not JSON events are skipped. A package
// that failed without a failing test (a build failure, a panic in TestMain)
// becomes an error result named PackageTestName; tests that started but never
// ended become errors too.
func ParseGoTest(data []byte) ([]*Result, error) {
	var (
		tests      = map[gotestKey]*gotestState{}
		packages   = map[string]*gotestState{}
		buildOut   = map[string]*strings.Builder{}
		badLines   int
		eventCount int
	)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev gotestEvent
		if line[0] != '{' || json.Unmarshal(line, &ev) != nil || ev.Action == "" {
			badLines++
			continue
		}
		eventCount++

		if ev.Package == "" {
			// build events (go 1.24+) name the package being built
			if ev.ImportPath != "" && ev.Action == "build-output" {
				pkg, _, _ := strings.Cut(ev.ImportPath, " ")
				b := buildOut[pkg]
				if b == nil {
					b = &strings.Builder{}
					buildOut[pkg] = b
				}
				appendBounded(b, ev.Output)
			}
			continue
		}

		var state *gotestState
		if ev.Test == "" {
			state = packages[ev.Package]
			if state == nil {
				state = &gotestState{order: len(packages)}
				packages[ev.Package] = state
			}
		} else {
			key := gotestKey{ev.Package, ev.Test}
			state = tests[key]
			if state == nil {
				state = &gotestState{order: len(tests)}
				tests[key] = state
			}
		}

		switch ev.Action {
		case "output":
			appendBounded(&state.output, ev.Output)
		case "pass", "fail", "skip":
			state.action = ev.Action
			state.elapsed = ev.Elapsed
			state.finished = true
		case "start", "run", "pause", "cont", "bench":
			// nothing to keep
		}
	}
	var err error
	if scanErr := scanner.Err(); scanErr != nil {
		err = fmt.Errorf("reading go test -json output: %w", scanErr)
	} else if eventCount == 0 && badLines > 0 {
		err = fmt.Errorf("no go test -json events in %d lines", badLines)
	}

	// tests in package then start order
	keys := make([]gotestKey, 0, len(tests))
	for key := range tests {
		keys = append(keys, key)
	}
	sortKeys(keys, tests, packages)

	failedTests := map[string]bool{}
	results := make([]*Result, 0, len(keys)+1)
	for _, key := range keys {
		state := tests[key]
		r := &Result{
			Suite:    key.pkg,
			Name:     key.test,
			Package:  key.pkg,
			Duration: seconds(state.elapsed),
		}
		pkg := packages[key.pkg]
		switch {
		case !state.finished:
			r.Status = model.TestStatusError
			reason := "the test did not finish: the report ends before its result"
			if pkg != nil && pkg.finished {
				reason = "the test did not finish: its package failed (a panic or a timeout?)"
			}
			r.Message = reason + "\n\n" + cleanGoOutput(state.output.String())
		case state.action == "pass":
			r.Status = model.TestStatusPass
		case state.action == "skip":
			r.Status = model.TestStatusSkip
			r.Message = cleanGoOutput(state.output.String())
		default:
			r.Status = model.TestStatusFail
			r.Message = cleanGoOutput(state.output.String())
		}
		if r.Status.Failed() {
			failedTests[key.pkg] = true
			if m := gotestFileLine.FindStringSubmatch(state.output.String()); m != nil {
				r.File = m[1]
			}
		}
		r.Message = truncateMessage(r.Message)
		results = append(results, r)
	}

	pkgs := make([]string, 0, len(packages))
	for pkg := range packages {
		pkgs = append(pkgs, pkg)
	}
	sortByOrder(pkgs, packages)
	for _, pkg := range pkgs {
		state := packages[pkg]
		if state.action != "fail" || failedTests[pkg] {
			continue
		}
		msg := state.output.String()
		if b := buildOut[pkg]; b != nil {
			msg = b.String() + msg
		}
		results = append(results, &Result{
			Suite:    pkg,
			Name:     PackageTestName,
			Package:  pkg,
			Status:   model.TestStatusError,
			Duration: seconds(state.elapsed),
			Message:  truncateMessage(cleanGoOutput(msg)),
		})
	}
	return results, err
}

func appendBounded(b *strings.Builder, s string) {
	if b.Len()+len(s) > gotestOutputMax {
		// keep the end, where failures are; strings.Builder cannot drop its
		// start, so rebuild it
		all := b.String() + s
		all = startUTF8(all[len(all)-gotestOutputMax/2:])
		b.Reset()
		b.WriteString(all)
		return
	}
	b.WriteString(s)
}

func cleanGoOutput(out string) string {
	lines := strings.Split(out, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if gotestFraming.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func seconds(s float64) time.Duration {
	if s <= 0 || s > 1e7 {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}

func sortKeys(keys []gotestKey, tests map[gotestKey]*gotestState, packages map[string]*gotestState) {
	pkgOrder := func(pkg string) int {
		if p := packages[pkg]; p != nil {
			return p.order
		}
		return len(packages)
	}
	sortSlice(keys, func(a, b gotestKey) bool {
		if pa, pb := pkgOrder(a.pkg), pkgOrder(b.pkg); pa != pb {
			return pa < pb
		}
		if a.pkg != b.pkg {
			return a.pkg < b.pkg
		}
		return tests[a].order < tests[b].order
	})
}

func sortByOrder(pkgs []string, packages map[string]*gotestState) {
	sortSlice(pkgs, func(a, b string) bool { return packages[a].order < packages[b].order })
}
