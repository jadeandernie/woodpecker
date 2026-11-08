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

package datastore

import (
	"fmt"
	"strings"

	"xorm.io/builder"
	"xorm.io/xorm"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// testChunk bounds the number of values in one IN (...) list.
const testChunk = 500

func chunks[T any](values []T, size int) [][]T {
	var out [][]T
	for len(values) > size {
		out = append(out, values[:size])
		values = values[size:]
	}
	if len(values) > 0 {
		out = append(out, values)
	}
	return out
}

func (s storage) TestCaseList(repoID int64, filter *model.TestCaseFilter) ([]*model.TestCase, error) {
	cond := builder.NewCond().And(builder.Eq{"repo_id": repoID})
	if filter != nil {
		if q := strings.ToLower(strings.TrimSpace(filter.Query)); q != "" {
			like := "%" + q + "%"
			cond = cond.And(builder.Expr("(LOWER(name) LIKE ? OR LOWER(suite) LIKE ? OR LOWER(file_path) LIKE ?)", like, like, like))
		}
		if filter.Group != "" {
			cond = cond.And(builder.Eq{"test_group": filter.Group})
		}
		if filter.Kind != "" {
			cond = cond.And(builder.Eq{"kind": filter.Kind})
		}
		if filter.IDs != nil {
			cases := make([]*model.TestCase, 0, len(filter.IDs))
			for _, ids := range chunks(filter.IDs, testChunk) {
				part := make([]*model.TestCase, 0, len(ids))
				if err := s.engine.Where(cond.And(builder.In("id", ids))).Find(&part); err != nil {
					return nil, err
				}
				cases = append(cases, part...)
			}
			return cases, nil
		}
	}
	cases := make([]*model.TestCase, 0)
	return cases, s.engine.Where(cond).OrderBy("id").Find(&cases)
}

func (s storage) TestCaseGet(repoID, id int64) (*model.TestCase, error) {
	tc := new(model.TestCase)
	return tc, wrapGet(s.engine.Where(builder.Eq{"repo_id": repoID, "id": id}).Get(tc))
}

func (s storage) TestCasesByKeyHash(repoID int64, hashes []string) ([]*model.TestCase, error) {
	cases := make([]*model.TestCase, 0, len(hashes))
	for _, part := range chunks(hashes, testChunk) {
		found := make([]*model.TestCase, 0, len(part))
		if err := s.engine.Where(builder.Eq{"repo_id": repoID}.And(builder.In("key_hash", part))).Find(&found); err != nil {
			return nil, err
		}
		cases = append(cases, found...)
	}
	return cases, nil
}

func (s storage) TestReportSave(cases []*model.TestCase, runs []*model.TestRun) error {
	return retryWrite(func() error {
		var inserted []*model.TestCase
		err := s.testReportSave(cases, runs, &inserted)
		if err != nil {
			// the transaction was rolled back: a retry inserts these again
			for _, tc := range inserted {
				tc.ID = 0
			}
			for _, run := range runs {
				run.ID = 0
			}
		}
		return err
	})
}

func (s storage) testReportSave(cases []*model.TestCase, runs []*model.TestRun, inserted *[]*model.TestCase) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	if err := sess.Begin(); err != nil {
		return err
	}

	idOf := make(map[string]int64, len(cases))
	for _, tc := range cases {
		if tc.ID == 0 {
			*inserted = append(*inserted, tc)
			if err := wrapInsert(sess.Insert(tc)); err != nil {
				return err
			}
		} else if _, err := sess.ID(tc.ID).AllCols().Update(tc); err != nil {
			return wrapUpdate(0, err)
		}
		idOf[tc.KeyHash] = tc.ID
	}
	for _, run := range runs {
		if run.CaseKeyHash != "" {
			id, ok := idOf[run.CaseKeyHash]
			if !ok {
				return fmt.Errorf("test run of an unknown test case %s", run.CaseKeyHash)
			}
			run.TestCaseID = id
		}
	}
	for _, part := range chunks(runs, 100) {
		if err := wrapInsert(sess.Insert(part)); err != nil {
			return err
		}
	}
	return wrapUpdate(0, sess.Commit())
}

