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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

func TestOneCarGreen(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	e.forge.push("a", "Add a", 1, "a.go")

	st := e.settle(e.controller())

	assert.Equal(t, "a", e.forge.target)
	assert.Empty(t, e.forge.cars)
	assert.Equal(t, model.TrainIdle, st.Phase)
	assert.Equal(t, "a", st.LastPromoted)
	assert.Equal(t, int64(1), st.LastPromotePipeline)
	assert.False(t, st.Frozen)

	trains := e.started(model.PromoteKindTrain)
	require.Len(t, trains, 1)
	assert.Equal(t, "a", trains[0].SHA)
	assert.Equal(t, "base", trains[0].Base)
	assert.Equal(t, []string{"a"}, trains[0].Cars)
	assert.Equal(t, []string{"a.go"}, trains[0].Files)
	assert.Empty(t, trains[0].Workflows)

	// nothing new: reconciling again starts nothing
	e.settle(e.controller())
	assert.Len(t, e.pipelines, 1)
}

func TestDisabledTrainDoesNothing(t *testing.T) {
	e := newFakeEnv(t)
	e.repo.Train.Enabled = false
	e.workflow("test", "")
	e.forge.push("a", "Add a", 1, "a.go")
	e.settle(e.controller())
	assert.Empty(t, e.pipelines)
	assert.Nil(t, e.state)
}

func TestSeveralCarsOneCandidate(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("server", "  path: 'server/**'")
	e.workflow("web", "  path: 'web/**'")
	e.forge.push("a", "Web tweak", 1, "web/app.ts")
	e.forge.push("b", "Server change", 2, "server/main.go")
	e.forge.push("c", "Docs", 3, "docs/readme.md")
	e.breaks["server"] = []string{"b"}

	st := e.settle(e.controller())

	assert.Equal(t, []string{`Revert "Server change" (broke server in promote #1)`}, e.forge.reverts)
	assert.NotContains(t, e.forge.freezeCalls, true, "a single candidate is reverted without freezing")
	assert.Empty(t, e.started(model.PromoteKindBisect))
	require.Len(t, e.started(model.PromoteKindTrain), 2)
	assert.Equal(t, "revert-b", e.forge.target)
	assert.Equal(t, model.TrainIdle, st.Phase)
	require.Len(t, st.Reverts, 1)
	assert.Equal(t, "b", st.Reverts[0].Car)
	assert.Equal(t, int64(2), st.Reverts[0].PR)
	assert.Equal(t, []string{"server"}, st.Reverts[0].Workflows)

	comments := e.comments()
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0], "#2 The merge train took this change off dev: it broke server in promote #1")
	var status bool
	for _, n := range e.forge.notices {
		if n.SHA == "b" && n.State == model.StatusFailure {
			status = true
		}
	}
	assert.True(t, status, "the culprit gets a failed train status")
}

func TestBisect(t *testing.T) {
	for _, n := range []int{5, 8} {
		for culprit := range n {
			t.Run(fmt.Sprintf("%d cars culprit %d", n, culprit), func(t *testing.T) {
				e := newFakeEnv(t)
				e.workflow("test", "  path: 'src/**'")
				shas := carSHAs("c", n)
				for i, sha := range shas {
					e.forge.push(sha, "Car "+sha, int64(i+1), fmt.Sprintf("src/%d.go", i))
				}
				e.breaks["test"] = []string{shas[culprit]}

				st := e.settle(e.controller())

				assert.Equal(t, []string{fmt.Sprintf(`Revert "Car %s" (broke test in promote #1)`, shas[culprit])}, e.forge.reverts)
				assert.Equal(t, "revert-"+shas[culprit], e.forge.target)
				assert.Equal(t, model.TrainIdle, st.Phase)
				assert.False(t, st.Frozen)
				assert.Equal(t, []bool{true, false}, e.forge.freezeCalls, "frozen for the bisect, then thawed")
				assert.LessOrEqual(t, e.maxRunning, 2, "never more probes than max_bisect_parallel")

				probes := e.started(model.PromoteKindBisect)
				require.NotEmpty(t, probes)
				assert.Equal(t, shas[n-1], probes[0].SHA, "flake check runs at the head first")
				for _, p := range probes {
					assert.Equal(t, []string{"test"}, p.Workflows)
				}
				// a flake check plus a 3-ary search: at most 1 + 2*ceil(log3(n)) probes
				assert.LessOrEqual(t, len(probes), 1+2*2)
				assert.True(t, hasEvent(st, "revert"))
				// the promote after the revert starts a fresh investigation list
				assert.Empty(t, st.Investigations)
			})
		}
	}
}

