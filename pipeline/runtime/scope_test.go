// Copyright 2026 Woodpecker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build test

package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/backend/dummy"
	backend_types "go.woodpecker-ci.org/woodpecker/v3/pipeline/backend/types"
	pipeline_errors "go.woodpecker-ci.org/woodpecker/v3/pipeline/errors"
	"go.woodpecker-ci.org/woodpecker/v3/pipeline/state"
	tracer_mocks "go.woodpecker-ci.org/woodpecker/v3/pipeline/tracing/mocks"
)

func withScope(scope string, inherits ...string) func(*backend_types.Step) {
	return func(s *backend_types.Step) {
		s.FailureScope = scope
		s.FailureScopeInherits = inherits
	}
}

func withDeps(deps ...string) func(*backend_types.Step) {
	return func(s *backend_types.Step) { s.DependsOn = deps }
}

// foundationConfig builds the stages a foundation with members a and b
// compiles to: setup, then per member before -> test -> after, then teardown.
func foundationConfig(setup, aTest, bTest *backend_types.Step) (*backend_types.Config, map[string]*backend_types.Step) {
	steps := map[string]*backend_types.Step{
		"setup":    setup,
		"a/before": cmdStep("a/before", withScope("a"), withDeps("setup")),
		"b/before": cmdStep("b/before", withScope("b"), withDeps("setup")),
		"a/test":   aTest,
		"b/test":   bTest,
		"a/after":  cmdStep("a/after", withScope("a"), withAlwaysRun(), withDeps("a/test")),
		"b/after":  cmdStep("b/after", withScope("b"), withAlwaysRun(), withDeps("b/test")),
		"teardown": cmdStep("teardown", withAlwaysRun(), withDeps("a/after", "b/after")),
	}
	return &backend_types.Config{
		Stages: []*backend_types.Stage{
			{Steps: []*backend_types.Step{steps["setup"]}},
			{Steps: []*backend_types.Step{steps["a/before"], steps["b/before"]}},
			{Steps: []*backend_types.Step{steps["a/test"], steps["b/test"]}},
			{Steps: []*backend_types.Step{steps["a/after"], steps["b/after"]}},
			{Steps: []*backend_types.Step{steps["teardown"]}},
		},
	}, steps
}

func runScoped(t *testing.T, config *backend_types.Config) ([]state.State, *Runtime) {
	t.Helper()
	tracer := newTestTracer(t)
	r := New(config, dummy.New(), WithTracer(tracer), WithLogger(newTestLogger(t)))
	require.NoError(t, r.Run(t.Context()))
	return getTracerStates(tracer), r
}

func assertRan(t *testing.T, traces []state.State, name string, exitCode int) {
	t.Helper()
	last := findLastTraceByName(traces, name)
	require.NotNil(t, last, "%s should be traced", name)
	assert.False(t, last.CurrStepState.Skipped, "%s should not be skipped", name)
	assert.True(t, last.CurrStepState.Exited, "%s should have exited", name)
	assert.Equal(t, exitCode, last.CurrStepState.ExitCode, "%s exit code", name)
}

func assertSkipped(t *testing.T, traces []state.State, name string) {
	t.Helper()
	last := findLastTraceByName(traces, name)
	require.NotNil(t, last, "%s should be traced", name)
	assert.True(t, last.CurrStepState.Skipped, "%s should be skipped", name)
}

func TestFailureScopeMemberFailureDoesNotSkipOtherMembers(t *testing.T) {
	t.Parallel()
	config, steps := foundationConfig(
		cmdStep("setup"),
		cmdStep("a/test", withScope("a"), withDeps("a/before"), withExitCode(1)),
		cmdStep("b/test", withScope("b"), withDeps("b/before")),
	)
	traces, r := runScoped(t, config)

	assertRan(t, traces, "a/test", 1)
	assertRan(t, traces, "b/test", 0)
	assertRan(t, traces, "a/after", 0)
	assertRan(t, traces, "b/after", 0)
	assertRan(t, traces, "teardown", 0)

	assert.Equal(t, "failure", steps["a/after"].Environment["CI_PIPELINE_STATUS"])
	assert.Equal(t, "success", steps["b/after"].Environment["CI_PIPELINE_STATUS"])
	assert.Equal(t, "failure", steps["teardown"].Environment["CI_PIPELINE_STATUS"])

	var exitErr *pipeline_errors.ExitError
	require.True(t, errors.As(r.Err(), &exitErr), "the workflow fails if a member failed")
	assert.Equal(t, "a/test-uuid", exitErr.UUID)
}

