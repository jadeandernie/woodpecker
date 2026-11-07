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

package gitealike

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sync/errgroup"

	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

const (
	// MaxCars caps the cars Cars walks; a longer train is an error.
	MaxCars = 200
	// carFetches bounds the per-car requests Cars runs at once.
	carFetches = 8
	// revertLookback is how many recent commits of the train branch Revert
	// searches for an earlier revert of the same car.
	revertLookback = 50
	// commentLookback is how many of a pull request's last comments Notify
	// searches for the same comment.
	commentLookback = 20
	// StatusContext is the commit status context Notify sets.
	StatusContext = "train"
	// PromoteBranchPrefix starts the name of the branch FastForward merges
	// through a pull request.
	PromoteBranchPrefix = "train/promote-"
)

// Cars implements forge.Train.
func (c *Client) Cars(ctx context.Context, owner, repo, branch, target string) (*forge.TrainCars, error) {
	head, err := c.branchHead(ctx, owner, repo, branch)
	if err != nil {
		return nil, fmt.Errorf("train branch %s: %w", branch, err)
	}
	targetHead, err := c.branchHead(ctx, owner, repo, target)
	if err != nil {
		return nil, fmt.Errorf("target branch %s: %w", target, err)
	}
	ahead, err := c.compare(ctx, owner, repo, target, branch)
	if err != nil {
		return nil, err
	}
	notOnTarget := make(map[string]*commit, len(ahead.Commits))
	for _, cm := range ahead.Commits {
		notOnTarget[cm.SHA] = cm
	}

	// Walk the first parents from the head while they are not on the target.
	var cars []*model.TrainCar
	for sha := head; ; {
		cm, ok := notOnTarget[sha]
		if !ok {
			break
		}
		if len(cars) >= MaxCars {
			return nil, fmt.Errorf("train branch %s is more than %d commits ahead of %s", branch, MaxCars, target)
		}
		if len(cm.Parents) == 0 {
			// The compare result may leave out parents; ask for the commit.
			if cm, err = c.getCommit(ctx, owner, repo, sha); err != nil {
				return nil, err
			}
		}
		car := &model.TrainCar{SHA: sha, Title: firstLine(cm.Commit.Message)}
		if len(cm.Parents) > 0 {
			car.Parent = cm.Parents[0].SHA
		}
		cars = append(cars, car)
		if car.Parent == "" {
			break
		}
		sha = car.Parent
	}
	for i, j := 0, len(cars)-1; i < j; i, j = i+1, j-1 {
		cars[i], cars[j] = cars[j], cars[i]
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(carFetches)
	for _, car := range cars {
		g.Go(func() error {
			files, err := c.carFiles(gctx, owner, repo, car)
			if err != nil {
				return fmt.Errorf("files of %s: %w", car.SHA, err)
			}
			car.Files = files
			pr, err := c.carPull(gctx, owner, repo, car.SHA)
			if err != nil {
				return fmt.Errorf("pull request of %s: %w", car.SHA, err)
			}
			car.PR = pr
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	moved, err := c.ahead(ctx, owner, repo, branch, target)
	if err != nil {
		return nil, err
	}
	return &forge.TrainCars{Head: head, Target: targetHead, Cars: cars, TargetMoved: moved}, nil
}

// carFiles returns the files a car changed against its first parent.
func (c *Client) carFiles(ctx context.Context, owner, repo string, car *model.TrainCar) ([]string, error) {
	if car.Parent != "" {
		var res struct {
			Files *[]affectedFile `json:"files"`
		}
		p := repoPath(owner, repo, "compare") + "/" + url.PathEscape(car.Parent) + "..." + url.PathEscape(car.SHA)
		if err := c.do(ctx, http.MethodGet, p, nil, nil, &res); err != nil {
			return nil, err
		}
		if res.Files != nil {
			files := make([]string, 0, len(*res.Files))
			for _, f := range *res.Files {
				files = append(files, f.Filename)
			}
			return files, nil
		}
	}
	// A root commit, or a forge whose compare lists no files: read the
	// commit's diff (against its first parent) instead.
	diff, err := c.commitDiff(ctx, owner, repo, car.SHA)
	if err != nil {
		return nil, err
	}
	return DiffFiles(diff)
}

// carPull returns the number of the pull request whose merge is sha, or 0.
func (c *Client) carPull(ctx context.Context, owner, repo, sha string) (int64, error) {
	var pr pullRequest
	err := c.do(ctx, http.MethodGet, repoPath(owner, repo, "commits", sha, "pull"), nil, nil, &pr)
	if isNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return pr.Number, nil
}

func (c *Client) commitDiff(ctx context.Context, owner, repo, sha string) (string, error) {
	body, err := c.raw(ctx, http.MethodGet, repoPath(owner, repo, "git", "commits", sha+".diff"), nil, nil)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimLeft(s, "\n"), "\n")
	return strings.TrimSpace(line)
}

// FastForward implements forge.Train. It moves target through a pull
// request from a branch train/promote-<promote> at sha, merged fast-forward
// only, so the forge's merge rules for target apply. It returns nil if target
// already contains sha, which also covers a call repeated after a promote
// landed.
func (c *Client) FastForward(ctx context.Context, owner, repo, target, sha string, promote int64) error {
	targetHead, err := c.branchHead(ctx, owner, repo, target)
	if err != nil {
		return err
	}
	if targetHead == sha {
		return nil
	}
	if ahead, err := c.ahead(ctx, owner, repo, target, sha); err != nil {
		return err
	} else if !ahead {
		return nil // target already contains sha
	}
	if diverged, err := c.ahead(ctx, owner, repo, sha, target); err != nil {
		return err
	} else if diverged {
		return fmt.Errorf("%w: %s has commits %s lacks", forge.ErrNotFastForward, target, shortSHA(sha))
	}

	branch := fmt.Sprintf("%s%d", PromoteBranchPrefix, promote)
	if err := c.ensureBranchAt(ctx, owner, repo, branch, sha); err != nil {
		return err
	}
	pr, err := c.ensurePull(ctx, owner, repo, branch, target, fmt.Sprintf("Promote #%d", promote))
	if err != nil {
		return err
	}
	mergeErr := c.merge(ctx, owner, repo, pr.Number, &mergeOptions{
		Do:                     "fast-forward-only",
		HeadCommitID:           sha,
		DeleteBranchAfterMerge: true,
	})
	if mergeErr != nil {
		// The merge may have landed even though the call failed.
		if head, err := c.branchHead(ctx, owner, repo, target); err != nil || head != sha {
			if isTryLater(mergeErr) || statusOf(mergeErr) == 0 ||
				statusOf(mergeErr) == http.StatusUnauthorized || statusOf(mergeErr) == http.StatusForbidden {
				// Still checking, unreachable or not allowed: not a verdict on sha.
				return fmt.Errorf("merge #%d into %s: %w", pr.Number, target, mergeErr)
			}
			c.closePull(ctx, owner, repo, pr.Number, branch)
			return fmt.Errorf("%w: merge #%d into %s: %w", forge.ErrNotFastForward, pr.Number, target, mergeErr)
		}
	}
	if err := c.deleteBranch(ctx, owner, repo, branch); err != nil {
		return fmt.Errorf("%s moved to %s, but deleting %s failed: %w", target, shortSHA(sha), branch, err)
	}
	return nil
}

// closePull closes a promote pull request that cannot merge and deletes its
// branch, as far as the forge lets it; the train promotes another number next.
func (c *Client) closePull(ctx context.Context, owner, repo string, index int64, branch string) {
	_ = c.do(ctx, http.MethodPatch, repoPath(owner, repo, "pulls", fmt.Sprint(index)), nil, map[string]string{"state": "closed"}, nil)
	_ = c.deleteBranch(ctx, owner, repo, branch)
}

// ensureBranchAt creates branch at sha, or checks that an existing one is
// there.
func (c *Client) ensureBranchAt(ctx context.Context, owner, repo, branch, sha string) error {
	var created branchInfo
	body := map[string]string{"new_branch_name": branch, "old_ref_name": sha}
	err := c.do(ctx, http.MethodPost, repoPath(owner, repo, "branches"), nil, body, &created)
	head := created.Commit.ID
	if statusOf(err) == http.StatusConflict {
		head, err = c.branchHead(ctx, owner, repo, branch)
	}
	if err != nil {
		return fmt.Errorf("create branch %s at %s: %w", branch, shortSHA(sha), err)
	}
	if head == "" {
		if head, err = c.branchHead(ctx, owner, repo, branch); err != nil {
			return err
		}
	}
	if head != sha {
		return fmt.Errorf("branch %s is at %s, not %s; delete it to promote again", branch, shortSHA(head), shortSHA(sha))
	}
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// Revert implements forge.Train. It reverses the car's diff against its
// first parent in Go and applies it to branch with the forge's diffpatch
// call. It returns ErrNothingToRevert if the car changed nothing.
func (c *Client) Revert(ctx context.Context, owner, repo, branch string, car *model.TrainCar, title string) (string, error) {
	trailer := forge.TrainRevertTrailer + ": " + car.SHA
	var recent []*commit
	q := url.Values{
		"sha": {branch}, "limit": {fmt.Sprint(revertLookback)},
		"stat": {"false"}, "verification": {"false"}, "files": {"false"},
	}
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo, "commits"), q, nil, &recent); err != nil {
		return "", err
	}
	for _, cm := range recent {
		for _, line := range strings.Split(cm.Commit.Message, "\n") {
			if strings.TrimSpace(line) == trailer {
				return cm.SHA, nil
			}
		}
	}

	head, err := c.branchHead(ctx, owner, repo, branch)
	if err != nil {
		return "", err
	}
	diff, err := c.commitDiff(ctx, owner, repo, car.SHA)
	if err != nil {
		return "", fmt.Errorf("diff of %s: %w", car.SHA, err)
	}
	patch, err := ReversePatch(diff)
	if err != nil {
		return "", fmt.Errorf("revert %s: %w", shortSHA(car.SHA), err)
	}
	var res struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	body := map[string]string{
		"branch":  branch,
		"sha":     head,
		"content": patch,
		"message": title + "\n\n" + trailer,
	}
	err = c.do(ctx, http.MethodPost, repoPath(owner, repo, "diffpatch"), nil, body, &res)
	if s := statusOf(err); s >= 400 && s != http.StatusUnauthorized && s != http.StatusForbidden {
		return "", fmt.Errorf("%w: %w", forge.ErrRevertConflict, err)
	}
	if err != nil {
		return "", err
	}
	if res.Commit.SHA == "" {
		return "", errors.New("diffpatch: forge returned no commit")
	}
	return res.Commit.SHA, nil
}

// Notify implements forge.Train.
func (c *Client) Notify(ctx context.Context, owner, repo string, notice *forge.TrainNotice) error {
	if notice.SHA != "" {
		body := map[string]string{
			"state":       statusState(notice.State),
			"context":     StatusContext,
			"description": notice.Description,
			"target_url":  notice.Link,
		}
		if err := c.do(ctx, http.MethodPost, repoPath(owner, repo, "statuses", notice.SHA), nil, body, nil); err != nil {
			return err
		}
	}
	if notice.PR == 0 || notice.Comment == "" {
		return nil
	}
	p := repoPath(owner, repo, "issues", fmt.Sprint(notice.PR), "comments")
	var comments []struct {
		Body string `json:"body"`
	}
	if err := c.do(ctx, http.MethodGet, p, nil, nil, &comments); err != nil {
		return err
	}
	for i := len(comments) - 1; i >= 0 && i >= len(comments)-commentLookback; i-- {
		if comments[i].Body == notice.Comment {
			return nil
		}
	}
	return c.do(ctx, http.MethodPost, p, nil, map[string]string{"body": notice.Comment}, nil)
}

// statusState maps a pipeline status to a Gitea commit status state.
func statusState(s model.StatusValue) string {
	switch s {
	case model.StatusSuccess:
		return "success"
	case model.StatusFailure, model.StatusCanceled, model.StatusSkipped:
		return "failure"
	case model.StatusPending, model.StatusRunning, model.StatusCreated, model.StatusBlocked:
		return "pending"
	case model.StatusDeclined:
		return "warning"
	default: // StatusError, StatusKilled and anything unknown
		return "error"
	}
}

// Sync implements forge.Train. It merges through a pull request from from
// into into. It returns "" and no error if into already has everything from
// has, which includes a call repeated after the merge landed.
func (c *Client) Sync(ctx context.Context, owner, repo, from, into, title string) (string, error) {
	if ahead, err := c.ahead(ctx, owner, repo, into, from); err != nil || !ahead {
		return "", err
	}
	fromHead, err := c.branchHead(ctx, owner, repo, from)
	if err != nil {
		return "", err
	}
	pr, err := c.ensurePull(ctx, owner, repo, from, into, title)
	if err != nil {
		return "", err
	}
	mergeErr := c.merge(ctx, owner, repo, pr.Number, &mergeOptions{
		Do:              "merge",
		MergeTitleField: title,
		HeadCommitID:    fromHead,
	})
	if mergeErr != nil {
		if ahead, err := c.ahead(ctx, owner, repo, into, from); err == nil && !ahead {
			// It landed after all.
			return c.branchHead(ctx, owner, repo, into)
		}
		if head, err := c.branchHead(ctx, owner, repo, from); err != nil || head != fromHead {
			return "", fmt.Errorf("merge #%d into %s (%s moved meanwhile): %w", pr.Number, into, from, mergeErr)
		}
		if s := statusOf(mergeErr); (s == http.StatusMethodNotAllowed || s == http.StatusConflict) && !isTryLater(mergeErr) {
			return "", fmt.Errorf("%w: merge #%d: %w", forge.ErrSyncConflict, pr.Number, mergeErr)
		}
		return "", fmt.Errorf("merge #%d into %s: %w", pr.Number, into, mergeErr)
	}
	var merged pullRequest
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo, "pulls", fmt.Sprint(pr.Number)), nil, nil, &merged); err == nil && merged.MergeCommitSHA != "" {
		return merged.MergeCommitSHA, nil
	}
	return c.branchHead(ctx, owner, repo, into)
}
