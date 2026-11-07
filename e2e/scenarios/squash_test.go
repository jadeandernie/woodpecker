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

//go:build test

package scenarios

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/e2e/setup"
	forge_types "go.woodpecker-ci.org/woodpecker/v3/server/forge/types"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/pipeline"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
)

// Two path-filtered workflows, so a pipeline's workflows show which changed
// files it was evaluated against.
var (
	squashAppWorkflow = []byte(`
skip_clone: true
when:
  path: 'app/**'
steps:
  - name: build
    image: dummy
    commands:
      - echo app
    environment:
      SLEEP: "3s"
`)

	squashDocsWorkflow = []byte(`
skip_clone: true
when:
  path: 'docs/**'
steps:
  - name: build
    image: dummy
    commands:
      - echo docs
`)
)

func startSquashServer(t *testing.T) *setup.ServerEnv {
	t.Helper()

	env := setup.StartServer(t.Context(), t, []*forge_types.FileMeta{
		{Name: ".woodpecker/app.yaml", Data: squashAppWorkflow},
		{Name: ".woodpecker/docs.yaml", Data: squashDocsWorkflow},
	})

	env.Fixtures.Repo.CancelPreviousPipelineEvents = []model.WebhookEvent{model.EventPush}
	env.Fixtures.Repo.CancelPreviousPipelineSquash = true
	require.NoError(t, env.Store.UpdateRepo(env.Fixtures.Repo), "enable squashing pending pipelines")

	return env
}

func squashPush(env *setup.ServerEnv, commit string, changedFiles ...string) *model.Pipeline {
	p := env.DummyPipeline(model.EventPush)
	p.Commit = commit
	p.ChangedFiles = changedFiles
	return p
}

func workflowNames(t *testing.T, s store.Store, p *model.Pipeline) []string {
	t.Helper()

	workflows, err := s.WorkflowGetTree(p)
	require.NoError(t, err, "workflow tree of pipeline %d", p.ID)
	names := make([]string, 0, len(workflows))
	for _, w := range workflows {
		names = append(names, w.Name)
	}
	return names
}

// TestSquashPendingPipelines pushes three commits to one branch on an agent
// with a single slot, so the second is still pending when the third arrives:
//   - the first pipeline, already running, is left to finish,
//   - the second, pending, is canceled as superseded by the third,
//   - the third is evaluated against the changed files of both, so it runs the
//     docs workflow only the second one's change matched, and nothing is lost.
func TestSquashPendingPipelines(t *testing.T) {
	env := startSquashServer(t)
	agent := setup.StartAgent(t, env.GRPCAddr, setup.WithCapacity(1))
	setup.WaitForAgentRegistered(t, env.Store, agent)

	first, err := pipeline.Create(t.Context(), env.Store, env.Fixtures.Repo, squashPush(env, "c0ffee01", "app/main.go"))
	require.NoError(t, err, "create first pipeline")
	setup.WaitForStepRunning(t, env.Store, first.ID, "build")

	second, err := pipeline.Create(t.Context(), env.Store, env.Fixtures.Repo, squashPush(env, "c0ffee02", "docs/readme.md"))
	require.NoError(t, err, "create second pipeline")
	require.Equal(t, model.StatusPending, second.Status, "second pipeline waits for the busy agent")

	third, err := pipeline.Create(t.Context(), env.Store, env.Fixtures.Repo, squashPush(env, "c0ffee03", "app/util.go"))
	require.NoError(t, err, "create third pipeline")

	superseded, err := env.Store.GetPipeline(second.ID)
	require.NoError(t, err, "get second pipeline")
	assert.Equal(t, model.StatusCanceled, superseded.Status, "pending pipeline is canceled")
	require.NotNil(t, superseded.CancelInfo, "pending pipeline records why it was canceled")
	assert.Equal(t, third.Number, superseded.CancelInfo.SupersededBy, "pending pipeline is superseded by the newest one")

	squashed, err := env.Store.GetPipeline(third.ID)
	require.NoError(t, err, "get third pipeline")
	assert.Equal(t, []string{"app/util.go", "docs/readme.md"}, squashed.ChangedFiles, "newest pipeline carries the superseded changes")
	assert.ElementsMatch(t, []string{"app", "docs"}, workflowNames(t, env.Store, squashed), "newest pipeline runs the workflows of both changes")

	finishedFirst := setup.WaitForPipeline(t, env.Store, first.ID)
	assert.Equal(t, model.StatusSuccess, finishedFirst.Status, "running pipeline is left to finish")
	assert.Nil(t, finishedFirst.CancelInfo, "running pipeline is not superseded")

	finishedThird := setup.WaitForPipeline(t, env.Store, third.ID)
	assert.Equal(t, model.StatusSuccess, finishedThird.Status, "newest pipeline succeeds")
}

