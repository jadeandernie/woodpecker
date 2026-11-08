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

package datastore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

func day(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.Unix()
}

type queryFixture struct {
	pipeline  *model.Pipeline
	created   int64
	workflows []*model.Workflow
}

func newQueryTestStore(t *testing.T) (*storage, *model.Repo, func()) {
	store, closer := newTestStore(t, new(model.Repo), new(model.Pipeline), new(model.Workflow), new(model.Step))

	repo := &model.Repo{ID: 1, Owner: "o", Name: "mono", FullName: "o/mono", ForgeRemoteID: "1"}
	require.NoError(t, store.CreateRepo(repo))
	require.NoError(t, store.CreateRepo(&model.Repo{ID: 2, Owner: "o", Name: "other", FullName: "o/other", ForgeRemoteID: "2"}))

	fixtures := []queryFixture{
		{
			pipeline: &model.Pipeline{
				RepoID: 1, Event: model.EventPush, Branch: "main", Ref: "refs/heads/main", Author: "alice", Sender: "alice",
				Status: model.StatusSuccess, Commit: "aaa111", Message: "Add knowledge store",
				Started: day("2026-11-01T10:00:00Z"), Finished: day("2026-11-01T10:10:00Z"),
			},
			created: day("2026-11-01T10:00:00Z"),
			workflows: []*model.Workflow{
				{Name: "build-service-knowledge-store-rpc"},
				{Name: "test-service-knowledge-store-rpc"},
			},
		},
		{
			pipeline: &model.Pipeline{
				RepoID: 1, Event: model.EventPull, Branch: "feature/login", Ref: "refs/pull/32/head", Author: "bob", Sender: "bob",
				Status: model.StatusFailure, Commit: "bbb222", Message: "Fix login page", Title: "Login fix",
				Started: day("2026-11-03T10:00:00Z"), Finished: day("2026-11-03T10:02:00Z"),
			},
			created: day("2026-11-03T10:00:00Z"),
			workflows: []*model.Workflow{
				{Name: "test-service-tensor-rpc"},
				{Name: "custom-check", Kind: "deploy"}, // declares deploy, its name says other
			},
		},
		{
			pipeline: &model.Pipeline{
				RepoID: 1, Event: model.EventPush, Branch: "main", Ref: "refs/heads/main", Author: "carol", Sender: "Dave",
				Status: model.StatusError, Commit: "ccc333", Message: "Deploy tensor",
				Started: day("2026-11-05T10:00:00Z"), // still running: no duration
			},
			created: day("2026-11-05T10:00:00Z"),
			workflows: []*model.Workflow{
				{Name: "deploy-service-tensor-rpc"},
				{Name: "nightly-knowledge-sweep", Kind: "build"}, // declares build, its name says test
			},
		},
		{
			pipeline: &model.Pipeline{
				RepoID: 1, Event: model.EventCron, Branch: "release_1", Ref: "refs/heads/release_1", Author: "erin",
				Status: model.StatusSuccess, Commit: "aaa999",
				Started: day("2026-11-07T10:00:00Z"), Finished: day("2026-11-07T10:30:00Z"),
			},
			created: day("2026-11-07T10:00:00Z"),
			workflows: []*model.Workflow{
				{Name: "gate-architecture"},
				{Name: "misc"},
			},
		},
		{
			// another repo's pipeline must never show up
			pipeline: &model.Pipeline{
				RepoID: 2, Event: model.EventPush, Branch: "main", Author: "alice", Status: model.StatusFailure,
				Commit: "aaa111", Message: "Fix login", Started: 1, Finished: 2,
			},
			created:   day("2026-11-03T10:00:00Z"),
			workflows: []*model.Workflow{{Name: "deploy-service-tensor-rpc"}, {Name: "misc"}},
		},
	}

	for _, f := range fixtures {
		require.NoError(t, store.CreatePipeline(f.pipeline))
		_, err := store.engine.Exec("UPDATE pipelines SET created = ? WHERE id = ?", f.created, f.pipeline.ID)
		require.NoError(t, err)
		for i, wf := range f.workflows {
			wf.PipelineID = f.pipeline.ID
			wf.PID = i + 1
			_, err := store.engine.Insert(wf)
			require.NoError(t, err)
		}
	}
	return store, repo, closer
}

