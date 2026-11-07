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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/builder"
	"go.woodpecker-ci.org/woodpecker/v3/server"
	forge_mocks "go.woodpecker-ci.org/woodpecker/v3/server/forge/mocks"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/queue"
	scheduler_mocks "go.woodpecker-ci.org/woodpecker/v3/server/scheduler/mocks"
	store_mocks "go.woodpecker-ci.org/woodpecker/v3/server/store/mocks"
)

// Evicting a running workflow signals its agent, which then reports the
// workflow done and the server kills every step still pending. Cancel must
// mark pending steps canceled before that signal, otherwise its write from
// the stale workflow tree races with the agent and can leave them killed.
func TestSquashPreviousPipelines(t *testing.T) {
	t.Parallel()

	squashRepo := &model.Repo{
		ID:                           1,
		CancelPreviousPipelineEvents: []model.WebhookEvent{model.EventPush},
		CancelPreviousPipelineSquash: true,
	}

	// as returned by the store: active pipelines, newest first
	newer := &model.Pipeline{ID: 11, Number: 11, Event: model.EventPush, Branch: "main", Status: model.StatusPending, ChangedFiles: []string{"z.go"}}
	noChanges := &model.Pipeline{ID: 6, Number: 6, Event: model.EventPush, Branch: "main", Status: model.StatusPending}
	tag := &model.Pipeline{ID: 5, Number: 5, Event: model.EventTag, Branch: "main", Status: model.StatusPending, ChangedFiles: []string{"t.go"}}
	otherBranch := &model.Pipeline{ID: 4, Number: 4, Event: model.EventPush, Branch: "dev", Status: model.StatusPending, ChangedFiles: []string{"d.go"}}
	blocked := &model.Pipeline{ID: 3, Number: 3, Event: model.EventPush, Branch: "main", Status: model.StatusBlocked, ChangedFiles: []string{"b.go"}}
	pending := &model.Pipeline{ID: 2, Number: 2, Event: model.EventPush, Branch: "main", Status: model.StatusPending, ChangedFiles: []string{"a.go", "c.go"}}
	running := &model.Pipeline{ID: 1, Number: 1, Event: model.EventPush, Branch: "main", Status: model.StatusRunning, ChangedFiles: []string{"r.go"}}

	active := func(self *model.Pipeline) []*model.Pipeline {
		return []*model.Pipeline{newer, self, noChanges, tag, otherBranch, blocked, pending, running}
	}

	t.Run("squashes older pending and blocked pipelines of the same branch", func(t *testing.T) {
		t.Parallel()

		self := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPush, Branch: "main", Status: model.StatusPending, ChangedFiles: []string{"c.go"}}
		s := store_mocks.NewMockStore(t)
		s.EXPECT().GetActivePipelineList(squashRepo).Return(active(self), nil).Once()

		superseded := squashPreviousPipelines(s, squashRepo, self)

		assert.Equal(t, []*model.Pipeline{blocked, pending}, superseded)
		assert.Equal(t, []string{"c.go", "b.go", "a.go"}, self.ChangedFiles)
		assert.Equal(t, []string{"a.go", "c.go"}, pending.ChangedFiles, "superseded pipelines keep their own files")
	})

	t.Run("pipeline without changed files only squashes those without", func(t *testing.T) {
		t.Parallel()

		self := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPush, Branch: "main", Status: model.StatusPending}
		s := store_mocks.NewMockStore(t)
		s.EXPECT().GetActivePipelineList(squashRepo).Return(active(self), nil).Once()

		superseded := squashPreviousPipelines(s, squashRepo, self)

		assert.Equal(t, []*model.Pipeline{noChanges}, superseded)
		assert.Empty(t, self.ChangedFiles)
	})

	t.Run("nothing to squash", func(t *testing.T) {
		t.Parallel()

		self := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPush, Branch: "release", Status: model.StatusPending, ChangedFiles: []string{"c.go"}}
		s := store_mocks.NewMockStore(t)
		s.EXPECT().GetActivePipelineList(squashRepo).Return(active(self), nil).Once()

		assert.Empty(t, squashPreviousPipelines(s, squashRepo, self))
		assert.Equal(t, []string{"c.go"}, self.ChangedFiles)
	})

	t.Run("setting off", func(t *testing.T) {
		t.Parallel()

		repo := &model.Repo{ID: 1, CancelPreviousPipelineEvents: []model.WebhookEvent{model.EventPush}}
		self := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPush, Branch: "main", ChangedFiles: []string{"c.go"}}
		s := store_mocks.NewMockStore(t)

		assert.Nil(t, squashPreviousPipelines(s, repo, self))
		assert.Equal(t, []string{"c.go"}, self.ChangedFiles)
	})

	t.Run("event does not cancel previous pipelines", func(t *testing.T) {
		t.Parallel()

		self := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPull, Refspec: "feature:main", ChangedFiles: []string{"c.go"}}
		s := store_mocks.NewMockStore(t)

		assert.Nil(t, squashPreviousPipelines(s, squashRepo, self))
		assert.Equal(t, []string{"c.go"}, self.ChangedFiles)
	})
}