func testRunCond(filter *model.TestRunFilter) builder.Cond {
	cond := builder.NewCond()
	if filter.IDs != nil {
		cond = cond.And(builder.In("id", filter.IDs))
	}
	if filter.RepoID != 0 {
		cond = cond.And(builder.Eq{"repo_id": filter.RepoID})
	}
	if filter.TestCaseID != 0 {
		cond = cond.And(builder.Eq{"test_case_id": filter.TestCaseID})
	}
	if filter.TestCaseIDs != nil {
		cond = cond.And(builder.In("test_case_id", filter.TestCaseIDs))
	}
	if filter.PipelineID != 0 {
		cond = cond.And(builder.Eq{"pipeline_id": filter.PipelineID})
	}
	if filter.PipelineIDs != nil {
		cond = cond.And(builder.In("pipeline_id", filter.PipelineIDs))
	}
	if filter.Branch != "" {
		cond = cond.And(builder.Eq{"branch": filter.Branch})
	}
	return cond
}

func (s storage) TestRunList(filter *model.TestRunFilter) ([]*model.TestRun, error) {
	runs := make([]*model.TestRun, 0)
	if filter == nil {
		return runs, nil
	}
	// split long ID lists; a limit applies to each part, which only the
	// unchunked single-case queries use
	switch {
	case len(filter.IDs) > testChunk:
		for _, ids := range chunks(filter.IDs, testChunk) {
			f := *filter
			f.IDs = ids
			part, err := s.TestRunList(&f)
			if err != nil {
				return nil, err
			}
			runs = append(runs, part...)
		}
		return runs, nil
	case len(filter.TestCaseIDs) > testChunk:
		for _, ids := range chunks(filter.TestCaseIDs, testChunk) {
			f := *filter
			f.TestCaseIDs = ids
			part, err := s.TestRunList(&f)
			if err != nil {
				return nil, err
			}
			runs = append(runs, part...)
		}
		return runs, nil
	}

	sess := s.engine.Where(testRunCond(filter)).Desc("id")
	if filter.WithoutMessage {
		sess = sess.Omit("message")
	}
	if filter.Limit > 0 {
		sess = sess.Limit(filter.Limit)
	}
	return runs, sess.Find(&runs)
}

func (s storage) TestRunStats(filter *model.TestRunFilter) ([]*model.TestCaseStats, error) {
	stats := make([]*model.TestCaseStats, 0)
	if filter == nil {
		return stats, nil
	}
	err := s.engine.Table("test_runs").
		Select(`test_case_id,
			COUNT(*) AS runs,
			SUM(CASE WHEN status = 'pass' THEN 1 ELSE 0 END) AS passes,
			SUM(CASE WHEN status IN ('fail', 'error') THEN 1 ELSE 0 END) AS failures,
			SUM(CASE WHEN status = 'skip' THEN 1 ELSE 0 END) AS skips,
			SUM(CASE WHEN status = 'skip' THEN 0 ELSE duration_ms END) AS duration_ms,
			MAX(id) AS last_run_id`).
		Where(testRunCond(filter)).
		GroupBy("test_case_id").
		Find(&stats)
	return stats, err
}

func (s storage) TestRunPipelineIDs(repoID int64, branch string, limit int) ([]int64, error) {
	ids := make([]int64, 0, limit)
	cond := builder.Eq{"repo_id": repoID}
	if branch != "" {
		cond["branch"] = branch
	}
	return ids, s.engine.Table("pipelines").Cols("id").Where(cond).Desc("id").Limit(limit).Find(&ids)
}

func (s storage) TestRunPrune(repoID int64, keepPipelines int, before int64) error {
	cond := builder.NewCond()
	if keepPipelines > 0 {
		// the newest pipeline beyond the ones whose runs are kept
		var cutoff []int64
		if err := s.engine.Table("pipelines").Cols("id").Where(builder.Eq{"repo_id": repoID}).
			Desc("id").Limit(1, keepPipelines).Find(&cutoff); err != nil {
			return err
		}
		if len(cutoff) > 0 {
			cond = cond.Or(builder.Lte{"pipeline_id": cutoff[0]})
		}
	}
	if before > 0 {
		cond = cond.Or(builder.Lt{"created": before})
	}
	if !cond.IsValid() {
		return nil
	}
	return retryWrite(func() error {
		sess := s.engine.NewSession()
		defer sess.Close()
		if err := sess.Begin(); err != nil {
			return err
		}
		if err := s.testRunsDelete(sess, builder.Eq{"repo_id": repoID}.And(cond), nil); err != nil {
			return err
		}
		return wrapUpdate(0, sess.Commit())
	})
}

