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
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

const (
	owner = "owner"
	repo  = "repo"
)

// trainGraph builds main at t0 and a train branch t0 <- c1 <- m2 <- c3,
// where m2 merges a side commit x1 (off t0) as its second parent.
func trainGraph(f *fakeForge) {
	f.commit("t0", "base", []string{"base.go"})
	f.commit("c1", "Add a\n\nbody", []string{"a.go"}, "t0")
	f.commit("x1", "side work", []string{"side.go"}, "t0")
	f.commit("m2", "Merge side", nil, "c1", "x1")
	f.commit("c3", "Fix c", []string{"c.go", "d.go"}, "m2")
	f.branches["main"] = "t0"
	f.branches["train"] = "c3"
	f.mergedPR["c1"] = 11
	f.mergedPR["m2"] = 12
}

func TestCars(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeForge(t)
	trainGraph(f)

	got, err := c.Cars(ctx, owner, repo, "train", "main")
	require.NoError(t, err)
	assert.Equal(t, &forge.TrainCars{
		Head:   "c3",
		Target: "t0",
		Cars: []*model.TrainCar{
			{SHA: "c1", Parent: "t0", Title: "Add a", PR: 11, Files: []string{"a.go"}},
			{SHA: "m2", Parent: "c1", Title: "Merge side", PR: 12, Files: []string{"side.go"}},
			{SHA: "c3", Parent: "m2", Title: "Fix c", Files: []string{"c.go", "d.go"}},
		},
	}, got, "x1 is a second parent and no car; c3 has no pull request")
	assert.Empty(t, f.writes())

	t.Run("target moved", func(t *testing.T) {
		f.commit("t1", "hotfix", []string{"hot.go"}, "t0")
		f.branches["main"] = "t1"
		got, err := c.Cars(ctx, owner, repo, "train", "main")
		require.NoError(t, err)
		assert.True(t, got.TargetMoved)
		assert.Equal(t, "t1", got.Target)
		assert.Len(t, got.Cars, 3)
	})

	t.Run("files from the diff when compare lists none", func(t *testing.T) {
		f.omitFiles = true
		defer func() { f.omitFiles = false }()
		for _, sha := range []string{"c1", "m2", "c3"} {
			f.diffs[sha] = lines("diff --git a/"+sha+".txt b/"+sha+".txt", "deleted file mode 100644",
				"--- a/"+sha+".txt", "+++ /dev/null", "@@ -1 +0,0 @@", "-x")
		}
		got, err := c.Cars(ctx, owner, repo, "train", "main")
		require.NoError(t, err)
		require.Len(t, got.Cars, 3)
		assert.Equal(t, []string{"m2.txt"}, got.Cars[1].Files)
	})

	t.Run("nothing on the train", func(t *testing.T) {
		f.branches["empty"] = "t1"
		got, err := c.Cars(ctx, owner, repo, "empty", "main")
		require.NoError(t, err)
		assert.Empty(t, got.Cars)
		assert.False(t, got.TargetMoved)
	})

	t.Run("missing branch", func(t *testing.T) {
		_, err := c.Cars(ctx, owner, repo, "nope", "main")
		assert.Equal(t, http.StatusNotFound, statusOf(err))
	})
}

func ffGraph(f *fakeForge) {
	f.commit("a", "a", nil)
	f.commit("b", "b", nil, "a")
	f.branches["main"] = "a"
}

