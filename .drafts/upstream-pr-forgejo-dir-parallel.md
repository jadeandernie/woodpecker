# Upstream PR draft: load Forgejo and Gitea config folder files in parallel

Repository: woodpecker-ci/woodpecker, base `main`.
Head: `jadeandernie:forgejo-dir-parallel`.
Enable "Allow edits by maintainers".

**Title:** Load Forgejo and Gitea config folder files in parallel

---

Closes #<issue number from upstream-issue-forgejo-dir-parallel.md>

`Dir` in the Forgejo and Gitea forges used to load a config folder's files one after the other through `File`. Each `File` call created a new client, and each new client first requests `/api/v1/version`. A folder with a few hundred files could therefore not load within the pipeline creation cap.

The new `Dir`:

- creates one client for the listing and all files, so the server version is checked once;
- loads the files with at most 10 at a time (`maxParallelFileFetches`), using an `errgroup` like the GitHub forge's REST fallback (#7025);
- keeps the listing order in its result;
- cancels the requests still running through the client's context when a file fails, and returns that file's error (`multi-pipeline cannot get <path>: ...`, as before).

`File` and `Dir` share a `getFile` helper, so a missing file still maps to `ErrConfigNotFound`. Results are unchanged, including `nil` for a folder without files.

### Tests

For both forges:

- `TestDirLoadsFilesInParallel` serves 25 files from a test server. The first requests wait for each other. The test checks that:
  - exactly `maxParallelFileFetches` files are in flight at once;
  - the result keeps the listing order;
  - `/api/v1/version` is requested once.
  On the old code it fails with 1 file in flight and 26 version requests.
- `TestDirFailsIfAFileFails` checks that a failing file fails `Dir` with that file's error, and that the error is not reported as a missing config.

`go test -race ./server/forge/...` passes.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
