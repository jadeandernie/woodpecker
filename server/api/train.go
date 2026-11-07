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
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/router/middleware/session"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
	"go.woodpecker-ci.org/woodpecker/v3/server/store/types"
	"go.woodpecker-ci.org/woodpecker/v3/server/train"
)

// GetTrain
//
//	@Summary		Get the merge train of a repository
//	@Description	Returns the repository's merge train settings and state: the phase, whether the train branch is frozen
//	@Description	and why, the cars under test, the current investigations, the reverts and the recent history.
//	@Router			/repos/{repo_id}/train [get]
//	@Produce		json
//	@Success		200	{object}	TrainStatus
//	@Tags			Repositories
//	@Param			Authorization	header	string	true	"Insert your personal access token"	default(Bearer <personal access token>)
//	@Param			repo_id			path	int		true	"the repository id"
func GetTrain(c *gin.Context) {
	repo := session.Repo(c)
	status, err := trainStatus(store.FromContext(c), repo)
	if err != nil {
		_ = c.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

// PostTrainResume
//
//	@Summary		Resume a frozen merge train
//	@Description	Unfreezes the train branch, drops the current investigations and lets the train promote again.
//	@Router			/repos/{repo_id}/train/resume [post]
//	@Produce		json
//	@Success		200	{object}	TrainStatus
//	@Tags			Repositories
//	@Param			Authorization	header	string	true	"Insert your personal access token"	default(Bearer <personal access token>)
//	@Param			repo_id			path	int		true	"the repository id"
func PostTrainResume(c *gin.Context) {
	trainAction(c, (*train.Controller).Resume)
}

// PostTrainRetry
//
//	@Summary		Promote the train branch head again
//	@Description	Starts a fresh promote of the train branch head, for example after a held or canceled promote.
//	@Description	Refused while a promote or an investigation runs, or while the train is frozen.
//	@Router			/repos/{repo_id}/train/retry [post]
//	@Produce		json
//	@Success		200	{object}	TrainStatus
//	@Tags			Repositories
//	@Param			Authorization	header	string	true	"Insert your personal access token"	default(Bearer <personal access token>)
//	@Param			repo_id			path	int		true	"the repository id"
func PostTrainRetry(c *gin.Context) {
	trainAction(c, (*train.Controller).Retry)
}

func trainAction(c *gin.Context, action func(*train.Controller, context.Context, int64, string) error) {
	repo := session.Repo(c)
	user := session.User(c)
	ctrl := train.Default()
	if ctrl == nil {
		c.String(http.StatusServiceUnavailable, train.ErrNotRunning.Error())
		return
	}
	if !repo.TrainConfig().Enabled {
		c.String(http.StatusConflict, train.ErrDisabled.Error())
		return
	}
	by := "the API"
	if user != nil {
		by = user.Login
	}
	err := action(ctrl, c, repo.ID, by)
	switch {
	case errors.Is(err, train.ErrBusy), errors.Is(err, train.ErrFrozen), errors.Is(err, train.ErrDisabled):
		c.String(http.StatusConflict, err.Error())
		return
	case errors.Is(err, train.ErrUnsupported):
		c.String(http.StatusNotImplemented, err.Error())
		return
	case err != nil:
		_ = c.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	status, err := trainStatus(store.FromContext(c), repo)
	if err != nil {
		_ = c.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func trainStatus(s store.Store, repo *model.Repo) (*model.TrainStatus, error) {
	st, err := s.TrainStateGet(repo.ID)
	if errors.Is(err, types.ErrRecordNotExist) {
		st, err = &model.TrainState{Phase: model.TrainIdle}, nil
	}
	if err != nil {
		return nil, err
	}
	return &model.TrainStatus{Settings: repo.TrainConfig(), HasBotToken: repo.TrainBotToken != "", State: st}, nil
}
