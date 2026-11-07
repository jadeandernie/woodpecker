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

package fixtures

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Handler returns an http.Handler that is capable of handling a variety of mock
// Forgejo requests and returning mock responses.
func Handler() http.Handler {
	gin.SetMode(gin.TestMode)

	e := gin.New()
	e.GET("/api/v1/repos/:owner/:name", getRepo)
	e.GET("/api/v1/repositories/:id", getRepoByID)
	e.GET("/api/v1/repos/:owner/:name/raw/:file", getRepoFile)
	e.POST("/api/v1/repos/:owner/:name/hooks", createRepoHook)
	e.GET("/api/v1/repos/:owner/:name/hooks", listRepoHooks)
	e.DELETE("/api/v1/repos/:owner/:name/hooks/:id", deleteRepoHook)
	e.POST("/api/v1/repos/:owner/:name/statuses/:commit", createRepoCommitStatus)
	e.GET("/api/v1/repos/:owner/:name/pulls", listRepoPulls)
	e.GET("/api/v1/repos/:owner/:name/pulls/:index/files", getPRFiles)
	e.GET("/api/v1/repos/:owner/:name/git/commits/:sha", getCommit)
	e.GET("/api/v1/user/repos", getUserRepos)
	e.GET("/api/v1/version", getVersion)

	return e
}

func listRepoHooks(c *gin.Context) {
	page := c.Query("page")
	if page != "" && page != "1" {
		c.String(http.StatusOK, "[]")
	} else {
		c.String(http.StatusOK, listRepoHookPayloads)
	}
}

func getRepo(c *gin.Context) {
	switch c.Param("name") {
	case "repo_not_found":
		c.String(http.StatusNotFound, "")
	default:
		c.String(http.StatusOK, repoPayload)
	}
}

func getRepoByID(c *gin.Context) {
	switch c.Param("id") {
	case "repo_not_found":
		c.String(http.StatusNotFound, "")
	default:
		c.String(http.StatusOK, repoPayload)
	}
}

func createRepoCommitStatus(c *gin.Context) {
	if c.Param("commit") == "v1.0.0" || c.Param("commit") == "9ecad50" {
		c.String(http.StatusOK, repoPayload)
	}
	c.String(http.StatusNotFound, "")
}

func getRepoFile(c *gin.Context) {
	file := c.Param("file")
	ref := c.Query("ref")

	if file == "file_not_found" {
		c.String(http.StatusNotFound, "")
	}
	if ref == "v1.0.0" || ref == "9ecad50" {
		c.String(http.StatusOK, repoFilePayload)
	}
	c.String(http.StatusNotFound, "")
}

func createRepoHook(c *gin.Context) {
	in := struct {
		Type string `json:"type"`
		Conf struct {
			Type string `json:"content_type"`
			URL  string `json:"url"`
		} `json:"config"`
	}{}
	_ = c.BindJSON(&in)
	if in.Type != "forgejo" ||
		in.Conf.Type != "json" ||
		in.Conf.URL != "http://localhost" {
		c.String(http.StatusInternalServerError, "")
		return
	}

	c.String(http.StatusOK, "{}")
}

func deleteRepoHook(c *gin.Context) {
	c.String(http.StatusOK, "{}")
}

func getUserRepos(c *gin.Context) {
	switch c.Request.Header.Get("Authorization") {
	case "token repos_not_found":
		c.String(http.StatusNotFound, "")
	default:
		page := c.Query("page")
		if page != "" && page != "1" {
			c.String(http.StatusOK, "[]")
		} else {
			c.String(http.StatusOK, userRepoPayload)
		}
	}
}

func getVersion(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]any{"version": "1.18.0"})
}

func getPRFiles(c *gin.Context) {
	page := c.Query("page")
	if page == "1" {
		c.String(http.StatusOK, prFilesPayload)
	} else {
		c.String(http.StatusOK, "[]")
	}
}

// fakeCommit is a commit of the fake commit graph served by getCommit.
type fakeCommit struct {
	parents []string
	files   []string
}

