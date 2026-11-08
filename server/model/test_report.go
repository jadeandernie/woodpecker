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

package model

import "strings"

// TestStatus is the result of one run of a test.
type TestStatus string //	@name	TestStatus

const (
	TestStatusPass  TestStatus = "pass"
	TestStatusFail  TestStatus = "fail"
	TestStatusSkip  TestStatus = "skip"
	TestStatusError TestStatus = "error"
)

// Letter is the one letter a status takes in TestCase.Recent.
func (s TestStatus) Letter() byte {
	switch s {
	case TestStatusPass:
		return 'p'
	case TestStatusFail:
		return 'f'
	case TestStatusSkip:
		return 's'
	default:
		return 'e'
	}
}

// Failed reports whether the status is a failure or an error.
func (s TestStatus) Failed() bool {
	return s == TestStatusFail || s == TestStatusError
}

// TestRecentRuns is how many run statuses TestCase.Recent keeps, and the
// window in which a test that both passed and failed counts as flaky.
const TestRecentRuns = 20

// TestCase is one test of a repo as test reports name it, with counters over
// its stored runs so lists need no aggregation.
type TestCase struct {
	ID     int64 `json:"id"      xorm:"pk autoincr 'id'"`
	RepoID int64 `json:"repo_id" xorm:"UNIQUE(s) INDEX 'repo_id'"`
	// Key identifies the test within the repo: its suite and name. KeyHash is
	// its SHA-256, which keeps the unique index short.
	Key     string `json:"key" xorm:"TEXT 'test_key'"`
	KeyHash string `json:"-"   xorm:"UNIQUE(s) VARCHAR(64) 'key_hash'"`
	// Suite is the JUnit class name or suite, or the Go package import path.
	Suite string `json:"suite" xorm:"TEXT 'suite'"`
	Name  string `json:"name"  xorm:"TEXT 'name'"`
	// File is the repo-relative path of the test's source file and Line its
	// line, when the report says so (or, for Go, the failure output names the
	// file); Line is 0 when unknown.
	File string `json:"file,omitempty" xorm:"TEXT 'file_path'"`
	Line int    `json:"line,omitempty" xorm:"'line'"`
	// Dir is the repo-relative directory of a Go test's package, when it could
	// be derived from the import path.
	Dir string `json:"dir,omitempty" xorm:"TEXT 'dir'"`
	// Group is the service the test belongs to (see the test reports docs),
	// Kind the kind or group of the workflow that last ran it and Workflow
	// that workflow's name.
	Group    string `json:"group"    xorm:"VARCHAR(250) INDEX 'test_group'"`
	Kind     string `json:"kind"     xorm:"VARCHAR(250) 'kind'"`
	Workflow string `json:"workflow" xorm:"VARCHAR(250) 'workflow'"`

	// FirstSeen is when the first pipeline that ran the test was created.
	FirstSeen         int64  `json:"first_seen"          xorm:"'first_seen'"`
	FirstSeenCommit   string `json:"first_seen_commit"   xorm:"VARCHAR(250) 'first_seen_commit'"`
	FirstSeenPipeline int64  `json:"first_seen_pipeline" xorm:"'first_seen_pipeline'"`

	// The last run, by pipeline number.
	LastRun            int64      `json:"last_run"             xorm:"'last_run'"`
	LastStatus         TestStatus `json:"last_status"          xorm:"VARCHAR(20) 'last_status'"`
	LastPipelineID     int64      `json:"-"                    xorm:"'last_pipeline_id'"`
	LastPipelineNumber int64      `json:"last_pipeline_number" xorm:"'last_pipeline_number'"`
	LastCommit         string     `json:"last_commit"          xorm:"VARCHAR(250) 'last_commit'"`
	LastBranch         string     `json:"last_branch"          xorm:"VARCHAR(250) 'last_branch'"`
	LastDurationMs     int64      `json:"last_duration_ms"     xorm:"'last_duration_ms'"`

	// Counters over the stored runs. Failures count errors too; DurationMs is
	// the total duration of the runs that were not skipped.
	Runs       int64 `json:"runs"        xorm:"'runs'"`
	Passes     int64 `json:"passes"      xorm:"'passes'"`
	Failures   int64 `json:"failures"    xorm:"'failures'"`
	Skips      int64 `json:"skips"       xorm:"'skips'"`
	DurationMs int64 `json:"duration_ms" xorm:"'duration_ms'"`
	// Recent holds the statuses of the last TestRecentRuns runs, oldest first,
	// one letter each (see TestStatus.Letter).
	Recent string `json:"recent" xorm:"VARCHAR(50) 'recent'"`
} //	@name	TestCase

