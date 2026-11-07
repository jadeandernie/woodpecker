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
	"slices"

	"github.com/rs/zerolog/log"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/builder"
	"go.woodpecker-ci.org/woodpecker/v3/server"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
)

// start a pipeline, make sure it was stored persistent in the store before.
// If the repo squashes pending pipelines, superseded are the previous pipelines
// squashed into this one (see squashPreviousPipelines); this pipeline is queued
// in the place of the oldest of them, and they are canceled once it is queued.
// No other previous pipeline is canceled.
func start(ctx context.Context, forge forge.Forge, store store.Store, activePipeline *model.Pipeline, user *model.User, repo *model.Repo, pipelineItems []*builder.Item, superseded []*model.Pipeline) (*model.Pipeline, error) {
	// call to cancel previous pipelines if needed
	if !repo.CancelPreviousPipelineSquash {
		if err := cancelPreviousPipelines(ctx, forge, store, activePipeline, repo, user); err != nil {
			// should be not breaking
			log.Error().Err(err).Msg("failed to cancel previous pipelines")
		}
	}

	tasks, err := pipelineTasks(repo, activePipeline, pipelineItems)
	if err != nil {
		return nil, err
	}
	// the squashed changes keep the place they had in the queue
	if len(superseded) > 0 {
		info := server.Config.Services.Scheduler.Info(ctx)
		takeOverQueuePlace(tasks, superseded, slices.Concat(info.Pending, info.WaitingOnDeps, info.Running))
	}

	// announce the new pipeline to UI subscribers and enqueue its tasks in one go
	if err := server.Config.Services.Scheduler.StartPipeline(ctx, repo, activePipeline, tasks); err != nil {
		log.Error().Err(err).Msg("startPipeline")
		return nil, err
	}

	// only cancel the superseded pipelines now that their changes are queued
	// with this one, so a failure to queue it loses none of them
	cancelSupersededPipelines(ctx, forge, store, activePipeline, superseded, repo, user)

	updatePipelineStatus(ctx, forge, activePipeline, repo, user)

	return activePipeline, nil
}

func publishPipeline(ctx context.Context, forge forge.Forge, pipeline *model.Pipeline, repo *model.Repo, repoUser *model.User) {
	if err := server.Config.Services.Scheduler.PublishPipelineEvent(ctx, repo, pipeline); err != nil {
		log.Error().Err(err).Msg("could not push pipeline status change to pubsub provider")
	}
	updatePipelineStatus(ctx, forge, pipeline, repo, repoUser)
}
