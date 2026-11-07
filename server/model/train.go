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
	"errors"
	"fmt"
)

// PromoteKind tells a merge train promote of the train branch head from a
// pipeline the train runs to investigate a regression.
type PromoteKind string //	@name	PromoteKind

const (
	PromoteKindTrain  PromoteKind = "train"
	PromoteKindBisect PromoteKind = "bisect"
)

// PromoteInfo describes what a promote pipeline tests.
type PromoteInfo struct {
	// Base is the target branch commit the train branch is compared against.
	Base string `json:"base"`
	// Cars are the train branch's first-parent commits under test, oldest first.
	Cars []string `json:"cars,omitempty"`
	// Kind tells a train promote from an investigation run.
	Kind PromoteKind `json:"kind"`
} //	@name	PromoteInfo

// IsTrain reports whether the pipeline promotes the train branch head.
func (p *PromoteInfo) IsTrain() bool {
	return p != nil && p.Kind == PromoteKindTrain
}

const (
	DefaultTrainBranch            = "dev"
	DefaultTrainTarget            = "main"
	DefaultTrainMaxBisectParallel = 2
)

// TrainSettings configures a repo's merge train.
type TrainSettings struct {
	Enabled            bool   `json:"enabled"`
	Branch             string `json:"branch"`
	Target             string `json:"target"`
	IgnoreBaseFailures bool   `json:"ignore_base_failures"`
	MaxBisectParallel  int    `json:"max_bisect_parallel"`
	FreezeProtection   bool   `json:"freeze_protection"`
} //	@name	TrainSettings

// DefaultTrainSettings returns the settings of a repo that never set any.
func DefaultTrainSettings() TrainSettings {
	return TrainSettings{
		Enabled:            false,
		Branch:             DefaultTrainBranch,
		Target:             DefaultTrainTarget,
		IgnoreBaseFailures: true,
		MaxBisectParallel:  DefaultTrainMaxBisectParallel,
		FreezeProtection:   true,
	}
}

// TrainConfig returns the repo's merge train settings with defaults filled in.
func (r *Repo) TrainConfig() TrainSettings {
	if r.Train == nil {
		return DefaultTrainSettings()
	}
	s := *r.Train
	if s.Branch == "" {
		s.Branch = DefaultTrainBranch
	}
	if s.Target == "" {
		s.Target = DefaultTrainTarget
	}
	if s.MaxBisectParallel < 1 {
		s.MaxBisectParallel = DefaultTrainMaxBisectParallel
	}
	return s
}

// TrainSettingsPatch changes some of a repo's merge train settings.
type TrainSettingsPatch struct {
	Enabled            *bool   `json:"enabled,omitempty"`
	Branch             *string `json:"branch,omitempty"`
	Target             *string `json:"target,omitempty"`
	IgnoreBaseFailures *bool   `json:"ignore_base_failures,omitempty"`
	MaxBisectParallel  *int    `json:"max_bisect_parallel,omitempty"`
	FreezeProtection   *bool   `json:"freeze_protection,omitempty"`
} //	@name	TrainSettingsPatch

var ErrInvalidTrainSettings = errors.New("invalid merge train settings")

// Apply returns the settings with the patch applied, or an error if the result
// is not a usable train.
func (p *TrainSettingsPatch) Apply(current TrainSettings) (TrainSettings, error) {
	s := current
	if p.Enabled != nil {
		s.Enabled = *p.Enabled
	}
	if p.Branch != nil {
		s.Branch = *p.Branch
	}
	if p.Target != nil {
		s.Target = *p.Target
	}
	if p.IgnoreBaseFailures != nil {
		s.IgnoreBaseFailures = *p.IgnoreBaseFailures
	}
	if p.MaxBisectParallel != nil {
		s.MaxBisectParallel = *p.MaxBisectParallel
	}
	if p.FreezeProtection != nil {
		s.FreezeProtection = *p.FreezeProtection
	}
	switch {
	case s.Branch == "" || s.Target == "":
		return current, fmt.Errorf("%w: branch and target are required", ErrInvalidTrainSettings)
	case s.Branch == s.Target:
		return current, fmt.Errorf("%w: branch and target must differ", ErrInvalidTrainSettings)
	case s.MaxBisectParallel < 1 || s.MaxBisectParallel > 16:
		return current, fmt.Errorf("%w: max_bisect_parallel must be between 1 and 16", ErrInvalidTrainSettings)
	}
	return s, nil
}

// TrainPhase is what the merge train of a repo is doing.
type TrainPhase string //	@name	TrainPhase

const (
	// TrainIdle waits for the train branch to get ahead of the target.
	TrainIdle TrainPhase = "idle"
	// TrainPromoting runs a promote pipeline of the train branch head.
	TrainPromoting TrainPhase = "promoting"
	// TrainInvestigating looks for the cars behind a red promote.
	TrainInvestigating TrainPhase = "investigating"
	// TrainHeld has nothing to test at the train branch head, for example
	// because no workflow matched; the next car starts a new promote.
	TrainHeld TrainPhase = "held"
	// TrainFrozen stopped the train until someone resumes it.
	TrainFrozen TrainPhase = "frozen"
)

// TrainCar is a first-parent commit of the train branch that is not on the
// target yet: usually the merge of a pull request.
type TrainCar struct {
	SHA    string   `json:"sha"`
	Parent string   `json:"parent,omitempty"`
	Title  string   `json:"title"`
	PR     int64    `json:"pr,omitempty"`
	Files  []string `json:"files,omitempty"`
} //	@name	TrainCar