func TestTwoCulpritsInTwoWorkflows(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("api", "  path: 'api/**'")
	e.workflow("ui", "  path: 'ui/**'")
	e.forge.push("c0", "Api one", 1, "api/a.go")
	e.forge.push("c1", "Ui one", 2, "ui/a.ts")
	e.forge.push("c2", "Api two", 3, "api/b.go")
	e.forge.push("c3", "Ui two", 4, "ui/b.ts")
	e.forge.push("c4", "Both", 5, "api/c.go", "ui/c.ts")
	e.breaks["api"] = []string{"c2"}
	e.breaks["ui"] = []string{"c1"}

	st := e.settle(e.controller())

	assert.Equal(t, []string{
		`Revert "Api two" (broke api in promote #1)`,
		`Revert "Ui one" (broke ui in promote #1)`,
	}, e.forge.reverts, "newest culprit first")
	assert.LessOrEqual(t, e.maxRunning, 2)
	assert.Equal(t, model.TrainIdle, st.Phase)
	assert.False(t, e.forge.frozen)
	assert.Empty(t, e.forge.cars, "the rest was promoted")
}

func TestTwoCulpritsInOneWorkflow(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	shas := carSHAs("c", 6)
	for i, sha := range shas {
		e.forge.push(sha, "Car "+sha, int64(i+1), "x.go")
	}
	e.breaks["test"] = []string{"c1", "c4"}

	st := e.settle(e.controller())

	// the bisect finds the oldest culprit; the promote after its revert is
	// red again, but every commit between c1 and its revert fails, so the
	// second culprit cannot be found from history: the train freezes
	assert.Equal(t, []string{`Revert "Car c1" (broke test in promote #1)`}, e.forge.reverts)
	assert.Equal(t, model.TrainFrozen, st.Phase)
	assert.Contains(t, st.FrozenReason, `cannot bisect test past the revert of "Car c1"`)
	assert.Contains(t, st.FrozenReason, `"Car c4"`)
	assert.True(t, e.forge.frozen)
	require.Len(t, st.Investigations, 1)
	assert.NotContains(t, st.Investigations[0].Candidates, "c1")
	assert.NotContains(t, st.Investigations[0].Candidates, "revert-c1")
}

func TestSecondCulpritAfterRevert(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	for i, sha := range carSHAs("c", 3) {
		e.forge.push(sha, "Car "+sha, int64(i+1), "x.go")
	}
	e.breaks["test"] = []string{"c1", "d2"}
	c := e.controller()
	ctx := context.Background()

	// run until c1 is reverted and the next promote starts
	for range 20 {
		require.NoError(t, c.Reconcile(ctx, 1))
		if len(e.forge.reverts) > 0 {
			break
		}
		e.finish()
	}
	require.Len(t, e.forge.reverts, 1)
	// new cars land behind the revert before the next promote ends
	for i, sha := range carSHAs("d", 4) {
		e.forge.push(sha, "Car "+sha, int64(10+i), "x.go")
	}
	st := e.settle(c)

	assert.Equal(t, []string{
		`Revert "Car c1" (broke test in promote #1)`,
		`Revert "Car d2" (broke test in promote #6)`,
	}, e.forge.reverts)
	assert.Equal(t, model.TrainIdle, st.Phase)
	assert.Empty(t, e.forge.cars)
}

func TestFlake(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	for i, sha := range carSHAs("c", 3) {
		e.forge.push(sha, "Car "+sha, int64(i+1), "x.go")
	}
	e.flaky["test"] = 1

	st := e.settle(e.controller())

	assert.Empty(t, e.forge.reverts)
	assert.Equal(t, "c2", e.forge.target, "the tested head is promoted after a green re-run")
	assert.Len(t, e.started(model.PromoteKindBisect), 1, "only the flake check ran")
	assert.Len(t, e.started(model.PromoteKindTrain), 1)
	assert.Equal(t, model.TrainIdle, st.Phase)
	assert.False(t, st.Frozen)
	assert.False(t, e.forge.frozen)
	require.Len(t, st.Investigations, 1)
	assert.Equal(t, model.TrainOutcomeFlake, st.Investigations[0].Outcome)
}

func TestFlakeWithoutCandidates(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("infra", "  path: 'infra/**'")
	e.forge.push("a", "App", 1, "app.go")
	e.flaky["infra"] = 1

	st := e.settle(e.controller())

	assert.Equal(t, "a", e.forge.target)
	assert.Equal(t, model.TrainIdle, st.Phase)
	assert.NotContains(t, e.forge.freezeCalls, true, "a recheck does not freeze")
}

