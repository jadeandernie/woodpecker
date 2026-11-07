// Copyright 2018 Drone.IO Inc.
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

	"github.com/stretchr/testify/assert"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

func TestTaskList(t *testing.T) {
	store, closer := newTestStore(t, new(model.Task))
	defer closer()

	assert.NoError(t, store.TaskInsert(&model.Task{
		ID:        "some_random_id",
		Data:      []byte("foo"),
		Labels:    map[string]string{"foo": "bar"},
		DepStatus: map[string]model.StatusValue{"test": "dep"},
	}))

	list, err := store.TaskList()
	assert.NoError(t, err)
	assert.Len(t, list, 1, "Expected one task in list")
	assert.Equal(t, "some_random_id", list[0].ID)
	assert.Equal(t, "foo", string(list[0].Data))
	assert.EqualValues(t, map[string]model.StatusValue{"test": "dep"}, list[0].DepStatus)

	assert.NoError(t, store.TaskDelete("some_random_id"))

	list, err = store.TaskList()
	assert.NoError(t, err)
	assert.Len(t, list, 0, "Want empty task list after delete")
}

// The queue is restored from the store when the server starts, so the place a
// task took over must come back with it.
func TestTaskQueuedInPlaceOf(t *testing.T) {
	store, closer := newTestStore(t, new(model.Task))
	defer closer()

	assert.NoError(t, store.TaskInsert(&model.Task{ID: "own", PipelineID: 5, Created: 500}))
	assert.NoError(t, store.TaskInsert(&model.Task{ID: "taken", PipelineID: 7, Created: 300, QueuedInPlaceOf: 3}))

	list, err := store.TaskList()
	assert.NoError(t, err)
	assert.Len(t, list, 2)
	for _, task := range list {
		switch task.ID {
		case "own":
			assert.Equal(t, int64(500), task.Created)
			assert.Zero(t, task.QueuedInPlaceOf)
		case "taken":
			assert.Equal(t, int64(300), task.Created)
			assert.Equal(t, int64(3), task.QueuedInPlaceOf)
		default:
			t.Errorf("unexpected task %s", task.ID)
		}
	}
}
