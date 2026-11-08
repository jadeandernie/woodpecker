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

package model

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The keys of a pipeline query, each the canonical name of a set of aliases.
const (
	PipelineQueryText     = "text"     // bare words: commit message, title or commit SHA prefix
	PipelineQueryStatus   = "status"   // pipeline status
	PipelineQueryEvent    = "event"    // webhook event
	PipelineQueryBranch   = "branch"   // branch, exact or with * globs
	PipelineQueryAuthor   = "author"   // author or sender, exact or with * globs
	PipelineQueryRef      = "ref"      // ref, containing the value or matching * globs
	PipelineQueryKind     = "kind"     // a workflow's kind or group: build, test, deploy, other
	PipelineQueryGroup    = "group"    // a workflow's subgroup (service group)
	PipelineQueryWorkflow = "workflow" // a workflow's name, exact or with * globs
	PipelineQueryDate     = "date"     // creation time (after: and before: are date ranges)
	PipelineQueryDuration = "duration" // finished - started, in seconds
	PipelineQueryCommit   = "commit"   // commit SHA prefix or range of prefixes
	PipelineQueryNumber   = "number"   // pipeline number
)

// pipelineQueryKeys maps every accepted key, alias included, to its canonical key.
var pipelineQueryKeys = map[string]string{
	"status":   PipelineQueryStatus,
	"is":       PipelineQueryStatus,
	"state":    PipelineQueryStatus,
	"event":    PipelineQueryEvent,
	"branch":   PipelineQueryBranch,
	"author":   PipelineQueryAuthor,
	"sender":   PipelineQueryAuthor,
	"ref":      PipelineQueryRef,
	"kind":     PipelineQueryKind,
	"type":     PipelineQueryKind,
	"group":    PipelineQueryGroup,
	"service":  PipelineQueryGroup,
	"subgroup": PipelineQueryGroup,
	"workflow": PipelineQueryWorkflow,
	"wf":       PipelineQueryWorkflow,
	"date":     PipelineQueryDate,
	"created":  PipelineQueryDate,
	"after":    "after",
	"since":    "after",
	"before":   "before",
	"until":    "before",
	"duration": PipelineQueryDuration,
	"runtime":  PipelineQueryDuration,
	"commit":   PipelineQueryCommit,
	"sha":      PipelineQueryCommit,
	"number":   PipelineQueryNumber,
}

var pipelineQueryStatusAliases = map[string]StatusValue{
	"failed":    StatusFailure,
	"failing":   StatusFailure,
	"errored":   StatusError,
	"passed":    StatusSuccess,
	"succeeded": StatusSuccess,
	"cancelled": StatusCanceled,
}

var pipelineQueryEventAliases = map[string]WebhookEvent{
	"pr":     EventPull,
	"pull":   EventPull,
	"deploy": EventDeploy,
}

// PipelineQueryError is a query the server cannot run; its message is meant
// for the user.
type PipelineQueryError struct {
	Msg string
}

func (e *PipelineQueryError) Error() string {
	return e.Msg
}

func queryErrorf(format string, args ...any) error {
	return &PipelineQueryError{Msg: fmt.Sprintf(format, args...)}
}

// PipelineQuery is a parsed pipeline list query: the pipelines it selects
// match every term.
type PipelineQuery struct {
	Terms []*PipelineQueryTerm
}

// PipelineQueryTerm selects the pipelines that match any of its values, or,
// when negated, those that match none of them.
type PipelineQueryTerm struct {
	Key    string // one of the PipelineQuery* keys
	Negate bool
	Values []*PipelineQueryValue
}

// PipelineQueryValue is one of a term's values.
type PipelineQueryValue struct {
	// Text is the value as written, without quotes. For status, event, kind
	// and group it is normalized (lower case, aliases resolved); for a commit
	// range it is the first SHA prefix.
	Text string
	// Min and Max bound date (unix seconds), duration (seconds) and number
	// values, both inclusive; nil is unbounded.
	Min *int64
	Max *int64
	// CommitTo is the second SHA prefix of a commit range, or empty.
	CommitTo string
}

// IsEmpty reports whether the query has no terms.
func (q *PipelineQuery) IsEmpty() bool {
	return q == nil || len(q.Terms) == 0
}

