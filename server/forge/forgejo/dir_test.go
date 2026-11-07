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

package forgejo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forge_types "go.woodpecker-ci.org/woodpecker/v3/server/forge/types"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// newDirTestForge returns a forge served by a test server with the given
// routes, answering the version request unless the routes do.
func newDirTestForge(t *testing.T, routes map[string]http.HandlerFunc) *Forgejo {
	t.Helper()
	mux := http.NewServeMux()
	if _, ok := routes["/api/v1/version"]; !ok {
		mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"version":"7.0.2"}`))
		})
	}
	// the contents listing is slow on large folders, so Dir must never use it
	mux.HandleFunc("/api/v1/repos/owner/repo/contents/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("contents API called: %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	for route, handler := range routes {
		mux.HandleFunc(route, handler)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	f, err := New(1, Opts{URL: server.URL})
	require.NoError(t, err)
	g, ok := f.(*Forgejo)
	require.True(t, ok)
	return g
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// treeRoute serves the git trees API for the given trees, keyed by ref, in
// pages of pageSize entries and, like Forgejo, reports every page as
// truncated and a missing tree as a bad request.
func treeRoute(t *testing.T, trees map[string][]treeEntry, pageSize int) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/owner/repo/git/trees/")
		entries, ok := trees[ref]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"sha not found [` + ref + `]"}`))
			return
		}
		assert.Equal(t, "0", r.URL.Query().Get("recursive"), "recursive listing of %s", ref)
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		require.NoError(t, err)
		from := min((page-1)*pageSize, len(entries))
		to := min(from+pageSize, len(entries))
		body, err := json.Marshal(map[string]any{
			"sha":         ref,
			"tree":        entries[from:to],
			"truncated":   true,
			"page":        page,
			"total_count": len(entries),
		})
		require.NoError(t, err)
		_, _ = w.Write(body)
	}
}

func blob(name string) treeEntry {
	return treeEntry{Path: name, Mode: "100644", Type: "blob", SHA: "sha-" + name}
}

func TestDirLoadsFilesInParallel(t *testing.T) {
	const fileCount = 214
	entries := make([]treeEntry, 0, fileCount+3)
	for i := range fileCount {
		entries = append(entries, blob(fmt.Sprintf("%03d.yaml", i)))
	}
	// sub folders, links and submodules are no pipeline files
	entries = append(entries,
		treeEntry{Path: "sub", Mode: "040000", Type: "tree", SHA: "sha-sub"},
		treeEntry{Path: "zlink.yaml", Mode: "120000", Type: "blob", SHA: "sha-link"},
		treeEntry{Path: "zmodule", Mode: "160000", Type: "commit", SHA: "sha-module"},
	)

	// the first files wait for each other, so Dir only gets past them
	// quickly if it loads as many files at once as it may
	allowed := make(chan struct{})
	release := sync.OnceFunc(func() { close(allowed) })
	timer := time.AfterFunc(5*time.Second, release)
	t.Cleanup(func() { timer.Stop() })

	var versionRequests, arrived, inFlight, maxInFlight atomic.Int32
	g := newDirTestForge(t, map[string]http.HandlerFunc{
		"/api/v1/version": func(w http.ResponseWriter, _ *http.Request) {
			versionRequests.Add(1)
			_, _ = w.Write([]byte(`{"version":"7.0.2"}`))
		},
		"/api/v1/repos/owner/repo/git/trees/": treeRoute(t, map[string][]treeEntry{"abc:.woodpecker": entries}, 100),
		"/api/v1/repos/owner/repo/raw/": func(w http.ResponseWriter, r *http.Request) {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for m := maxInFlight.Load(); n > m && !maxInFlight.CompareAndSwap(m, n); m = maxInFlight.Load() {
			}
			if arrived.Add(1) == maxParallelFileFetches {
				release()
			}
			<-allowed

			name := strings.TrimPrefix(r.URL.Path, "/api/v1/repos/owner/repo/raw/")
			_, _ = w.Write([]byte("steps: " + name))
		},
	})
	user := &model.User{AccessToken: "token"}
	repo := &model.Repo{Owner: "owner", Name: "repo"}
	pipeline := &model.Pipeline{Commit: "abc"}

	metas, err := g.Dir(t.Context(), user, repo, pipeline, ".woodpecker")
	require.NoError(t, err)

	// files keep the order of the listing, whatever order they were loaded in
	require.Len(t, metas, fileCount)
	for i, meta := range metas {
		name := fmt.Sprintf(".woodpecker/%03d.yaml", i)
		assert.Equal(t, &forge_types.FileMeta{Name: name, Data: []byte("steps: " + name)}, meta)
	}
	assert.EqualValues(t, maxParallelFileFetches, maxInFlight.Load(), "files loaded at once")
	assert.EqualValues(t, 1, versionRequests.Load(), "server version checks")
}

