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
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/router/middleware/session"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
)

const (
	repoStatsDefaultLimit = 50
	repoStatsMaxLimit     = 200
	// repoStatsAllBranches is the branch query value that selects every branch.
	repoStatsAllBranches = "*"
)

// parseEventList parses a comma separated list of webhook events.
func parseEventList(events string) (model.WebhookEventList, error) {
	eventList := strings.Split(events, ",")
	wel := make(model.WebhookEventList, 0, len(eventList))
	for _, event := range eventList {
		we := model.WebhookEvent(event)
		if err := we.Validate(); err != nil {
			return nil, err
		}
		wel = append(wel, we)
	}
	return wel, nil
}

// GetRepoStats
//
//	@Summary		Get the pipeline history of a repository with workflow groups, timing and memory
//	@Description	Lists the newest pipelines of a branch, newest first, each with its workflows' group, subgroup, state,
//	@Description	timing, dependencies and memory peak, for the repository insights views.
//	@Router			/repos/{repo_id}/stats [get]
//	@Produce		json
//	@Success		200	{object}	RepoStats
//	@Tags			Repositories
//	@Param			Authorization	header	string	true	"Insert your personal access token"	default(Bearer <personal access token>)
//	@Param			repo_id			path	int		true	"the repository id"
//	@Param			branch			query	string	false	"the branch; missing or empty for the default branch, '*' for all branches"
//	@Param			event			query	string	false	"filter pipelines by webhook events (comma separated)"
//	@Param			limit			query	int		false	"the number of pipelines, at most 200"	default(50)
func GetRepoStats(c *gin.Context) {
	repo := session.Repo(c)
	_store := store.FromContext(c)

	branch := c.Query("branch")
	switch branch {
	case "":
		branch = repo.Branch
	case repoStatsAllBranches:
		branch = ""
	}
	filter := &model.PipelineFilter{Branch: branch}

	if events := c.Query("event"); events != "" {
		wel, err := parseEventList(events)
		if err != nil {
			_ = c.AbortWithError(http.StatusBadRequest, err)
			return
		}
		filter.Events = wel
	}

	limit := repoStatsDefaultLimit
	if l := c.Query("limit"); l != "" {
		var err error
		limit, err = strconv.Atoi(l)
		if err != nil || limit < 1 {
			_ = c.AbortWithError(http.StatusBadRequest, errors.New("limit must be a positive number"))
			return
		}
		limit = min(limit, repoStatsMaxLimit)
	}

	pipelines, err := _store.GetPipelineList(repo, &model.ListOptionsWithAll{ListOptions: &model.ListOptions{Page: 1, PerPage: limit}}, filter)
	if err != nil {
		_ = c.AbortWithError(http.StatusInternalServerError, err)
		return
	}

	stats, err := buildRepoStats(_store, repo, branch, pipelines)
	if err != nil {
		_ = c.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, stats)
}

// buildRepoStats loads the workflows and their memory peaks of the pipelines
// (two queries for all of them) and assembles the stats response.
func buildRepoStats(_store store.Store, repo *model.Repo, branch string, pipelines []*model.Pipeline) (*model.RepoStats, error) {
	stats := &model.RepoStats{Branch: branch, Pipelines: make([]*model.StatsPipeline, 0, len(pipelines))}
	if len(pipelines) == 0 {
		return stats, nil
	}

	ids := make([]int64, len(pipelines))
	for i, pl := range pipelines {
		ids[i] = pl.ID
	}

	workflows, err := _store.WorkflowListByPipelines(ids)
	if err != nil {
		return nil, err
	}
	peaks, err := _store.WorkflowMemoryPeaks(ids)
	if err != nil {
		return nil, err
	}

	type workflowKey struct {
		pipelineID int64
		pid        int
	}
	peakOf := make(map[workflowKey]int64, len(peaks))
	for _, p := range peaks {
		peakOf[workflowKey{p.PipelineID, p.PID}] = p.MemoryPeak
	}

	grouper := model.NewWorkflowGrouper(repo.WorkflowGroups)
	workflowsOf := make(map[int64][]*model.StatsWorkflow, len(pipelines))
	for _, wf := range workflows {
		group, subgroup := grouper.Group(wf.Name)
		workflowsOf[wf.PipelineID] = append(workflowsOf[wf.PipelineID], &model.StatsWorkflow{
			ID:         wf.ID,
			Name:       wf.Name,
			Group:      group,
			Subgroup:   subgroup,
			State:      wf.State,
			Started:    wf.Started,
			Finished:   wf.Finished,
			DependsOn:  wf.DependsOn,
			MemoryPeak: peakOf[workflowKey{wf.PipelineID, wf.PID}],
		})
	}

	for _, pl := range pipelines {
		sp := &model.StatsPipeline{
			ID:        pl.ID,
			Number:    pl.Number,
			Status:    pl.Status,
			Event:     string(pl.Event),
			Branch:    pl.Branch,
			Commit:    pl.Commit,
			Message:   pl.ToAPIModel().Message,
			Created:   pl.Created,
			Started:   pl.Started,
			Finished:  pl.Finished,
			Workflows: workflowsOf[pl.ID],
		}
		if sp.Workflows == nil {
			sp.Workflows = []*model.StatsWorkflow{}
		}
		for _, wf := range sp.Workflows {
			sp.MemoryPeak = max(sp.MemoryPeak, wf.MemoryPeak)
		}
		stats.Pipelines = append(stats.Pipelines, sp)
	}
	return stats, nil
}