// TrainProbe is a pipeline an investigation runs.
type TrainProbe struct {
	Pipeline int64  `json:"pipeline"`
	SHA      string `json:"sha"`
	// Index is the probed candidate's position, -1 for a run at the head.
	Index int `json:"index"`
	// Result is empty while running, else "success", "failure" or
	// "inconclusive".
	Result string `json:"result,omitempty"`
} //	@name	TrainProbe

// TrainInvestigationStep is where an investigation of a regressed workflow is.
type TrainInvestigationStep string //	@name	TrainInvestigationStep

const (
	// TrainStepRecheck re-runs a workflow no car can have broken.
	TrainStepRecheck TrainInvestigationStep = "recheck"
	// TrainStepFlakeCheck re-runs the workflow at the head before bisecting.
	TrainStepFlakeCheck TrainInvestigationStep = "flake_check"
	// TrainStepBisect narrows the candidates down to the first bad one.
	TrainStepBisect TrainInvestigationStep = "bisect"
	// TrainStepDone has an outcome.
	TrainStepDone TrainInvestigationStep = "done"
)

// TrainOutcome is what an investigation found.
type TrainOutcome string //	@name	TrainOutcome

const (
	TrainOutcomeCulprit      TrainOutcome = "culprit"
	TrainOutcomeFlake        TrainOutcome = "flake"
	TrainOutcomeBaseBroken   TrainOutcome = "base_broken"
	TrainOutcomeInconclusive TrainOutcome = "inconclusive"
)

// TrainInvestigation finds the car that broke one workflow.
type TrainInvestigation struct {
	Workflow string `json:"workflow"`
	// Candidates are the SHAs of the cars whose changes match the workflow's
	// path filter, oldest first.
	Candidates []string               `json:"candidates"`
	Step       TrainInvestigationStep `json:"step"`
	// Good is the index of the newest candidate known good, -1 for the base.
	Good int `json:"good"`
	// Bad is the index of the oldest candidate known bad.
	Bad     int           `json:"bad"`
	Probes  []*TrainProbe `json:"probes,omitempty"`
	Outcome TrainOutcome  `json:"outcome,omitempty"`
	Culprit string        `json:"culprit,omitempty"`
	Detail  string        `json:"detail,omitempty"`
} //	@name	TrainInvestigation

// TrainRevert is a car the train took off the train branch.
type TrainRevert struct {
	Car       string   `json:"car"`
	Title     string   `json:"title"`
	PR        int64    `json:"pr,omitempty"`
	Workflows []string `json:"workflows"`
	Promote   int64    `json:"promote"`
	Commit    string   `json:"commit,omitempty"`
	Created   int64    `json:"created"`
} //	@name	TrainRevert

// TrainEvent is one line of a merge train's history.
type TrainEvent struct {
	Time     int64  `json:"time"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Pipeline int64  `json:"pipeline,omitempty"`
	SHA      string `json:"sha,omitempty"`
} //	@name	TrainEvent

// TrainHistoryLimit caps the events kept per repo.
const TrainHistoryLimit = 100

// TrainState is the persisted state of a repo's merge train.
type TrainState struct {
	ID     int64      `json:"-"     xorm:"pk autoincr 'id'"`
	RepoID int64      `json:"-"     xorm:"UNIQUE 'repo_id'"`
	Phase  TrainPhase `json:"phase" xorm:"varchar(50) 'phase'"`
	// Frozen is set while the train branch must not take new cars.
	Frozen       bool   `json:"frozen"        xorm:"'frozen'"`
	FrozenReason string `json:"frozen_reason" xorm:"TEXT 'frozen_reason'"`
	// Head is the train branch commit the current promote tests.
	Head string `json:"head" xorm:"varchar(250) 'head'"`
	// Base is the target commit the current promote is compared against.
	Base string `json:"base" xorm:"varchar(250) 'base'"`
	// Pipeline is the number of the current train promote.
	Pipeline       int64                 `json:"pipeline"                 xorm:"'pipeline'"`
	Cars           []*TrainCar           `json:"cars,omitempty"           xorm:"json 'cars'"`
	Regressed      []string              `json:"regressed,omitempty"      xorm:"json 'regressed'"`
	Investigations []*TrainInvestigation `json:"investigations,omitempty" xorm:"json 'investigations'"`
	// HeldHead is the head the train held at, so it is not promoted again.
	HeldHead string `json:"held_head,omitempty" xorm:"varchar(250) 'held_head'"`
	// LastPromoted is the last commit fast-forwarded onto the target.
	LastPromoted string `json:"last_promoted,omitempty" xorm:"varchar(250) 'last_promoted'"`
	// LastPromotePipeline is the number of the last promote of any result.
	LastPromotePipeline int64          `json:"last_promote_pipeline,omitempty" xorm:"'last_promote_pipeline'"`
	LastResult          string         `json:"last_result,omitempty"           xorm:"TEXT 'last_result'"`
	Reverts             []*TrainRevert `json:"reverts,omitempty"               xorm:"json 'reverts'"`
	History             []*TrainEvent  `json:"history,omitempty"               xorm:"json 'history'"`
	Updated             int64          `json:"updated"                         xorm:"'updated'"`
} //	@name	TrainState

// TableName return database table name for xorm.
func (TrainState) TableName() string {
	return "train_states"
}

// Log adds an event to the train's history, dropping the oldest beyond the
// limit.
func (s *TrainState) Log(now int64, kind, message string, pipeline int64, sha string) {
	s.History = append(s.History, &TrainEvent{Time: now, Kind: kind, Message: message, Pipeline: pipeline, SHA: sha})
	if over := len(s.History) - TrainHistoryLimit; over > 0 {
		s.History = s.History[over:]
	}
}

// Car returns the car with the given SHA from the current promote's cars.
func (s *TrainState) Car(sha string) *TrainCar {
	for _, car := range s.Cars {
		if car.SHA == sha {
			return car
		}
	}
	return nil
}
