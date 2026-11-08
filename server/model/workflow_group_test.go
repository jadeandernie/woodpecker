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

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultWorkflowGroup(t *testing.T) {
	tests := []struct {
		name     string
		group    string
		subgroup string
	}{
		{"gate-lint", "test", ""},
		{"gate-web-typecheck", "test", "web"},
		{"module-lib-tensor", "build", "tensor"},
		{"module-gen", "build", "gen"},
		{"service-knowledge-store-rpc", "build", "knowledge"},
		{"svc-knowledge", "build", "knowledge"},
		{"svc-auth_service", "build", "auth"},
		{"test-service-tensor-worker", "test", "tensor"},
		{"publish-service-knowledge", "deploy", "knowledge"},
		{"deploy-service-knowledge-store-rpc", "deploy", "knowledge"},
		{"deploy-service-auth-service-rpc", "deploy", "auth"},
		{"nightly-tensor", "test", "tensor"},
		{"acceptance-definition-slice", "test", "definition"},
		{"gate-medical-smoke", "test", "medical"},
		{"smoke-service-knowledge", "test", "knowledge"},
		{"regression-tensor", "test", "tensor"},
		{"qa-web", "test", "web"},
		{"bench-tensor-rpc", "test", "tensor"},
		{"benchmark_search", "test", ""},
		{"perf-knowledge", "test", "knowledge"},
		{"fuzz-parser", "test", "parser"},
		{"conformance-api", "test", "api"},
		{"tests-unit-auth", "test", "auth"},
		{"operators-app-web", "build", "web"},
		{"publish-operators-app-web", "deploy", "web"},
		{"worker-etl-x", "build", "x"},
		{"staging", "deploy", ""},
		{"ci", "other", ""},
		{"main", "other", ""},
		{"Build/Frontend", "build", "frontend"},
		{"", "other", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, subgroup := DefaultWorkflowGroup(tt.name)
			assert.Equal(t, tt.group, group)
			assert.Equal(t, tt.subgroup, subgroup)
		})
	}
}

func TestWorkflowGrouperRules(t *testing.T) {
	g := NewWorkflowGrouper([]*WorkflowGroupRule{
		{Pattern: `^bad(`, Group: "skipped"},
		{Pattern: `^e2e-(?P<subject>[a-z]+)$`, Group: "test", Subgroup: "${subject}"},
		{Pattern: `^(ship)-(\w+)`, Group: "deploy", Subgroup: "$2"},
		{Pattern: `^docs$`, Group: "other"},
		{Pattern: `^empty`, Group: "$9"},
	})

	tests := []struct {
		name     string
		group    string
		subgroup string
	}{
		{"e2e-knowledge", "test", "knowledge"},
		{"ship-tensor", "deploy", "tensor"},
		{"docs", "other", ""},
		{"empty-template", "other", ""},
		// no rule matches: the built-in heuristic
		{"svc-knowledge", "build", "knowledge"},
		{"bad(", "other", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, subgroup := g.Group(tt.name)
			assert.Equal(t, tt.group, group)
			assert.Equal(t, tt.subgroup, subgroup)
		})
	}
}

func TestAssignWorkflowGroups(t *testing.T) {
	workflows := []*Workflow{{Name: "deploy-service-knowledge-store-rpc"}, {Name: "web-check"}, nil}
	repo := &Repo{WorkflowGroups: []*WorkflowGroupRule{{Pattern: `^web-`, Group: "test", Subgroup: "web"}}}
	AssignWorkflowGroups(repo, workflows)
	assert.Equal(t, "deploy", workflows[0].Group)
	assert.Equal(t, "knowledge", workflows[0].Subgroup)
	assert.Equal(t, "test", workflows[1].Group)
	assert.Equal(t, "web", workflows[1].Subgroup)

	AssignWorkflowGroups(nil, workflows[:1])
	assert.Equal(t, "deploy", workflows[0].Group)
}

func TestValidateWorkflowGroupRules(t *testing.T) {
	assert.NoError(t, ValidateWorkflowGroupRules(nil))
	assert.NoError(t, ValidateWorkflowGroupRules([]*WorkflowGroupRule{{Pattern: `^svc-(\w+)`, Group: "build", Subgroup: "$1"}}))
	assert.Error(t, ValidateWorkflowGroupRules([]*WorkflowGroupRule{{Pattern: `^svc-(`, Group: "build"}}))
	assert.Error(t, ValidateWorkflowGroupRules([]*WorkflowGroupRule{{Pattern: `^svc`, Group: " "}}))
	assert.Error(t, ValidateWorkflowGroupRules([]*WorkflowGroupRule{{Group: "build"}}))
	assert.Error(t, ValidateWorkflowGroupRules([]*WorkflowGroupRule{nil}))
}
