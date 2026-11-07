// Copyright 2026 Woodpecker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pipeline

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	backend_types "go.woodpecker-ci.org/woodpecker/v3/pipeline/backend/types"
	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/builder"
	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/yaml/constraint"
	"go.woodpecker-ci.org/woodpecker/v3/server"
	forge_mocks "go.woodpecker-ci.org/woodpecker/v3/server/forge/mocks"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/pubsub/memory"
	"go.woodpecker-ci.org/woodpecker/v3/server/queue"
	"go.woodpecker-ci.org/woodpecker/v3/server/scheduler"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
	"go.woodpecker-ci.org/woodpecker/v3/server/store/datastore"
)

// groupEnv is a server with a store, a queue and a scheduler, in which the
// tests start pipelines and play the agents.
type groupEnv struct {
	t     *testing.T
	ctx   context.Context
	store store.Store
	queue queue.Queue
	forge *forge_mocks.MockForge
	repo  *model.Repo
	user  *model.User
}

func newGroupEnv(t *testing.T) *groupEnv {
	ctx := t.Context()

	s, err := datastore.NewEngine(&store.Opts{
		Driver: datastore.DriverSqlite,
		Config: ":memory:",
		// an in-memory sqlite database lives as long as its one connection
		XORM: store.XORM{MaxOpenConns: 1, MaxIdleConns: 1},
	})
	require.NoError(t, err)
	require.NoError(t, s.Migrate(ctx, true))
	t.Cleanup(func() { _ = s.Close() })

	repo := &model.Repo{Owner: "jade", Name: "app", FullName: "jade/app", ForgeRemoteID: "1", Branch: "main"}
	require.NoError(t, s.CreateRepo(repo))

	f := forge_mocks.NewMockForge(t)
	f.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	env := &groupEnv{t: t, ctx: ctx, store: s, forge: f, repo: repo, user: &model.User{Login: "jade"}}
	previous := server.Config.Services.Scheduler
	t.Cleanup(func() { server.Config.Services.Scheduler = previous })
	env.restart(queue.NewMemoryQueue(ctx))
	return env
}

// restart replaces the queue and the scheduler, as a server restart does.
func (env *groupEnv) restart(q queue.Queue) {
	env.queue = q
	server.Config.Services.Scheduler = scheduler.NewScheduler(env.ctx, env.store, q, memory.New())
}

// wf is a workflow of a pipeline the tests create.
type wf struct {
	name  string
	group string
	limit int
	deps  []string
}

// create stores a pipeline of the branch with the given workflows, as a
// webhook does before starting it.
func (env *groupEnv) create(branch string, workflows ...wf) (*model.Pipeline, []*builder.Item) {
	env.t.Helper()

	pipeline := &model.Pipeline{
		RepoID:  env.repo.ID,
		Event:   model.EventPush,
		Branch:  branch,
		Status:  model.StatusPending,
		Created: time.Now().Unix(),
	}
	require.NoError(env.t, env.store.CreatePipeline(pipeline))

	items := make([]*builder.Item, 0, len(workflows))
	for i, workflow := range workflows {
		var deps constraint.DependsOn
		for _, dep := range workflow.deps {
			deps = append(deps, constraint.Dependency{Name: dep})
		}
		items = append(items, &builder.Item{
			Workflow:         &builder.Workflow{PID: i + 1, Name: workflow.name},
			DependsOn:        deps,
			RunsOn:           []string{"success"},
			ConcurrencyGroup: workflow.group,
			ConcurrencyLimit: workflow.limit,
			Config: &backend_types.Config{Stages: []*backend_types.Stage{{Steps: []*backend_types.Step{
				{Name: workflow.name, UUID: fmt.Sprintf("%d-%s", pipeline.Number, workflow.name)},
			}}}},
		})
	}
	pipeline, err := saveWorkflowsFromPipelineBuilder(env.store, pipeline, items, false)
	require.NoError(env.t, err)
	return pipeline, items
}

