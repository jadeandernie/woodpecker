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
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
)

// ErrNothingToRevert is returned by Revert when the car changed no files, so
// there is nothing to take off the train branch. It is not a conflict: the
// car cannot have broken anything.
var ErrNothingToRevert = errors.New("car changes nothing to revert")

// errUnreversible wraps forge.ErrRevertConflict for patches git cannot apply
// in reverse from text alone.
func errUnreversible(format string, args ...any) error {
	return fmt.Errorf("%w: %s", forge.ErrRevertConflict, fmt.Sprintf(format, args...))
}

// fileDiff is one file's part of a git diff.
type fileDiff struct {
	gitLine string
	headers []string
	hunks   []*hunk
	// unreversible tells why git cannot apply the file's change in reverse
	// from the diff text: a binary change, or a copy, whose reverse is the
	// deletion of a file the diff does not hold.
	unreversible string
	// oldPath and newPath are as git prints them, without the a/ or b/
	// prefix and possibly quoted; empty for /dev/null.
	oldPath, newPath string
}

type hunk struct {
	oldRange, newRange, section string
	lines                       []hunkLine
}

type hunkLine struct {
	text string
	// noEOL is set when a "\ No newline at end of file" marker follows.
	noEOL string
}

// parseDiff splits the output of git diff into files.
func parseDiff(diff string) ([]*fileDiff, error) {
	lines := strings.Split(diff, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var files []*fileDiff
	var cur *fileDiff
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = &fileDiff{gitLine: line}
			files = append(files, cur)
		case strings.HasPrefix(line, "diff --cc ") || strings.HasPrefix(line, "diff --combined "):
			return nil, errUnreversible("combined diff of a merge")
		case cur == nil:
			if strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("unexpected line before the first file: %q", line)
			}
		case strings.HasPrefix(line, "@@ "):
			h, next, err := parseHunk(lines, i)
			if err != nil {
				return nil, err
			}
			cur.hunks = append(cur.hunks, h)
			i = next - 1
		case len(cur.hunks) > 0:
			return nil, fmt.Errorf("unexpected line after a hunk: %q", line)
		case strings.HasPrefix(line, "GIT binary patch") || strings.HasPrefix(line, "Binary files "):
			cur.unreversible = "binary change"
		default:
			cur.headers = append(cur.headers, line)
			switch {
			case strings.HasPrefix(line, "--- "):
				cur.oldPath = stripPrefix(line[4:], "a/")
			case strings.HasPrefix(line, "+++ "):
				cur.newPath = stripPrefix(line[4:], "b/")
			case strings.HasPrefix(line, "rename from "):
				cur.oldPath = strings.TrimPrefix(line, "rename from ")
			case strings.HasPrefix(line, "rename to "):
				cur.newPath = strings.TrimPrefix(line, "rename to ")
			case strings.HasPrefix(line, "copy from "):
				cur.oldPath = strings.TrimPrefix(line, "copy from ")
				cur.unreversible = "copy"
			case strings.HasPrefix(line, "copy to "):
				cur.newPath = strings.TrimPrefix(line, "copy to ")
				cur.unreversible = "copy"
			}
		}
	}
	for _, f := range files {
		if f.oldPath == "" && f.newPath == "" && !hasHeader(f, "new file mode ") && !hasHeader(f, "deleted file mode ") {
			// A mode change only: both sides have the path of the git line.
			p := gitLinePath(f.gitLine)
			f.oldPath, f.newPath = p, p
		}
	}
	return files, nil
}

func hasHeader(f *fileDiff, prefix string) bool {
	for _, h := range f.headers {
		if strings.HasPrefix(h, prefix) {
			return true
		}
	}
	return false
}

// stripPrefix removes git's a/ or b/ prefix from a ---/+++ path and the tab
// git appends to paths with spaces; /dev/null becomes "".
func stripPrefix(p, prefix string) string {
	if i := strings.IndexByte(p, '\t'); i >= 0 && !strings.HasPrefix(p, `"`) {
		p = p[:i]
	}
	switch {
	case p == "/dev/null":
		return ""
	case strings.HasPrefix(p, `"`+prefix):
		return `"` + p[len(prefix)+1:]
	default:
		return strings.TrimPrefix(p, prefix)
	}
}

