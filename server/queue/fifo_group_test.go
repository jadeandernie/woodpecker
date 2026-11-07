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

package queue

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// groupTask returns a task of the pipeline in the concurrency group without a
// limit, depending on the given tasks.
func groupTask(id string, pipelineID, created int64, group string, deps ...string) *model.Task {
	return &model.Task{
		ID:               id,
		PipelineID:       pipelineID,
		Created:          created,
		ConcurrencyGroup: group,
		Dependencies:     deps,
		DepStatus:        map[string]model.StatusValue{},
		RunOn:            []string{"success", "failure"},
	}
}

// assertNoTask asserts that an agent polling the queue gets no task.
func assertNoTask(ctx context.Context, t *testing.T, q *fifo, msg string) {
	t.Helper()
	pollCtx, cancel := context.WithTimeout(ctx, 3*processTimeInterval)
	defer cancel()
	got, err := q.Poll(pollCtx, 99, filterFnTrue)
	if !assert.Error(t, err, msg) {
		t.Logf("got task %s", got.ID)
	}
}

// poll returns the task the queue hands to an agent.
func poll(ctx context.Context, t *testing.T, q *fifo) *model.Task {
	t.Helper()
	pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, err := q.Poll(pollCtx, 1, filterFnTrue)
	require.NoError(t, err)
	return got
}

func TestFifoExclusiveGroup(t *testing.T) {
	t.Run("one pipeline at a time, its own workflows in order", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		// pipeline 1 publishes then deploys, pipeline 2 builds, then publishes
		// and deploys
		publish1 := groupTask("11", 1, 100, "1//deploy")
		deploy1 := groupTask("12", 1, 100, "1//deploy", "11")
		build2 := &model.Task{ID: "20", PipelineID: 2, Created: 200}
		publish2 := groupTask("21", 2, 200, "1//deploy", "20")
		deploy2 := groupTask("22", 2, 200, "1//deploy", "21")
		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{publish1, deploy1, build2, publish2, deploy2}))

		assert.Equal(t, "11", poll(ctx, t, q).ID)
		// workflows outside the group run meanwhile
		assert.Equal(t, "20", poll(ctx, t, q).ID)
		require.NoError(t, q.Done(ctx, "20", model.StatusSuccess))
		assertNoTask(ctx, t, q, "pipeline 2's publish waits for pipeline 1's deploy")

		require.NoError(t, q.Done(ctx, "11", model.StatusSuccess))
		assert.Equal(t, "12", poll(ctx, t, q).ID, "a pipeline does not wait for itself")
		assertNoTask(ctx, t, q, "pipeline 1 holds the group until its deploy is done")

		require.NoError(t, q.Done(ctx, "12", model.StatusFailure))
		assert.Equal(t, "21", poll(ctx, t, q).ID, "whatever pipeline 1's result")
		require.NoError(t, q.Done(ctx, "21", model.StatusSuccess))
		assert.Equal(t, "22", poll(ctx, t, q).ID)
		require.NoError(t, q.Done(ctx, "22", model.StatusSuccess))

		assert.Empty(t, q.holds)
	})

	t.Run("a canceled pipeline releases the group", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		publish1 := groupTask("11", 1, 100, "1//deploy")
		deploy1 := groupTask("12", 1, 100, "1//deploy", "11")
		publish2 := groupTask("21", 2, 200, "1//deploy")
		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{publish1, deploy1, publish2}))

		assert.Equal(t, "11", poll(ctx, t, q).ID)
		assertNoTask(ctx, t, q, "pipeline 2 waits")

		require.NoError(t, q.ErrorAtOnce(ctx, []string{"11", "12"}, ErrCancel))
		assert.Equal(t, "21", poll(ctx, t, q).ID)
	})

	t.Run("a task that will be skipped neither waits nor holds", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		publish1 := groupTask("11", 1, 100, "1//deploy")
		build2 := &model.Task{ID: "20", PipelineID: 2, Created: 200}
		publish2 := groupTask("21", 2, 200, "1//deploy", "20")
		publish2.RunOn = []string{"success"}
		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{publish1, build2, publish2}))

		assert.Equal(t, "11", poll(ctx, t, q).ID)
		assert.Equal(t, "20", poll(ctx, t, q).ID)
		require.NoError(t, q.Done(ctx, "20", model.StatusFailure))
		got := poll(ctx, t, q)
		assert.Equal(t, "21", got.ID, "handed out to be marked skipped")
		assert.False(t, got.ShouldRun())
		assert.NotContains(t, q.holds["1//deploy"], int64(2))
	})

	t.Run("groups and repos are independent", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{
			groupTask("11", 1, 100, "1//deploy-a"),
			groupTask("21", 2, 200, "1//deploy-b"),
			groupTask("31", 3, 300, "2//deploy-a"),
		}))

		assert.Equal(t, "11", poll(ctx, t, q).ID)
		assert.Equal(t, "21", poll(ctx, t, q).ID)
		assert.Equal(t, "31", poll(ctx, t, q).ID)
	})

	t.Run("a group with a limit is not exclusive", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		limited := func(id string, pipelineID int64) *model.Task {
			task := groupTask(id, pipelineID, pipelineID*100, "1//deploy")
			task.ConcurrencyLimit = 2
			return task
		}
		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{limited("11", 1), limited("21", 2)}))

		assert.Equal(t, "11", poll(ctx, t, q).ID)
		assert.Equal(t, "21", poll(ctx, t, q).ID)
		assert.Empty(t, q.holds)
	})
}

