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
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestDirLoadsFilesInParallel(t *testing.T) {
	const fileCount = 25
	listing := make([]string, 0, fileCount)
	for i := range fileCount {
		listing = append(listing, fmt.Sprintf(`{"type":"file","path":".woodpecker/%02d.yaml"}`, i))
	}

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
		"/api/v1/repos/owner/repo/contents/.woodpecker": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("[" + strings.Join(listing, ",") + "]"))
		},
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
		name := fmt.Sprintf(".woodpecker/%02d.yaml", i)
		assert.Equal(t, &forge_types.FileMeta{Name: name, Data: []byte("steps: " + name)}, meta)
	}
	assert.EqualValues(t, maxParallelFileFetches, maxInFlight.Load(), "files loaded at once")
	assert.EqualValues(t, 1, versionRequests.Load(), "server version checks")
}

func TestDirFailsIfAFileFails(t *testing.T) {
	g := newDirTestForge(t, map[string]http.HandlerFunc{
		"/api/v1/repos/owner/repo/contents/.woodpecker": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[` +
				`{"type":"file","path":".woodpecker/a.yaml"},` +
				`{"type":"file","path":".woodpecker/b.yaml"},` +
				`{"type":"file","path":".woodpecker/c.yaml"}]`))
		},
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
