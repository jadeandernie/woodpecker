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

package testreport

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
)

// serviceDirs are the directory names whose child directory names a service
// or library in a monorepo, e.g. app/knowledge/go or lib/auth/go.
var serviceDirs = map[string]bool{"app": true, "apps": true, "lib": true, "libs": true, "services": true, "packages": true}

// ServiceGroup returns the service a repo-relative path belongs to: the
// directory below app/ or lib/ (or apps/, libs/, services/, packages/), or
// "" when the path has none. isFile says whether the last element is a file.
func ServiceGroup(p string, isFile bool) string {
	segs := strings.Split(cleanPath(p), "/")
	last := len(segs)
	if isFile {
		last--
	}
	for i := 0; i+1 < last; i++ {
		if serviceDirs[segs[i]] && segs[i+1] != "" {
			return segs[i+1]
		}
	}
	return ""
}

var goMajorVersion = regexp.MustCompile(`^v[0-9]+$`)

// GoPackageDir maps a Go import path to its repo-relative directory. With a
// go.mod among the reports whose module path prefixes the import path the
// mapping is exact. Without one it is a guess: the path from its first app/
// or lib/ (etc.) element on, or the path after an element named like the
// repo (and a major version like v3). It returns "" when neither applies.
func GoPackageDir(pkg string, modules []GoModule, repoName string) (dir string, exact bool) {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return "", false
	}
	best := -1
	for i, mod := range modules {
		if (pkg == mod.Path || strings.HasPrefix(pkg, mod.Path+"/")) && (best < 0 || len(mod.Path) > len(modules[best].Path)) {
			best = i
		}
	}
	if best >= 0 {
		rest := strings.TrimPrefix(strings.TrimPrefix(pkg, modules[best].Path), "/")
		return cleanPath(path.Join(modules[best].Dir, rest)), true
	}

	segs := strings.Split(pkg, "/")
	// the first element is the module's host or name, never a repo directory
	for i := 1; i+1 < len(segs); i++ {
		if serviceDirs[segs[i]] {
			return strings.Join(segs[i:], "/"), false
		}
	}
	if repoName != "" {
		for i := len(segs) - 1; i >= 0; i-- {
			if !strings.EqualFold(segs[i], repoName) {
				continue
			}
			rest := segs[i+1:]
			if len(rest) > 0 && goMajorVersion.MatchString(rest[0]) {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				return "", false
			}
			return strings.Join(rest, "/"), false
		}
	}
	return "", false
}

