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

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/rs/zerolog/log"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/builder"
	"go.woodpecker-ci.org/woodpecker/v3/server"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
)

// A concurrency group without a limit (`concurrency: {group: <name>}`) lets
// one pipeline of a repository at a time run its workflows in the group, and
// keeps at most one more pipeline's waiting: like GitHub Actions' concurrency
// groups without cancel-in-progress. A pipeline's workflows in a group are its
// unit in the group. A unit is
//   - queued while none of its workflows has started,
//   - in progress once one has started, until all are done.
//
// The queue hands out the workflows of a queued unit only while no other
// pipeline's unit in the group is in progress (see queue.Queue.HoldGroup).
// When a pipeline with a unit in a group starts, the queued units of the
// group are superseded but for the newest pipeline's: their workflows are
// taken off the queue and end skipped, giving the newer pipeline as the reason.
// Both derive the units from the workflows in the store, so they cover units
// queued or started before a server restart too.

// supersededError is the error of a workflow superseded by the given pipeline.
func supersededError(number int64) string {
	return fmt.Sprintf("superseded by #%d", number)
}

// unitState is the state of a pipeline's unit in a concurrency group.
type unitState int

const (
	unitDone unitState = iota
	unitQueued
	unitInProgress
)

// stateOfUnit returns the state of the unit made of the given workflows.
func stateOfUnit(workflows []*model.Workflow) unitState {
	unfinished, started := false, false
	for _, workflow := range workflows {
		if workflow.Running() {
			unfinished = true
		}
		if workflow.State == model.StatusRunning || workflow.Started != 0 {
			started = true
		}
	}
	switch {
	case !unfinished:
		return unitDone
	case started:
		return unitInProgress
	default:
		return unitQueued
	}
}

// groupNames returns the names of the workflows of the given items by the
// concurrency group without a limit they are in.
func groupNames(items []*builder.Item) map[string][]string {
	names := map[string][]string{}
	for _, item := range items {
		if group := exclusiveGroup(item); group != "" {
			names[group] = append(names[group], item.Workflow.Name)
		}
	}
	return names
}

// unitOf returns the workflows of the pipeline in the concurrency group, to
// which the pipeline of the given branch has the workflows of the given names.
//
// A workflow in no group counts as one in the group if it has one of these
// names and its pipeline is of the same branch: it is from a pipeline created
// before the group was declared, or before the server knew about groups, and
// is superseded or waited for as the newer pipeline's workflows of its name.
func unitOf(pipeline *model.Pipeline, workflows []*model.Workflow, group string, names []string, branch string) []*model.Workflow {
	var unit []*model.Workflow
	for _, workflow := range workflows {
		switch {
		case workflow.ConcurrencyGroup == group:
		case workflow.ConcurrencyGroup == "" && pipeline.Branch == branch && slices.Contains(names, workflow.Name):
		default:
			continue
		}
		unit = append(unit, workflow)
	}
	return unit
}

// unfinishedIDs returns the IDs of the given workflows that are not done yet,
// which are those of their tasks in the queue.
func unfinishedIDs(workflows []*model.Workflow) []string {
	var ids []string
	for _, workflow := range workflows {
		if workflow.Running() {
			ids = append(ids, fmt.Sprint(workflow.ID))
		}
	}
	return ids
}

// pipelineTree is an active pipeline with its workflows.
type pipelineTree struct {
	pipeline  *model.Pipeline
	workflows []*model.Workflow
}

// otherActivePipelines returns the repo's active pipelines but the given one
// and those waiting for approval, newest first, with their workflows.
func otherActivePipelines(_store store.Store, repo *model.Repo, pipeline *model.Pipeline) []pipelineTree {
	active, err := _store.GetActivePipelineList(repo)
	if err != nil {
		log.Error().Err(err).Str("repo", repo.FullName).Msg("failed to list active pipelines for concurrency groups")
		return nil
	}

	trees := make([]pipelineTree, 0, len(active))
	for _, other := range active {
		if other.ID == pipeline.ID || other.Status == model.StatusBlocked {
			continue
		}
		workflows, err := _store.WorkflowGetTree(other)
		if err != nil {
			log.Error().Err(err).Str("repo", repo.FullName).Msgf("failed to load workflows of pipeline #%d for concurrency groups", other.Number)
			continue
		}
		trees = append(trees, pipelineTree{pipeline: other, workflows: workflows})
	}
	return trees
}