func TestBaseFailureIgnored(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	e.workflow("lint", "")
	e.forge.push("a", "App", 1, "app.go")
	e.alwaysRed["lint"] = true
	e.baseline = []string{"lint"}

	st := e.settle(e.controller())

	assert.Equal(t, "a", e.forge.target, "a workflow already red on the target does not hold the train")
	assert.Empty(t, e.started(model.PromoteKindBisect))
	assert.Equal(t, model.TrainIdle, st.Phase)
}

func TestBaseFailureNotIgnored(t *testing.T) {
	e := newFakeEnv(t)
	e.repo.Train.IgnoreBaseFailures = false
	e.workflow("lint", "  path: 'lint/**'")
	e.forge.push("a", "App", 1, "app.go")
	e.alwaysRed["lint"] = true
	e.baseline = []string{"lint"}

	st := e.settle(e.controller())

	assert.Equal(t, "base", e.forge.target)
	assert.Equal(t, model.TrainFrozen, st.Phase)
	assert.True(t, st.Frozen)
	assert.True(t, e.forge.frozen)
	assert.Contains(t, st.FrozenReason, "base broken outside the cars")
	require.Len(t, st.Investigations, 1)
	assert.Equal(t, model.TrainOutcomeBaseBroken, st.Investigations[0].Outcome)

	// frozen stays frozen
	n := len(e.pipelines)
	e.forge.push("b", "More", 2, "b.go")
	e.settle(e.controller())
	assert.Len(t, e.pipelines, n)

	// resume promotes again
	e.alwaysRed["lint"] = false
	require.NoError(t, e.controller().Resume(context.Background(), 1, "jade"))
	st = e.settle(e.controller())
	assert.Equal(t, "b", e.forge.target)
	assert.Equal(t, model.TrainIdle, st.Phase)
	assert.False(t, e.forge.frozen)
}

func TestRevertConflict(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("server", "  path: 'server/**'")
	e.forge.push("a", "Docs", 1, "docs.md")
	e.forge.push("b", "Server", 2, "server/x.go")
	e.breaks["server"] = []string{"b"}
	e.forge.conflicts["b"] = true

	st := e.settle(e.controller())

	assert.Equal(t, model.TrainFrozen, st.Phase)
	assert.True(t, st.Frozen)
	assert.True(t, e.forge.frozen)
	assert.Contains(t, st.FrozenReason, "revert conflict")
	assert.Empty(t, e.forge.reverts)
	assert.Equal(t, "base", e.forge.target)
	comments := e.comments()
	require.Len(t, comments, 1)
	assert.Contains(t, comments[0], "#2 ")
	assert.Contains(t, comments[0], "does not apply cleanly")
}

func TestMainMovedSyncs(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	e.forge.push("a", "App", 1, "app.go")
	e.forge.targetMoved = true

	st := e.settle(e.controller())

	trains := e.started(model.PromoteKindTrain)
	require.Len(t, trains, 1)
	assert.Equal(t, []string{"a", "sync-1"}, trains[0].Cars)
	assert.Equal(t, "sync-1", e.forge.target)
	assert.Equal(t, model.TrainIdle, st.Phase)
	assert.True(t, hasEvent(st, "sync"))
}

func TestMainMovedDuringPromote(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	e.forge.push("a", "App", 1, "app.go")
	e.forge.moveOnPromote = true

	st := e.settle(e.controller())

	trains := e.started(model.PromoteKindTrain)
	require.Len(t, trains, 2, "promoted again after syncing")
	assert.Equal(t, []string{"a", "sync-1"}, trains[1].Cars)
	assert.Equal(t, "sync-1", e.forge.target)
	assert.Equal(t, model.TrainIdle, st.Phase)
}

func TestSyncConflictFreezes(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	e.forge.push("a", "App", 1, "app.go")
	e.forge.targetMoved = true
	e.forge.syncConflict = true

	st := e.settle(e.controller())

	assert.Empty(t, e.pipelines)
	assert.Equal(t, model.TrainFrozen, st.Phase)
	assert.Contains(t, st.FrozenReason, "sync conflict")
}