// testRunsDeletePipeline deletes the test runs of a pipeline.
func (s storage) testRunsDeletePipeline(sess *xorm.Session, pipelineID int64) error {
	return s.testRunsDelete(sess, builder.Eq{"pipeline_id": pipelineID}, []int64{pipelineID})
}

// testRunsDeleteRepo deletes the test runs and cases of a repo.
func (s storage) testRunsDeleteRepo(sess *xorm.Session, repoID int64) error {
	if _, err := sess.Where("repo_id = ?", repoID).Delete(new(model.TestRun)); err != nil {
		return err
	}
	_, err := sess.Where("repo_id = ?", repoID).Delete(new(model.TestCase))
	return err
}

// testRunsDelete deletes the runs that match cond and takes them off their
// cases' counters. Cases left without runs are deleted. Cases whose last run
// was in one of deletedPipelines get their last run and recent statuses from
// the runs that are left.
func (s storage) testRunsDelete(sess *xorm.Session, cond builder.Cond, deletedPipelines []int64) error {
	var removed []*model.TestCaseStats
	err := sess.Table("test_runs").
		Select(`test_case_id,
			COUNT(*) AS runs,
			SUM(CASE WHEN status = 'pass' THEN 1 ELSE 0 END) AS passes,
			SUM(CASE WHEN status IN ('fail', 'error') THEN 1 ELSE 0 END) AS failures,
			SUM(CASE WHEN status = 'skip' THEN 1 ELSE 0 END) AS skips,
			SUM(CASE WHEN status = 'skip' THEN 0 ELSE duration_ms END) AS duration_ms`).
		Where(cond).GroupBy("test_case_id").Find(&removed)
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		return nil
	}
	if _, err := sess.Where(cond).Delete(new(model.TestRun)); err != nil {
		return err
	}

	ids := make([]int64, 0, len(removed))
	for _, r := range removed {
		if _, err := sess.Exec(`UPDATE test_cases SET runs = runs - ?, passes = passes - ?, failures = failures - ?,
			skips = skips - ?, duration_ms = duration_ms - ? WHERE id = ?`,
			r.Runs, r.Passes, r.Failures, r.Skips, r.DurationMs, r.TestCaseID); err != nil {
			return err
		}
		ids = append(ids, r.TestCaseID)
	}

	for _, part := range chunks(ids, testChunk) {
		if _, err := sess.Where(builder.In("id", part).And(builder.Lte{"runs": 0})).Delete(new(model.TestCase)); err != nil {
			return err
		}
	}
	if len(deletedPipelines) == 0 {
		return nil
	}

	for _, part := range chunks(ids, testChunk) {
		var stale []*model.TestCase
		if err := sess.Where(builder.In("id", part).And(builder.In("last_pipeline_id", deletedPipelines))).Find(&stale); err != nil {
			return err
		}
		for _, tc := range stale {
			var recent []*model.TestRun
			if err := sess.Where(builder.Eq{"test_case_id": tc.ID}).Omit("message").Desc("id").
				Limit(model.TestRecentRuns).Find(&recent); err != nil {
				return err
			}
			tc.Recent = ""
			for i := len(recent) - 1; i >= 0; i-- {
				tc.AddRecent(recent[i].Status)
			}
			if len(recent) > 0 {
				last := recent[0]
				for _, run := range recent {
					if run.PipelineNumber > last.PipelineNumber {
						last = run
					}
				}
				tc.LastRun = last.Created
				tc.LastStatus = last.Status
				tc.LastPipelineID = last.PipelineID
				tc.LastPipelineNumber = last.PipelineNumber
				tc.LastCommit = last.Commit
				tc.LastBranch = last.Branch
				tc.LastDurationMs = last.DurationMs
			}
			if _, err := sess.ID(tc.ID).Cols("recent", "last_run", "last_status", "last_pipeline_id", "last_pipeline_number",
				"last_commit", "last_branch", "last_duration_ms").Update(tc); err != nil {
				return err
			}
		}
	}
	return nil
}
