// Copyright 2022 Woodpecker Authors
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
	"context"
	"fmt"
	"slices"

	"github.com/rs/zerolog/log"

	"go.woodpecker-ci.org/woodpecker/v3/server"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
)

// Cancel the pipeline and returns the status.
func Cancel(ctx context.Context, _forge forge.Forge, store store.Store, repo *model.Repo, user *model.User, pipeline *model.Pipeline, cancelInfo *model.CancelInfo) error {
	if pipeline.Status != model.StatusRunning && pipeline.Status != model.StatusPending && pipeline.Status != model.StatusBlocked {
		return &ErrBadRequest{Msg: "Cannot cancel a non-running or non-pending or non-blocked pipeline"}
	}

	workflows, err := store.WorkflowGetTree(pipeline)
	if err != nil {
		return &ErrNotFound{Msg: err.Error()}
	}

	// Mark pending steps canceled before evicting their workflows: eviction
	// signals the agents, and an agent reporting its workflow done kills every
	// step still pending. Writing them afterwards from this stale tree races
	// with that and can leave them killed.
	for _, workflow := range workflows {
		for _, step := range workflow.Children {
			if step.State == model.StatusPending {
				if _, err = UpdateStepToStatusSkipped(store, *step, 0, model.StatusCanceled); err != nil {
					log.Error().Err(err).Msgf("cannot update workflow with id %d state", workflow.ID)
				}
			}
		}
	}

	// Then cancel/evict the running and pending workflows from the queue
	var workflowsToCancel []string
	for _, w := range workflows {
		if w.State == model.StatusRunning || w.State == model.StatusPending {
			workflowsToCancel = append(workflowsToCancel, fmt.Sprint(w.ID))
		}
	}

	if err := server.Config.Services.Scheduler.CancelWorkflows(ctx, workflowsToCancel); err != nil {
		log.Error().Err(err).Msgf("cancel workflows: %v", workflowsToCancel)
	}

	hasPendingOnly := true

	// Then update the DB status for pending pipelines
	// Running ones will be set when the agents stop on the cancel signal
	for _, workflow := range workflows {
		if workflow.State == model.StatusPending {
			if _, err = UpdateWorkflowToStatusSkipped(store, *workflow); err != nil {
				log.Error().Err(err).Msgf("cannot update workflow with id %d state", workflow.ID)
			}
		} else {
			hasPendingOnly = false
		}
	}

	plState := model.StatusKilled
	if hasPendingOnly {
		plState = model.StatusCanceled
	}
	killedPipeline, err := UpdateToStatusKilled(store, *pipeline, cancelInfo, plState)
	if err != nil {
		log.Error().Err(err).Msgf("UpdateToStatusKilled: %v", pipeline)
		return err
	}

	updatePipelineStatus(ctx, _forge, killedPipeline, repo, user)

	if killedPipeline.Workflows, err = store.WorkflowGetTree(killedPipeline); err != nil {
		return err
	}

	if err := server.Config.Services.Scheduler.PublishPipelineEvent(ctx, repo, killedPipeline); err != nil {
		log.Error().Err(err).Msg("could not push pipeline status change to pubsub provider")
	}

	return nil
}

func cancelPreviousPipelines(
	ctx context.Context,
	_forge forge.Forge,
	_store store.Store,
	pipeline *model.Pipeline,
	repo *model.Repo,
	user *model.User,
) error {
	// check this event should cancel previous pipelines
	eventIncluded := slices.Contains(repo.CancelPreviousPipelineEvents, pipeline.Event)
	if !eventIncluded {
		return nil
	}

	// get all active activeBuilds
	activeBuilds, err := _store.GetActivePipelineList(repo)
	if err != nil {
		return err
	}

	for _, active := range activeBuilds {
		if active.ID == pipeline.ID {
			// same pipeline. e.g. self
			continue
		}

		if !sameEventAndContext(pipeline, active) {
			continue
		}

		// let a pipeline that already started finish, only skip the queued ones
		if repo.CancelPreviousPipelineKeepRunning && active.Status == model.StatusRunning {
			continue
		}

		if err = Cancel(ctx, _forge, _store, repo, user, active, &model.CancelInfo{
			SupersededBy: pipeline.Number,
		}); err != nil {
			log.Error().
				Err(err).
				Str("ref", active.Ref).
				Int64("id", active.ID).
				Msg("failed to cancel pipeline")
		}
	}

	return nil
}