func TestPipelineListQuery(t *testing.T) {
	store, repo, closer := newQueryTestStore(t)
	defer closer()

	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		query string
		want  []int64 // pipeline numbers, newest first
	}{
		{"", []int64{4, 3, 2, 1}},
		// status / event
		{"status:failure,error", []int64{3, 2}},
		{"is:failed", []int64{2}},
		{"-status:success", []int64{3, 2}},
		{"event:pr", []int64{2}},
		{"event:push,cron", []int64{4, 3, 1}},
		// branch, with globs and an escaped _
		{"branch:main", []int64{3, 1}},
		{"branch:feature/*", []int64{2}},
		{"branch:release_*", []int64{4}},
		{"branch:releas_*", nil},
		{"-branch:main", []int64{4, 2}},
		// author or sender, ignoring case
		{"author:alice", []int64{1}},
		{"author:dave", []int64{3}},
		{"author:BOB", []int64{2}},
		{"author:a*", []int64{1}},
		// ref contains or glob
		{"ref:pull/32", []int64{2}},
		{"ref:refs/heads/*", []int64{4, 3, 1}},
		// kind: declared kinds win over names
		{"kind:build", []int64{3, 1}},
		{"kind:test", []int64{4, 2, 1}},
		{"type:deploy", []int64{3, 2}},
		{"kind:other", []int64{4}},
		{"kind:deploy,other", []int64{4, 3, 2}},
		{"-kind:deploy", []int64{4, 1}},
		// group (subgroup)
		{"group:knowledge", []int64{3, 1}},
		{"service:tensor", []int64{3, 2}},
		{"group:nothing", nil},
		{"-group:nothing", []int64{4, 3, 2, 1}},
		// workflow names
		{"workflow:gate-*", []int64{4}},
		{"workflow:TEST-SERVICE-TENSOR-RPC", []int64{2}},
		{"-workflow:test-*", []int64{4, 3}},
		{"workflow:*-rpc,misc", []int64{4, 3, 2, 1}},
		// dates (UTC days, end inclusive)
		{"date:2026-11-03", []int64{2}},
		{"date:2026-11-01..2026-11-05", []int64{3, 2, 1}},
		{"after:2026-11-05", []int64{4, 3}},
		{"before:2026-11-03", []int64{1}},
		{"date:2d", []int64{4}},
		{"date:2026-11-01,2026-11-07", []int64{4, 1}},
		// durations of finished pipelines
		{"duration:>=10m", []int64{4, 1}},
		{"runtime:<5m", []int64{2}},
		{"duration:5m..20m", []int64{1}},
		{"-duration:>=10m", []int64{2}},
		// commits
		{"commit:aaa", []int64{4, 1}},
		{"commit:bbb..ccc", []int64{3, 2}},
		{"commit:ccc..bbb", []int64{3, 2}},
		{"commit:aaa1..aaa9", []int64{4, 3, 2, 1}},
		{"-commit:aaa", []int64{3, 2}},
		// numbers
		{"number:2..3", []int64{3, 2}},
		{"#>3", []int64{4}},
		{"-#1", []int64{4, 3, 2}},
		// free text: message, title or commit prefix
		{"login", []int64{2}},
		{"LOGIN fix", []int64{2}},
		{`"knowledge store"`, []int64{1}},
		{"ccc3", []int64{3}},
		{"-login", []int64{4, 3, 1}},
		{"50%", nil},
		// combinations
		{"kind:test status:success", []int64{4, 1}},
		{"branch:main -kind:deploy", []int64{1}},
		{"status:failure,error service:tensor -event:pr", []int64{3}},
		{"kind:test group:knowledge after:2026-11-01", []int64{1}},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			q, err := model.ParsePipelineQuery(tt.query, now)
			require.NoError(t, err)
			pipelines, err := store.GetPipelineList(repo, nil, &model.PipelineFilter{Query: q})
			require.NoError(t, err)
			numbers := make([]int64, 0, len(pipelines))
			for _, p := range pipelines {
				assert.EqualValues(t, repo.ID, p.RepoID)
				numbers = append(numbers, p.Number)
			}
			if tt.want == nil {
				tt.want = []int64{}
			}
			assert.Equal(t, tt.want, numbers)
		})
	}

	t.Run("query with the older filters", func(t *testing.T) {
		q, err := model.ParsePipelineQuery("kind:deploy", now)
		require.NoError(t, err)
		pipelines, err := store.GetPipelineList(repo, nil, &model.PipelineFilter{Branch: "main", Query: q})
		require.NoError(t, err)
		require.Len(t, pipelines, 1)
		assert.EqualValues(t, 3, pipelines[0].Number)
	})

	t.Run("paginated", func(t *testing.T) {
		q, err := model.ParsePipelineQuery("-status:killed", now)
		require.NoError(t, err)
		pipelines, err := store.GetPipelineList(repo, &model.ListOptionsWithAll{ListOptions: &model.ListOptions{Page: 2, PerPage: 3}}, &model.PipelineFilter{Query: q})
		require.NoError(t, err)
		require.Len(t, pipelines, 1)
		assert.EqualValues(t, 1, pipelines[0].Number)
	})

	t.Run("unknown commit", func(t *testing.T) {
		q, err := model.ParsePipelineQuery("commit:fff..aaa", now)
		require.NoError(t, err)
		_, err = store.GetPipelineList(repo, nil, &model.PipelineFilter{Query: q})
		var qe *model.PipelineQueryError
		require.ErrorAs(t, err, &qe)
		assert.Equal(t, `no pipeline has a commit starting with "fff"`, qe.Error())
	})

	t.Run("repo workflow group rules", func(t *testing.T) {
		ruled := &model.Repo{ID: 1, WorkflowGroups: []*model.WorkflowGroupRule{
			{Pattern: `^misc$`, Group: "deploy", Subgroup: "odds"},
		}}
		q, err := model.ParsePipelineQuery("kind:deploy group:odds", now)
		require.NoError(t, err)
		pipelines, err := store.GetPipelineList(ruled, nil, &model.PipelineFilter{Query: q})
		require.NoError(t, err)
		require.Len(t, pipelines, 1)
		assert.EqualValues(t, 4, pipelines[0].Number)
	})
}

