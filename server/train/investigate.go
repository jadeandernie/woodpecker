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
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/metadata"
	"go.woodpecker-ci.org/woodpecker/v3/pipeline/frontend/yaml/constraint"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

const (
	probeSuccess      = "success"
	probeFailure      = "failure"
	probeInconclusive = "inconclusive"
)

// investigate attributes each regressed workflow to the cars whose changes
// match its path filter and starts finding the culprits.
func (r *run) investigate(regressed []string) (bool, error) {
	whens, err := r.c.env.Workflows(r.repo, r.st.Pipeline)
	if err != nil {
		return false, fmt.Errorf("read the workflows of promote #%d: %w", r.st.Pipeline, err)
	}
	syncTitle := fmt.Sprintf("Sync %s into %s", r.cfg.Target, r.cfg.Branch)

	reverted := r.revertedCars()

	invs := make([]*model.TrainInvestigation, 0, len(regressed))
	bisect := false
	for _, w := range regressed {
		when := whens[w]
		var candidates, masked []string
		start, end := reverted.window(w)
		for i, car := range r.st.Cars {
			if car.Title == syncTitle || reverted.skip[car.SHA] || !carMatches(when, r.cfg.Branch, car) {
				continue
			}
			candidates = append(candidates, car.SHA)
			if i > start && i < end {
				masked = append(masked, car.Title)
			}
		}
		inv := &model.TrainInvestigation{Workflow: w, Candidates: candidates, Good: -1, Bad: len(candidates) - 1}
		switch {
		case len(candidates) == 0:
			inv.Step = model.TrainStepRecheck
		case len(candidates) == 1:
			inv.Step = model.TrainStepDone
			inv.Outcome = model.TrainOutcomeCulprit
			inv.Culprit = candidates[0]
			inv.Detail = "the only car that matches the workflow's path filter"
		case len(masked) > 0:
			// every commit between a culprit and its revert fails the
			// workflow, so the cars in between cannot be told apart
			inv.Step = model.TrainStepDone
			inv.Outcome = model.TrainOutcomeInconclusive
			inv.Detail = fmt.Sprintf("cannot bisect %s past the revert of %q; one of %s breaks it too", w, r.st.Cars[start].Title, quoteList(masked))
		default:
			inv.Step = model.TrainStepFlakeCheck
			bisect = true
		}
		invs = append(invs, inv)
	}
	r.st.Investigations = invs
	r.st.Phase = model.TrainInvestigating
	if bisect {
		if err := r.lockBranch(fmt.Sprintf("bisecting promote #%d", r.st.Pipeline)); err != nil {
			return false, err
		}
		r.log("bisect", fmt.Sprintf("froze %s to bisect promote #%d", r.cfg.Branch, r.st.Pipeline), r.st.Pipeline, r.st.Head)
	}
	return true, nil
}

// revertedSet knows the cars on the train branch the train already reverted.
type revertedSet struct {
	// skip holds the reverted cars and their reverts: neither is a candidate.
	skip map[string]bool
	// windows maps a workflow to the car index ranges, from a culprit to its
	// revert, in which the workflow is known broken.
	windows map[string][][2]int
}

func (r *run) revertedCars() *revertedSet {
	set := &revertedSet{skip: map[string]bool{}, windows: map[string][][2]int{}}
	index := map[string]int{}
	for i, car := range r.st.Cars {
		index[car.SHA] = i
	}
	for _, rv := range r.st.Reverts {
		start, ok1 := index[rv.Car]
		end, ok2 := index[rv.Commit]
		if !ok1 || !ok2 {
			continue
		}
		set.skip[rv.Car] = true
		set.skip[rv.Commit] = true
		for _, w := range rv.Workflows {
			set.windows[w] = append(set.windows[w], [2]int{start, end})
		}
	}
	return set
}