func TestFastForward(t *testing.T) {
	ctx := context.Background()

	t.Run("already at sha", func(t *testing.T) {
		f, c := newFakeForge(t)
		ffGraph(f)
		require.NoError(t, c.FastForward(ctx, owner, repo, "main", "a", 7))
		f.branches["main"] = "b"
		require.NoError(t, c.FastForward(ctx, owner, repo, "main", "a", 7), "main already contains a")
		assert.Empty(t, f.writes())
	})

	t.Run("happy path", func(t *testing.T) {
		f, c := newFakeForge(t)
		ffGraph(f)
		require.NoError(t, c.FastForward(ctx, owner, repo, "main", "b", 7))
		assert.Equal(t, "b", f.branches["main"])
		assert.NotContains(t, f.branches, "train/promote-7")
		assert.Equal(t, []string{
			"POST /branches",
			"POST /pulls",
			"POST /pulls/100/merge",
			"DELETE /branches/train/promote-7",
		}, f.writes())
		assert.Equal(t, map[string]any{"new_branch_name": "train/promote-7", "old_ref_name": "b"},
			f.calls(http.MethodPost, "/branches")[0].Body)
		assert.Equal(t, map[string]any{"head": "train/promote-7", "base": "main", "title": "Promote #7"},
			f.calls(http.MethodPost, "/pulls")[0].Body)
		assert.Equal(t, map[string]any{"Do": "fast-forward-only", "head_commit_id": "b", "delete_branch_after_merge": true},
			f.calls(http.MethodPost, "/pulls/100/merge")[0].Body)

		require.NoError(t, c.FastForward(ctx, owner, repo, "main", "b", 7), "repeat after landing")
		assert.Len(t, f.writes(), 4)
	})

	t.Run("diverged target", func(t *testing.T) {
		f, c := newFakeForge(t)
		ffGraph(f)
		f.commit("c", "c", nil, "a")
		f.branches["main"] = "c"
		err := c.FastForward(ctx, owner, repo, "main", "b", 7)
		assert.ErrorIs(t, err, forge.ErrNotFastForward)
		assert.Empty(t, f.writes())
	})

	t.Run("merge rejected", func(t *testing.T) {
		f, c := newFakeForge(t)
		ffGraph(f)
		f.mergeHook = func(*fakePull, map[string]any) (int, string) {
			return http.StatusMethodNotAllowed, "Not allowed to merge: required status checks failed"
		}
		err := c.FastForward(ctx, owner, repo, "main", "b", 7)
		assert.ErrorIs(t, err, forge.ErrNotFastForward)
		assert.Equal(t, "a", f.branches["main"])
		assert.Equal(t, "closed", f.pulls[0].State, "the failed promote is closed")
		assert.NotContains(t, f.branches, "train/promote-7")
		assert.Equal(t, map[string]any{"state": "closed"}, f.calls(http.MethodPatch, "/pulls/100")[0].Body)
	})

	t.Run("forge still checking", func(t *testing.T) {
		f, c := newFakeForge(t)
		ffGraph(f)
		tries := 0
		f.mergeHook = func(*fakePull, map[string]any) (int, string) {
			if tries++; tries < 3 {
				return http.StatusMethodNotAllowed, "Please try again later"
			}
			return 0, ""
		}
		require.NoError(t, c.FastForward(ctx, owner, repo, "main", "b", 7))
		assert.Equal(t, 3, tries)
		assert.Equal(t, "b", f.branches["main"])

		f, c = newFakeForge(t)
		ffGraph(f)
		f.mergeHook = func(*fakePull, map[string]any) (int, string) {
			return http.StatusMethodNotAllowed, "Please try again later"
		}
		err := c.FastForward(ctx, owner, repo, "main", "b", 7)
		require.Error(t, err)
		assert.NotErrorIs(t, err, forge.ErrNotFastForward, "no verdict while the forge is checking")
		assert.Equal(t, "open", f.pulls[0].State)
	})

	t.Run("merge failed but landed", func(t *testing.T) {
		f, c := newFakeForge(t)
		ffGraph(f)
		f.mergeHook = func(pr *fakePull, body map[string]any) (int, string) {
			f.merge(pr, body)
			return http.StatusInternalServerError, "timeout"
		}
		require.NoError(t, c.FastForward(ctx, owner, repo, "main", "b", 7))
		assert.Equal(t, "b", f.branches["main"])
	})

	t.Run("branch already exists", func(t *testing.T) {
		f, c := newFakeForge(t)
		ffGraph(f)
		f.branches["train/promote-7"] = "b"
		require.NoError(t, c.FastForward(ctx, owner, repo, "main", "b", 7))
		assert.Equal(t, "b", f.branches["main"])

		f, c = newFakeForge(t)
		ffGraph(f)
		f.branches["train/promote-7"] = "a"
		err := c.FastForward(ctx, owner, repo, "main", "b", 7)
		require.ErrorContains(t, err, "train/promote-7 is at a")
		assert.NotErrorIs(t, err, forge.ErrNotFastForward)
		assert.Empty(t, f.calls(http.MethodPost, "/pulls"))
	})

	t.Run("pull request already open", func(t *testing.T) {
		f, c := newFakeForge(t)
		ffGraph(f)
		pr := &fakePull{Number: 42, State: "open"}
		pr.Head.Ref, pr.Base.Ref = "train/promote-7", "main"
		f.pulls = append(f.pulls, pr)
		require.NoError(t, c.FastForward(ctx, owner, repo, "main", "b", 7))
		assert.Empty(t, f.calls(http.MethodPost, "/pulls"))
		assert.Len(t, f.calls(http.MethodPost, "/pulls/42/merge"), 1)
	})
}

