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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeToken = "secret"
	fakeLogin = "train-bot"
	repoBase  = "/api/v1/repos/owner/repo"
)

type fakeCommit struct {
	sha     string
	msg     string
	parents []string
	files   []string
	seq     int
}

type fakePull struct {
	Number         int64  `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Head           struct {
		Ref    string `json:"ref"`
		RepoID int64  `json:"repo_id"`
	} `json:"head"`
	Base struct {
		Ref    string `json:"ref"`
		RepoID int64  `json:"repo_id"`
	} `json:"base"`
}

type recorded struct {
	Method, Path string
	Query        map[string][]string
	Body         map[string]any
}

// fakeForge is an in-memory Gitea API for one repo, owner/repo, with just
// enough git to answer the train's calls.
type fakeForge struct {
	t           *testing.T
	mu          sync.Mutex
	seq         int
	commits     map[string]*fakeCommit
	branches    map[string]string
	diffs       map[string]string
	mergedPR    map[string]int64
	pulls       []*fakePull
	protections []map[string]any
	statuses    []map[string]any
	comments    map[int64][]string
	requests    []recorded

	// omitFiles leaves the files out of compare results, like a forge that
	// does not list them.
	omitFiles bool

	// mergeHook, if set, answers a merge instead of the fake's git.
	mergeHook func(pr *fakePull, body map[string]any) (int, string)
	// diffpatchHook, if set, answers a diffpatch call.
	diffpatchHook func(body map[string]any) (int, string)
}

func newFakeForge(t *testing.T) (*fakeForge, *Client) {
	f := &fakeForge{
		t:        t,
		commits:  map[string]*fakeCommit{},
		branches: map[string]string{},
		diffs:    map[string]string{},
		mergedPR: map[string]int64{},
		comments: map[int64][]string{},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &Client{BaseURL: srv.URL + "/", Token: fakeToken, HTTPClient: srv.Client(), RetryDelays: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}}
}

func (f *fakeForge) commit(sha, msg string, files []string, parents ...string) {
	f.seq++
	f.commits[sha] = &fakeCommit{sha: sha, msg: msg, parents: parents, files: files, seq: f.seq}
}

func (f *fakeForge) resolve(ref string) (string, bool) {
	if sha, ok := f.branches[ref]; ok {
		return sha, true
	}
	_, ok := f.commits[ref]
	return ref, ok
}

func (f *fakeForge) reach(sha string) map[string]bool {
	seen := map[string]bool{}
	stack := []string{sha}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[s] || f.commits[s] == nil {
			continue
		}
		seen[s] = true
		stack = append(stack, f.commits[s].parents...)
	}
	return seen
}

// between returns the commits head has that base lacks, newest first.
func (f *fakeForge) between(base, head string) []*fakeCommit {
	baseSet := f.reach(base)
	var out []*fakeCommit
	for s := range f.reach(head) {
		if !baseSet[s] {
			out = append(out, f.commits[s])
		}
	}
	slices.SortFunc(out, func(a, b *fakeCommit) int { return b.seq - a.seq })
	return out
}

func apiCommit(c *fakeCommit) map[string]any {
	parents := []map[string]string{}
	for _, p := range c.parents {
		parents = append(parents, map[string]string{"sha": p})
	}
	return map[string]any{"sha": c.sha, "commit": map[string]string{"message": c.msg}, "parents": parents}
}

// calls returns the recorded requests with the given method and path suffix
// below the repo.
func (f *fakeForge) calls(method, suffix string) []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recorded
	for _, r := range f.requests {
		if r.Method == method && (r.Path == repoBase+suffix || (suffix == "" && !strings.HasPrefix(r.Path, repoBase))) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeForge) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if r.Method != http.MethodGet {
			out = append(out, r.Method+" "+strings.TrimPrefix(r.Path, repoBase))
		}
	}
	return out
}

var (
	reCompare  = regexp.MustCompile(`^/compare/(.+)\.\.\.(.+)$`)
	reDiff     = regexp.MustCompile(`^/git/commits/([^/]+)\.diff$`)
	reCommit   = regexp.MustCompile(`^/git/commits/([^/]+)$`)
	rePROfSHA  = regexp.MustCompile(`^/commits/([^/]+)/pull$`)
	rePull     = regexp.MustCompile(`^/pulls/(\d+)$`)
	reMerge    = regexp.MustCompile(`^/pulls/(\d+)/merge$`)
	reStatus   = regexp.MustCompile(`^/statuses/([^/]+)$`)
	reComments = regexp.MustCompile(`^/issues/(\d+)/comments$`)
	reProtect  = regexp.MustCompile(`^/branch_protections/(.+)$`)
)

func (f *fakeForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			f.t.Errorf("%s %s: bad JSON body: %v", r.Method, r.URL.Path, err)
		}
	}
	f.requests = append(f.requests, recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body})
	if r.Header.Get("Authorization") != "token "+fakeToken {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "token required"})
		return
	}
	if r.URL.Path == "/api/v1/user" {
		writeJSON(w, http.StatusOK, map[string]string{"login": fakeLogin})
		return
	}
	p, ok := strings.CutPrefix(r.URL.Path, repoBase)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such repo"})
		return
	}
	status, resp := f.route(r.Method, p, r.URL.Query(), body)
	if s, ok := resp.(string); ok {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, s)
		return
	}
	writeJSON(w, status, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

var notFound = map[string]string{"message": "The target couldn't be found."}

func (f *fakeForge) route(method, p string, q map[string][]string, body map[string]any) (int, any) {
	str := func(k string) string { s, _ := body[k].(string); return s }
	switch {
	case method == http.MethodGet && strings.HasPrefix(p, "/branches/"):
		name := strings.TrimPrefix(p, "/branches/")
		sha, ok := f.branches[name]
		if !ok {
			return http.StatusNotFound, notFound
		}
		return http.StatusOK, map[string]any{"name": name, "commit": map[string]string{"id": sha}}
	case method == http.MethodDelete && strings.HasPrefix(p, "/branches/"):
		name := strings.TrimPrefix(p, "/branches/")
		if _, ok := f.branches[name]; !ok {
			return http.StatusNotFound, notFound
		}
		delete(f.branches, name)
		return http.StatusNoContent, nil
	case method == http.MethodPost && p == "/branches":
		name := str("new_branch_name")
		if _, ok := f.branches[name]; ok {
			return http.StatusConflict, map[string]string{"message": "The branch already exists."}
		}
		sha, ok := f.resolve(str("old_ref_name"))
		if !ok {
			return http.StatusNotFound, notFound
		}
		f.branches[name] = sha
		return http.StatusCreated, map[string]any{"name": name, "commit": map[string]string{"id": sha}}
	case method == http.MethodGet && reCompare.MatchString(p):
		m := reCompare.FindStringSubmatch(p)
		base, ok1 := f.resolve(m[1])
		head, ok2 := f.resolve(m[2])
		if !ok1 || !ok2 {
			return http.StatusNotFound, notFound
		}
		commits := []map[string]any{}
		files := []map[string]string{}
		seen := map[string]bool{}
		for _, c := range f.between(base, head) {
			commits = append(commits, apiCommit(c))
			for _, file := range c.files {
				if !seen[file] {
					seen[file] = true
					files = append(files, map[string]string{"filename": file})
				}
			}
		}
		res := map[string]any{"total_commits": len(commits), "commits": commits, "files": files}
		if f.omitFiles {
			delete(res, "files")
		}
		return http.StatusOK, res
	case method == http.MethodGet && reDiff.MatchString(p):
		d, ok := f.diffs[reDiff.FindStringSubmatch(p)[1]]
		if !ok {
			return http.StatusNotFound, notFound
		}
		return http.StatusOK, d
	case method == http.MethodGet && reCommit.MatchString(p):
		c := f.commits[reCommit.FindStringSubmatch(p)[1]]
		if c == nil {
			return http.StatusNotFound, notFound
		}
		return http.StatusOK, apiCommit(c)
	case method == http.MethodGet && rePROfSHA.MatchString(p):
		n, ok := f.mergedPR[rePROfSHA.FindStringSubmatch(p)[1]]
		if !ok {
			return http.StatusNotFound, notFound
		}
		return http.StatusOK, map[string]any{"number": n}
	case method == http.MethodGet && p == "/commits":
		head, ok := f.resolve(q["sha"][0])
		if !ok {
			return http.StatusNotFound, notFound
		}
		limit, _ := strconv.Atoi(q["limit"][0])
		out := []map[string]any{}
		for _, c := range f.between("", head) {
			if len(out) < limit {
				out = append(out, apiCommit(c))
			}
		}
		return http.StatusOK, out
	case method == http.MethodGet && p == "/pulls":
		out := []*fakePull{}
		for _, pr := range f.pulls {
			if pr.State == q["state"][0] {
				out = append(out, pr)
			}
		}
		return http.StatusOK, out
	case method == http.MethodPost && p == "/pulls":
		pr := &fakePull{Number: int64(100 + len(f.pulls)), Title: str("title"), State: "open"}
		pr.Head.Ref, pr.Base.Ref = str("head"), str("base")
		pr.Head.RepoID, pr.Base.RepoID = 1, 1
		f.pulls = append(f.pulls, pr)
		return http.StatusCreated, pr
	case rePull.MatchString(p):
		pr := f.pull(rePull.FindStringSubmatch(p)[1])
		if pr == nil {
			return http.StatusNotFound, notFound
		}
		if method == http.MethodPatch && str("state") != "" {
			pr.State = str("state")
		}
		return http.StatusOK, pr
	case method == http.MethodPost && reMerge.MatchString(p):
		pr := f.pull(reMerge.FindStringSubmatch(p)[1])
		if pr == nil {
			return http.StatusNotFound, notFound
		}
		if f.mergeHook != nil {
			if status, msg := f.mergeHook(pr, body); status != 0 {
				return status, map[string]string{"message": msg}
			}
		}
		return f.merge(pr, body)
	case method == http.MethodPost && p == "/diffpatch":
		if f.diffpatchHook != nil {
			if status, msg := f.diffpatchHook(body); status != 0 {
				return status, map[string]string{"message": msg}
			}
		}
		branch := str("branch")
		sha := fmt.Sprintf("revert%d", f.seq+1)
		f.commit(sha, str("message"), nil, f.branches[branch])
		f.branches[branch] = sha
		return http.StatusOK, map[string]any{"commit": map[string]string{"sha": sha}}
	case method == http.MethodPost && reStatus.MatchString(p):
		f.statuses = append(f.statuses, body)
		return http.StatusCreated, body
	case reComments.MatchString(p):
		n, _ := strconv.ParseInt(reComments.FindStringSubmatch(p)[1], 10, 64)
		if method == http.MethodPost {
			f.comments[n] = append(f.comments[n], str("body"))
			return http.StatusCreated, body
		}
		out := []map[string]string{}
		for _, c := range f.comments[n] {
			out = append(out, map[string]string{"body": c})
		}
		return http.StatusOK, out
	case p == "/branch_protections" && method == http.MethodGet:
		return http.StatusOK, f.protections
	case p == "/branch_protections" && method == http.MethodPost:
		name := str("rule_name")
		if f.protection(name) != nil {
			return http.StatusForbidden, map[string]string{"message": "Branch protection already exist"}
		}
		rule := map[string]any{"required_approvals": 0}
		for k, v := range body {
			rule[k] = v
		}
		f.protections = append(f.protections, rule)
		return http.StatusCreated, rule
	case reProtect.MatchString(p):
		name := reProtect.FindStringSubmatch(p)[1]
		rule := f.protection(name)
		if rule == nil {
			return http.StatusNotFound, notFound
		}
		switch method {
		case http.MethodPatch:
			for k, v := range body {
				rule[k] = v
			}
			return http.StatusOK, rule
		case http.MethodDelete:
			f.protections = slices.DeleteFunc(f.protections, func(r map[string]any) bool { return r["rule_name"] == name })
			return http.StatusNoContent, nil
		}
	}
	f.t.Errorf("unexpected request %s %s", method, p)
	return http.StatusNotFound, notFound
}

func (f *fakeForge) pull(index string) *fakePull {
	n, _ := strconv.ParseInt(index, 10, 64)
	for _, pr := range f.pulls {
		if pr.Number == n {
			return pr
		}
	}
	return nil
}

func (f *fakeForge) protection(name string) map[string]any {
	for _, r := range f.protections {
		if r["rule_name"] == name {
			return r
		}
	}
	return nil
}

func (f *fakeForge) merge(pr *fakePull, body map[string]any) (int, any) {
	head := f.branches[pr.Head.Ref]
	base := f.branches[pr.Base.Ref]
	if id, _ := body["head_commit_id"].(string); id != "" && id != head {
		return http.StatusConflict, map[string]string{"message": "head out of date"}
	}
	switch body["Do"] {
	case "fast-forward-only":
		if !f.reach(head)[base] {
			return http.StatusMethodNotAllowed, map[string]string{"message": "Merge DivergingFastForwardOnly"}
		}
		f.branches[pr.Base.Ref] = head
		pr.MergeCommitSHA = head
	case "merge":
		title, _ := body["MergeTitleField"].(string)
		sha := fmt.Sprintf("merge%d", f.seq+1)
		f.commit(sha, title, nil, base, head)
		f.branches[pr.Base.Ref] = sha
		pr.MergeCommitSHA = sha
	default:
		return http.StatusUnprocessableEntity, map[string]string{"message": "bad Do"}
	}
	pr.Merged, pr.State = true, "closed"
	if del, _ := body["delete_branch_after_merge"].(bool); del {
		delete(f.branches, pr.Head.Ref)
	}
	return http.StatusOK, nil
}
