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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"
	"golang.org/x/sync/errgroup"

	"go.woodpecker-ci.org/woodpecker/v3/server"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge/common"
	forge_types "go.woodpecker-ci.org/woodpecker/v3/server/forge/types"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/store"
	"go.woodpecker-ci.org/woodpecker/v3/shared/httputil"
	shared_utils "go.woodpecker-ci.org/woodpecker/v3/shared/utils"
)

// maxParallelFileFetches bounds how many files of a config folder Dir loads at once.
const maxParallelFileFetches = 10

const (
	authorizeTokenURL = "%s/login/oauth/authorize"
	accessTokenURL    = "%s/login/oauth/access_token"
	defaultPageSize   = 50
	forgejoDevVersion = "v7.0.2"

	// maxPushFirstParentCommits caps the number of commits fetched to compute
	// the changed files of a branch push.
	maxPushFirstParentCommits = 50
)

type Forgejo struct {
	id                int64
	url               string
	oauth2URL         string
	oAuthClientID     string
	oAuthClientSecret string
	skipVerify        bool
	pageSize          int
}

// Opts defines configuration options.
type Opts struct {
	URL               string // Forgejo server url.
	OAuth2URL         string // User-facing Forgejo server url for OAuth2.
	OAuthClientID     string // OAuth2 Client ID
	OAuthClientSecret string // OAuth2 Client Secret
	SkipVerify        bool   // Skip ssl verification.
}

// New returns a Forge implementation that integrates with Forgejo,
// an open source Git service written in Go. See https://forgejo.org/
func New(id int64, opts Opts) (forge.Forge, error) {
	if opts.OAuth2URL == "" {
		opts.OAuth2URL = opts.URL
	}

	return &Forgejo{
		id:                id,
		url:               opts.URL,
		oauth2URL:         opts.OAuth2URL,
		oAuthClientID:     opts.OAuthClientID,
		oAuthClientSecret: opts.OAuthClientSecret,
		skipVerify:        opts.SkipVerify,
	}, nil
}

// Name returns the string name of this driver.
func (c *Forgejo) Name() string {
	return "forgejo"
}

// URL returns the root url of a configured forge.
func (c *Forgejo) URL() string {
	return c.url
}

func (c *Forgejo) oauth2Config(ctx context.Context) (*oauth2.Config, context.Context) {
	return &oauth2.Config{
			ClientID:     c.oAuthClientID,
			ClientSecret: c.oAuthClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  fmt.Sprintf(authorizeTokenURL, c.oauth2URL),
				TokenURL: fmt.Sprintf(accessTokenURL, c.oauth2URL),
			},
			RedirectURL: fmt.Sprintf("%s/authorize", server.Config.Server.OAuthHost),
		},

		context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: c.skipVerify},
			Proxy:           http.ProxyFromEnvironment,
		}})
}

// Login authenticates an account with Forgejo using basic authentication. The
// Forgejo account details are returned when the user is successfully authenticated.
func (c *Forgejo) Login(ctx context.Context, req *forge_types.OAuthRequest) (*model.User, string, error) {
	config, oauth2Ctx := c.oauth2Config(ctx)
	redirectURL := config.AuthCodeURL(req.State)

	// check the OAuth code
	if len(req.Code) == 0 {
		return nil, redirectURL, nil
	}

	token, err := config.Exchange(oauth2Ctx, req.Code)
	if err != nil {
		return nil, redirectURL, err
	}

	client, err := c.newClientToken(ctx, token.AccessToken)
	if err != nil {
		return nil, redirectURL, err
	}
	account, _, err := client.GetMyUserInfo()
	if err != nil {
		return nil, redirectURL, err
	}

	return &model.User{
		AccessToken:   token.AccessToken,
		RefreshToken:  token.RefreshToken,
		Expiry:        token.Expiry.UTC().Unix(),
		Login:         account.UserName,
		Email:         account.Email,
		ForgeRemoteID: model.ForgeRemoteID(fmt.Sprint(account.ID)),
		Avatar:        expandAvatar(c.url, account.AvatarURL),
	}, redirectURL, nil
}

