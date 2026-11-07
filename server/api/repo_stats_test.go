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

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
)

// seedStatsRepo creates a repo with four pipelines:
// #1 main push with three workflows, #2 main push, #3 dev push, #4 main cron.
func seedStatsRepo(t *testing.T, s store.Store, name string) *model.Repo {
	t.Helper()
	repo := &model.Repo{
		ForgeID:        1,
		ForgeRemoteID:  model.ForgeRemoteID("r-" + name),
		Owner:          "acme",
		Name:           name,
		FullName:       "acme/" + name,
		Branch:         "main",
		IsActive:       true,
		WebhookSecret:  "hash-" + name,
		WorkflowGroups: []*model.WorkflowGroupRule{{Pattern: `^docs-(\w+)$`, Group: "other", Subgroup: "$1"}},
	}
	require.NoError(t, s.CreateRepo(repo))

	pipeline := func(number int64, branch string, event model.WebhookEvent, status model.StatusValue, workflows ...*model.Workflow) *model.Pipeline {
		pl := &model.Pipeline{RepoID: repo.ID, Number: number, Branch: branch, Event: event, Status: status, Commit: "sha" + branch, Message: "msg", Cron: "nightly"}
		require.NoError(t, s.CreatePipeline(pl))
		for i, wf := range workflows {
			wf.PipelineID = pl.ID
			wf.PID = i + 1
			for j, step := range wf.Children {
				step.PipelineID = pl.ID
				step.PPID = wf.PID
				step.PID = 100*(i+1) + j
				step.UUID = name + "-" + wf.Name + "-" + step.Name + "-" + string(rune('a'+number))
			}
		}
		require.NoError(t, s.WorkflowsCreate(workflows))
		return pl
	}

	pipeline(1, "main", model.EventPush, model.StatusSuccess,
		&model.Workflow{Name: "svc-knowledge", State: model.StatusSuccess, Started: 10, Finished: 20, Children: []*model.Step{
			{Name: "clone", MemoryPeak: 100},
			{Name: "build", MemoryPeak: 400},
		}},
		&model.Workflow{Name: "deploy-service-knowledge-store-rpc", State: model.StatusSuccess, DependsOn: []string{"svc-knowledge"}, Children: []*model.Step{
			{Name: "deploy"},
		}},
		&model.Workflow{Name: "docs-site", State: model.StatusSuccess},
	)
	pipeline(2, "main", model.EventPush, model.StatusFailure,
		&model.Workflow{Name: "test-service-tensor-worker", State: model.StatusFailure, Children: []*model.Step{{Name: "test", MemoryPeak: 50}}},
	)
	pipeline(3, "dev", model.EventPush, model.StatusSuccess)
	pipeline(4, "main", model.EventCron, model.StatusSuccess)
	return repo
}

func getRepoStats(t *testing.T, s store.Store, repo *model.Repo, query string) (int, *model.RepoStats) {
	t.Helper()
	tc := newTestContext(t, s)
	withRepo(repo, &model.Perm{Pull: true})(tc)
	tc.Ctx.Request = httptest.NewRequest(http.MethodGet, "/stats?"+query, nil)
	GetRepoStats(tc.Ctx)
	if tc.Ctx.Writer.Status() != http.StatusOK {
		return tc.Ctx.Writer.Status(), nil
	}
	stats := new(model.RepoStats)
	tc.decodeJSON(t, stats)
	return http.StatusOK, stats
}

func pipelineNumbers(stats *model.RepoStats) []int64 {
	numbers := make([]int64, 0, len(stats.Pipelines))
	for _, pl := range stats.Pipelines {
		numbers = append(numbers, pl.Number)
	}
	return numbers
}