// start starts a pipeline created before.
func (env *groupEnv) start(pipeline *model.Pipeline, items []*builder.Item, squashed ...*model.Pipeline) *model.Pipeline {
	env.t.Helper()
	started, err := start(env.ctx, env.forge, env.store, pipeline, env.user, env.repo, items, squashed)
	require.NoError(env.t, err)
	return started
}

// run creates and starts a pipeline of the branch.
func (env *groupEnv) run(branch string, workflows ...wf) *model.Pipeline {
	env.t.Helper()
	return env.start(env.create(branch, workflows...))
}

// workflow loads the pipeline's workflow of the given name from the store.
func (env *groupEnv) workflow(pipeline *model.Pipeline, name string) *model.Workflow {
	env.t.Helper()
	workflows, err := env.store.WorkflowGetTree(pipeline)
	require.NoError(env.t, err)
	i := slices.IndexFunc(workflows, func(w *model.Workflow) bool { return w.Name == name })
	require.NotEqual(env.t, -1, i, "pipeline #%d has no workflow %s", pipeline.Number, name)
	return workflows[i]
}

// poll plays an agent that wants the pipeline's workflow of the given name,
// and reports whether the queue handed it out.
func (env *groupEnv) poll(pipeline *model.Pipeline, name string, wait time.Duration) bool {
	env.t.Helper()
	id := fmt.Sprint(env.workflow(pipeline, name).ID)
	ctx, cancel := context.WithTimeout(env.ctx, wait)
	defer cancel()
	task, err := env.queue.Poll(ctx, 1, func(task *model.Task) (bool, int) { return task.ID == id, 1 })
	return err == nil && task != nil
}

// startWorkflow hands out the pipeline's workflow of the given name to an
// agent, which starts it.
func (env *groupEnv) startWorkflow(pipeline *model.Pipeline, name string) {
	env.t.Helper()
	require.True(env.t, env.poll(pipeline, name, 2*time.Second), "queue hands out %s of #%d", name, pipeline.Number)
	workflow := env.workflow(pipeline, name)
	workflow.State = model.StatusRunning
	workflow.Started = time.Now().Unix()
	require.NoError(env.t, env.store.WorkflowUpdate(workflow))
	env.setPipelineStatus(pipeline, model.StatusRunning)
}

// finishWorkflow makes the agent report the pipeline's workflow of the given
// name done.
func (env *groupEnv) finishWorkflow(pipeline *model.Pipeline, name string, status model.StatusValue) {
	env.t.Helper()
	workflow := env.workflow(pipeline, name)
	workflow.State = status
	workflow.Finished = time.Now().Unix()
	require.NoError(env.t, env.store.WorkflowUpdate(workflow))
	require.NoError(env.t, env.queue.Done(env.ctx, fmt.Sprint(workflow.ID), status))
}

func (env *groupEnv) setPipelineStatus(pipeline *model.Pipeline, status model.StatusValue) {
	env.t.Helper()
	stored, err := env.store.GetPipeline(pipeline.ID)
	require.NoError(env.t, err)
	stored.Status = status
	require.NoError(env.t, env.store.UpdatePipeline(stored))
}

func (env *groupEnv) pipelineStatus(pipeline *model.Pipeline) model.StatusValue {
	env.t.Helper()
	stored, err := env.store.GetPipeline(pipeline.ID)
	require.NoError(env.t, err)
	return stored.Status
}

// queued reports whether the queue has the pipeline's workflow of the given
// name waiting to be handed out.
func (env *groupEnv) queued(pipeline *model.Pipeline, name string) bool {
	env.t.Helper()
	id := fmt.Sprint(env.workflow(pipeline, name).ID)
	info := env.queue.Info(env.ctx)
	return slices.ContainsFunc(slices.Concat(info.Pending, info.WaitingOnDeps), func(task *model.Task) bool { return task.ID == id })
}