// Refresh refreshes the Forgejo oauth2 access token. If the token is
// refreshed, the user is updated and a true value is returned.
func (c *Forgejo) Refresh(ctx context.Context, user *model.User) (bool, error) {
	config, oauth2Ctx := c.oauth2Config(ctx)
	config.RedirectURL = ""

	// Refresh token only: a stored access token makes oauth2 skip the exchange.
	source := config.TokenSource(oauth2Ctx, &oauth2.Token{
		RefreshToken: user.RefreshToken,
	})

	token, err := source.Token()
	if err != nil || len(token.AccessToken) == 0 {
		return false, err
	}

	user.AccessToken = token.AccessToken
	user.RefreshToken = token.RefreshToken
	user.Expiry = token.Expiry.UTC().Unix()
	return true, nil
}

// Teams is supported by the Forgejo driver.
func (c *Forgejo) Teams(ctx context.Context, u *model.User, p *model.ListOptions) ([]*model.Team, error) {
	// we paginate internally (https://github.com/woodpecker-ci/woodpecker/issues/5667)
	if p.Page != 1 {
		return nil, nil
	}

	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return nil, err
	}

	return shared_utils.Paginate(func(page int) ([]*model.Team, error) {
		orgs, _, err := client.ListMyOrgs(
			forgejo.ListOrgsOptions{
				Page:     page,
				PageSize: c.perPage(ctx),
			},
		)
		teams := make([]*model.Team, 0, len(orgs))
		for _, org := range orgs {
			teams = append(teams, toTeam(org, c.url))
		}
		return teams, err
	}, -1)
}

// TeamPerm is not supported by the Forgejo driver.
func (c *Forgejo) TeamPerm(_ *model.User, _ string) (*model.Perm, error) {
	return nil, nil
}

// Repo returns the Forgejo repository.
func (c *Forgejo) Repo(ctx context.Context, u *model.User, remoteID model.ForgeRemoteID, owner, name string) (*model.Repo, error) {
	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return nil, err
	}

	if remoteID.IsValid() {
		intID, err := strconv.ParseInt(string(remoteID), 10, 64)
		if err != nil {
			return nil, err
		}
		repo, resp, err := client.GetRepoByID(intID)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return nil, errors.Join(err, forge_types.ErrRepoNotFound)
			}
			return nil, err
		}
		return toRepo(repo), nil
	}

	repo, resp, err := client.GetRepo(owner, name)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, errors.Join(err, forge_types.ErrRepoNotFound)
		}
		return nil, err
	}
	return toRepo(repo), nil
}

// Repos returns a list of all repositories for the Forgejo account, including
// organization repositories.
func (c *Forgejo) Repos(ctx context.Context, u *model.User, p *model.ListOptions) ([]*model.Repo, error) {
	// we paginate internally (https://github.com/woodpecker-ci/woodpecker/issues/5667)
	if p.Page != 1 {
		return nil, nil
	}

	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return nil, err
	}

	repos, err := shared_utils.Paginate(func(page int) ([]*forgejo.Repository, error) {
		repos, _, err := client.ListMyRepos(
			forgejo.ListReposOptions{
				Page:     page,
				PageSize: c.perPage(ctx),
			},
		)
		return repos, err
	}, -1)

	result := make([]*model.Repo, 0, len(repos))
	for _, repo := range repos {
		if repo.Archived {
			continue
		}
		result = append(result, toRepo(repo))
	}
	return result, err
}

// File fetches the file from the Forgejo repository and returns its contents.
func (c *Forgejo) File(ctx context.Context, u *model.User, r *model.Repo, b *model.Pipeline, f string) ([]byte, error) {
	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return nil, err
	}

	return getFile(client, r, b, f)
}