func TestFailureScopeNormalSemanticsInsideMember(t *testing.T) {
	t.Parallel()
	steps := []*backend_types.Step{
		cmdStep("a/one", withScope("a"), withExitCode(1)),
		cmdStep("a/two", withScope("a"), withDeps("a/one")),
		cmdStep("a/notify", withScope("a"), withOnFailure(), withDeps("a/one")),
		cmdStep("b/one", withScope("b")),
		cmdStep("b/two", withScope("b"), withDeps("b/one")),
	}
	traces, r := runScoped(t, &backend_types.Config{Stages: []*backend_types.Stage{
		{Steps: []*backend_types.Step{steps[0], steps[3]}},
		{Steps: []*backend_types.Step{steps[1], steps[2], steps[4]}},
	}})

	assertRan(t, traces, "a/one", 1)
	assertSkipped(t, traces, "a/two")
	assertRan(t, traces, "a/notify", 0)
	assertRan(t, traces, "b/one", 0)
	assertRan(t, traces, "b/two", 0)
	assert.True(t, pipeline_errors.IsStepFailure(r.Err()))
}

func TestFailureScopeSetupFailureSkipsAllMembers(t *testing.T) {
	t.Parallel()
	config, steps := foundationConfig(
		cmdStep("setup", withExitCode(3)),
		cmdStep("a/test", withScope("a"), withDeps("a/before")),
		cmdStep("b/test", withScope("b"), withDeps("b/before")),
	)
	traces, r := runScoped(t, config)

	for _, name := range []string{"a/before", "b/before", "a/test", "b/test"} {
		assertSkipped(t, traces, name)
	}
	// after hooks and teardown run whatever happened before.
	assertRan(t, traces, "a/after", 0)
	assertRan(t, traces, "b/after", 0)
	assertRan(t, traces, "teardown", 0)
	assert.Equal(t, "failure", steps["a/after"].Environment["CI_PIPELINE_STATUS"])

	var exitErr *pipeline_errors.ExitError
	require.True(t, errors.As(r.Err(), &exitErr))
	assert.Equal(t, 3, exitErr.Code)
}

func TestFailureScopeInheritedScopeFailureSkips(t *testing.T) {
	t.Parallel()
	// b depends on a and inherits its scope, c is independent.
	steps := []*backend_types.Step{
		cmdStep("a/test", withScope("a"), withExitCode(1)),
		cmdStep("c/test", withScope("c")),
		cmdStep("b/test", withScope("b", "a"), withDeps("a/test")),
		cmdStep("b/after", withScope("b", "a"), withAlwaysRun(), withDeps("b/test")),
	}
	traces, _ := runScoped(t, &backend_types.Config{Stages: []*backend_types.Stage{
		{Steps: []*backend_types.Step{steps[0], steps[1]}},
		{Steps: []*backend_types.Step{steps[2]}},
		{Steps: []*backend_types.Step{steps[3]}},
	}})

	assertRan(t, traces, "c/test", 0)
	assertSkipped(t, traces, "b/test")
	assertRan(t, traces, "b/after", 0)
	assert.Equal(t, "failure", steps[3].Environment["CI_PIPELINE_STATUS"])
}

func TestFailureScopeIgnoredFailureDoesNotCount(t *testing.T) {
	t.Parallel()
	steps := []*backend_types.Step{
		cmdStep("a/flaky", withScope("a"), withExitCode(1), withIgnoreFailure()),
		cmdStep("a/next", withScope("a"), withDeps("a/flaky")),
	}
	traces, r := runScoped(t, &backend_types.Config{Stages: []*backend_types.Stage{
		{Steps: []*backend_types.Step{steps[0]}},
		{Steps: []*backend_types.Step{steps[1]}},
	}})
	assertRan(t, traces, "a/next", 0)
	assert.NoError(t, r.Err())
}

