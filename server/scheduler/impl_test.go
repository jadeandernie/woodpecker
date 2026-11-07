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

package scheduler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/server/pubsub"
)

type recordingPubSub struct {
	messages []pubsub.Message
}

func (r *recordingPubSub) Publish(_ context.Context, _ pubsub.Topics, message pubsub.Message) error {
	r.messages = append(r.messages, message)
	return nil
}

func (r *recordingPubSub) Subscribe(context.Context, pubsub.Topics, pubsub.Receiver) error {
	return nil
}

func TestPublishPipelineEventCarriesWorkflowGroups(t *testing.T) {
	ps := &recordingPubSub{}
	s := &impl{ps: ps}

	repo := &model.Repo{ID: 1, FullName: "acme/app", IsSCMPrivate: true}
	pipeline := &model.Pipeline{ID: 1, Workflows: []*model.Workflow{
		{Name: "deploy-service-knowledge-store-rpc"},
		{Name: "gate-lint"},
	}}
	require.NoError(t, s.PublishPipelineEvent(t.Context(), repo, pipeline))
	require.Len(t, ps.messages, 1)

	var event model.Event
	require.NoError(t, json.Unmarshal(ps.messages[0].Data, &event))
	require.Len(t, event.Pipeline.Workflows, 2)
	assert.Equal(t, "deploy", event.Pipeline.Workflows[0].Group)
	assert.Equal(t, "knowledge", event.Pipeline.Workflows[0].Subgroup)
	assert.Equal(t, "test", event.Pipeline.Workflows[1].Group)
	assert.Empty(t, event.Pipeline.Workflows[1].Subgroup)
}
