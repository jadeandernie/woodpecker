// Copyright 2026 Woodpecker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package base

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.yaml.in/yaml/v4"
)

type structDuration struct {
	Timeout Duration `yaml:"timeout,omitempty"`
}

func TestDurationYaml(t *testing.T) {
	got := structDuration{}
	assert.NoError(t, yaml.Unmarshal([]byte(`timeout: 30s`), &got))
	assert.Equal(t, Duration(30*time.Second), got.Timeout)
}

func TestDurationYamlInvalid(t *testing.T) {
	got := structDuration{}
	assert.Error(t, yaml.Unmarshal([]byte(`timeout: bananas`), &got))
}

func TestDurationYamlNegative(t *testing.T) {
	got := structDuration{}
	assert.Error(t, yaml.Unmarshal([]byte(`timeout: -1s`), &got))
}