// NormalizeFile makes a file path from a report repo-relative: an absolute
// path keeps what follows its last element named like the repo; one that
// has none is dropped, since it cannot be placed in the repo.
func NormalizeFile(file, repoName string) string {
	file = strings.ReplaceAll(strings.TrimSpace(file), "\\", "/")
	if file == "" {
		return ""
	}
	if strings.HasPrefix(file, "/") || (len(file) > 2 && file[1] == ':' && file[2] == '/') {
		segs := strings.Split(file, "/")
		for i := len(segs) - 2; i >= 0; i-- {
			if repoName != "" && strings.EqualFold(segs[i], repoName) {
				return cleanPath(strings.Join(segs[i+1:], "/"))
			}
		}
		return ""
	}
	p := cleanPath(file)
	if strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

// CodeLinks are the links from a test to its code.
type CodeLinks struct {
	// URL links to the test's file (and line) when Exact, else to its
	// package's directory; "" when neither is known.
	URL   string
	Exact bool
	// SearchURL searches the repo for the test's definition, "" where the
	// forge has no code search link.
	SearchURL string
}

// TestCodeLinks builds the links of a test case at a commit (or, without a
// commit, a branch) of a repo in a forge of the given type.
func TestCodeLinks(forgeType model.ForgeType, repoURL, commit, branch string, tc *model.TestCase) CodeLinks {
	repoURL = strings.TrimRight(repoURL, "/")
	if repoURL == "" || tc == nil {
		return CodeLinks{}
	}
	var links CodeLinks
	switch {
	case tc.File != "":
		links.URL = sourceURL(forgeType, repoURL, commit, branch, tc.File, tc.Line, false)
		links.Exact = links.URL != ""
	case tc.Dir != "":
		links.URL = sourceURL(forgeType, repoURL, commit, branch, tc.Dir, 0, true)
	}
	links.SearchURL = searchURL(forgeType, repoURL, searchTerm(tc))
	return links
}

// searchTerm is what finds a test's definition: `func TestX(` for a Go test
// (its top-level test for a subtest), else its name.
func searchTerm(tc *model.TestCase) string {
	name := tc.Name
	if tc.Name == PackageTestName {
		return ""
	}
	if isGoTestCase(tc) {
		top, _, _ := strings.Cut(name, "/")
		return fmt.Sprintf("func %s(", top)
	}
	return name
}

var goTestName = regexp.MustCompile(`^(Test|Benchmark|Example|Fuzz)[A-Z0-9_]?\w*(/.*)?$`)

func isGoTestCase(tc *model.TestCase) bool {
	return goTestName.MatchString(tc.Name) && (looksLikeGoImportPath(tc.Suite) || strings.HasSuffix(tc.File, "_test.go") || tc.Dir != "")
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func sourceURL(forgeType model.ForgeType, repoURL, commit, branch, file string, line int, dir bool) string {
	ref := commit
	if ref == "" {
		ref = branch
	}
	if ref == "" {
		return ""
	}
	p := escapePath(file)
	r := url.PathEscape(ref)
	switch forgeType {
	case model.ForgeTypeGithub:
		kind := "blob"
		if dir {
			kind = "tree"
		}
		return withLine(fmt.Sprintf("%s/%s/%s/%s", repoURL, kind, r, p), line, "#L")
	case model.ForgeTypeGitlab:
		kind := "blob"
		if dir {
			kind = "tree"
		}
		return withLine(fmt.Sprintf("%s/-/%s/%s/%s", repoURL, kind, r, p), line, "#L")
	case model.ForgeTypeBitbucket:
		return withLine(fmt.Sprintf("%s/src/%s/%s", repoURL, r, p), line, "#lines-")
	case model.ForgeTypeBitbucketDatacenter:
		return withLine(fmt.Sprintf("%s/browse/%s?at=%s", repoURL, p, url.QueryEscape(ref)), line, "#")
	case model.ForgeTypeAddon:
		return ""
	default: // gitea, forgejo
		kind := "commit"
		if commit == "" {
			kind = "branch"
		}
		return withLine(fmt.Sprintf("%s/src/%s/%s/%s", repoURL, kind, r, p), line, "#L")
	}
}

func withLine(u string, line int, anchor string) string {
	if line > 0 {
		return fmt.Sprintf("%s%s%d", u, anchor, line)
	}
	return u
}

func searchURL(forgeType model.ForgeType, repoURL, term string) string {
	if term == "" {
		return ""
	}
	q := url.QueryEscape(term)
	switch forgeType {
	case model.ForgeTypeGitea, model.ForgeTypeForgejo:
		// needs the forge's repository indexer
		return fmt.Sprintf("%s/search?q=%s", repoURL, q)
	case model.ForgeTypeGitlab:
		return fmt.Sprintf("%s/-/search?scope=blobs&search=%s", repoURL, q)
	case model.ForgeTypeGithub:
		u, err := url.Parse(repoURL)
		if err != nil || u.Host == "" {
			return ""
		}
		fullName := strings.Trim(u.Path, "/")
		return fmt.Sprintf("%s://%s/search?type=code&q=%s", u.Scheme, u.Host,
			url.QueryEscape(fmt.Sprintf("repo:%s %q", fullName, term)))
	default:
		return ""
	}
}
