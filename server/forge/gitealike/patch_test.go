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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.woodpecker-ci.org/woodpecker/v3/server/forge"
)

func lines(l ...string) string { return strings.Join(l, "\n") + "\n" }

func TestReversePatch(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			name: "modify with multiple hunks",
			in: lines(
				"diff --git a/main.go b/main.go",
				"index 1111111..2222222 100644",
				"--- a/main.go",
				"+++ b/main.go",
				"@@ -1,3 +1,3 @@ package main",
				" a",
				"-b",
				"+B",
				" c",
				"@@ -10,2 +10,3 @@ func x() {",
				" j",
				"+k1",
				"+k2",
				"-k",
			),
			want: lines(
				"diff --git a/main.go b/main.go",
				"index 2222222..1111111 100644",
				"--- a/main.go",
				"+++ b/main.go",
				"@@ -1,3 +1,3 @@ package main",
				" a",
				"-B",
				"+b",
				" c",
				"@@ -10,3 +10,2 @@ func x() {",
				" j",
				"-k1",
				"-k2",
				"+k",
			),
		},
		{
			name: "added file becomes a deletion",
			in: lines(
				"diff --git a/new.txt b/new.txt",
				"new file mode 100644",
				"index 0000000..3333333",
				"--- /dev/null",
				"+++ b/new.txt",
				"@@ -0,0 +1,2 @@",
				"+one",
				"+two",
			),
			want: lines(
				"diff --git a/new.txt b/new.txt",
				"deleted file mode 100644",
				"index 3333333..0000000",
				"--- a/new.txt",
				"+++ /dev/null",
				"@@ -1,2 +0,0 @@",
				"-one",
				"-two",
			),
		},
		{
			name: "deleted file becomes an addition",
			in: lines(
				"diff --git a/old.txt b/old.txt",
				"deleted file mode 100755",
				"index 4444444..0000000",
				"--- a/old.txt",
				"+++ /dev/null",
				"@@ -1 +0,0 @@",
				"-gone",
			),
			want: lines(
				"diff --git a/old.txt b/old.txt",
				"new file mode 100755",
				"index 0000000..4444444",
				"--- /dev/null",
				"+++ b/old.txt",
				"@@ -0,0 +1 @@",
				"+gone",
			),
		},
		{
			name: "rename with a change and a mode change",
			in: lines(
				"diff --git a/src/a.go b/lib/b.go",
				"old mode 100644",
				"new mode 100755",
				"similarity index 90%",
				"rename from src/a.go",
				"rename to lib/b.go",
				"index 5555555..6666666",
				"--- a/src/a.go",
				"+++ b/lib/b.go",
				"@@ -1 +1 @@",
				"-x",
				"+y",
			),
			want: lines(
				"diff --git a/lib/b.go b/src/a.go",
				"old mode 100755",
				"new mode 100644",
				"similarity index 90%",
				"rename from lib/b.go",
				"rename to src/a.go",
				"index 6666666..5555555",
				"--- a/lib/b.go",
				"+++ b/src/a.go",
				"@@ -1 +1 @@",
				"-y",
				"+x",
			),
		},
		{
			name: "pure rename",
			in: lines(
				"diff --git a/a b/b",
				"similarity index 100%",
				"rename from a",
				"rename to b",
			),
			want: lines(
				"diff --git a/b b/a",
				"similarity index 100%",
				"rename from b",
				"rename to a",
			),
		},
		{
			name: "no newline at end of file moves with its line",
			in: lines(
				"diff --git a/f b/f",
				"index 7777777..8888888 100644",
				"--- a/f",
				"+++ b/f",
				"@@ -1,2 +1,2 @@",
				" a",
				"-b",
				`\ No newline at end of file`,
				"+b",
			),
			want: lines(
				"diff --git a/f b/f",
				"index 8888888..7777777 100644",
				"--- a/f",
				"+++ b/f",
				"@@ -1,2 +1,2 @@",
				" a",
				"-b",
				"+b",
				`\ No newline at end of file`,
			),
		},
		{
			name: "removed line that looks like a header",
			in: lines(
				"diff --git a/doc.md b/doc.md",
				"index 9999999..aaaaaaa 100644",
				"--- a/doc.md",
				"+++ b/doc.md",
				"@@ -1,2 +1 @@",
				"--- a/x",
				" keep",
			),
			want: lines(
				"diff --git a/doc.md b/doc.md",
				"index aaaaaaa..9999999 100644",
				"--- a/doc.md",
				"+++ b/doc.md",
				"@@ -1 +1,2 @@",
				"+-- a/x",
				" keep",
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReversePatch(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReversePatchErrors(t *testing.T) {
	for name, in := range map[string]string{
		"binary files differ": lines(
			"diff --git a/img.png b/img.png",
			"index 1..2 100644",
			"Binary files a/img.png and b/img.png differ",
		),
		"git binary patch": lines(
			"diff --git a/img.png b/img.png",
			"index 1..2 100644",
			"GIT binary patch",
			"literal 1",
			"Ic${kh0RRB_0RR91",
		),
		"copy": lines(
			"diff --git a/a b/c",
			"similarity index 100%",
			"copy from a",
			"copy to c",
		),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReversePatch(in)
			assert.ErrorIs(t, err, forge.ErrRevertConflict)
		})
	}

	_, err := ReversePatch("")
	assert.ErrorIs(t, err, ErrNothingToRevert)
	assert.NotErrorIs(t, err, forge.ErrRevertConflict)

	_, err = ReversePatch(lines("diff --git a/f b/f", "--- a/f", "+++ b/f", "@@ -1,2 +1,2 @@", " a"))
	assert.Error(t, err, "a hunk shorter than its header")
}

func TestDiffFiles(t *testing.T) {
	files, err := DiffFiles(lines(
		"diff --git a/src/a.go b/lib/b.go",
		"similarity index 100%",
		"rename from src/a.go",
		"rename to lib/b.go",
		"diff --git a/new.txt b/new.txt",
		"new file mode 100644",
		"index 0000000..3333333",
		"--- /dev/null",
		"+++ b/new.txt",
		"@@ -0,0 +1 @@",
		"+one",
		"diff --git a/run.sh b/run.sh",
		"old mode 100644",
		"new mode 100755",
		"diff --git a/img.png b/img.png",
		"Binary files a/img.png and b/img.png differ",
	))
	require.NoError(t, err)
	assert.Equal(t, []string{"src/a.go", "lib/b.go", "new.txt", "run.sh", "img.png"}, files)
}

// TestReversePatchWithGit checks reversed patches against git itself: for each
// change, the reverse of git diff parent..child applied to child must give
// parent back.
func TestReversePatchWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	write := func(name, content string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	git("init", "-q", "-b", "main")
	var body strings.Builder
	for i := range 40 {
		body.WriteString("line ")
		body.WriteString(strings.Repeat("x", i%7))
		body.WriteString("\n")
	}
	write("keep.txt", body.String())
	write("gone.txt", "bye\n")
	write("dir/move me.txt", strings.Repeat("same content\n", 10))
	write("tail.txt", "no newline")
	git("add", "-A")
	git("commit", "-q", "-m", "parent")

	write("keep.txt", strings.Replace(strings.Replace(body.String(), "line x\n", "LINE X\n", 1), "line xxxxxx\n", "", 1)+"added\n")
	require.NoError(t, os.Remove(filepath.Join(dir, "gone.txt")))
	write("new.txt", "hello\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "other"), 0o755))
	git("mv", "dir/move me.txt", "other/moved.txt")
	write("other/moved.txt", strings.Repeat("same content\n", 10)+"one more\n")
	write("tail.txt", "no newline\nnow with one\n")
	require.NoError(t, os.Chmod(filepath.Join(dir, "new.txt"), 0o755))
	git("add", "-A")
	git("commit", "-q", "-m", "child")

	diff := git("diff", "-M", "HEAD~1", "HEAD")
	reversed, err := ReversePatch(diff)
	require.NoError(t, err)
	patchFile := filepath.Join(t.TempDir(), "revert.patch")
	require.NoError(t, os.WriteFile(patchFile, []byte(reversed), 0o644))
	git("apply", "--index", "--check", patchFile)
	git("apply", "--index", patchFile)
	assert.Empty(t, git("diff", "--cached", "HEAD~1"), "reverted tree differs from the parent")
}
