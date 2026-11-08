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

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func i64(v int64) *int64 { return &v }

func unix(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.Unix()
}

func TestParsePipelineQuery(t *testing.T) {
	now := time.Date(2026, 11, 8, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		name  string
		query string
		want  []*PipelineQueryTerm
	}{
		{name: "empty", query: "", want: nil},
		{name: "only spaces", query: "  \t ", want: nil},
		{
			name:  "status with OR and alias key",
			query: "is:failure,error",
			want: []*PipelineQueryTerm{{Key: PipelineQueryStatus, Values: []*PipelineQueryValue{
				{Text: "failure"}, {Text: "error"},
			}}},
		},
		{
			name:  "status aliases and case",
			query: "status:Failed,cancelled",
			want: []*PipelineQueryTerm{{Key: PipelineQueryStatus, Values: []*PipelineQueryValue{
				{Text: "failure"}, {Text: "canceled"},
			}}},
		},
		{
			name:  "negated event with alias value",
			query: "-event:pr,cron",
			want: []*PipelineQueryTerm{{Key: PipelineQueryEvent, Negate: true, Values: []*PipelineQueryValue{
				{Text: "pull_request"}, {Text: "cron"},
			}}},
		},
		{
			name:  "terms AND, free text and quoted value",
			query: `branch:main author:"Jade P" fix "login page"`,
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryBranch, Values: []*PipelineQueryValue{{Text: "main"}}},
				{Key: PipelineQueryAuthor, Values: []*PipelineQueryValue{{Text: "Jade P"}}},
				{Key: PipelineQueryText, Values: []*PipelineQueryValue{{Text: "fix"}}},
				{Key: PipelineQueryText, Values: []*PipelineQueryValue{{Text: "login page"}}},
			},
		},
		{
			name:  "quoted comma does not split",
			query: `branch:"a,b",c`,
			want: []*PipelineQueryTerm{{Key: PipelineQueryBranch, Values: []*PipelineQueryValue{
				{Text: "a,b"}, {Text: "c"},
			}}},
		},
		{
			name:  "escaped quote",
			query: `"say \"hi\""`,
			want:  []*PipelineQueryTerm{{Key: PipelineQueryText, Values: []*PipelineQueryValue{{Text: `say "hi"`}}}},
		},
		{
			name:  "negated free text",
			query: "-wip",
			want:  []*PipelineQueryTerm{{Key: PipelineQueryText, Negate: true, Values: []*PipelineQueryValue{{Text: "wip"}}}},
		},
		{
			name:  "quoted text with colon is text",
			query: `"fix: tests"`,
			want:  []*PipelineQueryTerm{{Key: PipelineQueryText, Values: []*PipelineQueryValue{{Text: "fix: tests"}}}},
		},
		{
			name:  "branch glob and ref",
			query: "branch:feature/* ref:refs/pull/*",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryBranch, Values: []*PipelineQueryValue{{Text: "feature/*"}}},
				{Key: PipelineQueryRef, Values: []*PipelineQueryValue{{Text: "refs/pull/*"}}},
			},
		},
		{
			name:  "kind and type alias, lower cased",
			query: "kind:Deploy type:test,other",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryKind, Values: []*PipelineQueryValue{{Text: "deploy"}}},
				{Key: PipelineQueryKind, Values: []*PipelineQueryValue{{Text: "test"}, {Text: "other"}}},
			},
		},
		{
			name:  "service group and workflow glob",
			query: "service:knowledge -workflow:nightly-*",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryGroup, Values: []*PipelineQueryValue{{Text: "knowledge"}}},
				{Key: PipelineQueryWorkflow, Negate: true, Values: []*PipelineQueryValue{{Text: "nightly-*"}}},
			},
		},
		{
			name:  "date day",
			query: "date:2026-11-01",
			want: []*PipelineQueryTerm{{Key: PipelineQueryDate, Values: []*PipelineQueryValue{
				{Text: "2026-11-01", Min: i64(unix("2026-11-01T00:00:00Z")), Max: i64(unix("2026-11-02T00:00:00Z") - 1)},
			}}},
		},
		{
			name:  "date range end inclusive",
			query: "date:2026-11-01..2026-11-07",
			want: []*PipelineQueryTerm{{Key: PipelineQueryDate, Values: []*PipelineQueryValue{
				{Text: "2026-11-01..2026-11-07", Min: i64(unix("2026-11-01T00:00:00Z")), Max: i64(unix("2026-11-08T00:00:00Z") - 1)},
			}}},
		},
		{
			name:  "open date ranges",
			query: "date:2026-11-01.. date:..2026-11-07",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryDate, Values: []*PipelineQueryValue{{Text: "2026-11-01..", Min: i64(unix("2026-11-01T00:00:00Z"))}}},
				{Key: PipelineQueryDate, Values: []*PipelineQueryValue{{Text: "..2026-11-07", Max: i64(unix("2026-11-08T00:00:00Z") - 1)}}},
			},
		},
		{
			name:  "date comparisons",
			query: "date:>2026-11-01 date:<=2026-11-01",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryDate, Values: []*PipelineQueryValue{{Text: ">2026-11-01", Min: i64(unix("2026-11-02T00:00:00Z"))}}},
				{Key: PipelineQueryDate, Values: []*PipelineQueryValue{{Text: "<=2026-11-01", Max: i64(unix("2026-11-02T00:00:00Z") - 1)}}},
			},
		},
		{
			name:  "relative date means since",
			query: "date:7d",
			want: []*PipelineQueryTerm{{Key: PipelineQueryDate, Values: []*PipelineQueryValue{
				{Text: "7d", Min: i64(unix("2026-11-01T15:30:00Z"))},
			}}},
		},
		{
			name:  "today",
			query: "date:today",
			want: []*PipelineQueryTerm{{Key: PipelineQueryDate, Values: []*PipelineQueryValue{
				{Text: "today", Min: i64(unix("2026-11-08T00:00:00Z")), Max: i64(unix("2026-11-09T00:00:00Z") - 1)},
			}}},
		},
		{
			name:  "after and before",
			query: "after:2026-11-01 before:24h since:2026-11-01T10:00:00+02:00",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryDate, Values: []*PipelineQueryValue{{Text: "2026-11-01", Min: i64(unix("2026-11-01T00:00:00Z"))}}},
				{Key: PipelineQueryDate, Values: []*PipelineQueryValue{{Text: "24h", Max: i64(unix("2026-11-07T15:30:00Z") - 1)}}},
				{Key: PipelineQueryDate, Values: []*PipelineQueryValue{{Text: "2026-11-01T10:00:00+02:00", Min: i64(unix("2026-11-01T08:00:00Z"))}}},
			},
		},
		{
			name:  "durations",
			query: "duration:>10m runtime:<5m duration:>=90s duration:5m..20m duration:1h30m",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryDuration, Values: []*PipelineQueryValue{{Text: ">10m", Min: i64(601)}}},
				{Key: PipelineQueryDuration, Values: []*PipelineQueryValue{{Text: "<5m", Max: i64(299)}}},
				{Key: PipelineQueryDuration, Values: []*PipelineQueryValue{{Text: ">=90s", Min: i64(90)}}},
				{Key: PipelineQueryDuration, Values: []*PipelineQueryValue{{Text: "5m..20m", Min: i64(300), Max: i64(1200)}}},
				{Key: PipelineQueryDuration, Values: []*PipelineQueryValue{{Text: "1h30m", Min: i64(5400)}}},
			},
		},
		{
			name:  "commit prefix and range",
			query: "commit:ABC123 sha:abc..def456",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryCommit, Values: []*PipelineQueryValue{{Text: "abc123"}}},
				{Key: PipelineQueryCommit, Values: []*PipelineQueryValue{{Text: "abc", CommitTo: "def456"}}},
			},
		},
		{
			name:  "numbers",
			query: "number:2100..2127 #>2100 -#5 number:<=3",
			want: []*PipelineQueryTerm{
				{Key: PipelineQueryNumber, Values: []*PipelineQueryValue{{Text: "2100..2127", Min: i64(2100), Max: i64(2127)}}},
				{Key: PipelineQueryNumber, Values: []*PipelineQueryValue{{Text: ">2100", Min: i64(2101)}}},
				{Key: PipelineQueryNumber, Negate: true, Values: []*PipelineQueryValue{{Text: "5", Min: i64(5), Max: i64(5)}}},
				{Key: PipelineQueryNumber, Values: []*PipelineQueryValue{{Text: "<=3", Max: i64(3)}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := ParsePipelineQuery(tt.query, now)
			require.NoError(t, err)
			assert.Equal(t, tt.want, q.Terms)
		})
	}
}