func TestGetPipelineFacets(t *testing.T) {
	store, repo, closer := newQueryTestStore(t)
	defer closer()

	facets, err := store.GetPipelineFacets(repo, 500)
	require.NoError(t, err)
	assert.Equal(t, []string{"release_1", "main", "feature/login"}, facets.Branches)
	assert.Equal(t, []string{"erin", "carol", "Dave", "bob", "alice"}, facets.Authors)
	assert.Equal(t, []model.WebhookEvent{model.EventCron, model.EventPull, model.EventPush}, facets.Events)
	assert.Equal(t, []string{"build", "test", "deploy", "other"}, facets.Kinds)
	assert.Equal(t, []string{"architecture", "knowledge", "tensor"}, facets.Groups)
	assert.Equal(t, []string{
		"build-service-knowledge-store-rpc", "custom-check", "deploy-service-tensor-rpc", "gate-architecture",
		"misc", "nightly-knowledge-sweep", "test-service-knowledge-store-rpc", "test-service-tensor-rpc",
	}, facets.Workflows)

	facets, err = store.GetPipelineFacets(repo, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"release_1"}, facets.Branches)
	assert.Equal(t, []string{"gate-architecture", "misc"}, facets.Workflows)
	assert.Equal(t, []string{"test", "other"}, facets.Kinds)

	facets, err = store.GetPipelineFacets(&model.Repo{ID: 99}, 500)
	require.NoError(t, err)
	assert.Empty(t, facets.Branches)
	assert.NotNil(t, facets.Workflows)
}