// sameEventAndContext reports whether two pipelines were triggered by the same
// event for the same context: the branch for pushes and train promotes, the
// refspec otherwise.
func sameEventAndContext(pipeline, other *model.Pipeline) bool {
	if other.Event != pipeline.Event {
		return false
	}

	switch pipeline.Event {
	case model.EventPush:
		return pipeline.Branch == other.Branch
	case model.EventPromote:
		// only a newer promote of the train branch supersedes an older one; the
		// merge train owns its bisect pipelines and never has them canceled
		return pipeline.Promote.IsTrain() && other.Promote.IsTrain() && pipeline.Branch == other.Branch
	default:
		return pipeline.Refspec == other.Refspec
	}
}

// squashPreviousPipelines returns the previous pipelines the given pipeline
// supersedes if the repo squashes pending pipelines, and adds their changed
// files to the pipeline's own, so path filters evaluated on it also cover the
// changes of the pipelines it is about to cancel.
//
// A previous pipeline is superseded if it was triggered by the same event for
// the same context, is older and has not started yet (pending or blocked).
// Pipelines without changed files (unknown changes, decided by `on_empty`)
// are only squashed into a pipeline without changed files and vice versa, as
// their union would change what `on_empty` decides for one of them.
//
// Call it before the pipeline's workflows are built, and hand the result to
// start, which cancels exactly these pipelines once the new one is queued.
func squashPreviousPipelines(_store store.Store, repo *model.Repo, pipeline *model.Pipeline) []*model.Pipeline {
	if !repo.CancelPreviousPipelineSquash || !slices.Contains(repo.CancelPreviousPipelineEvents, pipeline.Event) {
		return nil
	}

	activePipelines, err := _store.GetActivePipelineList(repo)
	if err != nil {
		log.Error().Err(err).Str("repo", repo.FullName).Msg("failed to list active pipelines to squash")
		return nil
	}

	ownChangesKnown := len(pipeline.ChangedFiles) > 0
	var superseded []*model.Pipeline
	for _, active := range activePipelines {
		if active.ID == pipeline.ID || active.Number >= pipeline.Number {
			continue
		}
		if active.Status != model.StatusPending && active.Status != model.StatusBlocked {
			continue
		}
		if !sameEventAndContext(pipeline, active) {
			continue
		}
		if (len(active.ChangedFiles) > 0) != ownChangesKnown {
			continue
		}
		superseded = append(superseded, active)
	}

	pipeline.ChangedFiles = mergeChangedFiles(pipeline.ChangedFiles, superseded)

	return superseded
}

// mergeChangedFiles returns the changed files followed by those of the given
// pipelines that are not in the list yet.
func mergeChangedFiles(changedFiles []string, pipelines []*model.Pipeline) []string {
	if len(pipelines) == 0 {
		return changedFiles
	}

	merged := slices.Clone(changedFiles)
	seen := make(map[string]struct{}, len(changedFiles))
	for _, file := range changedFiles {
		seen[file] = struct{}{}
	}
	for _, p := range pipelines {
		for _, file := range p.ChangedFiles {
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			merged = append(merged, file)
		}
	}
	return merged
}

// cancelSupersededPipelines cancels the pipelines squashed into the given one.
// A pipeline that started in the meantime is left to run: the new pipeline
// covers its changes as well, so nothing is lost, they just run twice.
func cancelSupersededPipelines(
	ctx context.Context,
	_forge forge.Forge,
	_store store.Store,
	pipeline *model.Pipeline,
	superseded []*model.Pipeline,
	repo *model.Repo,
	user *model.User,
) {
	for _, previous := range superseded {
		current, err := _store.GetPipeline(previous.ID)
		if err != nil {
			log.Error().Err(err).Int64("id", previous.ID).Msg("failed to load superseded pipeline")
			continue
		}

		if current.Status != model.StatusPending && current.Status != model.StatusBlocked {
			log.Debug().Str("repo", repo.FullName).Msgf("pipeline #%d superseded by #%d already left status %s, not canceling it", current.Number, pipeline.Number, current.Status)
			continue
		}

		if err := Cancel(ctx, _forge, _store, repo, user, current, &model.CancelInfo{
			SupersededBy: pipeline.Number,
		}); err != nil {
			log.Error().
				Err(err).
				Str("ref", current.Ref).
				Int64("id", current.ID).
				Msg("failed to cancel superseded pipeline")
		}
	}
}
