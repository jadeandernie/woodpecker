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

package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

func TestForgeStatusWorkflowsKeepsNonMatrixWorkflows(t *testing.T) {
	workflows := []*model.Workflow{
		{Name: "lint", PID: 1, State: model.StatusPending},
		{Name: "test", PID: 2, State: model.StatusPending},
	}

	assert.Equal(t, workflows, ForgeStatusWorkflows(workflows, nil))
	assert.Equal(t, []*model.Workflow{workflows[1]}, ForgeStatusWorkflows(workflows, workflows[1]))
}

func TestForgeStatusWorkflowsAddsMatrixSummary(t *testing.T) {
	workflows := []*model.Workflow{
		{Name: "test", PID: 1, AxisID: 1, State: model.StatusSuccess},
		{Name: "test", PID: 2, AxisID: 2, State: model.StatusPending},
		{Name: "lint", PID: 3, State: model.StatusSuccess},
	}

	statuses := ForgeStatusWorkflows(workflows, nil)

	assert.Equal(t, []*model.Workflow{
		workflows[0],
		workflows[1],
		{Name: "test", State: model.StatusPending},
		workflows[2],
	}, statuses)
}

func TestForgeStatusWorkflowsUpdatesChangedMatrixGroup(t *testing.T) {
	workflows := []*model.Workflow{
		{Name: "test", PID: 1, AxisID: 1, State: model.StatusSuccess},
		{Name: "test", PID: 2, AxisID: 2, State: model.StatusFailure},
		{Name: "lint", PID: 3, State: model.StatusSuccess},
	}

	statuses := ForgeStatusWorkflows(workflows, workflows[1])

	assert.Equal(t, []*model.Workflow{
		workflows[1],
		{Name: "test", State: model.StatusFailure},
	}, statuses)
}

func TestForgeStatusWorkflowsSkipsSummaryForSingleAxisID(t *testing.T) {
	workflows := []*model.Workflow{
		{Name: "test", PID: 1, AxisID: 1, State: model.StatusSuccess},
		{Name: "lint", PID: 2, State: model.StatusSuccess},
	}

	assert.Equal(t, workflows, ForgeStatusWorkflows(workflows, nil))
}