func TestParsePipelineQueryErrors(t *testing.T) {
	now := time.Date(2026, 11, 8, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		query string
		want  string
	}{
		{"stauts:failure", `unknown filter key "stauts" (did you mean "status"?)`},
		{"foo:bar", `unknown filter key "foo" (quote the term to search for it as text)`},
		{"status:", `filter "status" needs a value`},
		{"status:failure,", `empty value in "status:failure,"`},
		{"status:broken", `unknown status "broken"`},
		{"event:push,nope", `unknown event "nope"`},
		{`branch:"main`, `unterminated quote in "branch:\"main"`},
		{"duration:10", `invalid duration "10": use a unit like 90s, 10m or 1h30m`},
		{"duration:20m..5m", `range "20m..5m" ends before it starts`},
		{"date:2026-13-01", `invalid date "2026-13-01": use 2026-11-01, an RFC3339 time or a relative time like 7d or 24h`},
		{"date:..", `empty range ".."`},
		{"after:2026-11-01..2026-11-02", `after: takes a single date, like after:2026-11-01 or after:7d`},
		{"commit:xyz", `invalid commit "xyz": use a SHA prefix like abc123 or a range like abc123..def456`},
		{"commit:abc..", `invalid commit "abc..": use a SHA prefix like abc123 or a range like abc123..def456`},
		{"number:abc", `invalid pipeline number "abc"`},
		{"kind:a;b", `invalid kind "a;b"`},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			_, err := ParsePipelineQuery(tt.query, now)
			require.Error(t, err)
			var qe *PipelineQueryError
			require.ErrorAs(t, err, &qe)
			assert.Equal(t, tt.want, err.Error())
		})
	}
}

