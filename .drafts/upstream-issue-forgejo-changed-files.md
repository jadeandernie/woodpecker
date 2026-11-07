# Upstream issue draft: Forgejo/Gitea push changed files include merged-in base branch changes

Repository: woodpecker-ci/woodpecker, template "Bug report". Component: server.

**Title:** Forgejo/Gitea: merging a pull request reports every file the base branch changed since the branch point

---

### Describe the bug

For a branch push, the Forgejo and Gitea forges build the pipeline's changed files from the webhook payload. `getChangedFilesFromPushHook` (`server/forge/forgejo/helper.go`, same in `server/forge/gitea/helper.go`) unions the `added`, `removed` and `modified` lists of every commit in the payload.

When a pull request is merged with a merge commit, the push payload lists every commit of the PR branch. If the branch merged the base branch while it was open ("Update branch", or `git merge main`), those merge commits list every file the base branch changed since the branch point. All of them end up in the pipeline's changed files, although none of them changed on the base branch with this push.

In one case, merging a PR with 26 commits, 6 of them merges of `main`, gave the pipeline 2802 changed files. The merge commit's first-parent diff, which is what the push actually changed on `main`, has 143 files. Every path-filtered workflow ran, and `CI_PIPELINE_FILES` was useless.

### Steps to reproduce

1. Create a branch from `main` and change `a.txt`.
2. Change `b.txt` on `main`.
3. Merge `main` into the branch, then merge the branch into `main` with a merge commit (Forgejo or Gitea PR, merge style "merge").
4. The push pipeline on `main` has `a.txt` and `b.txt` as changed files. Only `a.txt` changed on `main` with this push.

### Expected behavior

The changed files of a branch push are the files that differ between the payload's `before` and `after`, i.e. what the push changed on the branch.

Other forges already do this:

- GitHub compares `before...after` with the REST compare API (#5618).
- Bitbucket Data Center uses its compare changes API (#5748).

### Suggested fix

For branch pushes with a known `before` (not a new branch, not a zero SHA), compute the files from the forge API instead of the payload:

- **Option A (works on all supported versions).** Walk the first-parent chain from `after` back to `before` with the commit API and union each commit's files. For a merge commit the commit API lists its first-parent diff, so a merged PR contributes exactly what it changed on the target branch. Bound the walk (e.g. 50 commits). Fall back to the payload list if the range cannot be resolved (force push, `before` not an ancestor, API error).
- **Option B.** If the forge's compare API returns the combined file list for `before...after`, use it like the GitHub forge does.

Keep the payload list as the fallback, and for tags, new branches and pull requests.

A working patch of option A exists for v3.17. On `main` it needs a rebase onto the hook parsing changes from #7407, and I can open a PR once the approach is agreed.

### Additional context

- Related: #7492 (keep previous file paths of renamed files for Gitea/Forgejo pull requests).
- The same over-reporting happens for a push of several commits that includes a merge of another branch.

### Validations

- [x] Read the docs
- [x] Checked that there isn't already an issue that reports the same bug
- [x] Checked that the bug isn't fixed in the `next` version already