// holdGroupsInProgress makes the other pipelines with a unit in progress in one
// of the given groups hold it in the queue, so the queue does not hand out the
// new pipeline's workflows in the group while they run. The queue knows this
// of the units it handed out itself already: this is for those handed out
// before a server restart, and those of workflows in no group (see unitOf).
func holdGroupsInProgress(ctx context.Context, repo *model.Repo, pipeline *model.Pipeline, groups map[string][]string, others []pipelineTree) {
	for group, names := range groups {
		for _, other := range others {
			unit := unitOf(other.pipeline, other.workflows, group, names, pipeline.Branch)
			if stateOfUnit(unit) != unitInProgress {
				continue
			}
			server.Config.Services.Scheduler.HoldGroup(ctx, concurrencyGroupKey(repo.ID, group), other.pipeline.ID, unfinishedIDs(unit))
		}
	}
}

// pipelineUnit is a pipeline's unit in a concurrency group.
type pipelineUnit struct {
	pipeline  *model.Pipeline
	workflows []*model.Workflow
	state     unitState
}

// supersedeQueuedUnits supersedes the queued units in each of the given groups
// but the newest pipeline's: the new pipeline's own unit unless a newer
// pipeline has one queued or in progress, as the older one would undo what the
// newer one deploys. Pipelines squashed into the new one are left to their
// cancellation. A unit the queue already handed out a workflow of is left as
// it is, it is in progress now.
//
// It returns the new pipeline, updated if its own unit was superseded.
func supersedeQueuedUnits(
	ctx context.Context,
	_forge forge.Forge,
	_store store.Store,
	pipeline *model.Pipeline,
	groups map[string][]string,
	others []pipelineTree,
	squashed []*model.Pipeline,
	repo *model.Repo,
	user *model.User,
) *model.Pipeline {
	// the workflows to supersede by pipeline, and the pipeline they are
	// superseded by
	type supersede struct {
		pipeline  *model.Pipeline
		workflows []string
		by        int64
	}
	var toSupersede []supersede

	for group, names := range groups {
		units := []pipelineUnit{{pipeline: pipeline, workflows: unitOf(pipeline, pipeline.Workflows, group, names, pipeline.Branch), state: unitQueued}}
		for _, other := range others {
			if slices.ContainsFunc(squashed, func(p *model.Pipeline) bool { return p.ID == other.pipeline.ID }) {
				continue
			}
			unit := unitOf(other.pipeline, other.workflows, group, names, pipeline.Branch)
			if state := stateOfUnit(unit); state != unitDone {
				units = append(units, pipelineUnit{pipeline: other.pipeline, workflows: unit, state: state})
			}
		}

		newest := slices.MaxFunc(units, func(a, b pipelineUnit) int {
			return cmp.Compare(a.pipeline.Number, b.pipeline.Number)
		})
		for _, unit := range units {
			if unit.state != unitQueued || unit.pipeline.ID == newest.pipeline.ID {
				continue
			}
			ids := unfinishedIDs(unit.workflows)
			if len(ids) == 0 {
				continue
			}
			toSupersede = append(toSupersede, supersede{pipeline: unit.pipeline, workflows: ids, by: newest.pipeline.Number})
		}
	}

	for _, s := range toSupersede {
		ok, err := server.Config.Services.Scheduler.SupersedeWorkflows(ctx, s.workflows)
		if err != nil {
			log.Error().Err(err).Str("repo", repo.FullName).Msgf("failed to supersede workflows %v of pipeline #%d", s.workflows, s.pipeline.Number)
		}
		if !ok {
			log.Debug().Str("repo", repo.FullName).Msgf("workflows %v of pipeline #%d started meanwhile, not superseding them by #%d", s.workflows, s.pipeline.Number, s.by)
			continue
		}

		updated := markSuperseded(ctx, _forge, _store, s.pipeline, s.workflows, s.by, repo, user)
		if updated != nil && updated.ID == pipeline.ID {
			pipeline = updated
		}
	}

	return pipeline
}