// assertSuperseded asserts that the pipeline's workflow of the given name and
// its steps were skipped as superseded by the given pipeline, and are no
// longer queued.
func (env *groupEnv) assertSuperseded(pipeline *model.Pipeline, name string, by *model.Pipeline) {
	env.t.Helper()
	workflow := env.workflow(pipeline, name)
	assert.Equal(env.t, model.StatusSkipped, workflow.State, "%s of #%d", name, pipeline.Number)
	assert.Equal(env.t, fmt.Sprintf("superseded by #%d", by.Number), workflow.Error, "%s of #%d", name, pipeline.Number)
	for _, step := range workflow.Children {
		assert.Equal(env.t, model.StatusSkipped, step.State, "step %s of #%d", step.Name, pipeline.Number)
		assert.Equal(env.t, workflow.Error, step.Error, "step %s of #%d", step.Name, pipeline.Number)
	}
	assert.False(env.t, env.queued(pipeline, name), "%s of #%d is no longer queued", name, pipeline.Number)
}

// assertQueued asserts that the pipeline's workflow of the given name is still
// pending in the store and the queue.
func (env *groupEnv) assertQueued(pipeline *model.Pipeline, name string) {
	env.t.Helper()
	workflow := env.workflow(pipeline, name)
	assert.Equal(env.t, model.StatusPending, workflow.State, "%s of #%d", name, pipeline.Number)
	assert.Empty(env.t, workflow.Error, "%s of #%d", name, pipeline.Number)
	assert.True(env.t, env.queued(pipeline, name), "%s of #%d is queued", name, pipeline.Number)
}

// The workflows of a pipeline that builds, then publishes and deploys an
// image, the latter two in a concurrency group.
var deployChain = []wf{
	{name: "build"},
	{name: "publish", group: "deploy", deps: []string{"build"}},
	{name: "deploy", group: "deploy", deps: []string{"publish"}},
}

// short is how long a test waits for the queue to not hand out a workflow.
const short = 400 * time.Millisecond