// TableName returns the database table name for xorm.
func (TestCase) TableName() string {
	return "test_cases"
}

// AddRecent appends a status to Recent, dropping the oldest beyond TestRecentRuns.
func (c *TestCase) AddRecent(status TestStatus) {
	c.Recent += string(status.Letter())
	if len(c.Recent) > TestRecentRuns {
		c.Recent = c.Recent[len(c.Recent)-TestRecentRuns:]
	}
}

// Flaky reports whether the test both passed and failed in its recent runs.
func (c *TestCase) Flaky() bool {
	return strings.ContainsRune(c.Recent, 'p') && strings.ContainsAny(c.Recent, "fe")
}

// TestRun is one run of a test case in a pipeline step.
type TestRun struct {
	ID             int64      `json:"id"              xorm:"pk autoincr 'id'"`
	TestCaseID     int64      `json:"test_case_id"    xorm:"INDEX 'test_case_id'"`
	RepoID         int64      `json:"-"               xorm:"INDEX(test_runs_repo_branch) 'repo_id'"`
	PipelineID     int64      `json:"-"               xorm:"INDEX 'pipeline_id'"`
	PipelineNumber int64      `json:"pipeline_number" xorm:"'pipeline_number'"`
	WorkflowID     int64      `json:"workflow_id"     xorm:"'workflow_id'"`
	Workflow       string     `json:"workflow"        xorm:"VARCHAR(250) 'workflow'"`
	StepID         int64      `json:"step_id"         xorm:"'step_id'"`
	Branch         string     `json:"branch"          xorm:"INDEX(test_runs_repo_branch) VARCHAR(250) 'branch'"`
	Commit         string     `json:"commit"          xorm:"VARCHAR(250) 'commit_sha'"`
	Status         TestStatus `json:"status"          xorm:"VARCHAR(20) 'status'"`
	DurationMs     int64      `json:"duration_ms"     xorm:"'duration_ms'"`
	// Message is the failure message and output, or the skip reason,
	// truncated to TestMessageMax bytes.
	Message string `json:"message,omitempty" xorm:"TEXT 'message'"`
	Created int64  `json:"created"           xorm:"'created'"`

	// CaseKeyHash names the case of a run that is being saved, whose
	// TestCaseID may not be known yet.
	CaseKeyHash string `json:"-" xorm:"-"`
} //	@name	TestRun

// TableName returns the database table name for xorm.
func (TestRun) TableName() string {
	return "test_runs"
}

// TestMessageMax is the most bytes of a run's message that are stored.
const TestMessageMax = 16 << 10

// TestCaseFilter selects test cases of a repo.
type TestCaseFilter struct {
	// Query matches name, suite and file, ignoring case.
	Query string
	Group string
	Kind  string
	IDs   []int64
}

// TestRunFilter selects test runs. Zero fields do not filter.
type TestRunFilter struct {
	IDs         []int64
	RepoID      int64
	TestCaseID  int64
	TestCaseIDs []int64
	PipelineID  int64
	PipelineIDs []int64
	Branch      string
	// Limit caps the number of runs, newest first; 0 lists all.
	Limit int
	// WithoutMessage leaves the messages out.
	WithoutMessage bool
}

