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

package train

import (
	"context"
	"errors"
	"time"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/builder"
	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/yaml"
	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/yaml/constraint"
	"go.woodpecker-ci.org/woodpecker/v3/server"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge/common"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/pipeline"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
	"go.woodpecker-ci.org/woodpecker/v3/server/store/types"
)

// TrainAuthor is the author of the pipelines the train starts.
const TrainAuthor = "merge-train"

// baselinePushRuns is how many of the target's newest push pipelines the
// baseline looks through for a finished one.
const baselinePushRuns = 10

type storeEnv struct {
	store store.Store
}

// NewEnv returns the server's Env over the store and the configured forges.
func NewEnv(s store.Store) Env {
	return &storeEnv{store: s}
}

func (e *storeEnv) Repo(id int64) (*model.Repo, error) {
	return e.store.GetRepo(id)
}

func (e *storeEnv) State(repoID int64) (*model.TrainState, error) {
	st, err := e.store.TrainStateGet(repoID)
	if errors.Is(err, types.ErrRecordNotExist) {
		return nil, nil
	}
	return st, err
}

func (e *storeEnv) SaveState(st *model.TrainState) error {
	return e.store.TrainStateSave(st)
}

func (e *storeEnv) Forge(ctx context.Context, repo *model.Repo) (forge.Train, *model.User, error) {
	_forge, err := server.Config.Services.Manager.ForgeFromRepo(repo)
	if err != nil {
		return nil, nil, err
	}
	t, ok := _forge.(forge.Train)
	if !ok {
		return nil, nil, ErrUnsupported
	}
	user, err := e.store.GetUser(repo.UserID)
	if err != nil {
		return nil, nil, err
	}
	forge.Refresh(ctx, _forge, e.store, user)
	if repo.TrainBotToken != "" {
		bot := *user
		bot.AccessToken = repo.TrainBotToken
		bot.RefreshToken = ""
		user = &bot
	}
	return t, user, nil
}

func (e *storeEnv) Start(ctx context.Context, repo *model.Repo, req *PromoteRequest) (int64, error) {
	p := &model.Pipeline{
		Event:          model.EventPromote,
		Commit:         req.SHA,
		Branch:         req.Branch,
		Ref:            "refs/heads/" + req.Branch,
		Message:        req.Message,
		Author:         TrainAuthor,
		Sender:         TrainAuthor,
		Timestamp:      time.Now().Unix(),
		ChangedFiles:   req.Files,
		WorkflowFilter: req.Workflows,
		Promote:        &model.PromoteInfo{Base: req.Base, Cars: req.Cars, Kind: req.Kind},
	}
	created, err := pipeline.Create(ctx, e.store, repo, p)
	if errors.Is(err, pipeline.ErrFiltered) {
		return 0, ErrFiltered
	}
	if err != nil {
		return 0, err
	}
	return created.Number, nil
}

func (e *storeEnv) Result(repo *model.Repo, number int64) (*PipelineResult, error) {
	p, err := e.store.GetPipelineNumber(repo, number)
	if err != nil {
		return nil, err
	}
	workflows, err := e.store.WorkflowListByPipelines([]int64{p.ID})
	if err != nil {
		return nil, err
	}
	res := &PipelineResult{Number: p.Number, Status: p.Status, Workflows: mergeWorkflowStates(workflows)}
	for _, perr := range p.Errors {
		if !perr.IsWarning {
			res.Errors = append(res.Errors, perr.Message)
		}
	}
	return res, nil
}

// mergeWorkflowStates folds matrix workflows of one name into one state: any
// failure fails the name, otherwise any non-success wins.
func mergeWorkflowStates(workflows []*model.Workflow) map[string]model.StatusValue {
	out := make(map[string]model.StatusValue, len(workflows))
	for _, w := range workflows {
		prev, ok := out[w.Name]
		switch {
		case !ok, prev == model.StatusSuccess:
			out[w.Name] = w.State
		case isFailure(w.State):
			out[w.Name] = model.StatusFailure
		}
	}
	return out
}

func (e *storeEnv) Workflows(repo *model.Repo, number int64) (map[string]constraint.When, error) {
	p, err := e.store.GetPipelineNumber(repo, number)
	if err != nil {
		return nil, err
	}
	configs, err := e.store.ConfigsForPipeline(p.ID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]constraint.When, len(configs))
	for _, cfg := range configs {
		// a config that does not parse gets no filter: it matches every car
		var when constraint.When
		if wf, err := yaml.ParseBytes(cfg.Data); err == nil {
			when = wf.When
		}
		out[builder.SanitizePath(cfg.Name)] = when
	}
	return out, nil
}

func (e *storeEnv) Baseline(repo *model.Repo, target string, lastPromote int64) ([]string, error) {
	var ids []int64
	pushes, err := e.store.GetPipelineList(repo,
		&model.ListOptionsWithAll{ListOptions: &model.ListOptions{Page: 1, PerPage: baselinePushRuns}},
		&model.PipelineFilter{Branch: target, Events: []model.WebhookEvent{model.EventPush}})
	if err != nil {
		return nil, err
	}
	for _, p := range pushes {
		if (&PipelineResult{Status: p.Status}).Done() {
			ids = append(ids, p.ID)
			break
		}
	}
	if lastPromote > 0 {
		p, err := e.store.GetPipelineNumber(repo, lastPromote)
		if err != nil && !errors.Is(err, types.ErrRecordNotExist) {
			return nil, err
		}
		if p != nil && err == nil {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	workflows, err := e.store.WorkflowListByPipelines(ids)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, w := range workflows {
		if isFailure(w.State) && !contains(out, w.Name) {
			out = append(out, w.Name)
		}
	}
	return out, nil
}

func (e *storeEnv) Link(repo *model.Repo, number int64) string {
	return common.GetPipelineStatusURL(repo, &model.Pipeline{Number: number}, nil)
}
