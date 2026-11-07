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
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/pipeline"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
)

// Interval is how often every enabled train is reconciled without a kick.
const Interval = 5 * time.Minute

var defaultController atomic.Pointer[Controller]

// Default returns the running controller, or nil before Start.
func Default() *Controller {
	return defaultController.Load()
}

// Kick asks the running controller to reconcile the repo's train soon. It
// never blocks and does nothing before Start.
func Kick(repoID int64) {
	if c := Default(); c != nil {
		c.Kick(repoID)
	}
}

// Start runs the server's train controller until ctx ends: it reconciles every
// enabled train now and every Interval, on each kick, and whenever a promote
// pipeline ends.
func Start(ctx context.Context, s store.Store) error {
	c := New(NewEnv(s))
	c.ctx = ctx
	defaultController.Store(c)
	defer defaultController.CompareAndSwap(c, nil)

	pipeline.OnPromoteDone(func(p *model.Pipeline) { Kick(p.RepoID) })

	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		c.kickAll(s)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (c *Controller) kickAll(s store.Store) {
	repos, err := s.RepoListAll(true, &model.ListOptionsWithAll{All: true})
	if err != nil {
		log.Error().Err(err).Msg("merge train: list repos")
		return
	}
	for _, repo := range repos {
		if repo.TrainConfig().Enabled {
			c.Kick(repo.ID)
		}
	}
}

// Kick queues a reconcile of the repo on its worker; several kicks while one
// runs fold into one more.
func (c *Controller) Kick(repoID int64) {
	c.mu.Lock()
	if c.ctx == nil {
		c.mu.Unlock()
		return
	}
	ch, ok := c.workers[repoID]
	if !ok {
		ch = make(chan struct{}, 1)
		c.workers[repoID] = ch
		go c.work(c.ctx, repoID, ch)
	}
	c.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *Controller) work(ctx context.Context, repoID int64, ch chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
		}
		if err := c.Reconcile(ctx, repoID); err != nil {
			log.Error().Err(err).Int64("repo_id", repoID).Msg("merge train reconcile failed")
		}
	}
}
