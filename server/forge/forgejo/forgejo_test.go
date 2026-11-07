// Copyright 2024 Woodpecker Authors
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

package forgejo

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"go.woodpecker-ci.org/woodpecker/v3/server/forge/forgejo/fixtures"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
	store_mocks "go.woodpecker-ci.org/woodpecker/v3/server/store/mocks"
)

func TestNew(t *testing.T) {
	forge, _ := New(1, Opts{
		URL:        "http://localhost:8080",
		SkipVerify: true,
	})

	f, _ := forge.(*Forgejo)
	assert.Equal(t, "http://localhost:8080", f.url)
	assert.True(t, f.skipVerify)
}

func Test_forgejo(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := httptest.NewServer(fixtures.Handler())
	defer s.Close()
	c, _ := New(1, Opts{
		URL:        s.URL,
		SkipVerify: true,
	})

	mockStore := store_mocks.NewMockStore(t)
	ctx := store.InjectToContext(t.Context(), mockStore)

	t.Run("netrc with user token", func(t *testing.T) {
		forge, _ := New(1, Opts{})
		netrc, _ := forge.Netrc(fakeUser, fakeRepo)
		assert.Equal(t, "forgejo.org", netrc.Machine)
		assert.Equal(t, fakeUser.Login, netrc.Login)
		assert.Equal(t, fakeUser.AccessToken, netrc.Password)
		assert.Equal(t, model.ForgeTypeForgejo, netrc.Type)
	})
	t.Run("netrc with machine account", func(t *testing.T) {
		forge, _ := New(1, Opts{})
		netrc, _ := forge.Netrc(nil, fakeRepo)
		assert.Equal(t, "forgejo.org", netrc.Machine)
		assert.Empty(t, netrc.Login)
		assert.Empty(t, netrc.Password)
	})

	t.Run("repository details", func(t *testing.T) {
		repo, err := c.Repo(ctx, fakeUser, fakeRepo.ForgeRemoteID, fakeRepo.Owner, fakeRepo.Name)
		assert.NoError(t, err)
		assert.Equal(t, fakeRepo.Owner, repo.Owner)
		assert.Equal(t, fakeRepo.Name, repo.Name)
		assert.Equal(t, fakeRepo.Owner+"/"+fakeRepo.Name, repo.FullName)
		assert.True(t, repo.IsSCMPrivate)
		assert.Equal(t, "http://localhost/test_name/repo_name.git", repo.Clone)
		assert.Equal(t, "http://localhost/test_name/repo_name", repo.ForgeURL)
	})
	t.Run("repo not found", func(t *testing.T) {
		_, err := c.Repo(ctx, fakeUser, "0", fakeRepoNotFound.Owner, fakeRepoNotFound.Name)
		assert.Error(t, err)
	})

	t.Run("repository list", func(t *testing.T) {
		repos, err := c.Repos(ctx, fakeUser, &model.ListOptions{Page: 1, PerPage: 10})
		assert.NoError(t, err)
		assert.Equal(t, fakeRepo.ForgeRemoteID, repos[0].ForgeRemoteID)
		assert.Equal(t, fakeRepo.Owner, repos[0].Owner)
		assert.Equal(t, fakeRepo.Name, repos[0].Name)
		assert.Equal(t, fakeRepo.Owner+"/"+fakeRepo.Name, repos[0].FullName)
	})
	t.Run("not found error", func(t *testing.T) {
		_, err := c.Repos(ctx, fakeUserNoRepos, &model.ListOptions{Page: 1, PerPage: 10})
		assert.Error(t, err)
	})

	t.Run("pull request list", func(t *testing.T) {
		prs, err := c.PullRequests(ctx, fakeUser, fakeRepo, &model.ListOptions{Page: 1, PerPage: 10})
		assert.NoError(t, err)
		assert.Len(t, prs, 1)
		assert.Equal(t, "add feature X", prs[0].Title)
	})
	t.Run("pull request list for repo without commits", func(t *testing.T) {
		// Forgejo answers with 404 for repos without commits; that must be treated as an empty list, not an error
		prs, err := c.PullRequests(ctx, fakeUser, fakeRepoEmpty, &model.ListOptions{Page: 1, PerPage: 10})
		assert.NoError(t, err)
		assert.Empty(t, prs)
	})

	t.Run("register repository", func(t *testing.T) {
		err := c.Activate(ctx, fakeUser, fakeRepo, "http://localhost")
		assert.NoError(t, err)
	})

	t.Run("remove hooks", func(t *testing.T) {
		err := c.Deactivate(ctx, fakeUser, fakeRepo, "http://localhost")
		assert.NoError(t, err)
	})

	t.Run("repository file", func(t *testing.T) {
		raw, err := c.File(ctx, fakeUser, fakeRepo, fakePipeline, ".woodpecker.yml")
		assert.NoError(t, err)
		assert.Equal(t, "{ platform: linux/amd64 }", string(raw))
	})

	t.Run("pipeline status", func(t *testing.T) {
		err := c.Status(ctx, fakeUser, fakeRepo, fakePipeline, fakeWorkflow)
		assert.NoError(t, err)
	})

	t.Run("PR hook", func(t *testing.T) {
		buf := bytes.NewBufferString(fixtures.HookPullRequest)
		req, _ := http.NewRequest(http.MethodPost, "/hook", buf)
		req.Header = http.Header{}
		req.Header.Set(hookEvent, hookPullRequest)
		mockStore.On("GetRepoNameFallback", mock.Anything, mock.Anything, mock.Anything).Return(fakeRepo, nil)
		mockStore.On("GetUser", mock.Anything).Return(fakeUser, nil)
		r, b, err := c.Hook(ctx, req)
		assert.NotNil(t, r)
		assert.NotNil(t, b)
		assert.NoError(t, err)
		assert.Equal(t, model.EventPull, b.Event)
		assert.Equal(t, []string{"README.md"}, b.ChangedFiles)
	})
}

