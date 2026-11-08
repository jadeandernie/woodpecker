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

package datastore

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"xorm.io/builder"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// likeEscapeChar escapes LIKE wildcards. It is not a backslash, whose meaning
// in string literals differs between databases.
const likeEscapeChar = "!"

var likeEscaper = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")

// likeCond matches column against a LIKE pattern built by likePattern.
func likeCond(column, pattern string) builder.Cond {
	return builder.Expr(fmt.Sprintf("(%s LIKE ? ESCAPE '%s')", column, likeEscapeChar), pattern)
}

// likePattern escapes s and turns its * globs into %.
func likePattern(s string) string {
	return strings.ReplaceAll(likeEscaper.Replace(s), "*", "%")
}

// textCond matches column exactly, or as a glob when value has a *.
func textCond(column, value string) builder.Cond {
	if strings.Contains(value, "*") {
		return likeCond(column, likePattern(value))
	}
	return builder.Expr(fmt.Sprintf("(%s = ?)", column), value)
}

// notCond negates cond, keeping it in parentheses whatever its kind.
func notCond(cond builder.Cond) (builder.Cond, error) {
	sql, args, err := builder.ToSQL(cond)
	if err != nil {
		return nil, err
	}
	return builder.Expr("NOT ("+sql+")", args...), nil
}

func falseCond() builder.Cond {
	return builder.Expr("(1 = 0)")
}

var hexPrefix = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// pipelineQueryResolver turns query terms into conditions on the pipelines
// table of one repo.
type pipelineQueryResolver struct {
	s       storage
	repo    *model.Repo
	grouper *model.WorkflowGrouper
	names   []string // distinct workflow names of the repo, loaded on first use
	loaded  bool
}

func (s storage) pipelineQueryCond(repo *model.Repo, q *model.PipelineQuery) (builder.Cond, error) {
	r := &pipelineQueryResolver{s: s, repo: repo, grouper: model.NewWorkflowGrouper(repo.WorkflowGroups)}
	cond := builder.NewCond()
	for _, term := range q.Terms {
		termCond, err := r.termCond(term)
		if err != nil {
			return nil, err
		}
		cond = cond.And(termCond)
	}
	return cond, nil
}

func (r *pipelineQueryResolver) quote(column string) string {
	return r.s.engine.Dialect().Quoter().Quote(column)
}

func (r *pipelineQueryResolver) termCond(term *model.PipelineQueryTerm) (builder.Cond, error) {
	// A duration filter only ever selects finished pipelines; negation
	// applies to the range.
	var base builder.Cond
	if term.Key == model.PipelineQueryDuration {
		base = builder.Expr("(started > 0 AND finished >= started)")
	}

	var cond builder.Cond
	switch term.Key {
	case model.PipelineQueryKind, model.PipelineQueryGroup, model.PipelineQueryWorkflow:
		c, err := r.workflowCond(term)
		if err != nil {
			return nil, err
		}
		cond = c
	case model.PipelineQueryStatus, model.PipelineQueryEvent:
		values := make([]any, 0, len(term.Values))
		for _, v := range term.Values {
			values = append(values, v.Text)
		}
		cond = builder.In(term.Key, values...)
	default:
		or := builder.NewCond()
		for _, v := range term.Values {
			c, err := r.valueCond(term.Key, v)
			if err != nil {
				return nil, err
			}
			or = or.Or(c)
		}
		cond = or
	}

	if term.Negate {
		c, err := notCond(cond)
		if err != nil {
			return nil, err
		}
		cond = c
	}
	if base != nil {
		cond = builder.And(base, cond)
	}
	return cond, nil
}

func rangeCond(column string, v *model.PipelineQueryValue) builder.Cond {
	cond := builder.NewCond()
	if v.Min != nil {
		cond = cond.And(builder.Expr(fmt.Sprintf("(%s >= ?)", column), *v.Min))
	}
	if v.Max != nil {
		cond = cond.And(builder.Expr(fmt.Sprintf("(%s <= ?)", column), *v.Max))
	}
	if !cond.IsValid() {
		return builder.Expr("(1 = 1)")
	}
	return cond
}

