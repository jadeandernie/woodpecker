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

package runtime

import (
	"context"
	"sync"

	backend_types "go.woodpecker-ci.org/woodpecker/v3/pipeline/backend/types"
)

// failureScopes records step failures per failure scope (see
// backend_types.Step.FailureScope), so that a failing step only affects the
// steps of its own scope.
type failureScopes struct {
	mu sync.Mutex
	// unscoped is the first failure of a step without a scope. It counts for
	// every step.
	unscoped error
	// scoped holds the first failure per scope.
	scoped map[string]error
}

func newFailureScopes() *failureScopes {
	return &failureScopes{scoped: make(map[string]error)}
}

// record stores the failure of a finished step.
func (f *failureScopes) record(step *backend_types.Step, err error) {
	if err == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if step.FailureScope == "" {
		if f.unscoped == nil {
			f.unscoped = err
		}
		return
	}
	if f.scoped[step.FailureScope] == nil {
		f.scoped[step.FailureScope] = err
	}
}

// statusFor returns the failure a scoped step sees: the first failure of an
// unscoped step, of its own scope, or of one of the scopes it inherits.
func (f *failureScopes) statusFor(step *backend_types.Step) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unscoped != nil {
		return f.unscoped
	}
	if err := f.scoped[step.FailureScope]; err != nil {
		return err
	}
	for _, scope := range step.FailureScopeInherits {
		if err := f.scoped[scope]; err != nil {
			return err
		}
	}
	return nil
}

// stepStatus returns the error that decides whether the step runs and what
// CI_PIPELINE_STATUS it gets. Steps without a failure scope see the
// workflow-wide error, scoped steps only the failures that concern them.
func (r *Runtime) stepStatus(step *backend_types.Step) error {
	if step.FailureScope == "" {
		return r.err.Get()
	}
	return r.scopes.statusFor(step)
}

// isScopedStage reports whether every step of the stage has a failure scope.
func isScopedStage(stage *backend_types.Stage) bool {
	if len(stage.Steps) == 0 {
		return false
	}
	for _, step := range stage.Steps {
		if step.FailureScope == "" {
			return false
		}
	}
	return true
}

// runScopedSegment runs the steps of consecutive scoped stages as a
// dependency graph instead of stage by stage: each step starts as soon as the
// steps it depends on within the segment finished, so a slow or failing step
// only holds up the steps that depend on it. Dependencies on steps outside the
// segment are already done when the segment starts. It returns a channel that
// emits the first step error once all steps finished.
func (r *Runtime) runScopedSegment(runnerCtx context.Context, stages []*backend_types.Stage) <-chan error {
	done := make(map[string]chan struct{})
	var steps []*backend_types.Step
	for _, stage := range stages {
		for _, step := range stage.Steps {
			done[step.Name] = make(chan struct{})
			steps = append(steps, step)
		}
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for _, step := range steps {
		wg.Go(func() {
			defer close(done[step.Name])
			for _, dep := range step.DependsOn {
				if ch, ok := done[dep]; ok {
					<-ch
				}
			}
			// A canceled workflow does not start further steps, as it would
			// not start further stages.
			if r.canceled() {
				return
			}
			if err := r.runStep(runnerCtx, step); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		})
	}

	result := make(chan error, 1)
	go func() {
		wg.Wait()
		result <- firstErr
		close(result)
	}()
	return result
}
