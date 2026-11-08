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

// Package testreport parses the test reports pipeline steps declare (JUnit
// XML and `go test -json` streams), stores their results as test cases and
// runs, and shapes them for the API.
package testreport

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// ReportFile is one file a step's `reports:` collected: its path relative to
// the workspace and its content.
type ReportFile struct {
	Name string
	Data []byte
}

// Result is one test result from a report.
type Result struct {
	// Suite is the JUnit class name (or the enclosing suite's name) or, for
	// `go test -json`, the package import path.
	Suite string
	Name  string
	// File and Line locate the test when the report says so; File is as the
	// report gives it (usually relative to the workspace).
	File string
	Line int
	// Package is the Go import path of the test's package, when known.
	Package  string
	Status   model.TestStatus
	Duration time.Duration
	Message  string
}

// Key identifies the test within a repo.
func (r *Result) Key() string {
	if r.Suite == "" {
		return r.Name
	}
	return r.Suite + "::" + r.Name
}

// GoModule is a go.mod file collected with the reports.
type GoModule struct {
	// Dir is the module's directory relative to the workspace, "" for the root.
	Dir  string
	Path string
}

// Report is what the files of one step contained.
type Report struct {
	Results []*Result
	Modules []GoModule
	// Warnings describe files or parts of files that could not be read.
	Warnings []string
}

// Format is a test report format.
type Format string

const (
	FormatUnknown Format = ""
	FormatJUnit   Format = "junit"
	FormatGoTest  Format = "go-test-json"
	FormatGoMod   Format = "go.mod"
)

// Detect guesses the format of a report file from its name and content.
func Detect(name string, data []byte) Format {
	if path.Base(name) == "go.mod" {
		return FormatGoMod
	}
	trimmed := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	trimmed = bytes.TrimLeft(trimmed, " \t\r\n")
	switch {
	case len(trimmed) == 0:
		return FormatUnknown
	case trimmed[0] == '<':
		if bytes.Contains(data, []byte("<testsuite")) || bytes.Contains(data, []byte("<testcase")) {
			return FormatJUnit
		}
	case trimmed[0] == '{':
		if bytes.Contains(data, []byte(`"Action"`)) {
			return FormatGoTest
		}
	}
	return FormatUnknown
}

// Parse reads the report files of a step. Files it does not recognise and
// parts it cannot read become warnings; what could be read is kept.
func Parse(files []ReportFile) *Report {
	report := &Report{}
	for _, file := range files {
		var (
			results []*Result
			err     error
		)
		switch Detect(file.Name, file.Data) {
		case FormatJUnit:
			results, err = ParseJUnit(file.Data)
		case FormatGoTest:
			results, err = ParseGoTest(file.Data)
		case FormatGoMod:
			if mod := ParseGoMod(file.Data); mod != "" {
				dir := path.Dir(cleanPath(file.Name))
				if dir == "." {
					dir = ""
				}
				report.Modules = append(report.Modules, GoModule{Dir: dir, Path: mod})
			}
			continue
		default:
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: not a JUnit XML or go test -json report", file.Name))
			continue
		}
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", file.Name, err))
		}
		report.Results = append(report.Results, results...)
	}
	return report
}

// ParseGoMod returns the module path a go.mod declares, or "".
func ParseGoMod(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			mod := strings.TrimSpace(rest)
			if i := strings.Index(mod, "//"); i >= 0 {
				mod = strings.TrimSpace(mod[:i])
			}
			return strings.Trim(mod, `"`+"`")
		}
	}
	return ""
}

// truncateMessage keeps a message within model.TestMessageMax bytes: its
// first quarter and its end, where failures usually say what went wrong.
func truncateMessage(msg string) string {
	msg = strings.ToValidUTF8(strings.TrimSpace(msg), "�")
	if len(msg) <= model.TestMessageMax {
		return msg
	}
	const marker = "\n…\n"
	head := model.TestMessageMax / 4
	tail := model.TestMessageMax - head - len(marker)
	return cutUTF8(msg[:head]) + marker + startUTF8(msg[len(msg)-tail:])
}

// cutUTF8 drops a partial rune at the end of s.
func cutUTF8(s string) string {
	for len(s) > 0 && !utf8.ValidString(s[len(s)-min(len(s), utf8.UTFMax):]) {
		s = s[:len(s)-1]
	}
	return s
}

// startUTF8 drops a partial rune at the start of s.
func startUTF8(s string) string {
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// cleanPath makes a report path repo-relative looking: forward slashes, no
// leading "./" or "/".
func cleanPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "./")
	if p == "." {
		return ""
	}
	return p
}

// sortSlice sorts s stably by less.
func sortSlice[T any](s []T, less func(a, b T) bool) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}