// After a server restart, the queue only has the tasks that were not handed
// out yet; the server makes the pipelines whose tasks in a group are running
// on agents hold the group again.
func TestFifoHoldGroup(t *testing.T) {
	t.Run("held while the agent extends the task, released when done", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)
		q.extension = time.Second

		// pipeline 1 published before the restart, its deploy is restored
		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{
			groupTask("12", 1, 100, "1//deploy"),
			groupTask("21", 2, 200, "1//deploy"),
		}))
		q.HoldGroup(ctx, "1//deploy", 1, []string{"11", "12"})
		assert.Equal(t, time.Time{}, q.holds["1//deploy"][1]["12"], "a task the queue has is held like one it handed out")
		assert.False(t, q.holds["1//deploy"][1]["11"].IsZero(), "a running task the queue does not have is held till its deadline")

		for range 3 {
			time.Sleep(q.extension / 3)
			require.NoError(t, q.Extend(ctx, 7, "11"), "its agent extends it")
		}
		assert.Equal(t, "12", poll(ctx, t, q).ID)
		require.NoError(t, q.Done(ctx, "12", model.StatusSuccess))
		assertNoTask(ctx, t, q, "pipeline 1's publish still holds the group")

		// the agent of the restored task reports it done
		require.NoError(t, q.Done(ctx, "11", model.StatusSuccess))
		assert.Equal(t, "21", poll(ctx, t, q).ID)
	})

	t.Run("a task its agent does not extend is released and not held again", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)
		q.extension = time.Second

		q.HoldGroup(ctx, "1//deploy", 1, []string{"11"})
		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{groupTask("21", 2, 200, "1//deploy")}))
		assertNoTask(ctx, t, q, "held till the deadline")

		assert.Equal(t, "21", poll(ctx, t, q).ID, "released once the deadline passed")
		assert.ErrorIs(t, q.Extend(ctx, 7, "11"), ErrNotFound)

		q.HoldGroup(ctx, "1//deploy", 1, []string{"11"})
		assert.Empty(t, q.holds["1//deploy"][1], "a lost task is not held again")
	})

	t.Run("nothing to hold", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		q.HoldGroup(ctx, "1//deploy", 1, nil)
		q.HoldGroup(ctx, "", 1, []string{"11"})
		assert.Empty(t, q.holds)
	})
}

func TestFifoSupersede(t *testing.T) {
	t.Run("removes the queued tasks, their dependents see them skipped", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		// pipeline 1's build runs, its publish and deploy wait on it
		build := &model.Task{ID: "10", PipelineID: 1, Created: 100}
		publish := groupTask("11", 1, 100, "1//deploy", "10")
		deploy := groupTask("12", 1, 100, "1//deploy", "11")
		notify := &model.Task{ID: "13", PipelineID: 1, Created: 100, Dependencies: []string{"12"}, DepStatus: map[string]model.StatusValue{}, RunOn: []string{"success", "failure"}}
		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{build, publish, deploy, notify}))
		assert.Equal(t, "10", poll(ctx, t, q).ID)

		ok, err := q.Supersede(ctx, []string{"11", "12"})
		require.NoError(t, err)
		assert.True(t, ok)

		info := q.Info(ctx)
		assert.Len(t, info.Running, 1)
		require.Len(t, info.WaitingOnDeps, 1)
		assert.Equal(t, "13", info.WaitingOnDeps[0].ID)
		assert.Equal(t, model.StatusSkipped, info.WaitingOnDeps[0].DepStatus["12"])

		// the build is untouched
		require.NoError(t, q.Done(ctx, "10", model.StatusSuccess))
		got := poll(ctx, t, q)
		assert.Equal(t, "13", got.ID)
		assert.True(t, got.ShouldRun(), "runs on failure too")
	})

	t.Run("leaves the tasks if one was handed out", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{
			groupTask("11", 1, 100, "1//deploy"),
			groupTask("12", 1, 100, "1//deploy", "11"),
		}))
		assert.Equal(t, "11", poll(ctx, t, q).ID)

		ok, err := q.Supersede(ctx, []string{"11", "12"})
		require.NoError(t, err)
		assert.False(t, ok, "running")

		ok, err = q.Supersede(ctx, []string{"12"})
		require.NoError(t, err)
		assert.False(t, ok, "held: its pipeline's unit is in progress")

		info := q.Info(ctx)
		assert.Len(t, info.Running, 1)
		assert.Len(t, info.WaitingOnDeps, 1)
	})

	t.Run("leaves the tasks if one is not in the queue", func(t *testing.T) {
		ctx, _, q := setupTestQueue(t)

		require.NoError(t, q.PushAtOnce(ctx, []*model.Task{groupTask("11", 1, 100, "1//deploy")}))

		ok, err := q.Supersede(ctx, []string{"11", "12"})
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Len(t, q.Info(ctx).Pending, 1)
	})
}
