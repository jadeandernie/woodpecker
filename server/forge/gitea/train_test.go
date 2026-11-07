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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

func TestTrainWiring(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(body))
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	f, err := New(1, Opts{URL: srv.URL})
	require.NoError(t, err)
	train, ok := f.(forge.Train)
	require.True(t, ok, "Gitea implements forge.Train")

	u := &model.User{AccessToken: "tok"}
	r := &model.Repo{Owner: "org", Name: "app"}
	require.NoError(t, train.Notify(context.Background(), u, r, &forge.TrainNotice{SHA: "abc", State: model.StatusSuccess}))
	assert.Equal(t, []string{
		`POST /api/v1/repos/org/app/statuses/abc token tok {"context":"train","description":"","state":"success","target_url":""}`,
	}, got)

	assert.Error(t, train.Notify(context.Background(), nil, r, &forge.TrainNotice{SHA: "abc"}))
}
