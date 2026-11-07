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

package gitea

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"

	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/forge/gitealike"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/shared/httputil"
)

var _ forge.Train = (*Gitea)(nil)

// trainClient returns a client of the Gitea-compatible API as u.
func (c *Gitea) trainClient(u *model.User) (*gitealike.Client, error) {
	if u == nil {
		return nil, errors.New("merge train: no user to act as")
	}
	httpClient := &http.Client{}
	if c.skipVerify {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			Proxy:           http.ProxyFromEnvironment,
		}
	}
	return &gitealike.Client{
		BaseURL:    c.url,
		Token:      u.AccessToken,
		HTTPClient: httputil.WrapClient(httpClient, "forge-gitea"),
	}, nil
}

// Cars implements forge.Train.
func (c *Gitea) Cars(ctx context.Context, u *model.User, r *model.Repo, branch, target string) (*forge.TrainCars, error) {
	client, err := c.trainClient(u)
	if err != nil {
		return nil, err
	}
	return client.Cars(ctx, r.Owner, r.Name, branch, target)
}

// FastForward implements forge.Train.
func (c *Gitea) FastForward(ctx context.Context, u *model.User, r *model.Repo, target, sha string, promote int64) error {
	client, err := c.trainClient(u)
	if err != nil {
		return err
	}
	return client.FastForward(ctx, r.Owner, r.Name, target, sha, promote)
}

// Revert implements forge.Train.
func (c *Gitea) Revert(ctx context.Context, u *model.User, r *model.Repo, branch string, car *model.TrainCar, title string) (string, error) {
	client, err := c.trainClient(u)
	if err != nil {
		return "", err
	}
	return client.Revert(ctx, r.Owner, r.Name, branch, car, title)
}

// Freeze implements forge.Train.
func (c *Gitea) Freeze(ctx context.Context, u *model.User, r *model.Repo, branch string, frozen bool) error {
	client, err := c.trainClient(u)
	if err != nil {
		return err
	}
	return client.Freeze(ctx, r.Owner, r.Name, branch, frozen)
}

// Notify implements forge.Train.
func (c *Gitea) Notify(ctx context.Context, u *model.User, r *model.Repo, notice *forge.TrainNotice) error {
	client, err := c.trainClient(u)
	if err != nil {
		return err
	}
	return client.Notify(ctx, r.Owner, r.Name, notice)
}

// Sync implements forge.Train.
func (c *Gitea) Sync(ctx context.Context, u *model.User, r *model.Repo, from, into, title string) (string, error) {
	client, err := c.trainClient(u)
	if err != nil {
		return "", err
	}
	return client.Sync(ctx, r.Owner, r.Name, from, into, title)
}