const carDiff = "diff --git a/f.txt b/f.txt\n" +
	"index 1111111..2222222 100644\n" +
	"--- a/f.txt\n" +
	"+++ b/f.txt\n" +
	"@@ -1 +1 @@\n" +
	"-old\n" +
	"+new\n"

func revertGraph(f *fakeForge) {
	f.commit("t0", "base", nil)
	f.commit("c1", "Change f", []string{"f.txt"}, "t0")
	f.commit("c2", "Other", []string{"g.txt"}, "c1")
	f.branches["train"] = "c2"
	f.diffs["c1"] = carDiff
}

func TestRevert(t *testing.T) {
	ctx := context.Background()
	car := &model.TrainCar{SHA: "c1", Parent: "t0", Title: "Change f"}

	t.Run("happy path", func(t *testing.T) {
		f, c := newFakeForge(t)
		revertGraph(f)
		sha, err := c.Revert(ctx, owner, repo, "train", car, `Revert "Change f"`)
		require.NoError(t, err)
		assert.Equal(t, f.branches["train"], sha)
		assert.Equal(t, []string{"POST /diffpatch"}, f.writes())
		assert.Equal(t, map[string]any{
			"branch": "train",
			"sha":    "c2",
			"content": "diff --git a/f.txt b/f.txt\n" +
				"index 2222222..1111111 100644\n" +
				"--- a/f.txt\n" +
				"+++ b/f.txt\n" +
				"@@ -1 +1 @@\n" +
				"-new\n" +
				"+old\n",
			"message": "Revert \"Change f\"\n\nTrain-Revert: c1",
		}, f.calls(http.MethodPost, "/diffpatch")[0].Body)
		q := f.calls(http.MethodGet, "/commits")[0].Query
		assert.Equal(t, []string{"train"}, q["sha"])
		assert.Equal(t, []string{"50"}, q["limit"])

		again, err := c.Revert(ctx, owner, repo, "train", car, `Revert "Change f"`)
		require.NoError(t, err)
		assert.Equal(t, sha, again, "a repeat finds the earlier revert by its trailer")
		assert.Len(t, f.calls(http.MethodPost, "/diffpatch"), 1)
	})

	t.Run("trailer already present", func(t *testing.T) {
		f, c := newFakeForge(t)
		revertGraph(f)
		f.commit("r1", "Revert \"Change f\"\n\nSome text.\nTrain-Revert: c1\n", nil, "c2")
		f.commit("c3", "Later", nil, "r1")
		f.branches["train"] = "c3"
		sha, err := c.Revert(ctx, owner, repo, "train", car, "Revert")
		require.NoError(t, err)
		assert.Equal(t, "r1", sha)
		assert.Empty(t, f.writes())
		assert.Empty(t, f.calls(http.MethodGet, "/git/commits/c1.diff"))
	})

	for _, status := range []int{http.StatusConflict, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		t.Run("diffpatch fails with "+http.StatusText(status), func(t *testing.T) {
			f, c := newFakeForge(t)
			revertGraph(f)
			f.diffpatchHook = func(map[string]any) (int, string) { return status, "patch does not apply" }
			_, err := c.Revert(ctx, owner, repo, "train", car, "Revert")
			assert.ErrorIs(t, err, forge.ErrRevertConflict)
			assert.Equal(t, status, statusOf(err))
		})
	}

	t.Run("not allowed is no conflict", func(t *testing.T) {
		f, c := newFakeForge(t)
		revertGraph(f)
		f.diffpatchHook = func(map[string]any) (int, string) { return http.StatusForbidden, "no write access" }
		_, err := c.Revert(ctx, owner, repo, "train", car, "Revert")
		require.Error(t, err)
		assert.NotErrorIs(t, err, forge.ErrRevertConflict)
	})

	t.Run("binary change is a conflict without a call", func(t *testing.T) {
		f, c := newFakeForge(t)
		revertGraph(f)
		f.diffs["c1"] = lines("diff --git a/i.png b/i.png", "index 1..2 100644", "Binary files a/i.png and b/i.png differ")
		_, err := c.Revert(ctx, owner, repo, "train", car, "Revert")
		assert.ErrorIs(t, err, forge.ErrRevertConflict)
		assert.Empty(t, f.writes())
	})

	t.Run("empty car", func(t *testing.T) {
		f, c := newFakeForge(t)
		revertGraph(f)
		f.diffs["c1"] = ""
		_, err := c.Revert(ctx, owner, repo, "train", car, "Revert")
		assert.ErrorIs(t, err, ErrNothingToRevert)
		assert.Empty(t, f.writes())
	})
}