func TestConcurrencyGroupSupersedesQueuedUnits(t *testing.T) {
	t.Run("a newer pipeline supersedes the queued unit, not the builds", func(t *testing.T) {
		env := newGroupEnv(t)

		first := env.run("main", deployChain...)
		env.startWorkflow(first, "build")
		second := env.run("main", deployChain...)

		env.assertSuperseded(first, "publish", second)
		env.assertSuperseded(first, "deploy", second)
		assert.Equal(t, model.StatusRunning, env.workflow(first, "build").State)
		assert.Equal(t, model.StatusRunning, env.pipelineStatus(first), "its build still runs")
		for _, name := range []string{"build", "publish", "deploy"} {
			env.assertQueued(second, name)
		}

		// a third supersedes the second's, the first's stays superseded by #2
		env.startWorkflow(second, "build")
		third := env.run("main", deployChain...)
		env.assertSuperseded(second, "publish", third)
		env.assertSuperseded(second, "deploy", third)
		env.assertSuperseded(first, "publish", second)
		env.assertQueued(third, "publish")
	})

	t.Run("a pipeline with nothing else left to run is done", func(t *testing.T) {
		env := newGroupEnv(t)

		first := env.run("main", deployChain...)
		env.startWorkflow(first, "build")
		env.finishWorkflow(first, "build", model.StatusSuccess)
		second := env.run("main", deployChain...)

		env.assertSuperseded(first, "publish", second)
		assert.Equal(t, model.StatusSuccess, env.pipelineStatus(first))

		third := env.run("main", wf{name: "deploy", group: "deploy"})
		env.assertSuperseded(second, "deploy", third)
		assert.Equal(t, model.StatusSkipped, env.workflow(second, "publish").State)
		assert.Equal(t, model.StatusPending, env.workflow(second, "build").State)
		assert.Equal(t, model.StatusPending, env.pipelineStatus(second), "its build is still queued")

		fourth := env.run("main", wf{name: "deploy", group: "deploy"})
		env.assertSuperseded(third, "deploy", fourth)
		assert.Equal(t, model.StatusSkipped, env.pipelineStatus(third), "none of its workflows ran")
	})

	t.Run("a unit in progress is waited for, not superseded", func(t *testing.T) {
		env := newGroupEnv(t)

		first := env.run("main", deployChain...)
		env.startWorkflow(first, "build")
		env.finishWorkflow(first, "build", model.StatusSuccess)
		env.startWorkflow(first, "publish")

		second := env.run("main", deployChain...)
		env.assertQueued(first, "deploy")
		env.startWorkflow(second, "build")
		env.finishWorkflow(second, "build", model.StatusSuccess)
		assert.False(t, env.poll(second, "publish", short), "the first pipeline's unit is in progress")

		// its own deploy is not held back by itself
		env.finishWorkflow(first, "publish", model.StatusSuccess)
		env.startWorkflow(first, "deploy")
		assert.False(t, env.poll(second, "publish", short), "the first pipeline's deploy runs")

		// one in progress, one queued: a third pipeline replaces the queued one
		third := env.run("main", deployChain...)
		env.assertSuperseded(second, "publish", third)
		env.assertSuperseded(second, "deploy", third)
		assert.Equal(t, model.StatusSuccess, env.pipelineStatus(second))
		assert.Equal(t, model.StatusRunning, env.workflow(first, "deploy").State)

		env.startWorkflow(third, "build")
		env.finishWorkflow(third, "build", model.StatusSuccess)
		assert.False(t, env.poll(third, "publish", short))

		// whatever its result
		env.finishWorkflow(first, "deploy", model.StatusFailure)
		env.startWorkflow(third, "publish")
	})

	t.Run("an older pipeline never supersedes a newer one", func(t *testing.T) {
		env := newGroupEnv(t)

		// the older pipeline is started after the newer one, as on approval
		older, olderItems := env.create("main", deployChain...)
		newer := env.run("main", deployChain...)
		older = env.start(older, olderItems)

		env.assertQueued(newer, "publish")
		env.assertQueued(newer, "deploy")
		env.assertSuperseded(older, "publish", newer)
		env.assertSuperseded(older, "deploy", newer)
		env.assertQueued(older, "build")
	})

	t.Run("an older pipeline does not deploy after a newer one in progress", func(t *testing.T) {
		env := newGroupEnv(t)

		older, olderItems := env.create("main", wf{name: "deploy", group: "deploy"})
		newer := env.run("main", wf{name: "deploy", group: "deploy"})
		env.startWorkflow(newer, "deploy")
		older = env.start(older, olderItems)

		env.assertSuperseded(older, "deploy", newer)
		assert.Equal(t, model.StatusSkipped, env.pipelineStatus(older))
	})

	t.Run("groups are independent", func(t *testing.T) {
		env := newGroupEnv(t)

		first := env.run("main", wf{name: "deploy-a", group: "a"}, wf{name: "deploy-b", group: "b"})
		second := env.run("main", wf{name: "deploy-a", group: "a"})

		env.assertSuperseded(first, "deploy-a", second)
		env.assertQueued(first, "deploy-b")
		env.assertQueued(second, "deploy-a")
	})

	t.Run("workflows in no group are untouched", func(t *testing.T) {
		env := newGroupEnv(t)

		first := env.run("main", wf{name: "build"}, wf{name: "deploy"})
		second := env.run("main", wf{name: "build"}, wf{name: "deploy"})

		env.assertQueued(first, "deploy")
		env.assertQueued(second, "deploy")
	})

	t.Run("a group with a limit supersedes nothing", func(t *testing.T) {
		env := newGroupEnv(t)

		first := env.run("main", wf{name: "deploy", group: "deploy", limit: 1})
		env.run("main", wf{name: "deploy", group: "deploy", limit: 1})

		env.assertQueued(first, "deploy")
		assert.Empty(t, env.workflow(first, "deploy").ConcurrencyGroup)
	})

	t.Run("a squashed pipeline is left to its cancellation", func(t *testing.T) {
		env := newGroupEnv(t)

		first := env.run("main", deployChain...)
		second, items := env.create("main", deployChain...)
		env.start(second, items, first)

		assert.Equal(t, model.StatusCanceled, env.pipelineStatus(first))
		for _, name := range []string{"publish", "deploy"} {
			assert.Empty(t, env.workflow(first, name).Error, name)
		}
		env.assertQueued(second, "publish")
	})
}