func Test_forgejoPushChangedFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := httptest.NewServer(fixtures.Handler())
	defer s.Close()
	f, _ := New(1, Opts{
		URL:        s.URL,
		SkipVerify: true,
	})
	c, _ := f.(*Forgejo)

	mockStore := store_mocks.NewMockStore(t)
	mockStore.On("GetRepoNameFallback", mock.Anything, mock.Anything, mock.Anything).Return(fakeRepo, nil).Maybe()
	mockStore.On("GetUser", mock.Anything).Return(fakeUser, nil).Maybe()
	ctx := store.InjectToContext(t.Context(), mockStore)

	t.Run("merged pull request only reports its own changes", func(t *testing.T) {
		// the pushed commits include mergeMainIntoFeature, whose files were changed by main, not the pull request
		files, err := c.getChangedFilesForPush(ctx, fakeRepo, "main1", "prMerge")
		assert.NoError(t, err)
		assert.Equal(t, []string{"service/a.go", "service/b.go"}, files)
	})
	t.Run("multiple first-parent commits are combined", func(t *testing.T) {
		files, err := c.getChangedFilesForPush(ctx, fakeRepo, "main1", "main2")
		assert.NoError(t, err)
		assert.Equal(t, []string{"service/a.go", "docs/c.md", "service/b.go"}, files)
	})
	t.Run("pushed branch whose first parent is the old head", func(t *testing.T) {
		files, err := c.getChangedFilesForPush(ctx, fakeRepo, "feature1", "feature2")
		assert.NoError(t, err)
		assert.Equal(t, []string{"service/b.go", "other/main.go"}, files)
	})
	t.Run("no commits pushed", func(t *testing.T) {
		files, err := c.getChangedFilesForPush(ctx, fakeRepo, "main1", "main1")
		assert.NoError(t, err)
		assert.Empty(t, files)
	})
	t.Run("new branch", func(t *testing.T) {
		_, err := c.getChangedFilesForPush(ctx, fakeRepo, "0000000000000000000000000000000000000000", "prMerge")
		assert.Error(t, err)
	})
	t.Run("before is not a first-parent ancestor", func(t *testing.T) {
		// e.g. a force push; the walk reaches the root commit without passing before
		_, err := c.getChangedFilesForPush(ctx, fakeRepo, "feature1", "prMerge")
		assert.Error(t, err)
	})
	t.Run("walk is capped", func(t *testing.T) {
		files, err := c.getChangedFilesForPush(ctx, fakeRepo, "chain0", fmt.Sprintf("chain%d", maxPushFirstParentCommits))
		assert.NoError(t, err)
		assert.Len(t, files, maxPushFirstParentCommits)

		_, err = c.getChangedFilesForPush(ctx, fakeRepo, "chain0", fmt.Sprintf("chain%d", maxPushFirstParentCommits+1))
		assert.Error(t, err)
	})
	t.Run("unknown commit", func(t *testing.T) {
		_, err := c.getChangedFilesForPush(ctx, fakeRepo, "main1", "unknown")
		assert.Error(t, err)
	})
	t.Run("no store in context", func(t *testing.T) {
		_, err := c.getChangedFilesForPush(t.Context(), fakeRepo, "main1", "prMerge")
		assert.Error(t, err)
	})

	pushHook := func(t *testing.T, payload string) *model.Pipeline {
		req, _ := http.NewRequest(http.MethodPost, "/hook", bytes.NewBufferString(payload))
		req.Header = http.Header{}
		req.Header.Set(hookEvent, hookPush)
		r, b, err := c.Hook(ctx, req)
		assert.NoError(t, err)
		assert.NotNil(t, r)
		if assert.NotNil(t, b) {
			assert.Equal(t, model.EventPush, b.Event)
		}
		return b
	}
	t.Run("push hook uses first-parent changes", func(t *testing.T) {
		// the payload lists CHANGELOG.md and app/controller/application.rb
		b := pushHook(t, fixtures.HookPush)
		assert.Equal(t, []string{"CHANGELOG.md"}, b.ChangedFiles)
	})
	t.Run("push hook falls back to payload for new branch", func(t *testing.T) {
		b := pushHook(t, fixtures.HookPushBranch)
		assert.Equal(t, []string{".woodpecker/.check.yml"}, b.ChangedFiles)
	})
	t.Run("push hook falls back to payload on API error", func(t *testing.T) {
		// the fake forge does not know the commits of this fixture
		b := pushHook(t, fixtures.HookPushMulti)
		assert.Equal(t, []string{"aaa", "aa"}, b.ChangedFiles)
	})
}

var (
	fakeUser = &model.User{
		Login:       "someuser",
		AccessToken: "cfcd2084",
	}

	fakeUserNoRepos = &model.User{
		Login:       "someuser",
		AccessToken: "repos_not_found",
	}

	fakeRepo = &model.Repo{
		Clone:         "http://forgejo.org/test_name/repo_name.git",
		ForgeRemoteID: "5",
		Owner:         "test_name",
		Name:          "repo_name",
		FullName:      "test_name/repo_name",
	}

	fakeRepoNotFound = &model.Repo{
		Owner:    "test_name",
		Name:     "repo_not_found",
		FullName: "test_name/repo_not_found",
	}

	fakeRepoEmpty = &model.Repo{
		Owner:    "test_name",
		Name:     "repo_without_commits",
		FullName: "test_name/repo_without_commits",
	}

	fakePipeline = &model.Pipeline{
		Commit: "9ecad50",
	}

	fakeWorkflow = &model.Workflow{
		Name:  "test",
		State: model.StatusSuccess,
	}
)
