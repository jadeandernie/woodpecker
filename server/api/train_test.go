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

package api

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

func TestGetTrain(t *testing.T) {
	s := newTestStore(t)
	cfg := model.DefaultTrainSettings()
	cfg.Enabled = true
	repo := &model.Repo{UserID: 1, ForgeRemoteID: "train", Owner: "jade", Name: "train", FullName: "jade/train", Train: &cfg, TrainBotToken: "secret"}
	require.NoError(t, s.CreateRepo(repo))

	get := func() *model.TrainStatus {
		tc := newTestContext(t, s)
		withRepo(repo, &model.Perm{Pull: true})(tc)
		GetTrain(tc.Ctx)
		require.Equal(t, http.StatusOK, tc.Ctx.Writer.Status())
		require.NotContains(t, tc.Recorder.Body.String(), "secret")
		out := new(model.TrainStatus)
		tc.decodeJSON(t, out)
		return out
	}

	t.Run("a train that never ran is idle", func(t *testing.T) {
		st := get()
		require.True(t, st.Settings.Enabled)
		require.True(t, st.HasBotToken)
		require.Equal(t, model.TrainIdle, st.State.Phase)
	})

	t.Run("the saved state", func(t *testing.T) {
		require.NoError(t, s.TrainStateSave(&model.TrainState{RepoID: repo.ID, Phase: model.TrainFrozen, Frozen: true, FrozenReason: "revert conflict"}))
		st := get()
		require.Equal(t, model.TrainFrozen, st.State.Phase)
		require.Equal(t, "revert conflict", st.State.FrozenReason)
	})

	t.Run("actions need the running controller", func(t *testing.T) {
		tc := newTestContext(t, s)
		withRepo(repo, &model.Perm{Push: true})(tc)
		withUser(&model.User{Login: "jade"})(tc)
		PostTrainResume(tc.Ctx)
		require.Equal(t, http.StatusServiceUnavailable, tc.Ctx.Writer.Status())
	})
}
