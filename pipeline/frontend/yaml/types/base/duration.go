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
	"errors"
	"time"
)

// Duration represents a Go duration string.
type Duration time.Duration

// UnmarshalYAML implements the Unmarshaler interface.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var stringType string
	if err := unmarshal(&stringType); err == nil {
		duration, err := time.ParseDuration(stringType)
		if err != nil {
			return err
		}
		if duration < 0 {
			return errors.New("duration must not be negative")
		}
		*d = Duration(duration)
		return nil
	}

	return errors.New("failed to unmarshal Duration")
}

// MarshalYAML implements custom YAML marshaling.
func (d Duration) MarshalYAML() (any, error) {
	if d == 0 {
		return nil, nil
	}
	return time.Duration(d).String(), nil
}
