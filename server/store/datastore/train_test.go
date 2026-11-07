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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/store/types"
)

func TestTrainState(t *testing.T) {
	store, closer := newTestStore(t, new(model.TrainState))
	defer closer()

	_, err := store.TrainStateGet(1)
	assert.ErrorIs(t, err, types.ErrRecordNotExist)

	state := &model.TrainState{RepoID: 1, Phase: model.TrainPromoting, Pipeline: 7, Cars: []*model.TrainCar{{SHA: "a", Title: "one"}}}
	require.NoError(t, store.TrainStateSave(state))

	// a second save without the ID updates the same row
	again := &model.TrainState{RepoID: 1, Phase: model.TrainFrozen, Frozen: true, FrozenReason: "revert conflict"}
	require.NoError(t, store.TrainStateSave(again))
	assert.Equal(t, state.ID, again.ID)

	got, err := store.TrainStateGet(1)
	require.NoError(t, err)
	assert.Equal(t, model.TrainFrozen, got.Phase)
	assert.True(t, got.Frozen)
	assert.Empty(t, got.Cars)

	got.Phase = model.TrainIdle
	got.Frozen = false
	got.Investigations = []*model.TrainInvestigation{{Workflow: "test", Candidates: []string{"a", "b"}, Good: -1, Bad: 1}}
	require.NoError(t, store.TrainStateSave(got))
	got, err = store.TrainStateGet(1)
	require.NoError(t, err)
	assert.False(t, got.Frozen)
	require.Len(t, got.Investigations, 1)
	assert.Equal(t, -1, got.Investigations[0].Good)
}