func TestPipelineQueryHasKey(t *testing.T) {
	q, err := ParsePipelineQuery("kind:test fix", time.Now())
	require.NoError(t, err)
	assert.True(t, q.HasKey(PipelineQueryKind))
	assert.True(t, q.HasKey(PipelineQueryText))
	assert.False(t, q.HasKey(PipelineQueryGroup))
	assert.False(t, q.IsEmpty())
	assert.True(t, (*PipelineQuery)(nil).IsEmpty())
}

func TestQueryGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"test-a", "test-a", true},
		{"test-a", "Test-A", true},
		{"test-a", "test-ab", false},
		{"test-*", "test-service-knowledge", true},
		{"*-rpc", "deploy-service-tensor-rpc", true},
		{"*knowledge*", "test-service-knowledge-store-rpc", true},
		{"test-*-rpc", "test-service-knowledge-store-rpc", true},
		{"test-*-rpc", "deploy-service-rpc", false},
		{"a*a", "a", false},
		{"*", "", true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, QueryGlobMatch(tt.pattern, tt.name), "%s ~ %s", tt.pattern, tt.name)
	}
}

func TestWorkflowKinds(t *testing.T) {
	assert.Nil(t, WorkflowKinds(nil))
	assert.Equal(t, []string{"build", "test", "deploy", "lint", "zz", "other"}, WorkflowKinds([]*Workflow{
		{Group: "other"}, {Group: "deploy"}, {Group: "zz"}, {Group: "test"}, nil,
		{Group: "lint"}, {Group: "build"}, {Group: "test"}, {Group: ""},
	}))
}