// Pipelines created before the server knew about concurrency groups, or before
// their workflows declared one, have the workflows in no group. The first
// pipeline of a branch with workflows of the same names in a group treats them
// as its group's.
func TestConcurrencyGroupBacklog(t *testing.T) {
	legacyChain := []wf{
		{name: "build"},
		{name: "publish", deps: []string{"build"}},
		{name: "deploy", deps: []string{"publish"}},
	}

	env := newGroupEnv(t)

	inProgress := env.run("main", legacyChain...)
	env.startWorkflow(inProgress, "build")
	env.finishWorkflow(inProgress, "build", model.StatusSuccess)
	env.startWorkflow(inProgress, "publish")
	queuedOne := env.run("main", legacyChain...)
	env.startWorkflow(queuedOne, "build")
	queuedTwo := env.run("main", legacyChain...)
	otherBranch := env.run("dev", legacyChain...)

	grouped := env.run("main", deployChain...)

	env.assertSuperseded(queuedOne, "publish", grouped)
	env.assertSuperseded(queuedOne, "deploy", grouped)
	env.assertSuperseded(queuedTwo, "publish", grouped)
	env.assertSuperseded(queuedTwo, "deploy", grouped)
	env.assertQueued(queuedTwo, "build")
	env.assertQueued(otherBranch, "publish")
	env.assertQueued(inProgress, "deploy")

	// the unit in progress holds the group
	env.startWorkflow(grouped, "build")
	env.finishWorkflow(grouped, "build", model.StatusSuccess)
	assert.False(t, env.poll(grouped, "publish", short))
	env.finishWorkflow(inProgress, "publish", model.StatusSuccess)
	env.startWorkflow(inProgress, "deploy")
	assert.False(t, env.poll(grouped, "publish", short))
	env.finishWorkflow(inProgress, "deploy", model.StatusSuccess)
	env.startWorkflow(grouped, "publish")
}

// After a server restart the queue only has the workflows that were not handed
// out yet. The pipelines whose units were in progress hold their groups again
// until their agents report the workflows done.
func TestConcurrencyGroupRestart(t *testing.T) {
	env := newGroupEnv(t)

	first := env.run("main", deployChain...)
	env.startWorkflow(first, "build")
	env.finishWorkflow(first, "build", model.StatusSuccess)
	env.startWorkflow(first, "publish")
	second := env.run("main", deployChain...)
	env.startWorkflow(second, "build")
	env.finishWorkflow(second, "build", model.StatusSuccess)

	// the restored queue has the first pipeline's deploy and the second's
	// publish and deploy, but not the first's publish
	info := env.queue.Info(env.ctx)
	restored := queue.NewMemoryQueue(env.ctx)
	require.NoError(t, restored.PushAtOnce(env.ctx, slices.Concat(info.Pending, info.WaitingOnDeps)))
	env.restart(restored)

	HoldGroupsInProgress(env.ctx, env.store)

	assert.False(t, env.poll(second, "publish", short), "the first pipeline's unit is in progress")
	publish := fmt.Sprint(env.workflow(first, "publish").ID)
	require.NoError(t, env.queue.Extend(env.ctx, 1, publish), "its agent extends the publish it runs")
	env.finishWorkflow(first, "publish", model.StatusSuccess)
	env.startWorkflow(first, "deploy")
	assert.False(t, env.poll(second, "publish", short))
	env.finishWorkflow(first, "deploy", model.StatusSuccess)
	env.startWorkflow(second, "publish")

	// the first new pipeline after the restart supersedes the queued units
	// as usual
	third := env.run("main", deployChain...)
	env.assertQueued(second, "deploy")
	env.assertQueued(third, "publish")
}