// getFile loads a file at the pipeline's commit, reporting a missing file as a missing config.
func getFile(client *forgejo.Client, r *model.Repo, b *model.Pipeline, f string) ([]byte, error) {
	cfg, resp, err := client.GetFile(r.Owner, r.Name, b.Commit, f)
	if err != nil && resp != nil && resp.StatusCode == http.StatusNotFound {
		return nil, errors.Join(err, &forge_types.ErrConfigNotFound{Configs: []string{f}})
	}
	return cfg, err
}

func (c *Forgejo) Dir(ctx context.Context, u *model.User, r *model.Repo, b *model.Pipeline, f string) ([]*forge_types.FileMeta, error) {
	// one client for the listing and all files, so the server version is only checked once
	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return nil, err
	}

	paths, err := listDirFiles(client, r, b, f)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}

	// load the files in parallel with a bounded number of workers on the shared
	// client, whose requests the first error cancels
	configs := make([]*forge_types.FileMeta, len(paths))
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(maxParallelFileFetches)
	client.SetContext(gCtx)
	for i, path := range paths {
		g.Go(func() error {
			data, err := getFile(client, r, b, path)
			if err != nil {
				return fmt.Errorf("multi-pipeline cannot get %s: %w", path, err)
			}
			configs[i] = &forge_types.FileMeta{Name: path, Data: data}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return configs, nil
}

// treePageSize is how many entries Dir asks for per page of a git tree listing.
const treePageSize = 100

// listDirFiles returns the paths of the regular files directly in folder f at
// the pipeline's commit, in tree order. It lists git trees rather than
// contents, which loads every file's last commit and is slow on large
// folders. Each tree is named by "<commit>:<first segment>", then by the
// subtree's sha for each further segment, since the API route cannot take a
// slash in the ref.
func listDirFiles(client *forgejo.Client, r *model.Repo, b *model.Pipeline, f string) ([]string, error) {
	segments := strings.Split(f, "/")
	ref := b.Commit + ":" + segments[0]
	for _, segment := range segments[1:] {
		entries, err := listTree(client, r, ref, f)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(entries, func(e forgejo.GitEntry) bool { return e.Type == "tree" && e.Path == segment })
		if i < 0 {
			return nil, &forge_types.ErrConfigNotFound{Configs: []string{f}}
		}
		ref = entries[i].SHA
	}

	entries, err := listTree(client, r, ref, f)
	if err != nil {
		return nil, err
	}
	// sub folders, links and submodules are no pipeline files
	var paths []string
	for _, e := range entries {
		if e.Type == "blob" && e.Mode != "120000" {
			paths = append(paths, f+"/"+e.Path)
		}
	}
	return paths, nil
}

// listTree returns all entries of one git tree, non-recursive, following its
// pages until it has as many entries as the tree holds. Forgejo marks every
// page as truncated, so only the total count tells when the listing is done.
func listTree(client *forgejo.Client, r *model.Repo, ref, f string) ([]forgejo.GitEntry, error) {
	var entries []forgejo.GitEntry
	for page := 1; ; page++ {
		tree, resp, err := client.GetTrees(r.Owner, r.Name, ref, forgejo.GetTreesOptions{
			ListOptions: forgejo.ListOptions{Page: page, PageSize: treePageSize},
		})
		if err != nil {
			// Forgejo answers a ref it cannot resolve with a bad request
			if resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest) {
				return nil, errors.Join(err, &forge_types.ErrConfigNotFound{Configs: []string{f}})
			}
			return nil, err
		}
		entries = append(entries, tree.Entries...)
		if len(entries) >= tree.TotalCount {
			return entries, nil
		}
		if len(tree.Entries) == 0 {
			return nil, fmt.Errorf("listing %s ended after %d of %d entries", f, len(entries), tree.TotalCount)
		}
	}
}

// Status is supported by the Forgejo driver.
func (c *Forgejo) Status(ctx context.Context, user *model.User, repo *model.Repo, pipeline *model.Pipeline, workflow *model.Workflow) error {
	client, err := c.newClientToken(ctx, user.AccessToken)
	if err != nil {
		return err
	}

	_, _, err = client.CreateStatus(
		repo.Owner,
		repo.Name,
		pipeline.Commit,
		forgejo.CreateStatusOption{
			State:       getStatus(workflow.State),
			TargetURL:   common.GetPipelineStatusURL(repo, pipeline, workflow),
			Description: common.GetPipelineStatusDescription(workflow.State),
			Context:     common.GetPipelineStatusContext(repo, pipeline, workflow),
		},
	)
	return err
}

// Netrc returns a netrc file capable of authenticating Forgejo requests and
// cloning Forgejo repositories. The netrc will use the global machine account
// when configured.
func (c *Forgejo) Netrc(u *model.User, r *model.Repo) (*model.Netrc, error) {
	login := ""
	token := ""

	if u != nil {
		login = u.Login
		token = u.AccessToken
	}

	host, err := common.ExtractHostFromCloneURL(r.Clone)
	if err != nil {
		return nil, err
	}

	return &model.Netrc{
		Login:    login,
		Password: token,
		Machine:  host,
		Type:     model.ForgeTypeForgejo,
	}, nil
}

// Activate activates the repository by registering post-commit hooks with
// the Forgejo repository.
func (c *Forgejo) Activate(ctx context.Context, u *model.User, r *model.Repo, link string) error {
	config := map[string]string{
		"url":          link,
		"secret":       r.WebhookSecret,
		"content_type": "json",
	}
	hook := forgejo.CreateHookOption{
		Type:   forgejo.HookTypeForgejo,
		Config: config,
		Events: []string{"push", "create", "pull_request", "release"},
		Active: true,
	}

	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return err
	}
	_, response, err := client.CreateRepoHook(r.Owner, r.Name, hook)
	if err != nil {
		if response != nil {
			if response.StatusCode == http.StatusNotFound {
				return fmt.Errorf("could not find repository")
			}
			if response.StatusCode == http.StatusOK {
				return fmt.Errorf("could not find repository, repository was probably renamed")
			}
		}
		return err
	}
	return nil
}

