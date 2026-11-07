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

// Package gitealike implements the merge train (forge.Train) once for the
// forges that speak the Gitea API, Forgejo and Gitea, with plain JSON over
// HTTP, since their SDKs lack some of the calls it needs.
package gitealike

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxErrorBody caps how much of a response body an APIError keeps.
const maxErrorBody = 512

// defaultRetryDelays are the waits between merge attempts while the forge is
// still checking whether a new pull request is mergeable.
var defaultRetryDelays = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

// Client talks to the Gitea-compatible API of one forge as one user.
type Client struct {
	// BaseURL is the forge URL, without the /api/v1 suffix.
	BaseURL string
	// Token is the user's access token.
	Token string
	// HTTPClient sends the requests; nil uses http.DefaultClient.
	HTTPClient *http.Client
	// RetryDelays are the waits between merge attempts the forge answers with
	// "try again later"; nil uses a default of about 15 seconds in all.
	RetryDelays []time.Duration
}

// APIError is a response from the forge with a non-2xx status.
type APIError struct {
	Method string
	Path   string
	Status int
	// Body is the response body, truncated.
	Body string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.Path, e.Status, http.StatusText(e.Status), e.Body)
}

// statusOf returns the HTTP status of an APIError in err's chain, or 0.
func statusOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

func isNotFound(err error) bool { return statusOf(err) == http.StatusNotFound }

// isTryLater tells the forge's answer to a merge of a pull request whose
// mergeability it is still checking.
func isTryLater(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusMethodNotAllowed &&
		strings.Contains(strings.ToLower(apiErr.Body), "try again later")
}

// repoPath returns the API path of a repo resource; each element is escaped
// as one path segment.
func repoPath(owner, repo string, elem ...string) string {
	var b strings.Builder
	b.WriteString("/repos/")
	b.WriteString(url.PathEscape(owner))
	b.WriteString("/")
	b.WriteString(url.PathEscape(repo))
	for _, e := range elem {
		b.WriteString("/")
		b.WriteString(url.PathEscape(e))
	}
	return b.String()
}

// do sends a request to the API path p (below /api/v1) with in as JSON body,
// and decodes a JSON response into out if it is not nil.
func (c *Client) do(ctx context.Context, method, p string, query url.Values, in, out any) error {
	body, err := c.raw(ctx, method, p, query, in)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, p, err)
	}
	return nil
}

// raw sends a request like do and returns the response body.
func (c *Client) raw(ctx context.Context, method, p string, query url.Values, in any) ([]byte, error) {
	u := strings.TrimSuffix(c.BaseURL, "/") + "/api/v1" + p
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reqBody io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("%s %s: encode request: %w", method, p, err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, p, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s %s: read response: %w", method, p, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > maxErrorBody {
			msg = msg[:maxErrorBody] + "..."
		}
		return nil, &APIError{Method: method, Path: p, Status: resp.StatusCode, Body: msg}
	}
	return body, nil
}

// --- API shapes ---

type commitMeta struct {
	SHA string `json:"sha"`
}

type commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
	Parents []commitMeta `json:"parents"`
}

type affectedFile struct {
	Filename string `json:"filename"`
}

type compareResult struct {
	TotalCommits int            `json:"total_commits"`
	Commits      []*commit      `json:"commits"`
	Files        []affectedFile `json:"files"`
}

type branchInfo struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type prBranch struct {
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
	RepoID int64  `json:"repo_id"`
}

type pullRequest struct {
	Number         int64    `json:"number"`
	State          string   `json:"state"`
	Merged         bool     `json:"merged"`
	MergeCommitSHA string   `json:"merge_commit_sha"`
	Head           prBranch `json:"head"`
	Base           prBranch `json:"base"`
}

// --- shared calls ---

func (c *Client) branchHead(ctx context.Context, owner, repo, branch string) (string, error) {
	var b branchInfo
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo, "branches", branch), nil, nil, &b); err != nil {
		return "", err
	}
	if b.Commit.ID == "" {
		return "", fmt.Errorf("branch %s: forge returned no head commit", branch)
	}
	return b.Commit.ID, nil
}