// fakeCommits is a commit graph where a pull request branch merged main into
// itself before being merged into main:
//
//	main:    base --- main1 ------------------------ prMerge
//	           \          \                         /
//	feature:    feature1 --- mergeMainIntoFeature --- feature2
//
// The first-parent diff of mergeMainIntoFeature contains what main1 changed,
// the first-parent diff of prMerge contains what the pull request changed.
// It also holds the parent of the HookPush fixture's commit.
var fakeCommits = map[string]fakeCommit{
	"base":                 {files: []string{"README.md"}},
	"main1":                {parents: []string{"base"}, files: []string{"other/main.go"}},
	"feature1":             {parents: []string{"base"}, files: []string{"service/a.go"}},
	"mergeMainIntoFeature": {parents: []string{"feature1", "main1"}, files: []string{"other/main.go"}},
	"feature2":             {parents: []string{"mergeMainIntoFeature"}, files: []string{"service/b.go"}},
	"prMerge":              {parents: []string{"main1", "feature2"}, files: []string{"service/a.go", "service/b.go"}},
	"main2":                {parents: []string{"prMerge"}, files: []string{"service/a.go", "docs/c.md"}},

	"ef98532add3b2feb7a137426bba1248724367df5": {
		parents: []string{"4b2626259b5a97b6b4eab5e6cca66adb986b672b"},
		files:   []string{"CHANGELOG.md"},
	},
}

// getCommit serves the commits of fakeCommits, and a linear chain of commits
// "chain<N>" whose parent is "chain<N-1>".
func getCommit(c *gin.Context) {
	sha := c.Param("sha")
	commit, ok := fakeCommits[sha]
	if n, err := strconv.Atoi(strings.TrimPrefix(sha, "chain")); err == nil && n > 0 {
		commit, ok = fakeCommit{parents: []string{fmt.Sprintf("chain%d", n-1)}, files: []string{fmt.Sprintf("file%d", n)}}, true
	}
	if !ok {
		c.String(http.StatusNotFound, "")
		return
	}

	parents := make([]gin.H, 0, len(commit.parents))
	for _, parent := range commit.parents {
		parents = append(parents, gin.H{"sha": parent})
	}
	files := make([]gin.H, 0, len(commit.files))
	for _, file := range commit.files {
		files = append(files, gin.H{"filename": file})
	}
	c.JSON(http.StatusOK, gin.H{"sha": sha, "parents": parents, "files": files})
}

func listRepoPulls(c *gin.Context) {
	// Repositories without commits answer with an empty list and status code 404
	if c.Param("name") == "repo_without_commits" {
		c.String(http.StatusNotFound, "[]")
		return
	}
	c.String(http.StatusOK, listRepoPullsPayload)
}

const listRepoHookPayloads = `
[
	{
		"id": 1,
		"type": "forgejo",
		"config": {
			"content_type": "json",
			"url": "http:\/\/localhost\/hook?access_token=1234567890"
		}
	}
]
`

const repoPayload = `
{
	"id": 5,
	"owner": {
		"login": "test_name",
		"email": "octocat@github.com",
		"avatar_url": "https:\/\/secure.gravatar.com\/avatar\/8c58a0be77ee441bb8f8595b7f1b4e87"
	},
	"full_name": "test_name\/repo_name",
	"private": true,
	"html_url": "http:\/\/localhost\/test_name\/repo_name",
	"clone_url": "http:\/\/localhost\/test_name\/repo_name.git",
	"permissions": {
		"admin": true,
		"push": true,
		"pull": true
	}
}
`

const repoFilePayload = `{ platform: linux/amd64 }`

const listRepoPullsPayload = `
[
	{
		"id": 1,
		"number": 1,
		"title": "add feature X",
		"state": "open"
	}
]
`

const userRepoPayload = `
[
	{
		"id": 5,
		"owner": {
			"login": "test_name",
			"email": "octocat@github.com",
			"avatar_url": "https:\/\/secure.gravatar.com\/avatar\/8c58a0be77ee441bb8f8595b7f1b4e87"
		},
		"full_name": "test_name\/repo_name",
		"private": true,
		"html_url": "http:\/\/localhost\/test_name\/repo_name",
		"clone_url": "http:\/\/localhost\/test_name\/repo_name.git",
		"permissions": {
			"admin": true,
			"push": true,
			"pull": true
		}
	}
]
`

const prFilesPayload = `
[
	{
		"filename": "README.md",
		"status": "changed",
		"additions": 2,
		"deletions": 0,
		"changes": 2,
		"html_url": "http://localhost/username/repo/src/commit/e79e4b0e8d9dd6f72b70e776c3317db7c19ca0fd/README.md",
		"contents_url": "http://localhost:3000/api/v1/repos/username/repo/contents/README.md?ref=e79e4b0e8d9dd6f72b70e776c3317db7c19ca0fd",
		"raw_url": "http://localhost/username/repo/raw/commit/e79e4b0e8d9dd6f72b70e776c3317db7c19ca0fd/README.md"
	}
]
`