// HasKey reports whether a term of the query has the key.
func (q *PipelineQuery) HasKey(key string) bool {
	if q == nil {
		return false
	}
	for _, term := range q.Terms {
		if term.Key == key {
			return true
		}
	}
	return false
}

// ParsePipelineQuery parses a pipeline list query. Relative dates like 7d are
// relative to now; calendar dates are UTC days. See
// docs/docs/20-usage/27-pipeline-filters.md for the syntax.
func ParsePipelineQuery(query string, now time.Time) (*PipelineQuery, error) {
	tokens, err := tokenizePipelineQuery(query)
	if err != nil {
		return nil, err
	}

	q := &PipelineQuery{}
	for _, tok := range tokens {
		term, err := parsePipelineQueryToken(tok, now.UTC())
		if err != nil {
			return nil, err
		}
		q.Terms = append(q.Terms, term)
	}
	return q, nil
}

// queryToken is a term as written: an optional key, and its values split on
// unquoted commas, quotes removed.
type queryToken struct {
	raw    string
	negate bool
	key    string // empty for free text
	hasKey bool
	values []string
	quoted bool // the free text was quoted
}

func tokenizePipelineQuery(query string) ([]*queryToken, error) {
	var tokens []*queryToken
	runes := []rune(query)
	i := 0
	for i < len(runes) {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}

		start := i
		tok := &queryToken{}
		if runes[i] == '-' && i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			tok.negate = true
			i++
		}

		// A key is a run of letters followed by ':'; '#' starts a number.
		keyEnd := i
		for keyEnd < len(runes) && unicode.IsLetter(runes[keyEnd]) {
			keyEnd++
		}
		switch {
		case keyEnd > i && keyEnd < len(runes) && runes[keyEnd] == ':':
			tok.key = strings.ToLower(string(runes[i:keyEnd]))
			tok.hasKey = true
			i = keyEnd + 1
		case runes[i] == '#':
			tok.key = PipelineQueryNumber
			tok.hasKey = true
			i++
		}

		var cur strings.Builder
		curQuoted := false
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			switch r := runes[i]; {
			case r == '"':
				i++
				closed := false
				for i < len(runes) {
					if runes[i] == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
						cur.WriteRune(runes[i+1])
						i += 2
						continue
					}
					if runes[i] == '"' {
						closed = true
						i++
						break
					}
					cur.WriteRune(runes[i])
					i++
				}
				if !closed {
					return nil, queryErrorf("unterminated quote in %q", string(runes[start:]))
				}
				curQuoted = true
			case r == ',' && tok.hasKey:
				tok.values = append(tok.values, cur.String())
				cur.Reset()
				i++
			default:
				cur.WriteRune(r)
				i++
			}
		}
		tok.values = append(tok.values, cur.String())
		tok.quoted = curQuoted
		tok.raw = string(runes[start:i])
		tokens = append(tokens, tok)
	}
	return tokens, nil
}

func parsePipelineQueryToken(tok *queryToken, now time.Time) (*PipelineQueryTerm, error) {
	if !tok.hasKey {
		text := tok.values[0]
		if text == "" {
			return nil, queryErrorf("empty search text in %q", tok.raw)
		}
		return &PipelineQueryTerm{
			Key:    PipelineQueryText,
			Negate: tok.negate,
			Values: []*PipelineQueryValue{{Text: text}},
		}, nil
	}

	key, ok := pipelineQueryKeys[tok.key]
	if !ok && tok.key != PipelineQueryNumber {
		return nil, unknownKeyError(tok.key)
	}
	if tok.key == PipelineQueryNumber {
		key = PipelineQueryNumber
	}

	displayKey := tok.key
	if len(tok.values) == 1 && tok.values[0] == "" {
		return nil, queryErrorf("filter %q needs a value", displayKey)
	}

	term := &PipelineQueryTerm{Key: key, Negate: tok.negate}
	for _, raw := range tok.values {
		if raw == "" {
			return nil, queryErrorf("empty value in %q", tok.raw)
		}
		value, err := parsePipelineQueryValue(key, displayKey, raw, now)
		if err != nil {
			return nil, err
		}
		term.Values = append(term.Values, value)
	}

	// after: and before: are open date ranges.
	if key == "after" || key == "before" {
		term.Key = PipelineQueryDate
	}
	return term, nil
}

