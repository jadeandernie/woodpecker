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
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/yaml"
	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/yaml/constraint"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// fakeForge keeps a train branch as a list of cars on top of the target.
type fakeForge struct {
	target      string
	cars        []*model.TrainCar
	targetMoved bool
	nextSync    int

	frozen        bool
	freezeCalls   []bool
	notices       []*forge.TrainNotice
	reverts       []string // revert titles in order
	conflicts     map[string]bool
	syncConflict  bool
	moveOnPromote bool // the target moves on its own when a fast-forward is tried
	fastForwards  []string
}

func newFakeForge() *fakeForge {
	return &fakeForge{target: "base", conflicts: map[string]bool{}}
}

// push adds a car merging pull request pr with the given files.
func (f *fakeForge) push(sha, title string, pr int64, files ...string) {
	f.cars = append(f.cars, &model.TrainCar{SHA: sha, Title: title, PR: pr, Files: files})
}

func (f *fakeForge) head() string {
	if len(f.cars) == 0 {
		return f.target
	}
	return f.cars[len(f.cars)-1].SHA
}

func (f *fakeForge) Cars(_ context.Context, _ *model.User, _ *model.Repo, _, _ string) (*forge.TrainCars, error) {
	cars := make([]*model.TrainCar, len(f.cars))
	for i, c := range f.cars {
		cp := *c
		cars[i] = &cp
	}
	return &forge.TrainCars{Head: f.head(), Target: f.target, Cars: cars, TargetMoved: f.targetMoved}, nil
}

func (f *fakeForge) FastForward(_ context.Context, _ *model.User, _ *model.Repo, _, sha string, _ int64) error {
	if f.moveOnPromote {
		f.moveOnPromote = false
		f.targetMoved = true
		return forge.ErrNotFastForward
	}
	if f.targetMoved {
		return forge.ErrNotFastForward
	}
	if f.target == sha {
		return nil
	}
	for i, c := range f.cars {
		if c.SHA == sha {
			f.cars = f.cars[i+1:]
			f.target = sha
			f.fastForwards = append(f.fastForwards, sha)
			return nil
		}
	}
	return fmt.Errorf("%s is not on the train branch", sha)
}

func (f *fakeForge) Revert(_ context.Context, _ *model.User, _ *model.Repo, _ string, car *model.TrainCar, title string) (string, error) {
	if f.conflicts[car.SHA] {
		return "", forge.ErrRevertConflict
	}
	sha := "revert-" + car.SHA
	for _, c := range f.cars {
		if c.SHA == sha {
			return sha, nil
		}
	}
	f.cars = append(f.cars, &model.TrainCar{SHA: sha, Title: title, Files: car.Files})
	f.reverts = append(f.reverts, title)
	return sha, nil
}

func (f *fakeForge) Freeze(_ context.Context, _ *model.User, _ *model.Repo, _ string, frozen bool) error {
	f.frozen = frozen
	f.freezeCalls = append(f.freezeCalls, frozen)
	return nil
}

func (f *fakeForge) Notify(_ context.Context, _ *model.User, _ *model.Repo, n *forge.TrainNotice) error {
	f.notices = append(f.notices, n)
	return nil
}

func (f *fakeForge) Sync(_ context.Context, _ *model.User, _ *model.Repo, from, _, title string) (string, error) {
	if f.syncConflict {
		return "", forge.ErrSyncConflict
	}
	if !f.targetMoved {
		return "", nil
	}
	f.nextSync++
	sha := fmt.Sprintf("sync-%d", f.nextSync)
	f.cars = append(f.cars, &model.TrainCar{SHA: sha, Title: title, Files: []string{from + "-only.txt"}})
	f.targetMoved = false
	return sha, nil
}

// fakePipeline is a pipeline the fake runner started.
type fakePipeline struct {
	req    *PromoteRequest
	result *PipelineResult
}

// fakeEnv runs pipelines against a model of which cars break which workflow.
type fakeEnv struct {
	t     *testing.T
	repo  *model.Repo
	forge *fakeForge

	state []byte // the persisted state, as JSON

	whens      map[string]constraint.When
	order      []string
	breaks     map[string][]string // workflow -> SHAs of cars that break it
	flaky      map[string]int      // workflow -> train promotes left to fail
	alwaysRed  map[string]bool
	baseline   []string
	filterAll  bool
	pipelines  []*fakePipeline
	maxRunning int
}

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	cfg := model.DefaultTrainSettings()
	cfg.Enabled = true
	return &fakeEnv{
		t:         t,
		repo:      &model.Repo{ID: 1, FullName: "jade/app", Train: &cfg},
		forge:     newFakeForge(),
		whens:     map[string]constraint.When{},
		breaks:    map[string][]string{},
		flaky:     map[string]int{},
		alwaysRed: map[string]bool{},
	}
}

