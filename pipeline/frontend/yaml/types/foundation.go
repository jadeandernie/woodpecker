// Copyright 2026 Woodpecker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package types

// Foundation is the shared setup of a group of workflows. A config file with a
// `foundation` section is not a workflow itself: its clone, services and hooks
// run once in a single workflow, named after the foundation, together with the
// steps of every workflow that names it in `uses_foundation` (its members).
type Foundation struct {
	// Name is how members refer to the foundation and the name of the workflow
	// it runs as. It defaults to the name of the file.
	Name string `yaml:"name,omitempty"`
	// MaxParallel is the number of lanes the members are spread over. Members
	// of one lane run one after another, lanes run at the same time. Values
	// below 1 mean 1.
	MaxParallel int `yaml:"max_parallel,omitempty"`
	// Setup runs once after the clone and the services started, before any
	// member.
	Setup ContainerList `yaml:"setup,omitempty"`
	// Before runs before each member, in the member's lane.
	Before ContainerList `yaml:"before,omitempty"`
	// After runs after each member, whatever the member's result.
	After ContainerList `yaml:"after,omitempty"`
	// Teardown runs once after all members, whatever their results.
	Teardown ContainerList `yaml:"teardown,omitempty"`
}