func TestFreeze(t *testing.T) {
	ctx := context.Background()

	t.Run("no rule yet", func(t *testing.T) {
		f, c := newFakeForge(t)
		require.NoError(t, c.Freeze(ctx, owner, repo, "main", true))
		assert.Equal(t, []string{"POST /branch_protections", "POST /branch_protections"}, f.writes())
		posts := f.calls(http.MethodPost, "/branch_protections")
		assert.Equal(t, "woodpecker-train-freeze-main", posts[0].Body["rule_name"])
		assert.Equal(t, markerCreated, posts[0].Body["protected_file_patterns"])
		assert.Equal(t, map[string]any{
			"rule_name":                  "main",
			"branch_name":                "main",
			"enable_push":                true,
			"enable_push_whitelist":      true,
			"push_whitelist_usernames":   []any{fakeLogin},
			"push_whitelist_teams":       []any{},
			"push_whitelist_deploy_keys": false,
			"enable_merge_whitelist":     true,
			"merge_whitelist_usernames":  []any{fakeLogin},
			"merge_whitelist_teams":      []any{},
			"apply_to_admins":            true,
		}, posts[1].Body)

		require.NoError(t, c.Freeze(ctx, owner, repo, "main", true))
		assert.Len(t, f.writes(), 2, "freezing a frozen branch changes nothing")

		require.NoError(t, c.Freeze(ctx, owner, repo, "main", false))
		assert.Equal(t, []string{
			"DELETE /branch_protections/main",
			"DELETE /branch_protections/woodpecker-train-freeze-main",
		}, f.writes()[2:])
		assert.Empty(t, f.protections)

		require.NoError(t, c.Freeze(ctx, owner, repo, "main", false))
		assert.Len(t, f.writes(), 4, "unfreezing an unfrozen branch changes nothing")
	})

	t.Run("existing rule", func(t *testing.T) {
		f, c := newFakeForge(t)
		original := map[string]any{
			"rule_name":                 "main",
			"branch_name":               "main",
			"enable_push":               true,
			"enable_push_whitelist":     true,
			"push_whitelist_usernames":  []any{"alice"},
			"push_whitelist_teams":      []any{"ops"},
			"enable_merge_whitelist":    false,
			"merge_whitelist_usernames": []any{},
			"merge_whitelist_teams":     []any{},
			"apply_to_admins":           false,
			"required_approvals":        float64(2),
		}
		rule := map[string]any{}
		for k, v := range original {
			rule[k] = v
		}
		f.protections = append(f.protections, rule)

		require.NoError(t, c.Freeze(ctx, owner, repo, "main", true))
		assert.Equal(t, []string{"POST /branch_protections", "PATCH /branch_protections/main"}, f.writes())
		backup := f.protection("woodpecker-train-freeze-main")
		require.NotNil(t, backup)
		assert.Equal(t, markerExisting, backup["protected_file_patterns"])
		assert.Equal(t, []any{"alice"}, backup["push_whitelist_usernames"])
		assert.Equal(t, []any{"ops"}, backup["push_whitelist_teams"])
		assert.Equal(t, []any{fakeLogin}, rule["push_whitelist_usernames"])
		assert.Equal(t, []any{fakeLogin}, rule["merge_whitelist_usernames"])
		assert.Equal(t, true, rule["enable_merge_whitelist"])
		assert.Equal(t, true, rule["apply_to_admins"])
		assert.Equal(t, float64(2), rule["required_approvals"], "other settings stay")

		require.NoError(t, c.Freeze(ctx, owner, repo, "main", true))
		assert.Len(t, f.writes(), 2)

		require.NoError(t, c.Freeze(ctx, owner, repo, "main", false))
		assert.Equal(t, []string{
			"PATCH /branch_protections/main",
			"DELETE /branch_protections/woodpecker-train-freeze-main",
		}, f.writes()[2:])
		original["push_whitelist_deploy_keys"] = false
		assert.Equal(t, original, rule, "unfreezing restores the rule")
		assert.Len(t, f.protections, 1)

		require.NoError(t, c.Freeze(ctx, owner, repo, "main", false))
		assert.Len(t, f.writes(), 4)
	})

	t.Run("resumes after a crash between backup and freeze", func(t *testing.T) {
		f, c := newFakeForge(t)
		f.protections = append(f.protections, map[string]any{
			"rule_name": "woodpecker-train-freeze-main", "protected_file_patterns": markerCreated,
		})
		require.NoError(t, c.Freeze(ctx, owner, repo, "main", true))
		assert.Equal(t, []string{"POST /branch_protections"}, f.writes())
		assert.Equal(t, "main", f.calls(http.MethodPost, "/branch_protections")[0].Body["rule_name"])
	})
}