// compare returns what head has that base lacks (base...head).
func (c *Client) compare(ctx context.Context, owner, repo, base, head string) (*compareResult, error) {
	var res compareResult
	p := repoPath(owner, repo, "compare") + "/" + url.PathEscape(base) + "..." + url.PathEscape(head)
	if err := c.do(ctx, http.MethodGet, p, nil, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ahead tells whether head has commits base lacks.
func (c *Client) ahead(ctx context.Context, owner, repo, base, head string) (bool, error) {
	res, err := c.compare(ctx, owner, repo, base, head)
	if err != nil {
		return false, err
	}
	return res.TotalCommits > 0 || len(res.Commits) > 0, nil
}

func (c *Client) getCommit(ctx context.Context, owner, repo, sha string) (*commit, error) {
	var res commit
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo, "git", "commits", sha), nil, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// maxPullPages caps the pages of open pull requests findPull reads.
const maxPullPages = 20

// findPull returns the open pull request from head into base within the
// repo, or nil.
func (c *Client) findPull(ctx context.Context, owner, repo, head, base string) (*pullRequest, error) {
	const limit = 50
	for page := 1; page <= maxPullPages; page++ {
		var pulls []*pullRequest
		q := url.Values{"state": {"open"}, "limit": {fmt.Sprint(limit)}, "page": {fmt.Sprint(page)}}
		if err := c.do(ctx, http.MethodGet, repoPath(owner, repo, "pulls"), q, nil, &pulls); err != nil {
			return nil, err
		}
		for _, pr := range pulls {
			sameRepo := pr.Head.RepoID == 0 || pr.Head.RepoID == pr.Base.RepoID
			if pr.Head.Ref == head && pr.Base.Ref == base && sameRepo {
				return pr, nil
			}
		}
		if len(pulls) < limit {
			break
		}
	}
	return nil, nil
}

// ensurePull returns the open pull request from head into base, creating it
// with title if there is none.
func (c *Client) ensurePull(ctx context.Context, owner, repo, head, base, title string) (*pullRequest, error) {
	pr, err := c.findPull(ctx, owner, repo, head, base)
	if err != nil || pr != nil {
		return pr, err
	}
	var created pullRequest
	body := map[string]string{"head": head, "base": base, "title": title}
	err = c.do(ctx, http.MethodPost, repoPath(owner, repo, "pulls"), nil, body, &created)
	if statusOf(err) == http.StatusConflict {
		// Someone opened it between the list and the create.
		if pr, findErr := c.findPull(ctx, owner, repo, head, base); findErr == nil && pr != nil {
			return pr, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// mergeOptions is the body of a pull request merge.
type mergeOptions struct {
	Do                     string `json:"Do"`
	MergeTitleField        string `json:"MergeTitleField,omitempty"`
	HeadCommitID           string `json:"head_commit_id,omitempty"`
	DeleteBranchAfterMerge bool   `json:"delete_branch_after_merge"`
}

// merge merges a pull request, waiting while the forge still checks whether
// it is mergeable.
func (c *Client) merge(ctx context.Context, owner, repo string, index int64, opts *mergeOptions) error {
	delays := c.RetryDelays
	if delays == nil {
		delays = defaultRetryDelays
	}
	p := repoPath(owner, repo, "pulls", fmt.Sprint(index), "merge")
	for attempt := 0; ; attempt++ {
		err := c.do(ctx, http.MethodPost, p, nil, opts, nil)
		if err == nil || !isTryLater(err) || attempt >= len(delays) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(delays[attempt]):
		}
	}
}

func (c *Client) deleteBranch(ctx context.Context, owner, repo, branch string) error {
	err := c.do(ctx, http.MethodDelete, repoPath(owner, repo, "branches", branch), nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) login(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", nil, nil, &u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("GET /user: forge returned no login")
	}
	return u.Login, nil
}
