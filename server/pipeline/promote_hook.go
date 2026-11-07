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

package pipeline

import (
	"sync"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

var (
	promoteDoneMu    sync.RWMutex
	promoteDoneHooks []func(*model.Pipeline)
)

// OnPromoteDone registers fn to be called whenever a promote pipeline ends,
// whatever its status. fn must not block: it runs on the caller's goroutine.
func OnPromoteDone(fn func(*model.Pipeline)) {
	promoteDoneMu.Lock()
	defer promoteDoneMu.Unlock()
	promoteDoneHooks = append(promoteDoneHooks, fn)
}

func promoteDone(p *model.Pipeline) {
	if p == nil || p.Event != model.EventPromote {
		return
	}
	promoteDoneMu.RLock()
	defer promoteDoneMu.RUnlock()
	for _, fn := range promoteDoneHooks {
		fn(p)
	}
}