func (r *pipelineQueryResolver) valueCond(key string, v *model.PipelineQueryValue) (builder.Cond, error) {
	switch key {
	case model.PipelineQueryText:
		pattern := "%" + likeEscaper.Replace(strings.ToLower(v.Text)) + "%"
		cond := builder.Or(
			likeCond("LOWER(COALESCE(message, ''))", pattern),
			likeCond("LOWER(COALESCE(title, ''))", pattern),
		)
		if hexPrefix.MatchString(v.Text) {
			cond = builder.Or(cond, likeCond(r.quote("commit"), strings.ToLower(v.Text)+"%"))
		}
		return cond, nil

	case model.PipelineQueryBranch:
		return textCond("branch", v.Text), nil

	case model.PipelineQueryAuthor:
		value := strings.ToLower(v.Text)
		return builder.Or(
			textCond("LOWER(COALESCE(author, ''))", value),
			textCond("LOWER(COALESCE(sender, ''))", value),
		), nil

	case model.PipelineQueryRef:
		if strings.Contains(v.Text, "*") {
			return textCond("ref", v.Text), nil
		}
		return likeCond("ref", "%"+likeEscaper.Replace(v.Text)+"%"), nil

	case model.PipelineQueryDate:
		return rangeCond("created", v), nil

	case model.PipelineQueryDuration:
		return rangeCond("(finished - started)", v), nil

	case model.PipelineQueryNumber:
		return rangeCond("number", v), nil

	case model.PipelineQueryCommit:
		if v.CommitTo == "" {
			return likeCond(r.quote("commit"), v.Text+"%"), nil
		}
		return r.commitRangeCond(v.Text, v.CommitTo)
	}
	return nil, fmt.Errorf("unhandled pipeline query key %q", key)
}

// commitNumbers gives the lowest and highest number of the repo's pipelines
// whose commit starts with prefix.
func (r *pipelineQueryResolver) commitNumbers(prefix string) (lo, hi int64, err error) {
	var bounds struct {
		Lo    int64 `xorm:"lo"`
		Hi    int64 `xorm:"hi"`
		Count int64 `xorm:"cnt"`
	}
	if _, err := r.s.engine.Table("pipelines").
		Select("MIN(number) AS lo, MAX(number) AS hi, COUNT(*) AS cnt").
		Where(builder.Eq{"repo_id": r.repo.ID}).
		And(likeCond(r.quote("commit"), prefix+"%")).
		Get(&bounds); err != nil {
		return 0, 0, err
	}
	if bounds.Count == 0 {
		return 0, 0, &model.PipelineQueryError{Msg: fmt.Sprintf("no pipeline has a commit starting with %q", prefix)}
	}
	return bounds.Lo, bounds.Hi, nil
}

// commitRangeCond selects the pipelines from the oldest one of the first
// commit prefix to the newest one of the second, by pipeline number: the
// server knows no git history.
func (r *pipelineQueryResolver) commitRangeCond(from, to string) (builder.Cond, error) {
	fromLo, fromHi, err := r.commitNumbers(from)
	if err != nil {
		return nil, err
	}
	toLo, toHi, err := r.commitNumbers(to)
	if err != nil {
		return nil, err
	}
	lo, hi := fromLo, toHi
	if lo > hi {
		// the range was written newest first
		lo, hi = toLo, fromHi
	}
	return rangeCond("number", &model.PipelineQueryValue{Min: &lo, Max: &hi}), nil
}

// workflowNames returns the distinct names of the workflows of the repo's pipelines.
func (r *pipelineQueryResolver) workflowNames() ([]string, error) {
	if r.loaded {
		return r.names, nil
	}
	names := make([]string, 0, 64)
	if err := r.s.engine.Table("workflows").
		Select("DISTINCT name").
		Where(builder.In("pipeline_id", builder.Select("id").From("pipelines").Where(builder.Eq{"repo_id": r.repo.ID}))).
		Find(&names); err != nil {
		return nil, err
	}
	r.names, r.loaded = names, true
	return names, nil
}

