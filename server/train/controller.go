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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// maxSteps bounds the state transitions one reconcile makes, so a forge that
// keeps reporting the same thing cannot spin the controller.
const maxSteps = 12

// Controller reconciles the merge trains of all repos. Reconciles of one repo
// are serialized; different repos run in parallel.
type Controller struct {
	env Env
	now func() time.Time

	mu      sync.Mutex
	locks   map[int64]*sync.Mutex
	workers map[int64]chan struct{}
	ctx     context.Context
}

// New returns a controller over env.
func New(env Env) *Controller {
	return &Controller{
		env:     env,
		now:     time.Now,
		locks:   map[int64]*sync.Mutex{},
		workers: map[int64]chan struct{}{},
	}
}

func (c *Controller) lock(repoID int64) func() {
	c.mu.Lock()
	l, ok := c.locks[repoID]
	if !ok {
		l = &sync.Mutex{}
		c.locks[repoID] = l
	}
	c.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Reconcile brings the repo's train in line with the forge and its pipelines.
// It is idempotent: running it again without anything changing does nothing.
func (c *Controller) Reconcile(ctx context.Context, repoID int64) error {
	defer c.lock(repoID)()
	r, err := c.load(ctx, repoID)
	if err != nil || r == nil {
		return err
	}
	return r.drive()
}

// Resume unfreezes the train, drops what it was investigating and lets it
// promote the train branch again.
func (c *Controller) Resume(ctx context.Context, repoID int64, by string) error {
	defer c.lock(repoID)()
	r, err := c.load(ctx, repoID)
	if err != nil {
		return err
	}
	if r == nil {
		return ErrDisabled
	}
	st := r.st
	st.Phase = model.TrainIdle
	st.Investigations = nil
	st.Regressed = nil
	st.HeldHead = ""
	r.log("resume", "resumed by "+by, 0, "")
	if err := r.unfreeze(); err != nil {
		_ = r.save()
		return err
	}
	if err := r.save(); err != nil {
		return err
	}
	return r.drive()
}

// Retry starts a fresh promote of the train branch head, for example after a
// held or canceled promote.
func (c *Controller) Retry(ctx context.Context, repoID int64, by string) error {
	defer c.lock(repoID)()
	r, err := c.load(ctx, repoID)
	if err != nil {
		return err
	}
	if r == nil {
		return ErrDisabled
	}
	switch {
	case r.st.Phase == model.TrainFrozen:
		return ErrFrozen
	case r.st.Phase == model.TrainPromoting, r.st.Phase == model.TrainInvestigating:
		return ErrBusy
	}
	r.st.Phase = model.TrainIdle
	r.st.HeldHead = ""
	r.log("retry", "retry asked by "+by, 0, "")
	if err := r.save(); err != nil {
		return err
	}
	return r.drive()
}

// load returns a run for the repo, or nil if its train is disabled.
func (c *Controller) load(ctx context.Context, repoID int64) (*run, error) {
	repo, err := c.env.Repo(repoID)
	if err != nil {
		return nil, err
	}
	cfg := repo.TrainConfig()
	if !cfg.Enabled {
		return nil, nil
	}
	st, err := c.env.State(repoID)
	if err != nil {
		return nil, err
	}
	if st == nil {
		st = &model.TrainState{RepoID: repoID, Phase: model.TrainIdle}
	}
	if st.Phase == "" {
		st.Phase = model.TrainIdle
	}
	f, u, err := c.env.Forge(ctx, repo)
	if err != nil {
		return nil, err
	}
	return &run{c: c, ctx: ctx, repo: repo, cfg: cfg, st: st, f: f, u: u}, nil
}

// run is one reconcile of one repo.
type run struct {
	c    *Controller
	ctx  context.Context
	repo *model.Repo
	cfg  model.TrainSettings
	st   *model.TrainState
	f    forge.Train
	u    *model.User
}

func (r *run) drive() error {
	for range maxSteps {
		again, err := r.step()
		if serr := r.save(); serr != nil && err == nil {
			err = serr
		}
		if err != nil || !again {
			return err
		}
	}
	return nil
}

func (r *run) step() (bool, error) {
	switch r.st.Phase {
	case model.TrainPromoting:
		return r.promoting()
	case model.TrainInvestigating:
		return r.investigating()
	case model.TrainFrozen:
		if r.cfg.FreezeProtection {
			return false, r.f.Freeze(r.ctx, r.u, r.repo, r.cfg.Branch, true)
		}
		return false, nil
	default:
		return r.idle()
	}
}

func (r *run) save() error {
	r.st.Updated = r.c.now().Unix()
	return r.c.env.SaveState(r.st)
}

func (r *run) log(kind, msg string, pipeline int64, sha string) {
	r.st.Log(r.c.now().Unix(), kind, msg, pipeline, sha)
	log.Info().Str("repo", r.repo.FullName).Str("train", kind).Int64("pipeline", pipeline).Msg(msg)
}

// idle looks for cars and starts a promote of the train branch head.
func (r *run) idle() (bool, error) {
	tc, err := r.f.Cars(r.ctx, r.u, r.repo, r.cfg.Branch, r.cfg.Target)
	if err != nil {
		return false, fmt.Errorf("read the cars of %s: %w", r.cfg.Branch, err)
	}
	if tc.TargetMoved {
		title := fmt.Sprintf("Sync %s into %s", r.cfg.Target, r.cfg.Branch)
		sha, err := r.f.Sync(r.ctx, r.u, r.repo, r.cfg.Target, r.cfg.Branch, title)
		switch {
		case errors.Is(err, forge.ErrSyncConflict):
			return r.freeze(fmt.Sprintf("sync conflict: %s does not merge cleanly into %s", r.cfg.Target, r.cfg.Branch))
		case err != nil:
			return false, fmt.Errorf("sync %s into %s: %w", r.cfg.Target, r.cfg.Branch, err)
		case sha != "":
			r.log("sync", fmt.Sprintf("%s moved on its own: merged it into %s", r.cfg.Target, r.cfg.Branch), 0, sha)
			return true, nil
		}
	}
	if len(tc.Cars) == 0 {
		r.st.Phase = model.TrainIdle
		r.st.Cars = nil
		r.st.HeldHead = ""
		return false, nil
	}
	if r.st.Phase == model.TrainHeld && tc.Head == r.st.HeldHead {
		return false, nil
	}
	return r.startTrain(tc)
}

func (r *run) startTrain(tc *forge.TrainCars) (bool, error) {
	shas := make([]string, len(tc.Cars))
	for i, car := range tc.Cars {
		shas[i] = car.SHA
	}
	n, err := r.c.env.Start(r.ctx, r.repo, &PromoteRequest{
		Branch:  r.cfg.Branch,
		SHA:     tc.Head,
		Message: fmt.Sprintf("Promote %d %s from %s to %s", len(tc.Cars), plural(len(tc.Cars), "car"), r.cfg.Branch, r.cfg.Target),
		Base:    tc.Target,
		Cars:    shas,
		Kind:    model.PromoteKindTrain,
		Files:   carFiles(tc.Cars),
	})
	r.st.Cars = tc.Cars
	r.st.Head = tc.Head
	r.st.Base = tc.Target
	r.st.Regressed = nil
	r.st.Investigations = nil
	if errors.Is(err, ErrFiltered) {
		r.st.Phase = model.TrainHeld
		r.st.HeldHead = tc.Head
		r.st.Pipeline = 0
		r.log("held", "no promote workflow matched", 0, tc.Head)
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("start a promote of %s: %w", tc.Head, err)
	}
	r.st.Phase = model.TrainPromoting
	r.st.HeldHead = ""
	r.st.Pipeline = n
	r.log("promote", fmt.Sprintf("promote #%d tests %d %s", n, len(tc.Cars), plural(len(tc.Cars), "car")), n, tc.Head)
	return false, nil
}

// promoting waits for the train promote and acts on its result.
func (r *run) promoting() (bool, error) {
	res, err := r.c.env.Result(r.repo, r.st.Pipeline)
	if err != nil {
		return false, err
	}
	if !res.Done() {
		return false, nil
	}
	n := r.st.Pipeline
	switch res.Status {
	case model.StatusSuccess:
		return r.promote()
	case model.StatusKilled, model.StatusCanceled, model.StatusDeclined, model.StatusSkipped:
		r.st.Phase = model.TrainHeld
		r.st.HeldHead = r.st.Head
		r.st.LastResult = fmt.Sprintf("promote #%d was %s", n, res.Status)
		r.log("held", r.st.LastResult, n, r.st.Head)
		return false, nil
	}

	failed := res.Failed()
	if len(failed) == 0 {
		msg := fmt.Sprintf("promote #%d ended %s", n, res.Status)
		if len(res.Errors) > 0 {
			msg += ": " + strings.Join(res.Errors, "; ")
		}
		return r.freeze(msg)
	}

	regressed := failed
	if r.cfg.IgnoreBaseFailures {
		base, err := r.c.env.Baseline(r.repo, r.cfg.Target, r.st.LastPromotePipeline)
		if err != nil {
			return false, fmt.Errorf("read the baseline of %s: %w", r.cfg.Target, err)
		}
		regressed = without(failed, base)
		if len(regressed) == 0 {
			r.log("baseline", fmt.Sprintf("promote #%d failed only %s, already red on %s", n, strings.Join(failed, ", "), r.cfg.Target), n, r.st.Head)
			return r.promote()
		}
	}
	r.st.Regressed = regressed
	r.st.LastResult = fmt.Sprintf("promote #%d regressed %s", n, strings.Join(regressed, ", "))
	r.log("red", r.st.LastResult, n, r.st.Head)
	return r.investigate(regressed)
}

// promote fast-forwards the target to the tested head.
func (r *run) promote() (bool, error) {
	n := r.st.Pipeline
	err := r.f.FastForward(r.ctx, r.u, r.repo, r.cfg.Target, r.st.Head, n)
	if errors.Is(err, forge.ErrNotFastForward) {
		tc, cerr := r.f.Cars(r.ctx, r.u, r.repo, r.cfg.Branch, r.cfg.Target)
		if cerr == nil && tc.TargetMoved {
			r.st.Phase = model.TrainIdle
			r.st.Investigations = nil
			r.st.Regressed = nil
			r.log("moved", fmt.Sprintf("%s moved while promote #%d ran; syncing and promoting again", r.cfg.Target, n), n, r.st.Head)
			return true, nil
		}
		return r.freeze(fmt.Sprintf("cannot fast-forward %s to %s from promote #%d: %v", r.cfg.Target, short(r.st.Head), n, err))
	}
	if err != nil {
		return false, fmt.Errorf("fast-forward %s to %s: %w", r.cfg.Target, r.st.Head, err)
	}
	r.st.LastPromoted = r.st.Head
	r.st.LastPromotePipeline = n
	r.st.LastResult = fmt.Sprintf("promote #%d moved %s to %s", n, r.cfg.Target, short(r.st.Head))
	r.st.Phase = model.TrainIdle
	r.log("promoted", r.st.LastResult, n, r.st.Head)
	r.notify(&forge.TrainNotice{
		SHA:         r.st.Head,
		State:       model.StatusSuccess,
		Description: fmt.Sprintf("promoted to %s in promote #%d", r.cfg.Target, n),
		Link:        r.c.env.Link(r.repo, n),
	})
	if r.st.Frozen {
		if err := r.unfreeze(); err != nil {
			return false, err
		}
	}
	return true, nil
}

// freeze stops the train until someone resumes it.
func (r *run) freeze(reason string) (bool, error) {
	r.st.Phase = model.TrainFrozen
	r.st.LastResult = reason
	r.log("frozen", reason, r.st.Pipeline, r.st.Head)
	return false, r.lockBranch(reason)
}

// lockBranch keeps new cars off the train branch.
func (r *run) lockBranch(reason string) error {
	r.st.Frozen = true
	r.st.FrozenReason = reason
	if !r.cfg.FreezeProtection {
		return nil
	}
	return r.f.Freeze(r.ctx, r.u, r.repo, r.cfg.Branch, true)
}

func (r *run) unfreeze() error {
	wasFrozen := r.st.Frozen
	r.st.Frozen = false
	r.st.FrozenReason = ""
	if !r.cfg.FreezeProtection {
		return nil
	}
	if err := r.f.Freeze(r.ctx, r.u, r.repo, r.cfg.Branch, false); err != nil {
		r.st.Frozen = wasFrozen
		return err
	}
	return nil
}

// notify tells the forge; a failed notice is logged, not retried.
func (r *run) notify(n *forge.TrainNotice) {
	if err := r.f.Notify(r.ctx, r.u, r.repo, n); err != nil {
		log.Warn().Err(err).Str("repo", r.repo.FullName).Msg("merge train notice failed")
	}
}

func carFiles(cars []*model.TrainCar) []string {
	seen := map[string]bool{}
	var out []string
	for _, car := range cars {
		for _, f := range car.Files {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

func without(list, drop []string) []string {
	var out []string
	for _, s := range list {
		if !contains(drop, s) {
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortStrings(s []string) { sort.Strings(s) }

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