func TestDirWalksNestedFolders(t *testing.T) {
	g := newDirTestForge(t, map[string]http.HandlerFunc{
		"/api/v1/repos/owner/repo/git/trees/": treeRoute(t, map[string][]treeEntry{
			"abc:ci":        {blob("a.yaml"), {Path: "pipelines", Mode: "040000", Type: "tree", SHA: "sha-pipelines"}},
			"sha-pipelines": {blob("b.yaml"), blob("c.yml")},
		}, 1),
		"/api/v1/repos/owner/repo/raw/": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.TrimPrefix(r.URL.Path, "/api/v1/repos/owner/repo/raw/")))
		},
	})

	metas, err := g.Dir(t.Context(), &model.User{}, &model.Repo{Owner: "owner", Name: "repo"}, &model.Pipeline{Commit: "abc"}, "ci/pipelines")
	require.NoError(t, err)
	assert.Equal(t, []*forge_types.FileMeta{
		{Name: "ci/pipelines/b.yaml", Data: []byte("ci/pipelines/b.yaml")},
		{Name: "ci/pipelines/c.yml", Data: []byte("ci/pipelines/c.yml")},
	}, metas)
}

func TestDirMissingFolderIsNotFound(t *testing.T) {
	for name, path := range map[string]string{"top": ".woodpecker", "nested": "ci/pipelines"} {
		t.Run(name, func(t *testing.T) {
			g := newDirTestForge(t, map[string]http.HandlerFunc{
				"/api/v1/repos/owner/repo/git/trees/": treeRoute(t, map[string][]treeEntry{"abc:ci": {blob("a.yaml")}}, 100),
			})

			metas, err := g.Dir(t.Context(), &model.User{}, &model.Repo{Owner: "owner", Name: "repo"}, &model.Pipeline{Commit: "abc"}, path)
			assert.Nil(t, metas)
			assert.ErrorIs(t, err, &forge_types.ErrConfigNotFound{})
		})
	}
}

func TestDirFailsIfTheListingIsShort(t *testing.T) {
	g := newDirTestForge(t, map[string]http.HandlerFunc{
		"/api/v1/repos/owner/repo/git/trees/": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"tree":[],"truncated":true,"page":2,"total_count":3}`))
		},
		"/api/v1/repos/owner/repo/raw/": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("steps: ok"))
		},
	})

	metas, err := g.Dir(t.Context(), &model.User{}, &model.Repo{Owner: "owner", Name: "repo"}, &model.Pipeline{Commit: "abc"}, ".woodpecker")
	assert.Nil(t, metas)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, &forge_types.ErrConfigNotFound{})
}

func TestDirFailsIfAFileFails(t *testing.T) {
	g := newDirTestForge(t, map[string]http.HandlerFunc{
		"/api/v1/repos/owner/repo/git/trees/": treeRoute(t, map[string][]treeEntry{
			"abc:.woodpecker": {blob("a.yaml"), blob("b.yaml"), blob("c.yaml")},
		}, 100),
		"/api/v1/repos/owner/repo/raw/": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/repos/owner/repo/raw/.woodpecker/b.yaml" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte("steps: ok"))
		},
	})

	metas, err := g.Dir(t.Context(), &model.User{}, &model.Repo{Owner: "owner", Name: "repo"}, &model.Pipeline{Commit: "abc"}, ".woodpecker")
	assert.Nil(t, metas)
	assert.ErrorContains(t, err, "multi-pipeline cannot get .woodpecker/b.yaml")
	assert.NotErrorIs(t, err, &forge_types.ErrConfigNotFound{})
}