// Deactivate deactivates the repository be removing repository push hooks from
// the Forgejo repository.
func (c *Forgejo) Deactivate(ctx context.Context, u *model.User, r *model.Repo, link string) error {
	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return err
	}

	// make sure a repo rename does not trick us
	forgeRepo, err := c.Repo(ctx, u, r.ForgeRemoteID, r.Owner, r.Name)
	if err != nil {
		return err
	}

	hooks, err := shared_utils.Paginate(func(page int) ([]*forgejo.Hook, error) {
		hooks, _, err := client.ListRepoHooks(forgeRepo.Owner, forgeRepo.Name, forgejo.ListHooksOptions{
			Page:     page,
			PageSize: c.perPage(ctx),
		})
		return hooks, err
	}, -1)
	if err != nil {
		return err
	}

	hook := matchingHooks(hooks, link)
	if hook != nil {
		_, err := client.DeleteRepoHook(forgeRepo.Owner, forgeRepo.Name, hook.ID)
		return err
	}

	return nil
}

// Branches returns the names of all branches for the named repository.
func (c *Forgejo) Branches(ctx context.Context, u *model.User, r *model.Repo, p *model.ListOptions) ([]string, error) {
	token := common.UserToken(ctx, r, u)
	client, err := c.newClientToken(ctx, token)
	if err != nil {
		return nil, err
	}

	branches, _, err := client.ListRepoBranches(r.Owner, r.Name,
		forgejo.ListRepoBranchesOptions{Page: p.Page, PageSize: p.PerPage})
	if err != nil {
		return nil, err
	}
	result := make([]string, len(branches))
	for i := range branches {
		result[i] = branches[i].Name
	}
	return result, err
}

