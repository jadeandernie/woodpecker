// Copyright 2022 Woodpecker Authors
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

package pipeline

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/builder"
	"go.woodpecker-ci.org/woodpecker/v3/rpc"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// pipelineTasks builds the queue tasks for a pipeline's workflow items.
// Enqueuing happens via the scheduler (see scheduler.StartPipeline).
func pipelineTasks(repo *model.Repo, activePipeline *model.Pipeline, pipelineItems []*builder.Item) ([]*model.Task, error) {
	var tasks []*model.Task
	for _, item := range pipelineItems {
		task := &model.Task{
			ID:         fmt.Sprint(item.Workflow.ID),
			PID:        item.Workflow.PID,
			Name:       item.Workflow.Name,
			Labels:     make(map[string]string),
			PipelineID: activePipeline.ID,
			RepoID:     repo.ID,
			Created:    activePipeline.Created,
		}
		// fall back to the current time if the pipeline has no creation
		// timestamp, so the queue always has a defined ordering key.
		if task.Created == 0 {
			task.Created = time.Now().Unix()
		}
		maps.Copy(task.Labels, item.Labels)
		err := task.ApplyLabelsFromRepo(repo)
		if err != nil {
			return nil, err
		}
		task.Dependencies = getTaskDependencies(item.DependsOn.Names(), pipelineItems)
		task.RunOn = item.RunsOn
		task.DepStatus = make(map[string]model.StatusValue)

		// Set up the concurrency limit if the workflow opted in.
		if item.ConcurrencyLimit > 0 {
			task.ConcurrencyLimit = item.ConcurrencyLimit

			// If no group assigned, each workflow is it's own unique group,
			// else we use defined group unique per repo.
			if item.ConcurrencyGroup == "" {
				task.ConcurrencyGroup = fmt.Sprintf("%d/%s/", repo.ID, item.Workflow.Name)
			} else {
				task.ConcurrencyGroup = concurrencyGroupKey(repo.ID, item.ConcurrencyGroup)
			}
		} else if group := exclusiveGroup(item); group != "" {
			// A group without a limit: one pipeline's workflows in it run at a
			// time (see model.Task.ExclusiveGroup and concurrency_group.go).
			task.ConcurrencyGroup = concurrencyGroupKey(repo.ID, group)
		}

		task.Data, err = json.Marshal(rpc.Workflow{
			ID:      fmt.Sprint(item.Workflow.ID),
			Config:  item.Config,
			Timeout: repo.Timeout,
		})
		if err != nil {
			return nil, err
		}

		tasks = append(tasks, task)
	}
	return tasks, nil
}

// exclusiveGroup returns the concurrency group without a limit the workflow is
// in, or "" if it is in none: `concurrency: {group: <name>}` without a limit.
func exclusiveGroup(item *builder.Item) string {
	if item.ConcurrencyLimit > 0 {
		return ""
	}
	return item.ConcurrencyGroup
}

// concurrencyGroupKey returns the queue's concurrency group of the repo's group
// of the given name.
func concurrencyGroupKey(repoID int64, group string) string {
	return fmt.Sprintf("%d//%s", repoID, group)
}

func getTaskDependencies(dependsOn []string, items []*builder.Item) (taskIDs []string) {
	for _, dep := range dependsOn {
		for _, pipelineItem := range items {
			if pipelineItem.Workflow.Name == dep {
				taskIDs = append(taskIDs, fmt.Sprint(pipelineItem.Workflow.ID))
			}
		}
	}
	return taskIDs
}

// takeOverQueuePlace queues the tasks of a pipeline in the place of the oldest
// pipeline it supersedes, so squashing pending pipelines does not move their
// changes to the back of the queue. The place a superseded pipeline has is the
// one its queued tasks hold, which is earlier than its own if it squashed
// pipelines itself, or else its creation time and ID, the order the queue keeps
// pipelines in. Pipelines without a creation time and no queued tasks are
// skipped, and a place behind the tasks' own is never taken. The pipeline
// itself keeps its creation time.
func takeOverQueuePlace(tasks []*model.Task, superseded []*model.Pipeline, queued []*model.Task) {
	isSuperseded := make(map[int64]bool, len(superseded))
	for _, previous := range superseded {
		isSuperseded[previous.ID] = true
	}
	held := make(map[int64]queuePlace, len(superseded))
	for _, task := range queued {
		if !isSuperseded[task.PipelineID] {
			continue
		}
		if place, ok := held[task.PipelineID]; !ok || placeOf(task).before(place) {
			held[task.PipelineID] = placeOf(task)
		}
	}

	var oldest queuePlace
	found := false
	for _, previous := range superseded {
		place, ok := held[previous.ID]
		if !ok {
			if previous.Created == 0 {
				continue
			}
			place = queuePlace{created: previous.Created, pipelineID: previous.ID}
		}
		if !found || place.before(oldest) {
			oldest, found = place, true
		}
	}
	if !found {
		return
	}

	for _, task := range tasks {
		if oldest.before(placeOf(task)) {
			task.Created = oldest.created
			task.QueuedInPlaceOf = oldest.pipelineID
		}
	}
}

// queuePlace is a place in the queue, which orders tasks by creation time and
// then by the ID of the pipeline whose place they have.
type queuePlace struct {
	created    int64
	pipelineID int64
}

func (p queuePlace) before(other queuePlace) bool {
	return cmp.Or(cmp.Compare(p.created, other.created), cmp.Compare(p.pipelineID, other.pipelineID)) < 0
}

// placeOf returns the place in the queue the task has.
func placeOf(task *model.Task) queuePlace {
	if task.QueuedInPlaceOf != 0 {
		return queuePlace{created: task.Created, pipelineID: task.QueuedInPlaceOf}
	}
	return queuePlace{created: task.Created, pipelineID: task.PipelineID}
}