func TestRestartMidBisect(t *testing.T) {
	run := func(restart bool) (*fakeEnv, *model.TrainState) {
		e := newFakeEnv(t)
		e.workflow("test", "")
		shas := carSHAs("c", 8)
		for i, sha := range shas {
			e.forge.push(sha, "Car "+sha, int64(i+1), "x.go")
		}
		e.breaks["test"] = []string{"c5"}

		c := e.controller()
		ctx := context.Background()
		for i := 0; i < 4; i++ { // promote, flake check, first bisect round
			require.NoError(t, c.Reconcile(ctx, 1))
			e.finish()
		}
		st, err := e.State(1)
		require.NoError(t, err)
		require.Equal(t, model.TrainInvestigating, st.Phase, "still bisecting")
		if restart {
			c = e.controller() // a new server: only the persisted state survives
		}
		return e, e.settle(c)
	}

	plain, plainState := run(false)
	restarted, restartedState := run(true)

	assert.Equal(t, plain.forge.reverts, restarted.forge.reverts)
	assert.Equal(t, []string{`Revert "Car c5" (broke test in promote #1)`}, restarted.forge.reverts)
	assert.Len(t, restarted.pipelines, len(plain.pipelines), "no probe ran twice")
	assert.Equal(t, plainState.Phase, restartedState.Phase)
	assert.Equal(t, model.TrainIdle, restartedState.Phase)
}

func TestHeldWhenNothingMatches(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	e.forge.push("a", "App", 1, "app.go")
	e.filterAll = true

	st := e.settle(e.controller())
	assert.Equal(t, model.TrainHeld, st.Phase)
	assert.Equal(t, "a", st.HeldHead)

	// the same head stays held; a new car promotes again
	e.filterAll = false
	st = e.settle(e.controller())
	assert.Equal(t, model.TrainHeld, st.Phase)
	e.forge.push("b", "More", 2, "b.go")
	st = e.settle(e.controller())
	assert.Equal(t, model.TrainIdle, st.Phase)
	assert.Equal(t, "b", e.forge.target)
}

func TestRetry(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	e.forge.push("a", "App", 1, "app.go")
	e.filterAll = true
	e.settle(e.controller())

	e.filterAll = false
	c := e.controller()
	require.NoError(t, c.Retry(context.Background(), 1, "jade"))
	st := e.settle(c)
	assert.Equal(t, "a", e.forge.target)
	assert.Equal(t, model.TrainIdle, st.Phase)

	// a running promote refuses a retry
	e.forge.push("b", "More", 2, "b.go")
	require.NoError(t, c.Reconcile(context.Background(), 1))
	assert.ErrorIs(t, c.Retry(context.Background(), 1, "jade"), ErrBusy)
}

func TestSplitPoints(t *testing.T) {
	assert.Equal(t, []int{0, 2}, splitPoints(-1, 4, 2))
	assert.Equal(t, []int{0}, splitPoints(-1, 1, 2))
	assert.Equal(t, []int{1}, splitPoints(-1, 4, 1))
	assert.Equal(t, []int{1, 4}, splitPoints(-1, 7, 2))
	assert.Empty(t, splitPoints(2, 3, 2))
	assert.Equal(t, []int{3, 4, 5}, splitPoints(2, 6, 5))
}

func TestCarMatches(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("all", "")
	e.workflow("server", "  path: 'server/**'")
	e.workflow("push-only", "  - event: push\n    path: 'server/**'")
	e.workflow("promote", "  - event: [push, promote]\n    path:\n      include: ['web/**']\n      exclude: ['web/docs/**']")
	e.workflow("other-branch", "  - branch: main\n    path: 'server/**'")

	server := &model.TrainCar{Title: "s", Files: []string{"server/a.go"}}
	web := &model.TrainCar{Title: "w", Files: []string{"web/a.ts"}}
	webDocs := &model.TrainCar{Title: "d", Files: []string{"web/docs/a.md"}}

	assert.True(t, carMatches(e.whens["all"], "dev", server))
	assert.True(t, carMatches(e.whens["server"], "dev", server))
	assert.False(t, carMatches(e.whens["server"], "dev", web))
	assert.True(t, carMatches(e.whens["push-only"], "dev", web), "no constraint applies to promote: every car")
	assert.True(t, carMatches(e.whens["promote"], "dev", web))
	assert.False(t, carMatches(e.whens["promote"], "dev", webDocs))
	assert.False(t, carMatches(e.whens["promote"], "dev", server))
	assert.True(t, carMatches(e.whens["other-branch"], "dev", web), "no constraint applies to the train branch: every car")
}

func hasEvent(st *model.TrainState, kind string) bool {
	for _, ev := range st.History {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func TestKickRunsReconcile(t *testing.T) {
	e := newFakeEnv(t)
	e.workflow("test", "")
	e.forge.push("a", "App", 1, "app.go")
	c := e.controller()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.Kick(1) // not started: nothing happens
	c.ctx = ctx
	c.Kick(1)
	c.Kick(1)
	assert.Eventually(t, func() bool {
		defer c.lock(1)()
		return len(e.pipelines) == 1
	}, 5*time.Second, 10*time.Millisecond)
}