// BranchHead returns the sha of the head (latest commit) of the specified branch.
func (c *Forgejo) BranchHead(ctx context.Context, u *model.User, r *model.Repo, branch string) (*model.Commit, error) {
	token := common.UserToken(ctx, r, u)
	client, err := c.newClientToken(ctx, token)
	if err != nil {
		return nil, err
	}

	b, _, err := client.GetRepoBranch(r.Owner, r.Name, branch)
	if err != nil {
		return nil, err
	}
	return &model.Commit{
		SHA:      b.Commit.ID,
		ForgeURL: b.Commit.URL,
	}, nil
}

func (c *Forgejo) PullRequests(ctx context.Context, u *model.User, r *model.Repo, p *model.ListOptions) ([]*model.PullRequest, error) {
	token := common.UserToken(ctx, r, u)
	client, err := c.newClientToken(ctx, token)
	if err != nil {
		return nil, err
	}

	pullRequests, resp, err := client.ListRepoPullRequests(r.Owner, r.Name, forgejo.ListPullRequestsOptions{
		Page: p.Page, PageSize: p.PerPage,
		State: forgejo.StateOpen,
	})
	if err != nil {
		// Repositories without commits return empty list with status code 404
		if pullRequests != nil && resp != nil && resp.StatusCode == http.StatusNotFound {
			err = nil
		} else {
			return nil, err
		}
	}

	result := make([]*model.PullRequest, len(pullRequests))
	for i := range pullRequests {
		result[i] = &model.PullRequest{
			Index: model.ForgeRemoteID(strconv.Itoa(int(pullRequests[i].Index))),
			Title: pullRequests[i].Title,
		}
	}
	return result, err
}

// Hook parses the incoming Forgejo hook and returns the Repository and Pipeline
// details. If the hook is unsupported nil values are returned.
func (c *Forgejo) Hook(ctx context.Context, r *http.Request) (*model.Repo, *model.Pipeline, error) {
	repo, pipeline, push, err := parseHook(r)
	if err != nil {
		return nil, nil, err
	}

	if pipeline != nil {
		switch pipeline.Event {
		case model.EventRelease, model.EventTag:
			if pipeline.TagTitle == "" {
				pipeline.TagTitle = strings.Split(pipeline.Ref, "/")[2]
			}
			// the create hook of a pushed annotated tag holds the SHA of the
			// tag object, not of the tagged commit
			sha, err := c.getTagCommitSHA(ctx, repo, pipeline.TagTitle)
			if err != nil {
				return nil, nil, err
			}
			pipeline.Commit = sha

		case model.EventPull, model.EventPullClosed, model.EventPullMetadata:
			if len(pipeline.ChangedFiles) == 0 {
				index, err := strconv.ParseInt(strings.Split(pipeline.Ref, "/")[2], 10, 64)
				if err != nil {
					return nil, nil, err
				}
				pipeline.ChangedFiles, err = c.getChangedFilesForPR(ctx, repo, index)
				if err != nil {
					log.Error().Err(err).Msgf("could not get changed files for PR %s#%d", repo.FullName, index)
				}
			}

		case model.EventPush:
			if push != nil {
				files, err := c.getChangedFilesForPush(ctx, repo, push.Before, push.After)
				if err != nil {
					log.Warn().Err(err).Msgf("could not get first-parent changed files for push %s..%s to %s, using the files of the pushed commits instead",
						push.Before, push.After, repo.FullName)
				} else {
					pipeline.ChangedFiles = files
				}
			}
		}
	}

	return repo, pipeline, nil
}

