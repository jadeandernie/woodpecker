// Copyright 2022 Woodpecker Authors
// Copyright 2021 Informatyka Boguslawski sp. z o.o. sp.k., http://www.ib.pl/
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

package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"go.woodpecker-ci.org/woodpecker/v3/server"
	"go.woodpecker-ci.org/woodpecker/v3/server/badges"
	"go.woodpecker-ci.org/woodpecker/v3/server/ccmenu"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/pipeline"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
	"go.woodpecker-ci.org/woodpecker/v3/server/store/types"
)

// badgeRepo looks up the repo of a badge request by id or by owner and name.
// On failure it writes the error response and returns false.
func badgeRepo(c *gin.Context, _store store.Store) (*model.Repo, bool) {
	var repo *model.Repo
	var err error

	if c.Param("repo_name") != "" {
		repo, err = _store.GetRepoName(c.Param("repo_id_or_owner") + "/" + c.Param("repo_name"))
	} else {
		var repoID int64
		repoID, err = strconv.ParseInt(c.Param("repo_id_or_owner"), 10, 64)
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return nil, false
		}
		repo, err = _store.GetRepo(repoID)
	}

	if err != nil {
		handleDBError(c, err)
		return nil, false
	}
	return repo, true
}

// badgeEvents parses the events query of a badge request, a comma separated
// list that defaults to push. On failure it writes the error response and
// returns false.
func badgeEvents(c *gin.Context) ([]model.WebhookEvent, bool) {
	eventsQuery := c.Query("events")
	if len(eventsQuery) == 0 {
		return []model.WebhookEvent{model.EventPush}, true
	}
	events, err := parseEventList(eventsQuery)
	if err != nil {
		_ = c.AbortWithError(http.StatusBadRequest, err)
		return nil, false
	}
	return events, true
}

// GetBadge
//
//	@Summary	Get status of pipeline as SVG badge
//	@Router		/badges/{repo_id}/status.svg [get]
//	@Produce	image/svg+xml
//	@Success	200
//	@Tags		Badges
//	@Param		repo_id	path	int	true	"the repository id"
func GetBadge(c *gin.Context) {
	_store := store.FromContext(c)

	repo, ok := badgeRepo(c, _store)
	if !ok {
		return
	}

	// if no commit was found then display
	// the 'none' badge, instead of throwing
	// an error response
	branch := c.Query("branch")
	if len(branch) == 0 {
		branch = repo.Branch
	}

	events, ok := badgeEvents(c)
	if !ok {
		return
	}

	name := "pipeline"
	var status *model.StatusValue = nil

	pl, err := _store.GetPipelineBadge(repo, branch, events)
	if err != nil {
		if !errors.Is(err, types.ErrRecordNotExist) {
			log.Warn().Err(err).Msg("could not get last pipeline for badge")
		}
	} else {
		status = &pl.Status
	}

	// we serve an SVG, so set content type appropriately.
	c.Writer.Header().Set("Content-Type", "image/svg+xml")

	// Allow workflow (and step) specific badges
	workflowName := c.Query("workflow")
	if len(workflowName) != 0 {
		name = workflowName
		status = nil

		workflows, err := _store.WorkflowGetTree(pl)
		if err == nil {
			for _, wf := range workflows {
				if wf.Name == workflowName {
					stepName := c.Query("step")
					if len(stepName) == 0 {
						if status != nil {
							merged := pipeline.MergeStatusValues(*status, wf.State)
							status = &merged
						} else {
							status = &wf.State
						}
						continue
					}
					// If step is explicitly requested
					name = workflowName + ": " + stepName
					for _, s := range wf.Children {
						if s.Name == stepName {
							if status != nil {
								merged := pipeline.MergeStatusValues(*status, s.State)
								status = &merged
							} else {
								status = &s.State
							}
						}
					}
				}
			}
		}
	}

	badge, err := badges.Generate(name, status)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to generate badge.")
	} else {
		c.String(http.StatusOK, badge)
	}
}

// GetBadgePipeline
//
//	@Summary		Open the pipeline a status badge shows
//	@Description	Redirects to the pipeline graph of the latest pipeline of the branch (the one the status badge shows),
//	@Description	or to the repository when it has none. Use it as the link of the status badge.
//	@Router			/badges/{repo_id}/pipeline [get]
//	@Success		302
//	@Tags			Badges
//	@Param			repo_id	path	int		true	"the repository id"
//	@Param			branch	query	string	false	"the branch, default the repository's default branch"
//	@Param			events	query	string	false	"the events to consider (comma separated), default push"
func GetBadgePipeline(c *gin.Context) {
	_store := store.FromContext(c)

	repo, ok := badgeRepo(c, _store)
	if !ok {
		return
	}

	branch := c.Query("branch")
	if len(branch) == 0 {
		branch = repo.Branch
	}

	events, ok := badgeEvents(c)
	if !ok {
		return
	}

	target := fmt.Sprintf("%s/repos/%d", server.Config.Server.RootPath, repo.ID)
	pl, err := _store.GetPipelineBadge(repo, branch, events)
	switch {
	case err == nil:
		target = fmt.Sprintf("%s/repos/%d/pipeline/%d/graph", server.Config.Server.RootPath, repo.ID, pl.Number)
	case !errors.Is(err, types.ErrRecordNotExist):
		log.Warn().Err(err).Msg("could not get last pipeline for badge link")
	}

	c.Redirect(http.StatusFound, target)
}

// GetCC
//
//	@Summary		Provide pipeline status information to the CCMenu tool
//	@Description	CCMenu displays the pipeline status of projects on a CI server as an item in the Mac's menu bar.
//	@Description	More details on how to install, you can find at http://ccmenu.org/
//	@Description	The response format adheres to CCTray v1 Specification, https://cctray.org/v1/
//	@Router			/badges/{repo_id}/cc.xml [get]
//	@Produce		xml
//	@Success		200
//	@Tags			Badges
//	@Param			repo_id	path	int	true	"the repository id"
func GetCC(c *gin.Context) {
	_store := store.FromContext(c)
	repo, ok := badgeRepo(c, _store)
	if !ok {
		return
	}

	pipelines, err := _store.GetPipelineList(repo, &model.ListOptionsWithAll{ListOptions: &model.ListOptions{Page: 1, PerPage: 1}}, nil)
	if err != nil && !errors.Is(err, types.ErrRecordNotExist) {
		log.Warn().Err(err).Msg("could not get pipeline list")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if len(pipelines) == 0 {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	url := fmt.Sprintf("%s/repos/%d/pipeline/%d", server.Config.Server.Host, repo.ID, pipelines[0].Number)
	cc := ccmenu.New(repo, pipelines[0], url)
	c.XML(http.StatusOK, cc)
}