// window returns the car index range in which some culprit of the workflow
// is on the branch without its revert, or an empty range.
func (s *revertedSet) window(workflow string) (int, int) {
	ws := s.windows[workflow]
	if len(ws) == 0 {
		return -1, -1
	}
	start, end := ws[0][0], ws[0][1]
	for _, w := range ws[1:] {
		start, end = min(start, w[0]), max(end, w[1])
	}
	return start, end
}

func quoteList(titles []string) string {
	q := make([]string, len(titles))
	for i, t := range titles {
		q[i] = fmt.Sprintf("%q", t)
	}
	return strings.Join(q, ", ")
}

// carMatches reports whether the workflow's path filter matches the car's
// changes. Only the constraints that can apply to a promote of the train
// branch count; a workflow without any matches every car.
func carMatches(when constraint.When, branch string, car *model.TrainCar) bool {
	applicable := 0
	for i := range when.Constraints {
		c := &when.Constraints[i]
		if len(c.Event) > 0 && !slices.Contains(c.Event, string(metadata.EventPromote)) {
			continue
		}
		if !c.Branch.Match(branch) {
			continue
		}
		applicable++
		if c.Path.Match(car.Files, car.Title) {
			return true
		}
	}
	return applicable == 0
}

// investigating collects probe results, advances each investigation and
// starts probes within the parallel budget.
func (r *run) investigating() (bool, error) {
	running := 0
	for _, inv := range r.st.Investigations {
		for _, p := range inv.Probes {
			if p.Result != "" {
				continue
			}
			res, err := r.c.env.Result(r.repo, p.Pipeline)
			if err != nil {
				return false, err
			}
			if !res.Done() {
				running++
				continue
			}
			p.Result = judge(res, inv.Workflow)
		}
	}

	var needy []*model.TrainInvestigation
	for _, inv := range r.st.Investigations {
		if advance(inv) {
			needy = append(needy, inv)
		}
	}

	budget := max(r.cfg.MaxBisectParallel, 1) - running
	for i, inv := range needy {
		if budget <= 0 {
			break
		}
		share := max(budget/(len(needy)-i), 1)
		started, err := r.probe(inv, share)
		if err != nil {
			return false, err
		}
		budget -= started
	}

	for _, inv := range r.st.Investigations {
		if inv.Step != model.TrainStepDone {
			return false, nil
		}
	}
	return r.finish()
}

// judge turns a probe pipeline into a result for one workflow.
func judge(res *PipelineResult, workflow string) string {
	state, ok := res.Workflows[workflow]
	switch {
	case !ok:
		return probeInconclusive
	case state == model.StatusSuccess:
		return probeSuccess
	case isFailure(state):
		return probeFailure
	default:
		return probeInconclusive
	}
}

