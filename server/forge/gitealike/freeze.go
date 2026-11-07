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
	"fmt"
	"net/http"
	"strings"
)

// Freezing a branch.
//
// Freeze puts a branch protection rule named exactly like the branch in a
// frozen shape: pushes and merges whitelisted to the train's user only, for
// admins too. A rule named exactly like a branch takes precedence over glob
// rules, so this works whatever other rules the repo has.
//
// The forge has no place to keep what the rule looked like before, so
// Freeze keeps it in the forge itself: before it changes or creates the rule
// it creates a backup rule named FreezeBackupPrefix+branch (for a branch that
// does not exist, so it protects nothing) holding the rule's earlier push and
// merge whitelist settings, and in its protected file patterns a marker
// telling whether the train created the branch's rule. Unfreezing restores
// those settings, or deletes the rule if the train created it, and then
// deletes the backup. Without a backup, unfreezing does nothing.
//
// The order of the steps makes both directions safe to repeat after a crash.

// FreezeBackupPrefix starts the name of the branch protection rule in which
// Freeze keeps the frozen branch's earlier settings.
const FreezeBackupPrefix = "woodpecker-train-freeze-"

const (
	markerCreated  = "woodpecker-train:created"
	markerExisting = "woodpecker-train:existing"
)

// protection holds the fields of a branch protection rule Freeze reads and
// writes; other settings of a rule are left as they are.
type protection struct {
	RuleName                string   `json:"rule_name,omitempty"`
	BranchName              string   `json:"branch_name,omitempty"`
	EnablePush              bool     `json:"enable_push"`
	EnablePushWhitelist     bool     `json:"enable_push_whitelist"`
	PushWhitelistUsernames  []string `json:"push_whitelist_usernames"`
	PushWhitelistTeams      []string `json:"push_whitelist_teams"`
	PushWhitelistDeployKeys bool     `json:"push_whitelist_deploy_keys"`
	EnableMergeWhitelist    bool     `json:"enable_merge_whitelist"`
	MergeWhitelistUsernames []string `json:"merge_whitelist_usernames"`
	MergeWhitelistTeams     []string `json:"merge_whitelist_teams"`
	ApplyToAdmins           bool     `json:"apply_to_admins"`
	ProtectedFilePatterns   string   `json:"protected_file_patterns,omitempty"`
}

func (p *protection) name() string {
	if p.RuleName != "" {
		return p.RuleName
	}
	return p.BranchName
}

// whitelists returns the whitelist settings of p with the slices non-nil, so
// an edit sets them even when empty.
func (p *protection) whitelists() protection {
	nonNil := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	return protection{
		EnablePush:              p.EnablePush,
		EnablePushWhitelist:     p.EnablePushWhitelist,
		PushWhitelistUsernames:  nonNil(p.PushWhitelistUsernames),
		PushWhitelistTeams:      nonNil(p.PushWhitelistTeams),
		PushWhitelistDeployKeys: p.PushWhitelistDeployKeys,
		EnableMergeWhitelist:    p.EnableMergeWhitelist,
		MergeWhitelistUsernames: nonNil(p.MergeWhitelistUsernames),
		MergeWhitelistTeams:     nonNil(p.MergeWhitelistTeams),
		ApplyToAdmins:           p.ApplyToAdmins,
	}
}

// frozenFor returns the whitelist settings that let only login push and
// merge.
func frozenFor(login string) protection {
	return protection{
		EnablePush:              true,
		EnablePushWhitelist:     true,
		PushWhitelistUsernames:  []string{login},
		PushWhitelistTeams:      []string{},
		EnableMergeWhitelist:    true,
		MergeWhitelistUsernames: []string{login},
		MergeWhitelistTeams:     []string{},
		ApplyToAdmins:           true,
	}
}

func onlyUser(users []string, login string) bool {
	return len(users) == 1 && strings.EqualFold(users[0], login)
}

func (p *protection) frozenFor(login string) bool {
	return p.EnablePush && p.EnablePushWhitelist && onlyUser(p.PushWhitelistUsernames, login) &&
		len(p.PushWhitelistTeams) == 0 && !p.PushWhitelistDeployKeys &&
		p.EnableMergeWhitelist && onlyUser(p.MergeWhitelistUsernames, login) &&
		len(p.MergeWhitelistTeams) == 0 && p.ApplyToAdmins
}

// Freeze implements forge.Train.
func (c *Client) Freeze(ctx context.Context, owner, repo, branch string, frozen bool) error {
	var rules []*protection
	if err := c.do(ctx, http.MethodGet, repoPath(owner, repo, "branch_protections"), nil, nil, &rules); err != nil {
		return err
	}
	var rule, backup *protection
	for _, r := range rules {
		switch r.name() {
		case branch:
			rule = r
		case FreezeBackupPrefix + branch:
			backup = r
		}
	}
	if frozen {
		return c.freeze(ctx, owner, repo, branch, rule, backup)
	}
	return c.unfreeze(ctx, owner, repo, branch, rule, backup)
}

func (c *Client) freeze(ctx context.Context, owner, repo, branch string, rule, backup *protection) error {
	login, err := c.login(ctx)
	if err != nil {
		return err
	}
	if backup == nil {
		b := protection{}
		b.ProtectedFilePatterns = markerCreated
		if rule != nil {
			b = rule.whitelists()
			b.ProtectedFilePatterns = markerExisting
		}
		b.RuleName = FreezeBackupPrefix + branch
		if err := c.do(ctx, http.MethodPost, repoPath(owner, repo, "branch_protections"), nil, &b, nil); err != nil {
			return fmt.Errorf("back up protection of %s: %w", branch, err)
		}
	}
	want := frozenFor(login)
	switch {
	case rule == nil:
		want.RuleName = branch
		want.BranchName = branch
		err = c.do(ctx, http.MethodPost, repoPath(owner, repo, "branch_protections"), nil, &want, nil)
	case !rule.frozenFor(login):
		err = c.do(ctx, http.MethodPatch, repoPath(owner, repo, "branch_protections", rule.name()), nil, &want, nil)
	}
	if err != nil {
		return fmt.Errorf("freeze %s: %w", branch, err)
	}
	return nil
}

func (c *Client) unfreeze(ctx context.Context, owner, repo, branch string, rule, backup *protection) error {
	if backup == nil {
		return nil
	}
	var err error
	switch {
	case rule == nil:
		// Gone already: deleted by an earlier unfreeze or by hand.
	case backup.ProtectedFilePatterns == markerCreated:
		err = c.do(ctx, http.MethodDelete, repoPath(owner, repo, "branch_protections", rule.name()), nil, nil, nil)
		if isNotFound(err) {
			err = nil
		}
	default:
		restore := backup.whitelists()
		err = c.do(ctx, http.MethodPatch, repoPath(owner, repo, "branch_protections", rule.name()), nil, &restore, nil)
	}
	if err != nil {
		return fmt.Errorf("unfreeze %s: %w", branch, err)
	}
	err = c.do(ctx, http.MethodDelete, repoPath(owner, repo, "branch_protections", backup.name()), nil, nil, nil)
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("unfreeze %s: drop backup: %w", branch, err)
	}
	return nil
}
