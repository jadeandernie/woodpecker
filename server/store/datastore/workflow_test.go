// Copyright 2022 Woodpecker Authors
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

func TestWorkflowLoad(t *testing.T) {
	store, closer := newTestStore(t, new(model.Step), new(model.Pipeline), new(model.Workflow))
	defer closer()

	wf := &model.Workflow{
		PipelineID: 1,
		PID:        1,
		Name:       "woodpecker",
		Children: []*model.Step{
			{
				UUID:       "ea6d4008-8ace-4f8a-ad03-53f1756465d9",
				PipelineID: 1,
				PID:        2,
				PPID:       1,
				State:      "success",
			},
			{
				UUID:       "2bf387f7-2913-4907-814c-c9ada88707c0",
				PipelineID: 1,
				PID:        3,
				PPID:       1,
				Name:       "build",
				State:      "success",
			},
		},
	}
	assert.NoError(t, store.WorkflowsCreate([]*model.Workflow{wf}))
	workflowGet, err := store.WorkflowLoad(1)
	assert.NoError(t, err)
	assert.EqualValues(t, 1, workflowGet.PipelineID)
	assert.Equal(t, 1, workflowGet.PID)
	assert.Len(t, workflowGet.Children, 0)
}

func TestWorkflowGetTree(t *testing.T) {
	store, closer := newTestStore(t, new(model.Step), new(model.Pipeline), new(model.Workflow))
	defer closer()

	wf := &model.Workflow{
		PipelineID: 1,
		PID:        1,
		Name:       "woodpecker",
		DependsOn:  []string{"lint"},
		Children: []*model.Step{
			{
				UUID:       "ea6d4008-8ace-4f8a-ad03-53f1756465d9",
				PipelineID: 1,
				PID:        2,
				PPID:       1,
				Name:       "test",
				State:      "success",
			},
			{
				UUID:       "2bf387f7-2913-4907-814c-c9ada88707c0",
				PipelineID: 1,
				PID:        3,
				PPID:       1,
				Name:       "build",
				DependsOn:  []string{"test"},
				State:      "success",
			},
		},
	}
	assert.NoError(t, store.WorkflowsCreate([]*model.Workflow{wf}))

	workflowsGet, err := store.WorkflowGetTree(&model.Pipeline{ID: 1})
	assert.NoError(t, err)
	assert.Len(t, workflowsGet, 1)
	workflowGet := workflowsGet[0]
	assert.Equal(t, "woodpecker", workflowGet.Name)
	assert.Equal(t, []string{"lint"}, workflowGet.DependsOn)
	assert.Len(t, workflowGet.Children, 2)
	assert.Equal(t, 2, workflowGet.Children[0].PID)
	assert.Equal(t, 3, workflowGet.Children[1].PID)
	assert.Equal(t, []string{"test"}, workflowGet.Children[1].DependsOn)
}

func TestWorkflowUpdate(t *testing.T) {
	store, closer := newTestStore(t, new(model.Step), new(model.Pipeline), new(model.Workflow))
	defer closer()

	wf := &model.Workflow{
		PipelineID: 1,
		PID:        1,
		Name:       "woodpecker",
		State:      "pending",
	}
	assert.NoError(t, store.WorkflowsCreate([]*model.Workflow{wf}))
	workflowGet, err := store.WorkflowLoad(1)
	assert.NoError(t, err)

	assert.Equal(t, model.StatusValue("pending"), workflowGet.State)

	wf.State = "success"

	assert.NoError(t, store.WorkflowUpdate(wf))
	workflowGet, err = store.WorkflowLoad(1)
	assert.NoError(t, err)
	assert.Equal(t, model.StatusValue("success"), workflowGet.State)
}

func TestWorkflowListByPipelinesAndMemoryPeaks(t *testing.T) {
	store, closer := newTestStore(t, new(model.Step), new(model.Pipeline), new(model.Workflow))
	defer closer()

	step := func(uuid string, pipelineID int64, pid, ppid int, memoryPeak int64) *model.Step {
		return &model.Step{UUID: uuid, PipelineID: pipelineID, PID: pid, PPID: ppid, Name: uuid, MemoryPeak: memoryPeak}
	}
	workflows := []*model.Workflow{
		{PipelineID: 1, PID: 2, Name: "test", DependsOn: []string{"build"}, Children: []*model.Step{
			step("p1-w2-a", 1, 3, 2, 300),
			step("p1-w2-b", 1, 4, 2, 700),
		}},
		{PipelineID: 1, PID: 1, Name: "build", Children: []*model.Step{
			step("p1-w1-a", 1, 5, 1, 0),
		}},
		{PipelineID: 2, PID: 1, Name: "build", Children: []*model.Step{
			step("p2-w1-a", 2, 2, 1, 100),
		}},
		{PipelineID: 3, PID: 1, Name: "other-pipeline", Children: []*model.Step{
			step("p3-w1-a", 3, 2, 1, 999),
		}},
	}
	assert.NoError(t, store.WorkflowsCreate(workflows))

	list, err := store.WorkflowListByPipelines([]int64{1, 2})
	assert.NoError(t, err)
	if assert.Len(t, list, 3) {
		assert.Equal(t, "build", list[0].Name)
		assert.EqualValues(t, 1, list[0].PipelineID)
		assert.Equal(t, "test", list[1].Name)
		assert.Equal(t, []string{"build"}, list[1].DependsOn)
		assert.EqualValues(t, 2, list[2].PipelineID)
		assert.Empty(t, list[1].Children)
	}

	peaks, err := store.WorkflowMemoryPeaks([]int64{1, 2})
	assert.NoError(t, err)
	assert.ElementsMatch(t, []*model.WorkflowMemoryPeak{
		{PipelineID: 1, PID: 2, MemoryPeak: 700},
		{PipelineID: 2, PID: 1, MemoryPeak: 100},
	}, peaks)

	list, err = store.WorkflowListByPipelines(nil)
	assert.NoError(t, err)
	assert.Empty(t, list)
	peaks, err = store.WorkflowMemoryPeaks(nil)
	assert.NoError(t, err)
	assert.Empty(t, peaks)
}
