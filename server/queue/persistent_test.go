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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	store_mocks "go.woodpecker-ci.org/woodpecker/v3/server/store/mocks"
	"go.woodpecker-ci.org/woodpecker/v3/server/store/types"
)

// A task that lingers in the in-memory queue but is already gone from the
// backup store must be dropped on Poll instead of being handed to the agent,
// otherwise it loops forever (re-poll, illegal-instruction, resubmit).
func TestPersistentQueuePollDropsStaleTask(t *testing.T) {
	ctx, cancel, q := setupTestQueue(t)
	defer cancel(nil)

	store := store_mocks.NewMockStore(t)
	store.EXPECT().TaskDelete("1").Return(types.ErrRecordNotExist).Once()

	pq := &persistentQueue{Queue: q, store: store}

	task := genDummyTask()
	assert.NoError(t, q.PushAtOnce(ctx, []*model.Task{task}))

	got, err := pq.Poll(ctx, 1, filterFnTrue)
	assert.NoError(t, err)
	assert.Nil(t, got, "stale task must not be returned to the agent")

	info := q.Info(ctx)
	assert.Equal(t, 0, info.Stats.Pending, "stale task must be removed from pending")
	assert.Equal(t, 0, info.Stats.Running, "stale task must be removed from running")
}

// A task that is still present in the backup store is polled normally.
func TestPersistentQueuePollReturnsLiveTask(t *testing.T) {
	ctx, cancel, q := setupTestQueue(t)
	defer cancel(nil)

	store := store_mocks.NewMockStore(t)
	store.EXPECT().TaskDelete("1").Return(nil).Once()

	pq := &persistentQueue{Queue: q, store: store}

	task := genDummyTask()
	assert.NoError(t, q.PushAtOnce(ctx, []*model.Task{task}))

	got, err := pq.Poll(ctx, 1, filterFnTrue)
	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, "1", got.ID)
}

// The queue is restored from the backup store when the server starts. A
// pipeline queued in place of an earlier one keeps that place across the
// restart, whichever order the store lists the tasks in.
func TestPersistentQueueRestoreKeepsQueuedInPlaceOf(t *testing.T) {
	var saved []*model.Task
	reversed := false

	store := store_mocks.NewMockStore(t)
	store.EXPECT().TaskInsert(mock.Anything).RunAndReturn(func(task *model.Task) error {
		saved = append(saved, task)
		return nil
	})
	store.EXPECT().TaskList().RunAndReturn(func() ([]*model.Task, error) {
		// read back copies, as a database would
		tasks := make([]*model.Task, 0, len(saved))
		for _, task := range saved {
			stored := *task
			tasks = append(tasks, &stored)
		}
		if reversed {
			slices.Reverse(tasks)
		}
		return tasks, nil
	})

	restart := func(t *testing.T) (context.Context, Queue) {
		t.Helper()
		ctx, cancel := context.WithCancelCause(t.Context())
		t.Cleanup(func() { cancel(nil) })
		return ctx, WithTaskStore(ctx, NewMemoryQueue(ctx), store)
	}

	ctx, q := restart(t)
	// main pushes 1 and 3 wait, a pull request (2) was created in between
	for _, task := range []*model.Task{
		{ID: "1-build", PipelineID: 1, Created: 100},
		{ID: "2-build", PipelineID: 2, Created: 150},
		{ID: "3-build", PipelineID: 3, Created: 180},
	} {
		assert.NoError(t, q.PushAtOnce(ctx, []*model.Task{task}))
	}
	// main push 5 squashes them and takes the place of 1
	assert.NoError(t, q.PushAtOnce(ctx, []*model.Task{
		{ID: "5-build", PipelineID: 5, Created: 100, QueuedInPlaceOf: 1},
		{ID: "5-test", PipelineID: 5, Created: 100, QueuedInPlaceOf: 1},
	}))
	// and a pull request comes in after it
	assert.NoError(t, q.PushAtOnce(ctx, []*model.Task{{ID: "6-build", PipelineID: 6, Created: 300}}))

	want := []string{"1-build", "5-build", "5-test", "2-build", "3-build", "6-build"}
	assert.Equal(t, want, pendingIDs(q.Info(ctx)))

	t.Run("store lists tasks in the order they were stored", func(t *testing.T) {
		reversed = false
		ctx, q := restart(t)
		assert.Equal(t, want, pendingIDs(q.Info(ctx)))
	})

	t.Run("store lists tasks in another order", func(t *testing.T) {
		reversed = true
		ctx, q := restart(t)
		got := pendingIDs(q.Info(ctx))
		assert.ElementsMatch(t, want, got)
		if assert.Len(t, got, len(want)) {
			assert.ElementsMatch(t, []string{"5-build", "5-test"}, got[:2], "pipeline 5 must stay ahead of pipelines created after the one it took the place of: %v", got)
		}
	})
}