func unknownKeyError(key string) error {
	best, bestDist := "", 3
	keys := make([]string, 0, len(pipelineQueryKeys))
	for k := range pipelineQueryKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if d := editDistance(key, k); d < bestDist {
			best, bestDist = k, d
		}
	}
	if best != "" {
		return queryErrorf("unknown filter key %q (did you mean %q?)", key, best)
	}
	return queryErrorf("unknown filter key %q (quote the term to search for it as text)", key)
}

// editDistance is the optimal string alignment distance of a and b: the
// edits (insert, delete, substitute or swap two adjacent letters) that turn a into b.
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

var (
	queryCommitPrefix = regexp.MustCompile(`^[0-9a-fA-F]{1,64}$`)
	queryWord         = regexp.MustCompile(`^[\p{L}\p{N}_.-]+$`)
)

func parsePipelineQueryValue(key, displayKey, raw string, now time.Time) (*PipelineQueryValue, error) {
	switch key {
	case PipelineQueryStatus:
		s := strings.ToLower(raw)
		status := StatusValue(s)
		if alias, ok := pipelineQueryStatusAliases[s]; ok {
			status = alias
		}
		if status.Validate() != nil {
			return nil, queryErrorf("unknown status %q", raw)
		}
		return &PipelineQueryValue{Text: string(status)}, nil

	case PipelineQueryEvent:
		s := strings.ToLower(raw)
		event := WebhookEvent(s)
		if alias, ok := pipelineQueryEventAliases[s]; ok {
			event = alias
		}
		if event.Validate() != nil {
			return nil, queryErrorf("unknown event %q", raw)
		}
		return &PipelineQueryValue{Text: string(event)}, nil

	case PipelineQueryKind, PipelineQueryGroup:
		s := strings.ToLower(raw)
		if !queryWord.MatchString(s) {
			return nil, queryErrorf("invalid %s %q", displayKey, raw)
		}
		return &PipelineQueryValue{Text: s}, nil

	case PipelineQueryBranch, PipelineQueryAuthor, PipelineQueryRef, PipelineQueryWorkflow:
		return &PipelineQueryValue{Text: raw}, nil

	case PipelineQueryCommit:
		from, to, isRange := strings.Cut(raw, "..")
		if !queryCommitPrefix.MatchString(from) || (isRange && !queryCommitPrefix.MatchString(to)) {
			return nil, queryErrorf("invalid commit %q: use a SHA prefix like abc123 or a range like abc123..def456", raw)
		}
		return &PipelineQueryValue{Text: strings.ToLower(from), CommitTo: strings.ToLower(to)}, nil

	case PipelineQueryNumber:
		return parseQueryRange(raw, func(s string) (int64, int64, bool, error) {
			n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
			if err != nil {
				return 0, 0, false, queryErrorf("invalid pipeline number %q", s)
			}
			return n, n, false, nil
		})

	case PipelineQueryDuration:
		v, err := parseQueryRange(raw, func(s string) (int64, int64, bool, error) {
			d, err := parseQueryDuration(s)
			if err != nil {
				return 0, 0, false, queryErrorf("invalid duration %q: use a unit like 90s, 10m or 1h30m", s)
			}
			secs := int64(d / time.Second)
			return secs, secs, true, nil
		})
		return v, err

	case PipelineQueryDate:
		return parseQueryRange(raw, func(s string) (int64, int64, bool, error) {
			return parseQueryDate(s, now)
		})

	case "after", "before":
		if strings.Contains(raw, "..") || strings.ContainsAny(raw[:1], "<>") {
			return nil, queryErrorf("%s: takes a single date, like %s:2026-11-01 or %s:7d", displayKey, displayKey, displayKey)
		}
		lo, _, _, err := parseQueryDate(raw, now)
		if err != nil {
			return nil, err
		}
		if key == "after" {
			return &PipelineQueryValue{Text: raw, Min: &lo}, nil
		}
		hi := lo - 1
		return &PipelineQueryValue{Text: raw, Max: &hi}, nil
	}
	return nil, queryErrorf("unknown filter key %q", displayKey)
}