func TestGetRepoStats(t *testing.T) {
	s := newTestStore(t)
	repo := seedStatsRepo(t, s, "stats")

	t.Run("defaults to the default branch, newest first, with groups and memory", func(t *testing.T) {
		status, stats := getRepoStats(t, s, repo, "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "main", stats.Branch)
		assert.Equal(t, []int64{4, 2, 1}, pipelineNumbers(stats))

		cron := stats.Pipelines[0]
		assert.Equal(t, "nightly", cron.Message)
		assert.Empty(t, cron.Workflows)
		assert.NotNil(t, cron.Workflows)

		failed := stats.Pipelines[1]
		assert.Equal(t, model.StatusFailure, failed.Status)
		assert.EqualValues(t, 50, failed.MemoryPeak)
		require.Len(t, failed.Workflows, 1)
		assert.Equal(t, "test", failed.Workflows[0].Group)
		assert.Equal(t, "tensor", failed.Workflows[0].Subgroup)

		pl := stats.Pipelines[2]
		assert.Equal(t, "push", pl.Event)
		assert.Equal(t, "main", pl.Branch)
		assert.Equal(t, "shamain", pl.Commit)
		assert.Equal(t, "msg", pl.Message)
		assert.EqualValues(t, 400, pl.MemoryPeak)
		require.Len(t, pl.Workflows, 3)

		build := pl.Workflows[0]
		assert.Equal(t, "svc-knowledge", build.Name)
		assert.Equal(t, "build", build.Group)
		assert.Equal(t, "knowledge", build.Subgroup)
		assert.EqualValues(t, 400, build.MemoryPeak)
		assert.EqualValues(t, 10, build.Started)
		assert.EqualValues(t, 20, build.Finished)

		deploy := pl.Workflows[1]
		assert.Equal(t, "deploy", deploy.Group)
		assert.Equal(t, "knowledge", deploy.Subgroup)
		assert.Equal(t, []string{"svc-knowledge"}, deploy.DependsOn)
		assert.EqualValues(t, 0, deploy.MemoryPeak)

		docs := pl.Workflows[2]
		assert.Equal(t, "other", docs.Group, "the repo rule wins over the built-in heuristic")
		assert.Equal(t, "site", docs.Subgroup)
	})

	t.Run("filters by event", func(t *testing.T) {
		status, stats := getRepoStats(t, s, repo, "event=push")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []int64{2, 1}, pipelineNumbers(stats))
	})

	t.Run("all branches", func(t *testing.T) {
		status, stats := getRepoStats(t, s, repo, "branch=*")
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, stats.Branch)
		assert.Equal(t, []int64{4, 3, 2, 1}, pipelineNumbers(stats))
	})

	t.Run("one branch", func(t *testing.T) {
		status, stats := getRepoStats(t, s, repo, "branch=dev")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "dev", stats.Branch)
		assert.Equal(t, []int64{3}, pipelineNumbers(stats))
	})

	t.Run("limit", func(t *testing.T) {
		status, stats := getRepoStats(t, s, repo, "limit=1")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []int64{4}, pipelineNumbers(stats))

		status, stats = getRepoStats(t, s, repo, "limit=1000")
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, stats.Pipelines, 3)
	})

	t.Run("bad queries", func(t *testing.T) {
		for _, query := range []string{"limit=0", "limit=x", "event=bogus"} {
			status, _ := getRepoStats(t, s, repo, query)
			assert.Equal(t, http.StatusBadRequest, status, query)
		}
	})

	t.Run("empty history", func(t *testing.T) {
		status, stats := getRepoStats(t, s, repo, "branch=none")
		require.Equal(t, http.StatusOK, status)
		assert.NotNil(t, stats.Pipelines)
		assert.Empty(t, stats.Pipelines)
	})
}

