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

package testreport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
)

// Retention of test runs: runs of pipelines older than a repo's
// RetentionPipelines newest pipelines, and runs older than RetentionDays, are
// pruned at most every PruneInterval per repo, after an ingest. A test case
// left without runs is deleted with them, so tests that no longer run drop
// off the list.
var (
	RetentionPipelines = 200
	RetentionDays      = 90
	PruneInterval      = 10 * time.Minute
)

var (
	// repoLocks serialises the ingests of a repo, which read, update and
	// write the counters of its test cases.
	repoLocks sync.Map // repo ID -> *sync.Mutex
	lastPrune sync.Map // repo ID -> time.Time
)

func repoLock(repoID int64) *sync.Mutex {
	mu, _ := repoLocks.LoadOrStore(repoID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// KeyHash is the hash that identifies a test key in the store.
func KeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Ingest parses the report files a step of a pipeline collected and stores
// their results: a run per result, and the test cases with their counters.
// Files it cannot read are logged and skipped; it fails only when the store
// does.
func Ingest(s store.Store, repo *model.Repo, pipeline *model.Pipeline, workflow *model.Workflow, step *model.Step, files []ReportFile) error {
	report := Parse(files)
	for _, warning := range report.Warnings {
		log.Warn().Str("repo", repo.FullName).Int64("pipeline", pipeline.Number).Msgf("test report: %s", warning)
	}
	if len(report.Results) == 0 {
		return nil
	}

	mu := repoLock(repo.ID)
	mu.Lock()
	defer mu.Unlock()

	hashes := make([]string, 0, len(report.Results))
	seen := make(map[string]bool, len(report.Results))
	for _, r := range report.Results {
		if h := KeyHash(r.Key()); !seen[h] {
			seen[h] = true
			hashes = append(hashes, h)
		}
	}
	existing, err := s.TestCasesByKeyHash(repo.ID, hashes)
	if err != nil {
		return fmt.Errorf("loading test cases: %w", err)
	}

	cases, runs := apply(&ingestContext{
		repo:     repo,
		pipeline: pipeline,
		workflow: workflow,
		step:     step,
		modules:  report.Modules,
		now:      time.Now().Unix(),
	}, existing, report.Results)
	if err := s.TestReportSave(cases, runs); err != nil {
		return fmt.Errorf("saving test results: %w", err)
	}

	maybePrune(s, repo.ID)
	return nil
}

func maybePrune(s store.Store, repoID int64) {
	now := time.Now()
	if last, ok := lastPrune.Load(repoID); ok && now.Sub(last.(time.Time)) < PruneInterval {
		return
	}
	lastPrune.Store(repoID, now)
	var before int64
	if RetentionDays > 0 {
		before = now.AddDate(0, 0, -RetentionDays).Unix()
	}
	if err := s.TestRunPrune(repoID, RetentionPipelines, before); err != nil {
		log.Error().Err(err).Int64("repo_id", repoID).Msg("test report: pruning old test runs")
	}
}

type ingestContext struct {
	repo     *model.Repo
	pipeline *model.Pipeline
	workflow *model.Workflow
	step     *model.Step
	modules  []GoModule
	now      int64
}

// apply turns results into runs and updates (or creates) their cases. It
// returns the cases to save, existing first in their given order then the
// new ones, and the runs.
func apply(ctx *ingestContext, existing []*model.TestCase, results []*Result) ([]*model.TestCase, []*model.TestRun) {
	byHash := make(map[string]*model.TestCase, len(existing))
	for _, tc := range existing {
		byHash[tc.KeyHash] = tc
	}
	var created []*model.TestCase
	touched := make(map[string]bool, len(results))

	wfName, wfGroup, wfSubgroup, wfKind := "", "", "", ""
	var workflowID int64
	if ctx.workflow != nil {
		wfName = ctx.workflow.Name
		workflowID = ctx.workflow.ID
		wfGroup, wfSubgroup = model.NewWorkflowGrouper(ctx.repo.WorkflowGroups).Group(wfName)
		wfKind = ctx.workflow.Kind
	}
	if wfKind == "" {
		wfKind = wfGroup
	}
	var stepID int64
	if ctx.step != nil {
		stepID = ctx.step.ID
	}

	runs := make([]*model.TestRun, 0, len(results))
	for _, r := range results {
		key := r.Key()
		hash := KeyHash(key)

		file := NormalizeFile(r.File, ctx.repo.Name)
		dir := ""
		if r.Package != "" {
			dir, _ = GoPackageDir(r.Package, ctx.modules, ctx.repo.Name)
		}
		if file != "" && !strings.Contains(file, "/") && r.Package != "" {
			// a Go failure names only the file's base name
			if dir != "" {
				file = path.Join(dir, file)
			} else {
				file = ""
			}
		}
		group := ServiceGroup(file, true)
		if group == "" {
			group = ServiceGroup(dir, false)
		}
		if group == "" {
			group = wfSubgroup
		}
		if group == "" {
			group = wfName
		}
		if group == "" {
			group = model.WorkflowGroupOther
		}

		tc := byHash[hash]
		if tc == nil {
			tc = &model.TestCase{
				RepoID:            ctx.repo.ID,
				Key:               key,
				KeyHash:           hash,
				FirstSeen:         ctx.pipeline.Created,
				FirstSeenCommit:   ctx.pipeline.Commit,
				FirstSeenPipeline: ctx.pipeline.Number,
			}
			byHash[hash] = tc
			created = append(created, tc)
		}
		touched[hash] = true
		tc.Suite = r.Suite
		tc.Name = r.Name
		if file != "" {
			tc.File = file
			tc.Line = r.Line
		}
		if dir != "" {
			tc.Dir = dir
		}
		if ctx.pipeline.Created > 0 && (tc.FirstSeen == 0 || ctx.pipeline.Created < tc.FirstSeen) {
			tc.FirstSeen = ctx.pipeline.Created
			tc.FirstSeenCommit = ctx.pipeline.Commit
			tc.FirstSeenPipeline = ctx.pipeline.Number
		}

		durationMs := r.Duration.Milliseconds()
		tc.Runs++
		switch {
		case r.Status == model.TestStatusPass:
			tc.Passes++
		case r.Status == model.TestStatusSkip:
			tc.Skips++
		default:
			tc.Failures++
		}
		if r.Status != model.TestStatusSkip {
			tc.DurationMs += durationMs
		}
		tc.AddRecent(r.Status)
		if ctx.pipeline.Number >= tc.LastPipelineNumber {
			tc.LastRun = ctx.now
			tc.LastStatus = r.Status
			tc.LastPipelineID = ctx.pipeline.ID
			tc.LastPipelineNumber = ctx.pipeline.Number
			tc.LastCommit = ctx.pipeline.Commit
			tc.LastBranch = ctx.pipeline.Branch
			tc.LastDurationMs = durationMs
			tc.Group = group
			tc.Kind = wfKind
			tc.Workflow = wfName
		}

		runs = append(runs, &model.TestRun{
			RepoID:         ctx.repo.ID,
			PipelineID:     ctx.pipeline.ID,
			PipelineNumber: ctx.pipeline.Number,
			WorkflowID:     workflowID,
			Workflow:       wfName,
			StepID:         stepID,
			Branch:         ctx.pipeline.Branch,
			Commit:         ctx.pipeline.Commit,
			Status:         r.Status,
			DurationMs:     durationMs,
			Message:        r.Message,
			Created:        ctx.now,
			CaseKeyHash:    hash,
		})
	}

	cases := make([]*model.TestCase, 0, len(touched))
	for _, tc := range existing {
		if touched[tc.KeyHash] {
			cases = append(cases, tc)
		}
	}
	cases = append(cases, created...)
	return cases, runs
}