// OrgMembership returns if user is member of organization and if user
// is admin/owner in this organization.
func (c *Forgejo) OrgMembership(ctx context.Context, u *model.User, owner string) (*model.OrgPerm, error) {
	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return nil, err
	}

	member, _, err := client.CheckOrgMembership(owner, u.Login)
	if err != nil {
		return nil, err
	}

	if !member {
		return &model.OrgPerm{}, nil
	}

	perm, _, err := client.GetOrgPermissions(owner, u.Login)
	if err != nil {
		return &model.OrgPerm{Member: member}, err
	}

	return &model.OrgPerm{Member: member, Admin: perm.IsAdmin || perm.IsOwner}, nil
}

func (c *Forgejo) Org(ctx context.Context, u *model.User, owner string) (*model.Org, error) {
	client, err := c.newClientToken(ctx, u.AccessToken)
	if err != nil {
		return nil, err
	}

	org, _, orgErr := client.GetOrg(owner)
	if orgErr == nil && org != nil {
		return &model.Org{
			Name:    org.UserName,
			Private: forgejo.VisibleType(org.Visibility) != forgejo.VisibleTypePublic,
		}, nil
	}

	user, _, err := client.GetUserInfo(owner)
	if err != nil {
		if orgErr != nil {
			err = errors.Join(orgErr, err)
		}
		return nil, err
	}
	return &model.Org{
		Name:    user.UserName,
		IsUser:  true,
		Private: user.Visibility != forgejo.VisibleTypePublic,
	}, nil
}

// newClientToken returns a Forgejo client with token.
func (c *Forgejo) newClientToken(ctx context.Context, token string) (*forgejo.Client, error) {
	httpClient := &http.Client{}
	if c.skipVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	wrappedClient := httputil.WrapClient(httpClient, "forge-forgejo")
	client, err := forgejo.NewClient(c.url, forgejo.SetToken(token), forgejo.SetHTTPClient(wrappedClient), forgejo.SetContext(ctx))
	if err != nil &&
		(errors.Is(err, &forgejo.ErrUnknownVersion{}) || strings.Contains(err.Error(), "Malformed version")) {
		// we guess it's a dev forgejo version
		log.Error().Err(err).Msgf("could not detect forgejo version, assume dev version %s", forgejoDevVersion)
		client, err = forgejo.NewClient(c.url, forgejo.SetForgejoVersion(forgejoDevVersion), forgejo.SetToken(token), forgejo.SetHTTPClient(wrappedClient), forgejo.SetContext(ctx))
	}
	return client, err
}

// getStatus is a helper function that converts a Woodpecker
// status to a Forgejo status.
func getStatus(status model.StatusValue) forgejo.StatusState {
	switch status {
	case model.StatusPending, model.StatusBlocked, model.StatusCreated:
		return forgejo.StatusPending
	case model.StatusRunning:
		return forgejo.StatusPending
	case model.StatusSuccess:
		return forgejo.StatusSuccess
	case model.StatusFailure, model.StatusKilled, model.StatusSkipped, model.StatusCanceled:
		return forgejo.StatusFailure
	case model.StatusDeclined:
		return forgejo.StatusWarning
	case model.StatusError:
		return forgejo.StatusError
	default:
		log.Warn().Str("status", string(status)).Msg("unknown pipeline status")
		return forgejo.StatusFailure
	}
}

func (c *Forgejo) getChangedFilesForPR(ctx context.Context, repo *model.Repo, index int64) ([]string, error) {
	_store, ok := store.TryFromContext(ctx)
	if !ok {
		log.Error().Msg("could not get store from context")
		return []string{}, nil
	}

	repo, err := _store.GetRepoNameFallback(c.id, repo.ForgeRemoteID, repo.FullName)
	if err != nil {
		return nil, err
	}

	user, err := _store.GetUser(repo.UserID)
	if err != nil {
		return nil, err
	}

	forge.Refresh(ctx, c, _store, user)

	client, err := c.newClientToken(ctx, user.AccessToken)
	if err != nil {
		return nil, err
	}

	return shared_utils.Paginate(func(page int) ([]string, error) {
		forgejoFiles, _, err := client.ListPullRequestFiles(repo.Owner, repo.Name, index,
			forgejo.ListPullRequestFilesOptions{Page: page})
		if err != nil {
			return nil, err
		}

		var files []string
		for _, file := range forgejoFiles {
			files = append(files, file.Filename)
		}
		return files, nil
	}, -1)
}