// advance moves an investigation on from its finished probes and reports
// whether it needs new probes.
func advance(inv *model.TrainInvestigation) bool {
	if inv.Step == model.TrainStepDone {
		return false
	}
	for _, p := range inv.Probes {
		if p.Result == "" {
			if inv.Step == model.TrainStepBisect {
				// a round is still out; judge it as a whole
				return false
			}
		}
	}

	switch inv.Step {
	case model.TrainStepRecheck, model.TrainStepFlakeCheck:
		p := headProbe(inv)
		switch {
		case p == nil:
			return true
		case p.Result == "":
			return false
		case p.Result == probeSuccess:
			inv.Step = model.TrainStepDone
			inv.Outcome = model.TrainOutcomeFlake
			inv.Detail = fmt.Sprintf("passed when re-run in pipeline #%d", p.Pipeline)
			return false
		case p.Result == probeInconclusive:
			inv.Step = model.TrainStepDone
			inv.Outcome = model.TrainOutcomeInconclusive
			inv.Detail = fmt.Sprintf("re-run in pipeline #%d did not run %s", p.Pipeline, inv.Workflow)
			return false
		case inv.Step == model.TrainStepRecheck:
			inv.Step = model.TrainStepDone
			inv.Outcome = model.TrainOutcomeBaseBroken
			inv.Detail = fmt.Sprintf("base broken outside the cars: %s failed again in pipeline #%d and no car touches its paths", inv.Workflow, p.Pipeline)
			return false
		default:
			inv.Step = model.TrainStepBisect
		}
	}

	// bisect: recompute the bounds from every finished probe
	good, bad := -1, len(inv.Candidates)-1
	for _, p := range inv.Probes {
		if p.Index < 0 {
			continue
		}
		switch p.Result {
		case probeInconclusive:
			inv.Step = model.TrainStepDone
			inv.Outcome = model.TrainOutcomeInconclusive
			inv.Detail = fmt.Sprintf("bisect probe #%d did not run %s", p.Pipeline, inv.Workflow)
			return false
		case probeFailure:
			bad = min(bad, p.Index)
		}
	}
	for _, p := range inv.Probes {
		if p.Index >= 0 && p.Result == probeSuccess && p.Index < bad {
			good = max(good, p.Index)
		}
	}
	inv.Good, inv.Bad = good, bad
	if bad-good <= 1 {
		inv.Step = model.TrainStepDone
		inv.Outcome = model.TrainOutcomeCulprit
		inv.Culprit = inv.Candidates[bad]
		inv.Detail = fmt.Sprintf("first of %d candidates to fail %s", len(inv.Candidates), inv.Workflow)
		return false
	}
	return true
}

func headProbe(inv *model.TrainInvestigation) *model.TrainProbe {
	for i := len(inv.Probes) - 1; i >= 0; i-- {
		if inv.Probes[i].Index < 0 {
			return inv.Probes[i]
		}
	}
	return nil
}

// probe starts up to k pipelines for the investigation's next step.
func (r *run) probe(inv *model.TrainInvestigation, k int) (int, error) {
	if inv.Step != model.TrainStepBisect {
		return r.startProbe(inv, -1, r.st.Head)
	}
	started := 0
	for _, i := range splitPoints(inv.Good, inv.Bad, k) {
		n, err := r.startProbe(inv, i, inv.Candidates[i])
		started += n
		if err != nil {
			return started, err
		}
	}
	return started, nil
}