// workflow adds a workflow with the given when clause in yaml.
func (e *fakeEnv) workflow(name, when string) {
	var w constraint.When
	if when != "" {
		wf, err := yaml.ParseString("when:\n" + when)
		require.NoError(e.t, err)
		w = wf.When
	}
	e.whens[name] = w
	e.order = append(e.order, name)
}

func (e *fakeEnv) Repo(int64) (*model.Repo, error) { return e.repo, nil }

func (e *fakeEnv) State(int64) (*model.TrainState, error) {
	if e.state == nil {
		return nil, nil
	}
	st := new(model.TrainState)
	return st, json.Unmarshal(e.state, st)
}

func (e *fakeEnv) SaveState(st *model.TrainState) error {
	// persist only what the store persists: the json view drops nothing the
	// train needs, which these round trips check.
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	var back model.TrainState
	if err := json.Unmarshal(b, &back); err != nil {
		return err
	}
	back.RepoID = st.RepoID
	e.state, err = json.Marshal(&back)
	return err
}

func (e *fakeEnv) Forge(context.Context, *model.Repo) (forge.Train, *model.User, error) {
	return e.forge, &model.User{Login: "jade"}, nil
}

func (e *fakeEnv) Start(_ context.Context, _ *model.Repo, req *PromoteRequest) (int64, error) {
	if e.filterAll {
		return 0, ErrFiltered
	}
	for _, w := range req.Workflows {
		if _, ok := e.whens[w]; !ok {
			return 0, ErrFiltered
		}
	}
	n := int64(len(e.pipelines) + 1)
	e.pipelines = append(e.pipelines, &fakePipeline{req: req, result: &PipelineResult{Number: n, Status: model.StatusRunning}})
	if running := e.running(); running > e.maxRunning && req.Kind == model.PromoteKindBisect {
		e.maxRunning = running
	}
	return n, nil
}

func (e *fakeEnv) running() int {
	n := 0
	for _, p := range e.pipelines {
		if p.req.Kind == model.PromoteKindBisect && !p.result.Done() {
			n++
		}
	}
	return n
}

func (e *fakeEnv) Result(_ *model.Repo, number int64) (*PipelineResult, error) {
	return e.pipelines[number-1].result, nil
}

func (e *fakeEnv) Workflows(*model.Repo, int64) (map[string]constraint.When, error) {
	return e.whens, nil
}

func (e *fakeEnv) Baseline(*model.Repo, string, int64) ([]string, error) {
	return e.baseline, nil
}

func (e *fakeEnv) Link(_ *model.Repo, n int64) string {
	return fmt.Sprintf("https://ci.example/repos/1/pipeline/%d", n)
}

// finish ends every running pipeline according to the model.
func (e *fakeEnv) finish() int {
	done := 0
	for _, p := range e.pipelines {
		if p.result.Done() {
			continue
		}
		done++
		p.result.Workflows = map[string]model.StatusValue{}
		status := model.StatusSuccess
		for _, w := range e.order {
			if len(p.req.Workflows) > 0 && !slices.Contains(p.req.Workflows, w) {
				continue
			}
			state := model.StatusSuccess
			if e.fails(p.req, w) {
				state = model.StatusFailure
				status = model.StatusFailure
			}
			p.result.Workflows[w] = state
		}
		p.result.Status = status
	}
	return done
}

func (e *fakeEnv) fails(req *PromoteRequest, w string) bool {
	if e.alwaysRed[w] {
		return true
	}
	if req.Kind == model.PromoteKindTrain && e.flaky[w] > 0 {
		e.flaky[w]--
		return true
	}
	for _, sha := range e.breaks[w] {
		if slices.Contains(req.Cars, sha) && !slices.Contains(req.Cars, "revert-"+sha) {
			return true
		}
	}
	return false
}

// settle reconciles and finishes pipelines until the train stops moving.
func (e *fakeEnv) settle(c *Controller) *model.TrainState {
	e.t.Helper()
	for range 100 {
		require.NoError(e.t, c.Reconcile(context.Background(), e.repo.ID))
		if e.finish() == 0 {
			break
		}
	}
	st, err := e.State(e.repo.ID)
	require.NoError(e.t, err)
	return st
}

func (e *fakeEnv) controller() *Controller {
	return New(e)
}

// train lists the started pipelines of the given kind.
func (e *fakeEnv) started(kind model.PromoteKind) []*PromoteRequest {
	var out []*PromoteRequest
	for _, p := range e.pipelines {
		if p.req.Kind == kind {
			out = append(out, p.req)
		}
	}
	return out
}

func (e *fakeEnv) comments() []string {
	var out []string
	for _, n := range e.forge.notices {
		if n.Comment != "" {
			out = append(out, fmt.Sprintf("#%d %s", n.PR, n.Comment))
		}
	}
	return out
}

func carSHAs(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

func hasPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