// getChangedFilesForPush returns the files changed by a branch push from
// before to after, as the union of the files changed by each commit on the
// first-parent chain from after back to before.
//
// The commit list of the push payload can not be used for this: a commit that
// merges the base branch into a feature branch lists every file the base branch
// changed since the branch point, so merging such a pull request would report
// all of them as changed. Following the first parents only counts what the
// push actually changed on the pushed branch.
//
// An error is returned if the range can not be resolved, e.g. for new branches,
// force pushes, or pushes of more than maxPushFirstParentCommits commits.
func (c *Forgejo) getChangedFilesForPush(ctx context.Context, repo *model.Repo, before, after string) ([]string, error) {
	if isZeroSHA(before) || isZeroSHA(after) {
		return nil, fmt.Errorf("push range %s..%s has no base commit", before, after)
	}

	_store, ok := store.TryFromContext(ctx)
	if !ok {
		return nil, errors.New("could not get store from context")
	}

	repo, err := _store.GetRepoNameFallback(c.id, repo.ForgeRemoteID, repo.FullName)
	if err != nil {
		return nil, err
	}

	user, err := _store.GetUser(repo.UserID)
	if err != nil {
		return nil, err
	}

	forge.Refresh(ctx, c, _store, user)

	client, err := c.newClientToken(ctx, user.AccessToken)
	if err != nil {
		return nil, err
	}

	var files []string
	sha := after
	for i := 0; sha != before; i++ {
		if i == maxPushFirstParentCommits {
			return nil, fmt.Errorf("%s is not within %d first-parent commits of %s", before, maxPushFirstParentCommits, after)
		}

		commit, _, err := client.GetSingleCommit(repo.Owner, repo.Name, sha)
		if err != nil {
			return nil, fmt.Errorf("could not get commit %s: %w", sha, err)
		}
		for _, file := range commit.Files {
			files = append(files, file.Filename)
		}

		if len(commit.Parents) == 0 || commit.Parents[0] == nil {
			return nil, fmt.Errorf("%s is not a first-parent ancestor of %s", before, after)
		}
		sha = commit.Parents[0].SHA
	}

	return shared_utils.DeduplicateStrings(files), nil
}

// isZeroSHA reports whether sha is empty or the all-zero object id a forge
// sends for a ref that did not exist before (or no longer exists after) a push.
func isZeroSHA(sha string) bool {
	return strings.Trim(sha, "0") == ""
}

func (c *Forgejo) getTagCommitSHA(ctx context.Context, repo *model.Repo, tagName string) (string, error) {
	_store, ok := store.TryFromContext(ctx)
	if !ok {
		log.Error().Msg("could not get store from context")
		return "", nil
	}

	repo, err := _store.GetRepoNameFallback(c.id, repo.ForgeRemoteID, repo.FullName)
	if err != nil {
		return "", err
	}

	user, err := _store.GetUser(repo.UserID)
	if err != nil {
		return "", err
	}

	forge.Refresh(ctx, c, _store, user)

	client, err := c.newClientToken(ctx, user.AccessToken)
	if err != nil {
		return "", err
	}

	tag, _, err := client.GetTag(repo.Owner, repo.Name, tagName)
	if err != nil {
		return "", err
	}

	return tag.Commit.SHA, nil
}

func (c *Forgejo) perPage(ctx context.Context) int {
	if c.pageSize == 0 {
		client, err := c.newClientToken(ctx, "")
		if err != nil {
			return defaultPageSize
		}

		api, _, err := client.GetGlobalAPISettings()
		if err != nil {
			return defaultPageSize
		}
		c.pageSize = api.MaxResponseItems
	}
	return c.pageSize
}