func TestGetPipelineWorkflowGroups(t *testing.T) {
	s := newTestStore(t)
	repo := seedStatsRepo(t, s, "groups")

	tc := newTestContext(t, s)
	withRepo(repo, &model.Perm{Pull: true})(tc)
	withParam("pipeline_number", "1")(tc)
	GetPipeline(tc.Ctx)
	require.Equal(t, http.StatusOK, tc.Ctx.Writer.Status())

	pl := new(model.Pipeline)
	tc.decodeJSON(t, pl)
	require.Len(t, pl.Workflows, 3)
	assert.Equal(t, "build", pl.Workflows[0].Group)
	assert.Equal(t, "knowledge", pl.Workflows[0].Subgroup)
	assert.Equal(t, "deploy", pl.Workflows[1].Group)
	assert.Equal(t, "other", pl.Workflows[2].Group)
	assert.Equal(t, "site", pl.Workflows[2].Subgroup)
}

func TestGetBadgePipeline(t *testing.T) {
	s := newTestStore(t)
	repo := seedStatsRepo(t, s, "badge")

	originalRootPath := server.Config.Server.RootPath
	server.Config.Server.RootPath = "/ci"
	t.Cleanup(func() { server.Config.Server.RootPath = originalRootPath })

	redirect := func(t *testing.T, repoParam, query string) (int, string) {
		t.Helper()
		tc := newTestContext(t, s)
		withParam("repo_id_or_owner", repoParam)(tc)
		tc.Ctx.Request = httptest.NewRequest(http.MethodGet, "/pipeline?"+query, nil)
		GetBadgePipeline(tc.Ctx)
		return tc.Recorder.Code, tc.Recorder.Header().Get("Location")
	}
	repoID := strItoa(repo.ID)

	status, location := redirect(t, repoID, "")
	assert.Equal(t, http.StatusFound, status)
	assert.Equal(t, "/ci/repos/"+repoID+"/pipeline/2/graph", location)

	_, location = redirect(t, repoID, "branch=dev")
	assert.Equal(t, "/ci/repos/"+repoID+"/pipeline/3/graph", location)

	_, location = redirect(t, repoID, "events=cron")
	assert.Equal(t, "/ci/repos/"+repoID+"/pipeline/4/graph", location)

	status, location = redirect(t, repoID, "branch=none")
	assert.Equal(t, http.StatusFound, status)
	assert.Equal(t, "/ci/repos/"+repoID, location)

	status, _ = redirect(t, repoID, "events=bogus")
	assert.Equal(t, http.StatusBadRequest, status)

	status, _ = redirect(t, "nope", "")
	assert.Equal(t, http.StatusBadRequest, status)
}

func TestPatchRepoWorkflowGroups(t *testing.T) {
	s := newTestStore(t)
	repo := seedStatsRepo(t, s, "patch")

	patch := func(t *testing.T, rules []*model.WorkflowGroupRule) int {
		t.Helper()
		tc := newTestContext(t, s)
		withUser(&model.User{Login: "admin"})(tc)
		withRepo(repo, &model.Perm{Admin: true})(tc)
		withRequest(http.MethodPatch, &model.RepoPatch{WorkflowGroups: &rules})(tc)
		PatchRepo(tc.Ctx)
		return tc.Ctx.Writer.Status()
	}

	assert.Equal(t, http.StatusBadRequest, patch(t, []*model.WorkflowGroupRule{{Pattern: `^svc-(`, Group: "build"}}))
	stored, err := s.GetRepo(repo.ID)
	require.NoError(t, err)
	assert.Len(t, stored.WorkflowGroups, 1, "a rejected patch keeps the old rules")

	rules := []*model.WorkflowGroupRule{{Pattern: `^svc-(\w+)`, Group: "build", Subgroup: "$1"}, {Pattern: `^ship`, Group: "deploy"}}
	assert.Equal(t, http.StatusOK, patch(t, rules))
	stored, err = s.GetRepo(repo.ID)
	require.NoError(t, err)
	assert.Equal(t, rules, stored.WorkflowGroups)

	assert.Equal(t, http.StatusOK, patch(t, []*model.WorkflowGroupRule{}))
	stored, err = s.GetRepo(repo.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.WorkflowGroups)
}