// TestCaseStats are the counters of one test case over a set of runs.
type TestCaseStats struct {
	TestCaseID int64 `xorm:"test_case_id"`
	Runs       int64 `xorm:"runs"`
	Passes     int64 `xorm:"passes"`
	Failures   int64 `xorm:"failures"`
	Skips      int64 `xorm:"skips"`
	DurationMs int64 `xorm:"duration_ms"`
	LastRunID  int64 `xorm:"last_run_id"`
}

// TestCaseInfo is a test case with the statistics and links the web UI shows.
type TestCaseInfo struct {
	TestCase
	// PassRate is passes / (passes + failures), or null when no run passed or failed.
	PassRate      *float64 `json:"pass_rate"`
	AvgDurationMs int64    `json:"avg_duration_ms"`
	// P50DurationMs is the median duration of the runs in History that were not skipped.
	P50DurationMs int64 `json:"p50_duration_ms"`
	Flaky         bool  `json:"flaky"`
	// CodeURL links to the test's code in the forge: its file and line, or
	// its package directory when CodeExact is false.
	CodeURL   string `json:"code_url,omitempty"`
	CodeExact bool   `json:"code_exact"`
	// CodeSearchURL searches the forge for the test's definition, when the
	// forge supports code search links.
	CodeSearchURL string `json:"code_search_url,omitempty"`
	// History holds the recent runs, oldest first, without messages.
	History []*TestRun `json:"history"`
} //	@name	TestCaseInfo

// TestSummary counts tests by their state.
type TestSummary struct {
	Total   int `json:"total"`
	Failing int `json:"failing"`
	Flaky   int `json:"flaky"`
	Passing int `json:"passing"`
	Skipped int `json:"skipped"`
	// PassRate is passes / (passes + failures) over all runs of the tests, or
	// null when there are none.
	PassRate *float64 `json:"pass_rate"`
} //	@name	TestSummary

// TestGroup is a service group of tests with its counts.
type TestGroup struct {
	Name    string `json:"name"`
	Total   int    `json:"total"`
	Failing int    `json:"failing"`
	Flaky   int    `json:"flaky"`
} //	@name	TestGroup

// TestList is a page of a repo's tests with a summary and the groups.
type TestList struct {
	// Summary counts the tests that match the filters other than status.
	Summary *TestSummary `json:"summary"`
	// Groups counts the tests that match the filters other than group.
	Groups []*TestGroup `json:"groups"`
	// Total is the number of tests that match all filters.
	Total   int             `json:"total"`
	Page    int             `json:"page"`
	PerPage int             `json:"per_page"`
	Tests   []*TestCaseInfo `json:"tests"`
	// Branch is the branch the statistics are for, "" for all branches.
	Branch string `json:"branch"`
	// RetentionPipelines and RetentionDays bound the stored runs.
	RetentionPipelines int `json:"retention_pipelines"`
	RetentionDays      int `json:"retention_days"`
} //	@name	TestList

// TestCaseDetail is a test case with its stored runs, newest first.
type TestCaseDetail struct {
	*TestCaseInfo
	Runs []*TestRun `json:"runs"`
} //	@name	TestCaseDetail

// PipelineTestRun is a test run of a pipeline with its test case.
type PipelineTestRun struct {
	*TestRun
	Name          string `json:"name"`
	Suite         string `json:"suite"`
	Group         string `json:"group"`
	CodeURL       string `json:"code_url,omitempty"`
	CodeExact     bool   `json:"code_exact"`
	CodeSearchURL string `json:"code_search_url,omitempty"`
} //	@name	PipelineTestRun

// PipelineTests are the test runs of a pipeline, failures first.
type PipelineTests struct {
	Total   int                `json:"total"`
	Passed  int                `json:"passed"`
	Failed  int                `json:"failed"`
	Skipped int                `json:"skipped"`
	Runs    []*PipelineTestRun `json:"runs"`
} //	@name	PipelineTests