func TestNotify(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeForge(t)
	notice := &forge.TrainNotice{
		SHA:         "abc",
		State:       model.StatusFailure,
		Description: "Car failed",
		Link:        "https://ci.example/train/1",
		PR:          5,
		Comment:     "The train dropped this pull request.",
	}
	require.NoError(t, c.Notify(ctx, owner, repo, notice))
	assert.Equal(t, []string{"POST /statuses/abc", "POST /issues/5/comments"}, f.writes())
	assert.Equal(t, map[string]any{
		"state":       "failure",
		"context":     "train",
		"description": "Car failed",
		"target_url":  "https://ci.example/train/1",
	}, f.calls(http.MethodPost, "/statuses/abc")[0].Body)
	assert.Equal(t, map[string]any{"body": notice.Comment}, f.calls(http.MethodPost, "/issues/5/comments")[0].Body)

	f.comments[5] = append(f.comments[5], "someone else")
	require.NoError(t, c.Notify(ctx, owner, repo, notice))
	assert.Equal(t, []string{"POST /statuses/abc"}, f.writes()[2:], "the same comment is not posted twice")
	assert.Len(t, f.comments[5], 2)

	require.NoError(t, c.Notify(ctx, owner, repo, &forge.TrainNotice{PR: 5, Comment: "Merged."}))
	assert.Equal(t, []string{"POST /issues/5/comments"}, f.writes()[3:], "no status without a sha")

	require.NoError(t, c.Notify(ctx, owner, repo, &forge.TrainNotice{SHA: "abc", State: model.StatusRunning, PR: 5}))
	assert.Equal(t, []string{"POST /statuses/abc"}, f.writes()[4:], "no comment without text")
}

func TestStatusState(t *testing.T) {
	for in, want := range map[model.StatusValue]string{
		model.StatusSuccess:  "success",
		model.StatusFailure:  "failure",
		model.StatusCanceled: "failure",
		model.StatusSkipped:  "failure",
		model.StatusPending:  "pending",
		model.StatusRunning:  "pending",
		model.StatusCreated:  "pending",
		model.StatusBlocked:  "pending",
		model.StatusDeclined: "warning",
		model.StatusError:    "error",
		model.StatusKilled:   "error",
		"unknown":            "error",
	} {
		assert.Equal(t, want, statusState(in), in)
	}
}

