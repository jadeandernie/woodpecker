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

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
)

// memoryPeakWait is how long WaitStep waits for the stats stream to end after
// the container stopped.
const memoryPeakWait = 2 * time.Second

// memoryPeak follows a container's memory use while it runs and keeps the
// highest value seen, so the step can report its peak memory use.
type memoryPeak struct {
	peak atomic.Int64
	done chan struct{}
}

// watchMemory streams the container's stats until ctx is done or the
// container exits. Errors only lose the measurement, never the step.
func watchMemory(ctx context.Context, cl client.APIClient, containerName string) *memoryPeak {
	m := &memoryPeak{done: make(chan struct{})}
	go func() {
		defer close(m.done)
		res, err := cl.ContainerStats(ctx, containerName, client.ContainerStatsOptions{Stream: true})
		if err != nil {
			log.Debug().Err(err).Str("container", containerName).Msg("cannot follow memory use")
			return
		}
		defer res.Body.Close()
		m.follow(res.Body)
	}()
	return m
}

// follow decodes a stream of stats samples and keeps the highest memory use.
func (m *memoryPeak) follow(stream io.Reader) {
	dec := json.NewDecoder(stream)
	for {
		var stats container.StatsResponse
		if err := dec.Decode(&stats); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				log.Trace().Err(err).Msg("memory stats stream ended")
			}
			return
		}
		if used := memoryUsed(stats.MemoryStats); used > m.peak.Load() {
			m.peak.Store(used)
		}
	}
}

// memoryUsed is the memory a container uses, as `docker stats` shows it: the
// usage without the page cache the kernel can reclaim. cgroup v1 also reports
// the highest usage itself.
func memoryUsed(s container.MemoryStats) int64 {
	if s.MaxUsage > 0 {
		return int64(s.MaxUsage) //nolint:gosec // memory sizes fit in int64
	}
	if s.PrivateWorkingSet > 0 { // windows
		return int64(s.PrivateWorkingSet) //nolint:gosec // memory sizes fit in int64
	}
	used := s.Usage
	cache := s.Stats["inactive_file"] // cgroup v2
	if cache == 0 {
		cache = s.Stats["total_inactive_file"] // cgroup v1
	}
	if cache < used {
		used -= cache
	}
	return int64(used) //nolint:gosec // memory sizes fit in int64
}

// Peak waits for the stream to end and returns the highest memory use seen.
func (m *memoryPeak) Peak(ctx context.Context) int64 {
	select {
	case <-m.done:
	case <-ctx.Done():
	}
	return m.peak.Load()
}
