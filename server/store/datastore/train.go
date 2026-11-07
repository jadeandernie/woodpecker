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
	"time"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

func (s storage) TrainStateGet(repoID int64) (*model.TrainState, error) {
	state := new(model.TrainState)
	return state, wrapGet(s.engine.Where("repo_id = ?", repoID).Get(state))
}

func (s storage) TrainStateSave(state *model.TrainState) error {
	state.Updated = time.Now().Unix()
	return retryWrite(func() error {
		if state.ID == 0 {
			existing := new(model.TrainState)
			found, err := s.engine.Where("repo_id = ?", state.RepoID).Cols("id").Get(existing)
			if err != nil {
				return err
			}
			if !found {
				return wrapInsert(s.engine.Insert(state))
			}
			state.ID = existing.ID
		}
		_, err := s.engine.ID(state.ID).AllCols().Update(state)
		return err
	})
}