func syncGraph(f *fakeForge) {
	f.commit("t0", "base", nil)
	f.commit("c1", "car", nil, "t0")
	f.commit("h1", "hotfix", nil, "t0")
	f.branches["main"] = "h1"
	f.branches["train"] = "c1"
}

func TestSync(t *testing.T) {
	ctx := context.Background()

	t.Run("nothing to do", func(t *testing.T) {
		f, c := newFakeForge(t)
		syncGraph(f)
		f.branches["main"] = "t0"
		sha, err := c.Sync(ctx, owner, repo, "main", "train", "Sync main into train")
		require.NoError(t, err)
		assert.Empty(t, sha)
		assert.Empty(t, f.writes())
	})

	t.Run("merge", func(t *testing.T) {
		f, c := newFakeForge(t)
		syncGraph(f)
		sha, err := c.Sync(ctx, owner, repo, "main", "train", "Sync main into train")
		require.NoError(t, err)
		assert.Equal(t, f.branches["train"], sha)
		assert.Equal(t, []string{"c1", "h1"}, f.commits[sha].parents)
		assert.Equal(t, "h1", f.branches["main"], "the source branch stays")
		assert.Equal(t, []string{"POST /pulls", "POST /pulls/100/merge"}, f.writes())
		assert.Equal(t, map[string]any{"head": "main", "base": "train", "title": "Sync main into train"},
			f.calls(http.MethodPost, "/pulls")[0].Body)
		assert.Equal(t, map[string]any{
			"Do":                        "merge",
			"MergeTitleField":           "Sync main into train",
			"head_commit_id":            "h1",
			"delete_branch_after_merge": false,
		}, f.calls(http.MethodPost, "/pulls/100/merge")[0].Body)

		again, err := c.Sync(ctx, owner, repo, "main", "train", "Sync main into train")
		require.NoError(t, err)
		assert.Empty(t, again, "a repeat after the merge has nothing to do")
		assert.Len(t, f.writes(), 2)
	})

	t.Run("conflict", func(t *testing.T) {
		f, c := newFakeForge(t)
		syncGraph(f)
		f.mergeHook = func(*fakePull, map[string]any) (int, string) {
			return http.StatusConflict, "merge conflict in f.txt"
		}
		_, err := c.Sync(ctx, owner, repo, "main", "train", "Sync")
		assert.ErrorIs(t, err, forge.ErrSyncConflict)
		assert.Equal(t, "c1", f.branches["train"])
	})

	t.Run("not mergeable", func(t *testing.T) {
		f, c := newFakeForge(t)
		syncGraph(f)
		f.mergeHook = func(*fakePull, map[string]any) (int, string) {
			return http.StatusMethodNotAllowed, "Please try again later"
		}
		_, err := c.Sync(ctx, owner, repo, "main", "train", "Sync")
		require.Error(t, err)
		assert.NotErrorIs(t, err, forge.ErrSyncConflict, "still checking is no conflict")
	})

	t.Run("merge failed but landed", func(t *testing.T) {
		f, c := newFakeForge(t)
		syncGraph(f)
		f.mergeHook = func(pr *fakePull, body map[string]any) (int, string) {
			f.merge(pr, body)
			return http.StatusBadGateway, "proxy timeout"
		}
		sha, err := c.Sync(ctx, owner, repo, "main", "train", "Sync")
		require.NoError(t, err)
		assert.Equal(t, f.branches["train"], sha)
	})
}

func TestClientErrors(t *testing.T) {
	f, c := newFakeForge(t)
	c.Token = "wrong"
	_, err := c.Cars(context.Background(), owner, repo, "train", "main")
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusUnauthorized, apiErr.Status)
	assert.Equal(t, "/repos/owner/repo/branches/train", apiErr.Path)
	assert.Contains(t, err.Error(), "token required")
	assert.Len(t, f.requests, 1)
}