// workflowCond selects the pipelines with a workflow of the term's kinds,
// subgroups or names (any of its values). The kind, group and subgroup of a
// workflow name are derived in Go, so the condition lists the names that match.
func (r *pipelineQueryResolver) workflowCond(term *model.PipelineQueryTerm) (builder.Cond, error) {
	names, err := r.workflowNames()
	if err != nil {
		return nil, err
	}

	matches := func(name string) bool {
		for _, v := range term.Values {
			switch term.Key {
			case model.PipelineQueryKind:
				if group, _ := r.grouper.Group(name); strings.ToLower(group) == v.Text {
					return true
				}
			case model.PipelineQueryGroup:
				if _, subgroup := r.grouper.Group(name); strings.ToLower(subgroup) == v.Text {
					return true
				}
			case model.PipelineQueryWorkflow:
				if model.QueryGlobMatch(v.Text, name) {
					return true
				}
			}
		}
		return false
	}

	matched := make([]any, 0, len(names))
	for _, name := range names {
		if matches(name) {
			matched = append(matched, name)
		}
	}

	var wfCond builder.Cond
	switch {
	case term.Key == model.PipelineQueryKind:
		// a declared kind wins over the name
		kinds := make([]any, 0, len(term.Values))
		for _, v := range term.Values {
			kinds = append(kinds, v.Text)
		}
		wfCond = builder.In("w.kind", kinds...)
		if len(matched) > 0 {
			wfCond = builder.Or(wfCond, builder.And(builder.Eq{"w.kind": ""}, builder.In("w.name", matched...)))
		}
	case len(matched) == 0:
		return falseCond(), nil
	default:
		wfCond = builder.In("w.name", matched...)
	}

	sql, args, err := builder.ToSQL(wfCond)
	if err != nil {
		return nil, err
	}
	return builder.Expr("EXISTS (SELECT 1 FROM workflows w WHERE w.pipeline_id = pipelines.id AND ("+sql+"))", args...), nil
}

// GetPipelineFacets lists the branches, authors, events, workflow kinds,
// subgroups and names of the repo's newest pipelines (at most limit).
// Branches and authors are newest first; the rest are sorted.
func (s storage) GetPipelineFacets(repo *model.Repo, limit int) (*model.PipelineFacets, error) {
	type row struct {
		Number int64              `xorm:"number"`
		Branch string             `xorm:"branch"`
		Author string             `xorm:"author"`
		Sender string             `xorm:"sender"`
		Event  model.WebhookEvent `xorm:"event"`
	}
	rows := make([]*row, 0, limit)
	if err := s.engine.Table("pipelines").
		Select("number, branch, author, sender, event").
		Where(builder.Eq{"repo_id": repo.ID}).
		Desc("number").
		Limit(limit).
		Find(&rows); err != nil {
		return nil, err
	}

	facets := &model.PipelineFacets{
		Branches:  []string{},
		Authors:   []string{},
		Events:    []model.WebhookEvent{},
		Kinds:     []string{},
		Groups:    []string{},
		Workflows: []string{},
	}
	if len(rows) == 0 {
		return facets, nil
	}

	seen := make(map[string]bool)
	add := func(kind, value string, list *[]string) {
		if value == "" || seen[kind+"\x00"+value] {
			return
		}
		seen[kind+"\x00"+value] = true
		*list = append(*list, value)
	}
	var events []string
	minNumber := rows[0].Number
	for _, r := range rows {
		add("branch", r.Branch, &facets.Branches)
		add("author", r.Author, &facets.Authors)
		add("author", r.Sender, &facets.Authors)
		add("event", string(r.Event), &events)
		minNumber = min(minNumber, r.Number)
	}
	sort.Strings(events)
	for _, e := range events {
		facets.Events = append(facets.Events, model.WebhookEvent(e))
	}

	type wfRow struct {
		Name string `xorm:"name"`
		Kind string `xorm:"kind"`
	}
	wfRows := make([]*wfRow, 0, 64)
	if err := s.engine.Table("workflows").
		Select("DISTINCT name, kind").
		Where(builder.In("pipeline_id", builder.Select("id").From("pipelines").
			Where(builder.Eq{"repo_id": repo.ID}.And(builder.Gte{"number": minNumber})))).
		Find(&wfRows); err != nil {
		return nil, err
	}

	workflows := make([]*model.Workflow, 0, len(wfRows))
	for _, r := range wfRows {
		workflows = append(workflows, &model.Workflow{Name: r.Name, Kind: r.Kind})
	}
	model.AssignWorkflowGroups(repo, workflows)
	for _, wf := range workflows {
		add("workflow", wf.Name, &facets.Workflows)
		add("group", wf.Subgroup, &facets.Groups)
	}
	if kinds := model.WorkflowKinds(workflows); kinds != nil {
		facets.Kinds = kinds
	}
	sort.Strings(facets.Workflows)
	sort.Strings(facets.Groups)
	return facets, nil
}