// TestSquashGatedPipelines checks that a pipeline waiting for approval
// squashes the pending ones it supersedes only once it is approved, and that a
// blocked pipeline is superseded like a pending one.
func TestSquashGatedPipelines(t *testing.T) {
	env := startSquashServer(t)
	agent := setup.StartAgent(t, env.GRPCAddr)
	setup.WaitForAgentRegistered(t, env.Store, agent)

	env.Fixtures.Repo.RequireApproval = model.RequireApprovalAllEvents
	require.NoError(t, env.Store.UpdateRepo(env.Fixtures.Repo), "enable repo approval")

	first, err := pipeline.Create(t.Context(), env.Store, env.Fixtures.Repo, squashPush(env, "c0ffee01", "docs/readme.md"))
	require.NoError(t, err, "create first pipeline")
	require.Equal(t, model.StatusBlocked, first.Status)

	second, err := pipeline.Create(t.Context(), env.Store, env.Fixtures.Repo, squashPush(env, "c0ffee02", "app/main.go"))
	require.NoError(t, err, "create second pipeline")
	require.Equal(t, model.StatusBlocked, second.Status)

	stillBlocked, err := env.Store.GetPipeline(first.ID)
	require.NoError(t, err, "get first pipeline")
	assert.Equal(t, model.StatusBlocked, stillBlocked.Status, "a blocked pipeline supersedes nothing before approval")

	approved, err := pipeline.Approve(t.Context(), env.Store, second, env.Fixtures.Owner, env.Fixtures.Repo)
	require.NoError(t, err, "approve second pipeline")

	superseded, err := env.Store.GetPipeline(first.ID)
	require.NoError(t, err, "get first pipeline")
	// Cancel reports a pipeline whose workflows were blocked as killed, the
	// same as when it cancels previous pipelines without squashing.
	assert.Equal(t, model.StatusKilled, superseded.Status, "blocked pipeline is canceled once a newer one is approved")
	require.NotNil(t, superseded.CancelInfo)
	assert.Equal(t, second.Number, superseded.CancelInfo.SupersededBy)

	finished := setup.WaitForPipeline(t, env.Store, approved.ID)
	assert.Equal(t, model.StatusSuccess, finished.Status, "approved pipeline succeeds")
	assert.Equal(t, []string{"app/main.go", "docs/readme.md"}, finished.ChangedFiles)
	assert.ElementsMatch(t, []string{"app", "docs"}, workflowNames(t, env.Store, finished))
}

// TestSquashOffCancelsRunningPipelines guards the default: without squashing,
// canceling previous pipelines still kills the running one too.
func TestSquashOffCancelsRunningPipelines(t *testing.T) {
	env := startSquashServer(t)
	env.Fixtures.Repo.CancelPreviousPipelineSquash = false
	require.NoError(t, env.Store.UpdateRepo(env.Fixtures.Repo), "disable squashing")

	agent := setup.StartAgent(t, env.GRPCAddr, setup.WithCapacity(1))
	setup.WaitForAgentRegistered(t, env.Store, agent)

	first, err := pipeline.Create(t.Context(), env.Store, env.Fixtures.Repo, squashPush(env, "c0ffee01", "app/main.go"))
	require.NoError(t, err, "create first pipeline")
	setup.WaitForStepRunning(t, env.Store, first.ID, "build")

	second, err := pipeline.Create(t.Context(), env.Store, env.Fixtures.Repo, squashPush(env, "c0ffee02", "docs/readme.md"))
	require.NoError(t, err, "create second pipeline")

	killed := setup.WaitForPipeline(t, env.Store, first.ID)
	assert.Equal(t, model.StatusKilled, killed.Status, "running pipeline is canceled without squashing")
	require.NotNil(t, killed.CancelInfo)
	assert.Equal(t, second.Number, killed.CancelInfo.SupersededBy)

	finished := setup.WaitForPipeline(t, env.Store, second.ID)
	assert.Equal(t, model.StatusSuccess, finished.Status)
	assert.Equal(t, []string{"docs/readme.md"}, finished.ChangedFiles, "changed files are not carried without squashing")
}