// A superseded pipeline that an agent picked up between the squash and the
// cancel is left running: the new pipeline already covers its changes.
func TestCancelSupersededPipelines(t *testing.T) {
	repo := &model.Repo{ID: 1}
	user := &model.User{ID: 1}
	pipeline := &model.Pipeline{ID: 10, Number: 10}
	stillPending := &model.Pipeline{ID: 2, Number: 2, Status: model.StatusPending}
	startedMeanwhile := &model.Pipeline{ID: 3, Number: 3, Status: model.StatusPending}

	s := store_mocks.NewMockStore(t)
	s.EXPECT().GetPipeline(int64(3)).Return(&model.Pipeline{ID: 3, Number: 3, Status: model.StatusRunning}, nil).Once()
	s.EXPECT().GetPipeline(int64(2)).Return(&model.Pipeline{ID: 2, Number: 2, Status: model.StatusPending}, nil).Once()
	s.EXPECT().WorkflowGetTree(mock.MatchedBy(func(p *model.Pipeline) bool { return p.ID == 2 })).Return(nil, nil).Twice()
	s.EXPECT().UpdatePipeline(mock.MatchedBy(func(p *model.Pipeline) bool {
		return p.ID == 2 && p.Status == model.StatusCanceled && p.CancelInfo != nil && p.CancelInfo.SupersededBy == 10
	})).Return(nil).Once()

	scheduler := scheduler_mocks.NewMockScheduler(t)
	scheduler.EXPECT().CancelWorkflows(mock.Anything, mock.Anything).Return(nil).Once()
	scheduler.EXPECT().PublishPipelineEvent(mock.Anything, repo, mock.MatchedBy(func(p *model.Pipeline) bool { return p.ID == 2 })).Return(nil).Once()

	forge := forge_mocks.NewMockForge(t)

	previous := server.Config.Services.Scheduler
	server.Config.Services.Scheduler = scheduler
	t.Cleanup(func() { server.Config.Services.Scheduler = previous })

	cancelSupersededPipelines(t.Context(), forge, s, pipeline, []*model.Pipeline{startedMeanwhile, stillPending}, repo, user)
}

