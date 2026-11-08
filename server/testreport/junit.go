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
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

type junitProblem struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type junitCase struct {
	Name      string         `xml:"name,attr"`
	Classname string         `xml:"classname,attr"`
	File      string         `xml:"file,attr"`
	Line      string         `xml:"line,attr"`
	Time      string         `xml:"time,attr"`
	Failures  []junitProblem `xml:"failure"`
	Errors    []junitProblem `xml:"error"`
	Skipped   *junitProblem  `xml:"skipped"`
	SystemOut string         `xml:"system-out"`
	SystemErr string         `xml:"system-err"`
}

type junitSuite struct {
	name string
	file string
}

// ParseJUnit reads a JUnit XML report: <testsuites> or <testsuite> roots,
// nested suites, and <testcase> elements with name, classname, file, line and
// time attributes and <failure>, <error> or <skipped> children. It reads the
// document as a stream, so a truncated report yields the cases before the
// point where it breaks off, with an error.
func ParseJUnit(data []byte) ([]*Result, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	// reports that declare another encoding are mostly ASCII; read them as is
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }

	var (
		results []*Result
		suites  []junitSuite
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return results, fmt.Errorf("invalid JUnit XML: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "testsuite":
				suite := junitSuite{}
				for _, attr := range el.Attr {
					switch attr.Name.Local {
					case "name":
						suite.name = attr.Value
					case "file", "filepath":
						suite.file = attr.Value
					}
				}
				suites = append(suites, suite)
			case "testcase":
				var tc junitCase
				if err := dec.DecodeElement(&tc, &el); err != nil {
					return results, fmt.Errorf("invalid JUnit XML test case: %w", err)
				}
				if r := junitResult(&tc, suites); r != nil {
					results = append(results, r)
				}
			}
		case xml.EndElement:
			if el.Name.Local == "testsuite" && len(suites) > 0 {
				suites = suites[:len(suites)-1]
			}
		}
	}
	return results, nil
}

func junitResult(tc *junitCase, suites []junitSuite) *Result {
	name := strings.TrimSpace(tc.Name)
	if name == "" {
		return nil
	}
	r := &Result{
		Name:     name,
		Suite:    strings.TrimSpace(tc.Classname),
		File:     cleanPath(tc.File),
		Duration: parseSeconds(tc.Time),
		Status:   model.TestStatusPass,
	}
	if r.Suite == "" && len(suites) > 0 {
		r.Suite = strings.TrimSpace(suites[len(suites)-1].name)
	}
	if r.File == "" {
		for i := len(suites) - 1; i >= 0; i-- {
			if suites[i].file != "" {
				r.File = cleanPath(suites[i].file)
				break
			}
		}
	}
	if line, err := strconv.Atoi(strings.TrimSpace(tc.Line)); err == nil && line > 0 {
		r.Line = line
	}
	if looksLikeGoImportPath(r.Suite) {
		r.Package = r.Suite
	}

	var problems []junitProblem
	switch {
	case len(tc.Failures) > 0:
		r.Status = model.TestStatusFail
		problems = tc.Failures
	case len(tc.Errors) > 0:
		r.Status = model.TestStatusError
		problems = tc.Errors
	case tc.Skipped != nil:
		r.Status = model.TestStatusSkip
		problems = []junitProblem{*tc.Skipped}
	}

	var msg strings.Builder
	for _, p := range problems {
		for _, part := range []string{strings.TrimSpace(p.Message), strings.TrimSpace(p.Text)} {
			// the text often repeats the message
			if part == "" || strings.Contains(msg.String(), part) {
				continue
			}
			if msg.Len() > 0 {
				msg.WriteString("\n")
			}
			msg.WriteString(part)
		}
	}
	if r.Status.Failed() {
		for _, out := range []string{tc.SystemErr, tc.SystemOut} {
			if out = strings.TrimSpace(out); out != "" {
				if msg.Len() > 0 {
					msg.WriteString("\n\n")
				}
				msg.WriteString(out)
			}
		}
	}
	r.Message = truncateMessage(msg.String())
	return r
}

// parseSeconds reads a JUnit time attribute: seconds, maybe with thousands
// separators.
func parseSeconds(s string) time.Duration {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) || f > 1e7 {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}

// looksLikeGoImportPath reports whether a JUnit class name looks like the Go
// import path gotestsum and go-junit-report put there: a domain, then a path.
func looksLikeGoImportPath(s string) bool {
	if strings.ContainsAny(s, " \t:\\") {
		return false
	}
	first, rest, ok := strings.Cut(s, "/")
	return ok && rest != "" && strings.Contains(first, ".") && !strings.HasSuffix(first, ".")
}