// parseQueryRange parses a single value (x), a comparison (>x, >=x, <x, <=x)
// or a range (a..b, a.., ..b). bound gives the inclusive interval [lo, hi] a
// single point stands for, and whether a single value means "at least"
// (open) rather than the interval itself.
func parseQueryRange(raw string, bound func(string) (lo, hi int64, open bool, err error)) (*PipelineQueryValue, error) {
	value := &PipelineQueryValue{Text: raw}
	set := func(dst **int64, v int64) { *dst = &v }

	switch {
	case strings.HasPrefix(raw, ">="), strings.HasPrefix(raw, "<="):
		lo, hi, _, err := bound(raw[2:])
		if err != nil {
			return nil, err
		}
		if raw[0] == '>' {
			set(&value.Min, lo)
		} else {
			set(&value.Max, hi)
		}
	case strings.HasPrefix(raw, ">"), strings.HasPrefix(raw, "<"):
		lo, hi, _, err := bound(raw[1:])
		if err != nil {
			return nil, err
		}
		if raw[0] == '>' {
			set(&value.Min, hi+1)
		} else {
			set(&value.Max, lo-1)
		}
	case strings.Contains(raw, ".."):
		from, to, _ := strings.Cut(raw, "..")
		if from == "" && to == "" {
			return nil, queryErrorf("empty range %q", raw)
		}
		if from != "" {
			lo, _, _, err := bound(from)
			if err != nil {
				return nil, err
			}
			set(&value.Min, lo)
		}
		if to != "" {
			_, hi, _, err := bound(to)
			if err != nil {
				return nil, err
			}
			set(&value.Max, hi)
		}
		if value.Min != nil && value.Max != nil && *value.Min > *value.Max {
			return nil, queryErrorf("range %q ends before it starts", raw)
		}
	default:
		lo, hi, open, err := bound(raw)
		if err != nil {
			return nil, err
		}
		set(&value.Min, lo)
		if !open {
			set(&value.Max, hi)
		}
	}
	return value, nil
}

var queryDurationPart = regexp.MustCompile(`^(\d+(?:\.\d+)?)(w|d|h|m|s)`)

// parseQueryDuration parses durations like 90s, 10m, 1h30m, 2d or 1w.
func parseQueryDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	units := map[string]time.Duration{
		"w": 7 * 24 * time.Hour,
		"d": 24 * time.Hour,
		"h": time.Hour,
		"m": time.Minute,
		"s": time.Second,
	}
	var total time.Duration
	rest := strings.ToLower(s)
	for rest != "" {
		m := queryDurationPart.FindStringSubmatch(rest)
		if m == nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, err
		}
		total += time.Duration(n * float64(units[m[2]]))
		rest = rest[len(m[0]):]
	}
	return total, nil
}

// parseQueryDate gives the interval of unix seconds [lo, hi] a date stands
// for: a whole UTC day for 2026-11-01, today and yesterday; the instant for
// RFC3339 times and relative times like 7d (7 days before now). open is set
// for relative times, so that date:7d means "in the last 7 days".
func parseQueryDate(s string, now time.Time) (lo, hi int64, open bool, err error) {
	day := func(t time.Time) (int64, int64, bool, error) {
		start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return start.Unix(), start.Add(24*time.Hour).Unix() - 1, false, nil
	}

	switch strings.ToLower(s) {
	case "today":
		return day(now)
	case "yesterday":
		return day(now.Add(-24 * time.Hour))
	}

	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return day(t)
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), t.Unix(), false, nil
		}
	}
	if d, err := parseQueryDuration(s); err == nil {
		t := now.Add(-d).Unix()
		return t, t, true, nil
	}
	return 0, 0, false, queryErrorf("invalid date %q: use 2026-11-01, an RFC3339 time or a relative time like 7d or 24h", s)
}

// QueryGlobMatch reports whether a name matches a pattern in which * matches
// any run of characters, ignoring case. A pattern without * must equal the name.
func QueryGlobMatch(pattern, name string) bool {
	pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	name = name[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, part := range parts[1 : len(parts)-1] {
		idx := strings.Index(name, part)
		if idx < 0 {
			return false
		}
		name = name[idx+len(part):]
	}
	return strings.HasSuffix(name, last)
}

// PipelineFacets are the values seen in a repo's recent pipelines, for the
// pickers of the pipeline filter builder.
type PipelineFacets struct {
	Branches  []string       `json:"branches"`
	Authors   []string       `json:"authors"`
	Events    []WebhookEvent `json:"events"`
	Kinds     []string       `json:"kinds"`
	Groups    []string       `json:"groups"`
	Workflows []string       `json:"workflows"`
} //	@name	PipelineFacets