// With squashing on, start cancels exactly the superseded pipelines and only
// once the new pipeline is queued, never other active pipelines of the branch.
func TestStartSquash(t *testing.T) {
	repo := &model.Repo{
		ID:                           1,
		OrgID:                        1,
		CancelPreviousPipelineEvents: []model.WebhookEvent{model.EventPush},
		CancelPreviousPipelineSquash: true,
	}
	user := &model.User{ID: 1}
	pipeline := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPush, Branch: "main"}
	superseded := &model.Pipeline{ID: 2, Number: 2, Event: model.EventPush, Branch: "main", Status: model.StatusPending}

	t.Run("cancels superseded pipelines after queuing", func(t *testing.T) {
		s := store_mocks.NewMockStore(t)
		s.EXPECT().GetPipeline(int64(2)).Return(&model.Pipeline{ID: 2, Number: 2, Status: model.StatusPending}, nil).Once()
		s.EXPECT().WorkflowGetTree(mock.Anything).Return(nil, nil).Twice()
		s.EXPECT().UpdatePipeline(mock.MatchedBy(func(p *model.Pipeline) bool {
			return p.ID == 2 && p.Status == model.StatusCanceled && p.CancelInfo.SupersededBy == 10
		})).Return(nil).Once()

		scheduler := scheduler_mocks.NewMockScheduler(t)
		scheduler.EXPECT().Info(mock.Anything).Return(queue.InfoT{}).Once()
		queued := scheduler.EXPECT().StartPipeline(mock.Anything, repo, pipeline, mock.Anything).Return(nil).Once()
		scheduler.EXPECT().CancelWorkflows(mock.Anything, mock.Anything).Return(nil).Once().NotBefore(queued)
		scheduler.EXPECT().PublishPipelineEvent(mock.Anything, repo, mock.Anything).Return(nil).Once()

		previous := server.Config.Services.Scheduler
		server.Config.Services.Scheduler = scheduler
		t.Cleanup(func() { server.Config.Services.Scheduler = previous })

		_, err := start(t.Context(), forge_mocks.NewMockForge(t), s, pipeline, user, repo, nil, []*model.Pipeline{superseded})
		assert.NoError(t, err)
	})

	t.Run("cancels nothing if queuing fails", func(t *testing.T) {
		s := store_mocks.NewMockStore(t)

		scheduler := scheduler_mocks.NewMockScheduler(t)
		scheduler.EXPECT().Info(mock.Anything).Return(queue.InfoT{}).Once()
		scheduler.EXPECT().StartPipeline(mock.Anything, repo, pipeline, mock.Anything).Return(assert.AnError).Once()

		previous := server.Config.Services.Scheduler
		server.Config.Services.Scheduler = scheduler
		t.Cleanup(func() { server.Config.Services.Scheduler = previous })

		_, err := start(t.Context(), forge_mocks.NewMockForge(t), s, pipeline, user, repo, nil, []*model.Pipeline{superseded})
		assert.ErrorIs(t, err, assert.AnError)
	})

	items := []*builder.Item{
		{Workflow: &builder.Workflow{ID: 1, Name: "build"}},
		{Workflow: &builder.Workflow{ID: 2, Name: "deploy"}},
	}
	// queuedTasks captures the tasks start queues for the pipeline. The queue
	// holds inQueue; nil means start must not look at it.
	queuedTasks := func(t *testing.T, pipeline *model.Pipeline, inQueue *queue.InfoT) *[]*model.Task {
		t.Helper()
		var queued []*model.Task
		scheduler := scheduler_mocks.NewMockScheduler(t)
		if inQueue != nil {
			scheduler.EXPECT().Info(mock.Anything).Return(*inQueue).Once()
		}
		scheduler.EXPECT().StartPipeline(mock.Anything, repo, pipeline, mock.Anything).
			Run(func(_ context.Context, _ *model.Repo, _ *model.Pipeline, tasks []*model.Task) { queued = tasks }).
			Return(nil).Once()

		previous := server.Config.Services.Scheduler
		server.Config.Services.Scheduler = scheduler
		t.Cleanup(func() { server.Config.Services.Scheduler = previous })
		return &queued
	}

	t.Run("queues in the place of the oldest superseded pipeline", func(t *testing.T) {
		squashing := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPush, Branch: "main", Created: 400}
		oldest := &model.Pipeline{ID: 2, Number: 2, Event: model.EventPush, Branch: "main", Status: model.StatusPending, Created: 200}
		newer := &model.Pipeline{ID: 3, Number: 3, Event: model.EventPush, Branch: "main", Status: model.StatusBlocked, Created: 300}

		// both started in the meantime, so neither is canceled
		s := store_mocks.NewMockStore(t)
		s.EXPECT().GetPipeline(int64(3)).Return(&model.Pipeline{ID: 3, Number: 3, Status: model.StatusRunning}, nil).Once()
		s.EXPECT().GetPipeline(int64(2)).Return(&model.Pipeline{ID: 2, Number: 2, Status: model.StatusRunning}, nil).Once()
		queued := queuedTasks(t, squashing, &queue.InfoT{})

		_, err := start(t.Context(), forge_mocks.NewMockForge(t), s, squashing, user, repo, items, []*model.Pipeline{newer, oldest})
		assert.NoError(t, err)

		if assert.Len(t, *queued, 2) {
			for _, task := range *queued {
				assert.Equal(t, int64(10), task.PipelineID)
				assert.Equal(t, int64(200), task.Created, "task %s must be ordered like the oldest superseded pipeline", task.Name)
				assert.Equal(t, int64(2), task.QueuedInPlaceOf, "task %s must take the place of the oldest superseded pipeline", task.Name)
			}
		}
		assert.Equal(t, int64(400), squashing.Created, "the pipeline keeps its own creation time")
	})

	t.Run("takes over the place a superseded pipeline holds in the queue", func(t *testing.T) {
		// 3 squashed 1 and holds its place, then 10 squashes 3
		squashing := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPush, Branch: "main", Created: 400}
		previous := &model.Pipeline{ID: 3, Number: 3, Event: model.EventPush, Branch: "main", Status: model.StatusPending, Created: 300}

		s := store_mocks.NewMockStore(t)
		s.EXPECT().GetPipeline(int64(3)).Return(&model.Pipeline{ID: 3, Number: 3, Status: model.StatusRunning}, nil).Once()
		queued := queuedTasks(t, squashing, &queue.InfoT{
			Pending: []*model.Task{
				{ID: "40", PipelineID: 4, Created: 50},
				{ID: "30", PipelineID: 3, Created: 100, QueuedInPlaceOf: 1},
				{ID: "50", PipelineID: 5, Created: 150},
			},
			WaitingOnDeps: []*model.Task{
				{ID: "31", PipelineID: 3, Created: 100, QueuedInPlaceOf: 1},
			},
		})

		_, err := start(t.Context(), forge_mocks.NewMockForge(t), s, squashing, user, repo, items, []*model.Pipeline{previous})
		assert.NoError(t, err)

		if assert.Len(t, *queued, 2) {
			for _, task := range *queued {
				assert.Equal(t, int64(100), task.Created, "task %s must be ordered like the place pipeline 3 holds", task.Name)
				assert.Equal(t, int64(1), task.QueuedInPlaceOf, "task %s must take the place pipeline 3 holds", task.Name)
			}
		}
		assert.Equal(t, int64(400), squashing.Created, "the pipeline keeps its own creation time")
	})

	t.Run("pipeline that supersedes none keeps its own place", func(t *testing.T) {
		own := &model.Pipeline{ID: 10, Number: 10, Event: model.EventPush, Branch: "main", Created: 400}
		queued := queuedTasks(t, own, nil)

		_, err := start(t.Context(), forge_mocks.NewMockForge(t), store_mocks.NewMockStore(t), own, user, repo, items, nil)
		assert.NoError(t, err)

		if assert.Len(t, *queued, 2) {
			for _, task := range *queued {
				assert.Equal(t, int64(400), task.Created)
				assert.Zero(t, task.QueuedInPlaceOf)
			}
		}
	})
}