// splitPoints returns up to k indices strictly between good and bad that cut
// the range into equal parts.
func splitPoints(good, bad, k int) []int {
	inner := bad - good - 1
	k = min(k, inner)
	var out []int
	for i := 1; i <= k; i++ {
		p := good + i*(bad-good)/(k+1)
		if p > good && p < bad && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func (r *run) startProbe(inv *model.TrainInvestigation, index int, sha string) (int, error) {
	var cars []string
	var upto []*model.TrainCar
	for _, car := range r.st.Cars {
		cars = append(cars, car.SHA)
		upto = append(upto, car)
		if car.SHA == sha {
			break
		}
	}
	where := "the head"
	if index >= 0 {
		where = fmt.Sprintf("candidate %d of %d", index+1, len(inv.Candidates))
	}
	n, err := r.c.env.Start(r.ctx, r.repo, &PromoteRequest{
		Branch:    r.cfg.Branch,
		SHA:       sha,
		Message:   fmt.Sprintf("Bisect %s of promote #%d at %s", inv.Workflow, r.st.Pipeline, where),
		Base:      r.st.Base,
		Cars:      cars,
		Kind:      model.PromoteKindBisect,
		Files:     carFiles(upto),
		Workflows: []string{inv.Workflow},
	})
	if errors.Is(err, ErrFiltered) {
		inv.Probes = append(inv.Probes, &model.TrainProbe{SHA: sha, Index: index, Result: probeInconclusive})
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("start a probe of %s at %s: %w", inv.Workflow, sha, err)
	}
	inv.Probes = append(inv.Probes, &model.TrainProbe{Pipeline: n, SHA: sha, Index: index})
	if inv.Step == model.TrainStepBisect {
		// remember the round's bounds as soon as it starts
		inv.Detail = fmt.Sprintf("bisecting candidates %d to %d", inv.Good+2, inv.Bad+1)
	}
	return 1, nil
}

// finish reverts the culprits, newest first, and decides how the train goes on.
func (r *run) finish() (bool, error) {
	n := r.st.Pipeline
	culprits := map[string][]string{}
	var problems []string
	for _, inv := range r.st.Investigations {
		switch inv.Outcome {
		case model.TrainOutcomeCulprit:
			culprits[inv.Culprit] = append(culprits[inv.Culprit], inv.Workflow)
		case model.TrainOutcomeFlake:
			r.log("flake", fmt.Sprintf("%s in promote #%d was a flake: %s", inv.Workflow, n, inv.Detail), n, r.st.Head)
		default:
			problems = append(problems, fmt.Sprintf("%s: %s", inv.Workflow, inv.Detail))
		}
	}

	for i := len(r.st.Cars) - 1; i >= 0; i-- {
		car := r.st.Cars[i]
		workflows := culprits[car.SHA]
		if len(workflows) == 0 || r.reverted(car.SHA, n) {
			continue
		}
		title := fmt.Sprintf("Revert \"%s\" (broke %s in promote #%d)", car.Title, strings.Join(workflows, ", "), n)
		sha, err := r.f.Revert(r.ctx, r.u, r.repo, r.cfg.Branch, car, title)
		switch {
		case errors.Is(err, forge.ErrRevertConflict):
			r.notify(&forge.TrainNotice{
				PR:      car.PR,
				Comment: fmt.Sprintf("The merge train found that this change broke %s in promote #%d (%s), but its revert does not apply cleanly to %s. The train is frozen until someone takes it off by hand and resumes the train.", strings.Join(workflows, ", "), n, r.c.env.Link(r.repo, n), r.cfg.Branch),
			})
			return r.freeze(fmt.Sprintf("revert conflict: %q broke %s in promote #%d but does not revert cleanly", car.Title, strings.Join(workflows, ", "), n))
		case errors.Is(err, forge.ErrNothingToRevert):
			problems = append(problems, fmt.Sprintf("%s: culprit %q changes nothing to revert", strings.Join(workflows, ", "), car.Title))
			continue
		case err != nil:
			return false, fmt.Errorf("revert %s: %w", car.SHA, err)
		}
		r.st.Reverts = append(r.st.Reverts, &model.TrainRevert{
			Car:       car.SHA,
			Title:     car.Title,
			PR:        car.PR,
			Workflows: workflows,
			Promote:   n,
			Commit:    sha,
			Created:   r.c.now().Unix(),
		})
		r.log("revert", title, n, sha)
		if err := r.save(); err != nil {
			return false, err
		}
		link := r.c.env.Link(r.repo, n)
		r.notify(&forge.TrainNotice{
			SHA:         car.SHA,
			State:       model.StatusFailure,
			Description: fmt.Sprintf("reverted: broke %s in promote #%d", strings.Join(workflows, ", "), n),
			Link:        link,
			PR:          car.PR,
			Comment: fmt.Sprintf("The merge train took this change off %s: it broke %s in promote #%d (%s). The revert is %s. Fix the change and open a new pull request.",
				r.cfg.Branch, strings.Join(workflows, ", "), n, link, short(sha)),
		})
	}

	if len(problems) > 0 {
		return r.freeze(strings.Join(problems, "; "))
	}
	if len(culprits) == 0 {
		// every regression was a flake: the tested head is good
		return r.promote()
	}
	r.st.LastResult = fmt.Sprintf("promote #%d: reverted %d %s", n, len(culprits), plural(len(culprits), "car"))
	r.st.Phase = model.TrainIdle
	if r.st.Frozen {
		if err := r.unfreeze(); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (r *run) reverted(car string, promote int64) bool {
	for _, rv := range r.st.Reverts {
		if rv.Car == car && rv.Promote == promote {
			return true
		}
	}
	return false
}
