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

// Package train runs the merge train: it promotes the train branch onto its
// target once a promote pipeline is green, and takes the cars that broke a red
// promote off the train branch.
package train

import (
	"context"
	"errors"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/yaml/constraint"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

var (
	// ErrUnsupported is returned when the repo's forge cannot run a train.
	ErrUnsupported = errors.New("the forge of this repository cannot run a merge train")
	// ErrDisabled is returned for a repo whose train is off.
	ErrDisabled = errors.New("the merge train of this repository is disabled")
	// ErrBusy is returned when a retry is asked for while a promote or an
	// investigation runs.
	ErrBusy = errors.New("the merge train is busy")
	// ErrFrozen is returned when a retry is asked for while the train is
	// frozen; it has to be resumed instead.
	ErrFrozen = errors.New("the merge train is frozen, resume it first")
	// ErrFiltered is returned by Env.Start when no workflow matched.
	ErrFiltered = errors.New("no workflow matched the promote")
	// ErrNotRunning is returned when the controller has not been started.
	ErrNotRunning = errors.New("the merge train controller is not running")
)

// PromoteRequest asks for a promote pipeline.
type PromoteRequest struct {
	Branch  string
	SHA     string
	Message string
	Base    string
	Cars    []string
	Kind    model.PromoteKind
	// Files are the changed files the path filters see.
	Files []string
	// Workflows limits the pipeline to these workflows; empty runs all.
	Workflows []string
}

// PipelineResult is what the train needs to know about a pipeline it started.
type PipelineResult struct {
	Number int64
	Status model.StatusValue
	Errors []string
	// Workflows maps each workflow name to its merged state.
	Workflows map[string]model.StatusValue
}

// Done reports whether the pipeline reached a final status.
func (r *PipelineResult) Done() bool {
	switch r.Status {
	case model.StatusCreated, model.StatusPending, model.StatusRunning, model.StatusBlocked, "":
		return false
	}
	return true
}

// Failed returns the names of the workflows that failed or errored.
func (r *PipelineResult) Failed() []string {
	var out []string
	for name, state := range r.Workflows {
		if isFailure(state) {
			out = append(out, name)
		}
	}
	sortStrings(out)
	return out
}

func isFailure(s model.StatusValue) bool {
	return s == model.StatusFailure || s == model.StatusError
}

// Env is everything the controller needs from the server; the real one is
// built by NewEnv, tests use a fake.
type Env interface {
	Repo(id int64) (*model.Repo, error)
	// State returns the repo's train state, or nil if there is none yet.
	State(repoID int64) (*model.TrainState, error)
	SaveState(state *model.TrainState) error
	// Forge returns the repo's forge as a train and the user to act as.
	Forge(ctx context.Context, repo *model.Repo) (forge.Train, *model.User, error)
	// Start creates a promote pipeline and returns its number, or ErrFiltered.
	Start(ctx context.Context, repo *model.Repo, req *PromoteRequest) (int64, error)
	Result(repo *model.Repo, number int64) (*PipelineResult, error)
	// Workflows returns the workflow level when constraints of the pipeline's
	// configs, by workflow name.
	Workflows(repo *model.Repo, number int64) (map[string]constraint.When, error)
	// Baseline returns the workflows red on the target's last push pipeline
	// and on the given promote pipeline, if any.
	Baseline(repo *model.Repo, target string, lastPromote int64) ([]string, error)
	// Link returns the web URL of a pipeline.
	Link(repo *model.Repo, number int64) string
}