// markSuperseded marks the given workflows of the pipeline, which the queue
// dropped, and their pending steps skipped as superseded by the given
// pipeline, and finishes the pipeline if nothing else of it is left to run.
// It returns the updated pipeline, or nil if it failed to load it.
func markSuperseded(ctx context.Context, _forge forge.Forge, _store store.Store, pipeline *model.Pipeline, workflowIDs []string, by int64, repo *model.Repo, user *model.User) *model.Pipeline {
	logger := log.With().Str("repo", repo.FullName).Int64("pipeline", pipeline.Number).Logger()
	reason := supersededError(by)

	current, err := _store.GetPipeline(pipeline.ID)
	if err != nil {
		logger.Error().Err(err).Msg("failed to load superseded pipeline")
		return nil
	}
	workflows, err := _store.WorkflowGetTree(current)
	if err != nil {
		logger.Error().Err(err).Msg("failed to load workflows of superseded pipeline")
		return nil
	}

	var superseded []*model.Workflow
	for _, workflow := range workflows {
		// only those still pending: the queue no longer hands them out, but a
		// stale view of the store may have missed one that is done already
		if !slices.Contains(workflowIDs, fmt.Sprint(workflow.ID)) || workflow.State != model.StatusPending {
			continue
		}
		workflow.State = model.StatusSkipped
		workflow.Error = reason
		if err := _store.WorkflowUpdate(workflow); err != nil {
			logger.Error().Err(err).Msgf("failed to mark workflow %s superseded", workflow.Name)
		}
		for _, step := range workflow.Children {
			if step.State != model.StatusPending {
				continue
			}
			step.State = model.StatusSkipped
			step.Error = reason
			if err := _store.StepUpdate(step); err != nil {
				logger.Error().Err(err).Msgf("failed to mark step %s of workflow %s superseded", step.Name, workflow.Name)
			}
		}
		superseded = append(superseded, workflow)
	}
	logger.Info().Msgf("superseded workflows %v by #%d", workflowIDs, by)

	// the pipeline is done if this was all of it left to run, as no agent
	// reports done for the superseded workflows
	if !model.IsThereRunningStage(workflows) {
		status := PipelineStatus(workflows)
		if allSkipped(workflows) {
			status = model.StatusSkipped
		}
		if current, err = UpdateStatusToDone(_store, *current, status, time.Now().Unix()); err != nil {
			logger.Error().Err(err).Msg("failed to finish superseded pipeline")
		}
	}
	current.Workflows = workflows

	if err := server.Config.Services.Scheduler.PublishPipelineEvent(ctx, repo, current); err != nil {
		logger.Error().Err(err).Msg("could not push pipeline status change to pubsub provider")
	}
	for _, workflow := range superseded {
		if err := _forge.Status(ctx, user, repo, current, workflow); err != nil {
			logger.Error().Err(err).Msgf("error setting commit status of superseded workflow %s", workflow.Name)
			break
		}
	}

	return current
}

// allSkipped reports whether none of the workflows ran.
func allSkipped(workflows []*model.Workflow) bool {
	for _, workflow := range workflows {
		if workflow.State != model.StatusSkipped {
			return false
		}
	}
	return len(workflows) > 0
}

// HoldGroupsInProgress makes the pipelines with a unit in progress in a
// concurrency group without a limit hold the group in the queue, for each
// repository the queue has workflows of such a group of. Call it once the
// scheduler is set up on server start, before agents connect: the queue
// restored from the store only has the workflows that were not handed out,
// so without this it would hand out a unit queued in a group while another
// pipeline's started before the restart is still running.
func HoldGroupsInProgress(ctx context.Context, _store store.Store) {
	info := server.Config.Services.Scheduler.Info(ctx)

	repoIDs := map[int64]struct{}{}
	for _, task := range slices.Concat(info.Pending, info.WaitingOnDeps) {
		if task.ExclusiveGroup() != "" {
			repoIDs[task.RepoID] = struct{}{}
		}
	}

	for repoID := range repoIDs {
		repo, err := _store.GetRepo(repoID)
		if err != nil {
			log.Error().Err(err).Int64("repo-id", repoID).Msg("failed to load repo to hold its concurrency groups")
			continue
		}
		active, err := _store.GetActivePipelineList(repo)
		if err != nil {
			log.Error().Err(err).Str("repo", repo.FullName).Msg("failed to list active pipelines to hold their concurrency groups")
			continue
		}
		for _, pipeline := range active {
			if pipeline.Status == model.StatusBlocked {
				continue
			}
			workflows, err := _store.WorkflowGetTree(pipeline)
			if err != nil {
				log.Error().Err(err).Str("repo", repo.FullName).Msgf("failed to load workflows of pipeline #%d to hold its concurrency groups", pipeline.Number)
				continue
			}

			units := map[string][]*model.Workflow{}
			for _, workflow := range workflows {
				if workflow.ConcurrencyGroup != "" {
					units[workflow.ConcurrencyGroup] = append(units[workflow.ConcurrencyGroup], workflow)
				}
			}
			for group, unit := range units {
				if stateOfUnit(unit) != unitInProgress {
					continue
				}
				log.Info().Str("repo", repo.FullName).Msgf("pipeline #%d holds concurrency group %q since before the restart", pipeline.Number, group)
				server.Config.Services.Scheduler.HoldGroup(ctx, concurrencyGroupKey(repo.ID, group), pipeline.ID, unfinishedIDs(unit))
			}
		}
	}
}
