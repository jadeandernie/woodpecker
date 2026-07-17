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

import "go.woodpecker-ci.org/woodpecker/v3/server/model"

type forgeStatusGroup struct {
	count     int
	hasAxisID bool
	lastIndex int
	status    model.StatusValue
}

// ForgeStatusWorkflows returns the persisted workflow statuses plus synthetic
// aggregate statuses for matrix-expanded workflow groups.
func ForgeStatusWorkflows(workflows []*model.Workflow, changed *model.Workflow) []*model.Workflow {
	groups := forgeStatusGroups(workflows)

	if changed != nil {
		statuses := []*model.Workflow{changed}
		if group, ok := groups[changed.Name]; ok && group.isMatrixGroup() {
			statuses = append(statuses, forgeStatusSummary(changed.Name, group.status))
		}
		return statuses
	}

	statuses := make([]*model.Workflow, 0, len(workflows)+len(groups))
	for index, workflow := range workflows {
		statuses = append(statuses, workflow)

		if group := groups[workflow.Name]; group.isMatrixGroup() && group.lastIndex == index {
			statuses = append(statuses, forgeStatusSummary(workflow.Name, group.status))
		}
	}
	return statuses
}

func forgeStatusGroups(workflows []*model.Workflow) map[string]forgeStatusGroup {
	groups := make(map[string]forgeStatusGroup, len(workflows))
	for index, workflow := range workflows {
		group := groups[workflow.Name]
		group.count++
		group.lastIndex = index
		group.hasAxisID = group.hasAxisID || workflow.AxisID != 0
		if group.count == 1 {
			group.status = workflow.State
		} else {
			group.status = MergeStatusValues(group.status, workflow.State)
		}
		groups[workflow.Name] = group
	}
	return groups
}

func (g forgeStatusGroup) isMatrixGroup() bool {
	return g.count > 1 && g.hasAxisID
}

func forgeStatusSummary(name string, status model.StatusValue) *model.Workflow {
	return &model.Workflow{
		Name:  name,
		State: status,
	}
}