// gitLinePath returns the path of a "diff --git a/P b/P" line whose two
// paths are equal.
func gitLinePath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if strings.HasPrefix(rest, `"`) {
		if q, err := strconv.QuotedPrefix(rest); err == nil {
			return `"` + strings.TrimPrefix(q[1:], "a/")
		}
	}
	if p := (len(rest) - 5) / 2; p > 0 && len(rest) == 2*p+5 && rest[2+p:2+p+3] == " b/" {
		return rest[2 : 2+p]
	}
	if i := strings.Index(rest, " b/"); i >= 0 {
		return strings.TrimPrefix(rest[:i], "a/")
	}
	return rest
}

// parseHunk reads the hunk that starts at lines[start] by its line counts and
// returns it with the index of the line after it.
func parseHunk(lines []string, start int) (*hunk, int, error) {
	header := lines[start]
	end := strings.Index(header[3:], " @@")
	if end < 0 {
		return nil, 0, fmt.Errorf("bad hunk header %q", header)
	}
	ranges := strings.Fields(header[3 : 3+end])
	if len(ranges) != 2 || !strings.HasPrefix(ranges[0], "-") || !strings.HasPrefix(ranges[1], "+") {
		return nil, 0, fmt.Errorf("bad hunk header %q", header)
	}
	oldCount, err := rangeCount(ranges[0][1:])
	if err != nil {
		return nil, 0, fmt.Errorf("bad hunk header %q: %w", header, err)
	}
	newCount, err := rangeCount(ranges[1][1:])
	if err != nil {
		return nil, 0, fmt.Errorf("bad hunk header %q: %w", header, err)
	}
	h := &hunk{oldRange: ranges[0][1:], newRange: ranges[1][1:], section: header[3+end+3:]}
	i := start + 1
	for oldCount > 0 || newCount > 0 {
		if i >= len(lines) {
			return nil, 0, fmt.Errorf("hunk %q ends early", header)
		}
		line := lines[i]
		switch {
		case line == "" || line[0] == ' ':
			// git prints an empty context line as " "; tolerate a stripped one.
			if line == "" {
				line = " "
			}
			oldCount--
			newCount--
		case line[0] == '-':
			oldCount--
		case line[0] == '+':
			newCount--
		case line[0] == '\\':
			if len(h.lines) == 0 {
				return nil, 0, fmt.Errorf("hunk %q: marker without a line", header)
			}
			h.lines[len(h.lines)-1].noEOL = line
			i++
			continue
		default:
			return nil, 0, fmt.Errorf("hunk %q: unexpected line %q", header, line)
		}
		if oldCount < 0 || newCount < 0 {
			return nil, 0, fmt.Errorf("hunk %q has more lines than its header says", header)
		}
		h.lines = append(h.lines, hunkLine{text: line})
		i++
	}
	if i < len(lines) && strings.HasPrefix(lines[i], `\`) && len(h.lines) > 0 {
		h.lines[len(h.lines)-1].noEOL = lines[i]
		i++
	}
	return h, i, nil
}

// rangeCount returns the line count of a hunk range "start[,count]".
func rangeCount(r string) (int, error) {
	_, count, found := strings.Cut(r, ",")
	if !found {
		return 1, nil
	}
	return strconv.Atoi(count)
}

// ReversePatch returns the patch that undoes diff, the output of git diff.
// It returns an error wrapping forge.ErrRevertConflict for changes it cannot
// reverse from text (binary files, copies, combined diffs) and ErrNothingToRevert
// for an empty diff.
func ReversePatch(diff string) (string, error) {
	files, err := parseDiff(diff)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", ErrNothingToRevert
	}
	for _, f := range files {
		if f.unreversible != "" {
			return "", errUnreversible("%s: %s", f.unreversible, f.gitLine)
		}
	}
	var b strings.Builder
	for _, f := range files {
		reverseFile(&b, f)
	}
	return b.String(), nil
}

func reverseFile(b *strings.Builder, f *fileDiff) {
	gitLine := f.gitLine
	if hasHeader(f, "rename from ") {
		gitLine = "diff --git " + withPrefix("a/", f.newPath) + " " + withPrefix("b/", f.oldPath)
	}
	b.WriteString(gitLine)
	b.WriteByte('\n')

	headers := make([]string, len(f.headers))
	minus, plus := -1, -1
	for i, h := range f.headers {
		switch {
		case strings.HasPrefix(h, "old mode "):
			headers[i] = "new mode " + strings.TrimPrefix(h, "old mode ")
		case strings.HasPrefix(h, "new mode "):
			headers[i] = "old mode " + strings.TrimPrefix(h, "new mode ")
		case strings.HasPrefix(h, "new file mode "):
			headers[i] = "deleted file mode " + strings.TrimPrefix(h, "new file mode ")
		case strings.HasPrefix(h, "deleted file mode "):
			headers[i] = "new file mode " + strings.TrimPrefix(h, "deleted file mode ")
		case strings.HasPrefix(h, "rename from "):
			headers[i] = "rename to " + strings.TrimPrefix(h, "rename from ")
		case strings.HasPrefix(h, "rename to "):
			headers[i] = "rename from " + strings.TrimPrefix(h, "rename to ")
		case strings.HasPrefix(h, "index "):
			headers[i] = reverseIndex(h)
		case strings.HasPrefix(h, "--- "):
			minus = i
		case strings.HasPrefix(h, "+++ "):
			plus = i
		default:
			headers[i] = h
		}
	}
	if minus >= 0 && plus >= 0 {
		headers[minus] = "--- " + swapPrefix(f.headers[plus][4:])
		headers[plus] = "+++ " + swapPrefix(f.headers[minus][4:])
	}
	orderPair(headers, "old mode ", "new mode ")
	orderPair(headers, "rename from ", "rename to ")
	for _, h := range headers {
		b.WriteString(h)
		b.WriteByte('\n')
	}

	for _, h := range f.hunks {
		b.WriteString("@@ -" + h.newRange + " +" + h.oldRange + " @@" + h.section + "\n")
		writeReversedLines(b, h.lines)
	}
}

// writeReversedLines swaps added and removed lines; in each run of changed
// lines the removed ones come first, as git prints them.
func writeReversedLines(b *strings.Builder, lines []hunkLine) {
	write := func(l hunkLine, sign byte) {
		b.WriteByte(sign)
		b.WriteString(l.text[1:])
		b.WriteByte('\n')
		if l.noEOL != "" {
			b.WriteString(l.noEOL)
			b.WriteByte('\n')
		}
	}
	for i := 0; i < len(lines); {
		if lines[i].text[0] == ' ' {
			write(lines[i], ' ')
			i++
			continue
		}
		j := i
		for j < len(lines) && lines[j].text[0] != ' ' {
			j++
		}
		for _, l := range lines[i:j] {
			if l.text[0] == '+' {
				write(l, '-')
			}
		}
		for _, l := range lines[i:j] {
			if l.text[0] == '-' {
				write(l, '+')
			}
		}
		i = j
	}
}

// orderPair puts the line starting with first before the one starting with
// second.
func orderPair(headers []string, first, second string) {
	fi, si := -1, -1
	for i, h := range headers {
		if strings.HasPrefix(h, first) {
			fi = i
		}
		if strings.HasPrefix(h, second) {
			si = i
		}
	}
	if fi >= 0 && si >= 0 && si < fi {
		headers[fi], headers[si] = headers[si], headers[fi]
	}
}

// reverseIndex turns "index a..b[ mode]" into "index b..a[ mode]".
func reverseIndex(h string) string {
	rest := strings.TrimPrefix(h, "index ")
	ids, mode, _ := strings.Cut(rest, " ")
	from, to, ok := strings.Cut(ids, "..")
	if !ok {
		return h
	}
	out := "index " + to + ".." + from
	if mode != "" {
		out += " " + mode
	}
	return out
}

// swapPrefix turns a b/ path of a ---/+++ line into an a/ path and back.
func swapPrefix(p string) string {
	for _, pair := range [][2]string{{"a/", "b/"}, {"b/", "a/"}, {`"a/`, `"b/`}, {`"b/`, `"a/`}} {
		if strings.HasPrefix(p, pair[0]) {
			return pair[1] + p[len(pair[0]):]
		}
	}
	return p
}

// withPrefix puts git's a/ or b/ in front of a possibly quoted path.
func withPrefix(prefix, p string) string {
	if strings.HasPrefix(p, `"`) {
		return `"` + prefix + p[1:]
	}
	return prefix + p
}

// unquote decodes a path git quoted because of special characters.
func unquote(p string) string {
	if strings.HasPrefix(p, `"`) {
		if s, err := strconv.Unquote(p); err == nil {
			return s
		}
	}
	return p
}

// DiffFiles returns the paths a git diff touches, old and new names of
// renames included, in the order they appear.
func DiffFiles(diff string) ([]string, error) {
	files, err := parseDiff(diff)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p = unquote(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, f := range files {
		if f.oldPath == "" && f.newPath == "" {
			add(gitLinePath(f.gitLine))
			continue
		}
		add(f.oldPath)
		add(f.newPath)
	}
	return out, nil
}