func TestFailureScopeLanesDoNotWaitForEachOther(t *testing.T) {
	t.Parallel()
	// Stage-wise, b/next would wait for a/slow. As a scoped segment it only
	// waits for b/fast.
	traces, _ := runScoped(t, &backend_types.Config{Stages: []*backend_types.Stage{
		{Steps: []*backend_types.Step{
			cmdStep("a/slow", withScope("a"), withSleep("300ms")),
			cmdStep("b/fast", withScope("b")),
		}},
		{Steps: []*backend_types.Step{
			cmdStep("a/next", withScope("a"), withDeps("a/slow")),
			cmdStep("b/next", withScope("b"), withDeps("b/fast")),
		}},
	}})

	bNextDone := indexOfTrace(traces, func(s state.State) bool {
		return s.CurrStep.Name == "b/next" && s.CurrStepState.Exited
	})
	aSlowDone := indexOfTrace(traces, func(s state.State) bool {
		return s.CurrStep.Name == "a/slow" && s.CurrStepState.Exited
	})
	aNextStarted := indexOfTrace(traces, func(s state.State) bool {
		return s.CurrStep.Name == "a/next" && !s.CurrStepState.Exited
	})
	require.NotEqual(t, -1, bNextDone)
	require.NotEqual(t, -1, aSlowDone)
	assert.Less(t, bNextDone, aSlowDone, "b/next must not wait for a/slow")
	assert.Greater(t, aNextStarted, aSlowDone, "a/next must wait for a/slow")
}

func TestFailureScopeCancelStopsSegment(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(t.Context())

	tracer := tracer_mocks.NewMockTracer(t)
	tracer.On("Trace", mock.Anything).Run(func(args mock.Arguments) {
		s, _ := args.Get(0).(*state.State)
		if s.CurrStep != nil && s.CurrStep.Name == "a/one" && s.CurrStepState.Exited {
			cancel(nil)
		}
	}).Return(nil).Maybe()

	r := New(&backend_types.Config{Stages: []*backend_types.Stage{
		{Steps: []*backend_types.Step{cmdStep("a/one", withScope("a"))}},
		{Steps: []*backend_types.Step{cmdStep("a/two", withScope("a"), withDeps("a/one"))}},
		{Steps: []*backend_types.Step{cmdStep("teardown", withAlwaysRun())}},
	}}, dummy.New(), WithTracer(tracer), WithContext(ctx), WithLogger(newTestLogger(t)))

	assert.ErrorIs(t, r.Run(t.Context()), pipeline_errors.ErrCancel)
	traces := getTracerStates(tracer)
	assert.Nil(t, findFirstTraceByName(traces, "a/two"), "a/two must not start after cancel")
	assert.Nil(t, findFirstTraceByName(traces, "teardown"), "later stages must not start after cancel")
}

func TestScopedSegment(t *testing.T) {
	scoped := &backend_types.Stage{Steps: []*backend_types.Step{cmdStep("a", withScope("x"))}}
	mixed := &backend_types.Stage{Steps: []*backend_types.Step{cmdStep("a", withScope("x")), cmdStep("b")}}
	plain := &backend_types.Stage{Steps: []*backend_types.Step{cmdStep("b")}}
	empty := &backend_types.Stage{}

	assert.Len(t, scopedSegment([]*backend_types.Stage{scoped, scoped, plain, scoped}), 2)
	assert.Empty(t, scopedSegment([]*backend_types.Stage{plain, scoped}))
	assert.Empty(t, scopedSegment([]*backend_types.Stage{mixed}))
	assert.Empty(t, scopedSegment([]*backend_types.Stage{empty}))
	assert.Empty(t, scopedSegment(nil))
}

func TestFailureScopesStatus(t *testing.T) {
	f := newFailureScopes()
	a := cmdStep("a", withScope("a"))
	b := cmdStep("b", withScope("b", "a"))
	c := cmdStep("c", withScope("c"))
	plain := cmdStep("plain")

	errA := errors.New("a failed")
	f.record(a, nil)
	assert.NoError(t, f.statusFor(a))
	f.record(a, errA)
	f.record(a, errors.New("second failure is not kept"))
	assert.Equal(t, errA, f.statusFor(a))
	assert.Equal(t, errA, f.statusFor(b), "b inherits a")
	assert.NoError(t, f.statusFor(c))

	errPlain := errors.New("setup failed")
	f.record(plain, errPlain)
	assert.Equal(t, errPlain, f.statusFor(c), "unscoped failures count for every scope")
	assert.Equal(t, errPlain, f.statusFor(a), "unscoped failures take precedence")
}
