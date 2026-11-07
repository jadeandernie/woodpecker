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

package forge

import (
	"context"
	"errors"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

var (
	// ErrRevertConflict is returned when a car's reverse diff does not apply
	// to the train branch.
	ErrRevertConflict = errors.New("revert does not apply cleanly")
	// ErrNotFastForward is returned when the target cannot be fast-forwarded
	// to the tested commit, for example because it moved on its own.
	ErrNotFastForward = errors.New("target cannot be fast-forwarded")
	// ErrSyncConflict is returned when the target does not merge cleanly into
	// the train branch.
	ErrSyncConflict = errors.New("target does not merge cleanly into the train branch")
	// ErrNothingToRevert is returned by Revert when the car changed no files.
	ErrNothingToRevert = errors.New("car changes nothing to revert")
)

// TrainRevertTrailer is the commit message trailer a revert carries with the
// SHA of the car it reverts, so a revert is never made twice.
const TrainRevertTrailer = "Train-Revert"

// TrainCars describes how a train branch differs from its target.
type TrainCars struct {
	// Head is the train branch head.
	Head string
	// Target is the target branch head.
	Target string
	// Cars are the first-parent commits of the train branch that are not on
	// the target, oldest first, with the files each changed against its first
	// parent and the pull request it merged, if any.
	Cars []*model.TrainCar
	// TargetMoved is set if the target has commits the train branch lacks.
	TargetMoved bool
}

// TrainNotice tells a car's author what the train did with it.
type TrainNotice struct {
	// SHA is the commit to set the "train" commit status on; empty skips it.
	SHA         string
	State       model.StatusValue
	Description string
	Link        string
	// PR is the pull request to comment on; zero or an empty Comment skips it.
	PR      int64
	Comment string
}

// Train is an optional interface for forges that can run a merge train: it
// reads the train branch's cars and moves branches on the server's behalf.
//
// Implementations: Forgejo, Gitea.
//
// Every method must be idempotent against forge state: the train calls them
// again after a crash or restart without knowing whether the first call landed.
type Train interface {
	// Cars returns the first-parent commits of branch that target lacks.
	Cars(ctx context.Context, u *model.User, r *model.Repo, branch, target string) (*TrainCars, error)

	// FastForward moves target to sha, which must descend from target's head;
	// promote is the pipeline number that tested sha. It returns
	// ErrNotFastForward if target cannot be fast-forwarded to sha.
	FastForward(ctx context.Context, u *model.User, r *model.Repo, target, sha string, promote int64) error

	// Revert commits the reverse of car's changes against its first parent
	// onto branch with the given title, and a TrainRevertTrailer naming the
	// car. It returns the revert commit, the existing one if branch already
	// carries a revert of car, ErrRevertConflict if the changes do not
	// reverse cleanly, or ErrNothingToRevert if the car changed nothing.
	Revert(ctx context.Context, u *model.User, r *model.Repo, branch string, car *model.TrainCar, title string) (string, error)

	// Freeze stops (frozen) or allows pushes and merges to branch by anyone
	// but the user the train writes as.
	Freeze(ctx context.Context, u *model.User, r *model.Repo, branch string, frozen bool) error

	// Notify sets the "train" commit status and comments on a pull request.
	Notify(ctx context.Context, u *model.User, r *model.Repo, notice *TrainNotice) error

	// Sync merges from into into with a merge commit titled title and returns
	// it, or ErrSyncConflict if they do not merge cleanly.
	Sync(ctx context.Context, u *model.User, r *model.Repo, from, into, title string) (string, error)
}
