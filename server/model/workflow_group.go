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
	"fmt"
	"regexp"
	"strings"
)

// The groups the built-in heuristic sorts workflows into.
const (
	WorkflowGroupBuild  = "build"
	WorkflowGroupTest   = "test"
	WorkflowGroupDeploy = "deploy"
	WorkflowGroupOther  = "other"
)

// WorkflowGroupRule sorts the workflows whose name matches Pattern into a
// group and subgroup. Pattern is a Go regular expression; Group and Subgroup
// are templates expanded with the match, so they may use $1 or ${name}.
type WorkflowGroupRule struct {
	Pattern  string `json:"pattern"`
	Group    string `json:"group"`
	Subgroup string `json:"subgroup,omitempty"`
} //	@name	WorkflowGroupRule

// workflowStages maps the first token of a workflow name (its stage) to a group.
var workflowStages = map[string]string{
	"build":       WorkflowGroupBuild,
	"module":      WorkflowGroupBuild,
	"service":     WorkflowGroupBuild,
	"svc":         WorkflowGroupBuild,
	"worker":      WorkflowGroupBuild,
	"operators":   WorkflowGroupBuild,
	"compile":     WorkflowGroupBuild,
	"lint":        WorkflowGroupBuild,
	"image":       WorkflowGroupBuild,
	"docker":      WorkflowGroupBuild,
	"test":        WorkflowGroupTest,
	"gate":        WorkflowGroupTest,
	"check":       WorkflowGroupTest,
	"nightly":     WorkflowGroupTest,
	"acceptance":  WorkflowGroupTest,
	"e2e":         WorkflowGroupTest,
	"integration": WorkflowGroupTest,
	"unit":        WorkflowGroupTest,
	"verify":      WorkflowGroupTest,
	"tests":       WorkflowGroupTest,
	"smoke":       WorkflowGroupTest,
	"regression":  WorkflowGroupTest,
	"qa":          WorkflowGroupTest,
	"bench":       WorkflowGroupTest,
	"benchmark":   WorkflowGroupTest,
	"perf":        WorkflowGroupTest,
	"fuzz":        WorkflowGroupTest,
	"conformance": WorkflowGroupTest,
	"deploy":      WorkflowGroupDeploy,
	"publish":     WorkflowGroupDeploy,
	"release":     WorkflowGroupDeploy,
	"staging":     WorkflowGroupDeploy,
	"promote":     WorkflowGroupDeploy,
	"rollout":     WorkflowGroupDeploy,
}

// workflowNoiseTokens are name tokens that describe the kind of thing a
// workflow builds rather than its subject, so they never become a subgroup.
var workflowNoiseTokens = map[string]bool{
	"service":   true,
	"svc":       true,
	"lib":       true,
	"module":    true,
	"rpc":       true,
	"store":     true,
	"search":    true,
	"worker":    true,
	"app":       true,
	"operators": true,
	"etl":       true,
	"ci":        true,
}

func splitWorkflowName(name string) []string {
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '-' || r == '_' || r == '/' || r == '.'
	})
}

// DefaultWorkflowGroup sorts a workflow by its name alone: the first token of
// the name (split on '-', '_', '/' and '.') is its stage, which gives the group
// (build, test, deploy or other), and the first later token that is neither a
// stage word nor a noise word like "service" or "rpc" is its subgroup, or ""
// when there is none.
func DefaultWorkflowGroup(name string) (group, subgroup string) {
	tokens := splitWorkflowName(name)
	if len(tokens) == 0 {
		return WorkflowGroupOther, ""
	}

	group, ok := workflowStages[tokens[0]]
	if !ok {
		group = WorkflowGroupOther
	}

	for _, token := range tokens[1:] {
		if _, isStage := workflowStages[token]; isStage || workflowNoiseTokens[token] {
			continue
		}
		return group, token
	}
	return group, ""
}

// ValidateWorkflowGroupRules checks that every rule has a valid pattern and a group.
func ValidateWorkflowGroupRules(rules []*WorkflowGroupRule) error {
	for i, rule := range rules {
		if rule == nil {
			return fmt.Errorf("workflow group rule %d is empty", i+1)
		}
		if rule.Pattern == "" {
			return fmt.Errorf("workflow group rule %d has no pattern", i+1)
		}
		if _, err := regexp.Compile(rule.Pattern); err != nil {
			return fmt.Errorf("workflow group rule %d has an invalid pattern: %w", i+1, err)
		}
		if strings.TrimSpace(rule.Group) == "" {
			return fmt.Errorf("workflow group rule %d has no group", i+1)
		}
	}
	return nil
}

type compiledWorkflowGroupRule struct {
	re       *regexp.Regexp
	group    string
	subgroup string
}

// WorkflowGrouper sorts workflows into groups by a repo's rules, falling back
// to DefaultWorkflowGroup for names no rule matches.
type WorkflowGrouper struct {
	rules []compiledWorkflowGroupRule
}

// NewWorkflowGrouper compiles the rules. Rules with an invalid pattern are
// skipped; PatchRepo rejects them, so they only come from older data.
func NewWorkflowGrouper(rules []*WorkflowGroupRule) *WorkflowGrouper {
	g := &WorkflowGrouper{}
	for _, rule := range rules {
		if rule == nil || rule.Pattern == "" {
			continue
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			continue
		}
		g.rules = append(g.rules, compiledWorkflowGroupRule{re: re, group: rule.Group, subgroup: rule.Subgroup})
	}
	return g
}

// Group returns the group and subgroup of a workflow name: those of the first
// rule whose pattern matches it, or else those of DefaultWorkflowGroup.
func (g *WorkflowGrouper) Group(name string) (group, subgroup string) {
	for _, rule := range g.rules {
		match := rule.re.FindStringSubmatchIndex(name)
		if match == nil {
			continue
		}
		group = string(rule.re.ExpandString(nil, rule.group, name, match))
		subgroup = string(rule.re.ExpandString(nil, rule.subgroup, name, match))
		if group == "" {
			group = WorkflowGroupOther
		}
		return group, subgroup
	}
	return DefaultWorkflowGroup(name)
}

// Assign sets Group and Subgroup on each workflow. A workflow that declares a
// kind is in the group of that kind, whatever its name says.
func (g *WorkflowGrouper) Assign(workflows []*Workflow) {
	for _, wf := range workflows {
		if wf != nil {
			wf.Group, wf.Subgroup = g.Group(wf.Name)
			if wf.Kind != "" {
				wf.Group = wf.Kind
			}
		}
	}
}

// AssignWorkflowGroups sets Group and Subgroup on the workflows by the repo's
// workflow group rules (repo may be nil).
func AssignWorkflowGroups(repo *Repo, workflows []*Workflow) {
	if len(workflows) == 0 {
		return
	}
	var rules []*WorkflowGroupRule
	if repo != nil {
		rules = repo.WorkflowGroups
	}
	NewWorkflowGrouper(rules).Assign(workflows)
}
