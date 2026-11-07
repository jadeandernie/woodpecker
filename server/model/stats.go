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

package model

// RepoStats is the pipeline history of a repo, newest first, with each
// workflow's group, timing and memory.
type RepoStats struct {
	// Branch is the branch the history is for, "" for all branches.
	Branch    string           `json:"branch"`
	Pipelines []*StatsPipeline `json:"pipelines"`
} //	@name	RepoStats

// StatsPipeline is one pipeline in the repo stats history.
type StatsPipeline struct {
	ID       int64       `json:"id"`
	Number   int64       `json:"number"`
	Status   StatusValue `json:"status"`
	Event    string      `json:"event"`
	Branch   string      `json:"branch"`
	Commit   string      `json:"commit"`
	Message  string      `json:"message"`
	Created  int64       `json:"created"`
	Started  int64       `json:"started"`
	Finished int64       `json:"finished"`
	// MemoryPeak is the highest memory use of any step in bytes, 0 when not measured.
	MemoryPeak int64            `json:"memory_peak"`
	Workflows  []*StatsWorkflow `json:"workflows"`
} //	@name	StatsPipeline

// StatsWorkflow is one workflow of a pipeline in the repo stats history.
type StatsWorkflow struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	Group     string      `json:"group"`
	Subgroup  string      `json:"subgroup"`
	State     StatusValue `json:"state"`
	Started   int64       `json:"started"`
	Finished  int64       `json:"finished"`
	DependsOn []string    `json:"depends_on,omitempty"`
	// MemoryPeak is the highest memory use of the workflow's steps in bytes, 0 when not measured.
	MemoryPeak int64 `json:"memory_peak"`
} //	@name	StatsWorkflow

// WorkflowMemoryPeak is the highest memory use of the steps of one workflow,
// which is identified by its pipeline and its pid (the steps' ppid).
type WorkflowMemoryPeak struct {
	PipelineID int64 `xorm:"pipeline_id"`
	PID        int   `xorm:"ppid"`
	MemoryPeak int64 `xorm:"memory_peak"`
}
