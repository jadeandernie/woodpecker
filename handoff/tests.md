# Handoff: test reports and the Tests tab

Goal: steps declare report files (`reports:`), the agent uploads them, and the server parses JUnit XML and
`go test -json` and stores one row per test case plus one per run. The web UI gets a repo Tests tab and a pipeline
Tests tab, with links into Forgejo. Repo 1 on http://192.168.1.155:8091 is cognitiveos, whose Go modules are
`dev.jade/app/<service>/go` and `dev.jade/lib/<name>/go`.

Phase 1 is everything on the server and in the web UI. It does not touch `pipeline/`, `rpc/`, `agent/` or
`server/rpc/`. Phase 2 is the YAML key, the agent collection and the RPC upload.

## Phase 1: done (commit 019cc7458)

Everything below builds and passes `go vet`. The datastore tests pass.

- **Model** (`server/model/test_report.go`):
  - `test_cases` has one row per (repo, `suite::name`). It is unique on `repo_id` + `key_hash`, the SHA-256 of
    the key, so MySQL index limits are not an issue.
  - Each case holds denormalized counters: runs, passes, failures, skips, total duration. It also holds the
    `last_*` fields, first-seen data, file, line and dir, the group, kind and workflow, and `recent`: the last 20
    statuses as the letters `p/f/s/e`, used for flaky detection (`Flaky()`).
  - `test_runs` has one row per result: case, pipeline (id and number), workflow, step, branch, commit, status,
    duration, and a message capped at 16 KiB.
  - Both tables are registered in `migration.go` `allBeans`. The filter types and API response types
    (`TestCaseInfo`, `TestList`, `TestSummary`, `TestGroup`, `TestCaseDetail`, `PipelineTests`) are defined, but
    nothing uses the API types yet.
- **Parsers** (`server/testreport/`):
  - `Detect` / `Parse` sniff the format of each file.
  - `ParseJUnit` streams the XML, so a truncated file still yields its cases. It handles nested suites,
    file/line/time attributes, failure/error/skipped, and system-out/err on failures. A Go import path in
    `classname` is used as the package.
  - `ParseGoTest` gives one result per test and subtest. It handles build-output events and removes framing lines
    from messages.
    - A test that never finished becomes `error`.
    - A package that failed without a failing test becomes an `error` case named `(package)`.
    - The file comes from the `xxx_test.go:N:` in a failure's output. Only the base name is used, joined to the
      package directory.
  - A `go.mod` passed among the report files makes package → directory mapping exact.
- **Links** (`links.go`):
  - `ServiceGroup` takes the directory below `app/`, `lib/`, `apps/`, `libs/`, `services/` or `packages/`.
  - `GoPackageDir` is exact with a go.mod. Without one it guesses from `app/` or `lib/` segments, or the repo name.
  - `NormalizeFile` turns absolute paths into repo-relative ones.
  - `TestCodeLinks` builds file/line or directory URLs for Gitea/Forgejo, GitHub, GitLab, Bitbucket and Bitbucket
    DC, plus a code-search URL. For a Go test the search term is `func TestX(`.
- **Ingest** (`ingest.go`): `Ingest(store, repo, pipeline, workflow, step, files)`:
  1. Parses the files.
  2. Takes a per-repo mutex.
  3. Loads existing cases by key hash.
  4. Updates the counters. `last_*`, group, kind and workflow only move forward by pipeline number, so a late
     ingest of an old pipeline does not overwrite newer data.
  5. Saves cases and runs in one transaction.
  6. Prunes, at most once every 10 minutes per repo.
- **Group** order: the path's service group, then the workflow subgroup (`NewWorkflowGrouper`), then the workflow
  name, then `other`.
  - This deviates from the original spec, which put the workflow first. With the path first, a test keeps its
    group whichever workflow ran it.
- **Store** (`server/store/datastore/test_report.go`, 8 methods on `store.Store`, mocks regenerated):
  - list and get cases;
  - load cases by key hash;
  - save a report in one transaction (on error it resets the IDs, so a retry is safe);
  - list runs, newest first, optionally without messages;
  - per-case run stats;
  - the newest pipeline IDs of a repo or branch;
  - prune runs.
- **Retention and deletes**:
  - Pruning keeps the newest 200 pipelines and 90 days of runs (package vars in `ingest.go`). It takes the pruned
    runs off the case counters, deletes cases left with no runs, and recomputes `last_*` and `recent` for cases
    whose last pipeline was removed.
  - `deletePipeline` and `deleteRepo` call the same code. The repo delete runs before the pipeline loop, so
    nothing is recounted per pipeline.
  - `pipeline.go` and `repo.go` get one small hunk each.

## Phase 1: left

1. **API** (`server/api/test_report.go`; routes in the repo group of `server/router/api.go`, next to
   `repo.GET("/stats", ...)`):
   - `GET /api/repos/{repo_id}/tests` with `q`, `group`, `status` (failing / flaky / passing / skipped), `kind`,
     `branch`, `sort` (name / last_run / failures / pass_rate / duration / first_seen), `page` and `per_page`.
     - It returns `TestList`: summary counts, groups with counts (for the sidebar), and the page of
       `TestCaseInfo`.
     - Load the filtered cases with `TestCaseList`, then sort and paginate in Go. Cases per repo are bounded by
       retention.
     - Without `branch`, use the case counters.
     - With `branch`, use `TestRunStats` for the branch's runs, take the last status from the newest run per
       case, and compute flaky from the branch's last 20 pipelines (`TestRunPipelineIDs`).
   - `GET /api/repos/{repo_id}/tests/{test_id}` returns `TestCaseDetail`: the case, the last N runs
     (`TestRunList` with a limit, messages included), and p50 and average duration.
   - `GET /api/repos/{repo_id}/pipelines/{number}/tests` returns `PipelineTests`: the runs of one pipeline, joined
     to their cases, failures first.
   - Code links: `testreport.TestCodeLinks(forge.Type, repo.ForgeURL, commit, branch, tc)`. Use the run's commit
     when there is one, else `tc.LastCommit`.
   - Add swag annotations, regenerate `cmd/server/openapi/docs.go`, and add client methods in
     `woodpecker-go/woodpecker`.
2. **Web**:
   - A `web/src/lib/api/tests.ts` module that uses `ApiClient._get`, with types.
   - A repo route and tab (`router.ts`, `RepoWrapper.vue`), following `RepoInsights.vue`.
     - It shows the groups on the left, with search, status and kind filters.
     - Each row shows a status dot, the name, a `KindIcon`, a pass-rate bar, durations, last run, first seen, a
       strip of the last 20 runs from `recent`, and a link to the code.
     - Clicking a row opens the run history and the failure message.
   - A pipeline Tests tab (`PipelineWrapper.vue`) that lists failures first, each with its message and code link.
   - `en.json` keys under `repo.tests.*`.
   - vitest for the pure TS helpers.
3. **Docs**: `docs/docs/20-usage/29-test-reports.md`, covering the `reports:` key, the formats, `go.mod` for exact
   links, retention, and the Forgejo indexer requirement for search links.
4. **Tests**: table tests for `ParseJUnit` and `ParseGoTest` (including truncated input, build failures, subtests
   and a BOM), plus `GoPackageDir`, `NormalizeFile`, `ServiceGroup`, `TestCodeLinks`, and `apply()` / `Ingest`
   (with a real test store: counters, out-of-order pipelines, prune). `server/testreport` has no tests yet.

## Phase 2: reports from steps

1. **YAML** (`pipeline/frontend/yaml/types/container.go`): add a step key
   `Reports base_types.StringOrSlice \`yaml:"reports,omitempty"\``. It takes paths or globs relative to the
   workspace, for example `reports: [out/go-test.json, "**/junit*.xml", go.mod]`.
   - Add it to the JSON schema (`pipeline/frontend/yaml/linter/schema/schema.json`) and to the linter. The linter
     should reject absolute paths and `..`.
   - Plugins may use it too.
2. **Compiler and backend**:
   - Add `Reports []string \`json:"reports,omitempty"\`` to `backend_types.Step` (`pipeline/backend/types/step.go`).
   - Set it in `createProcess` (`pipeline/frontend/yaml/compiler/convert.go`), resolving relative paths against
     the step's workspace (`workspace.Base` + `workspace.Path`).
   - Add a compiler test.
3. **Agent collection**: add an optional backend interface, so backends without support need no change:

   ```go
   type ReportCollector interface {
       CollectReports(ctx context.Context, step *Step, taskUUID string) ([]ReportFile, error)
   }
   ```

   - Call it from `pipeline/runtime/step.go` `completeStepWithContext`, after `WaitStep` and before
     `DestroyStep`. Call it on success and on failure, because failed runs are what matter most. Skip it when the
     step was killed or canceled.
   - Caps: 10 MiB per file, 32 MiB per step, 200 files. Log a warning when a cap is hit; don't fail the step.
   - Docker (`pipeline/backend/docker`): `CopyFromContainer(ctx, containerID, path)` returns a tar stream. Read
     the regular files from it. For globs, copy the longest directory without glob characters and match the rest
     with `doublestar` (already a dependency). The container has exited but still exists before `DestroyStep`.
   - Local (`pipeline/backend/local`): a plain read from the step's work directory. Resolve symlinks and refuse
     paths outside it.
   - Kubernetes: unsupported for now. Log once per step that `reports:` is ignored on this backend. Possible later
     approaches: a sidecar that tars the files to stdout, or an exec in a keep-alive container.
   - Dummy backend: return fixtures, so runtime tests can cover the path.
4. **RPC** (`rpc/`, `rpc/proto/`):
   - Add `rpc Reports (ReportsRequest) returns (Empty) {}` to `woodpecker.proto`, with
     `message ReportsRequest { string step_uuid = 1; repeated ReportFile files = 2; }` and
     `message ReportFile { string name = 1; bytes data = 2; }`.
   - Bump `rpc/proto/version.go` `Version` from 16 to 17.
   - Regenerate with `go generate ./rpc/proto/`, using the protoc-gen-go and protoc-gen-go-grpc versions from
     `make install-protoc-gen-go`. Keep the generated headers consistent with the existing files.
   - Add `Reports(ctx, stepUUID string, files []ReportFile) error` to `rpc.Peer` (`rpc/peer.go`). Regenerate
     `rpc/mocks`. Implement it in `agent/rpc/client_grpc.go` with the same retry loop as `Log`.
   - The default gRPC message limit is 4 MiB. Either send one request per file (and cap files at 4 MiB), or raise
     `MaxCallSendMsgSize` / `MaxRecvMsgSize` for this call. Per-file requests are simpler.
   - The agent should call it only when the server's version is at least 17, so a new agent against an old server
     just skips it.
5. **Server handler**:
   - In `server/rpc/server.go`, add `WoodpeckerServer.Reports`, which converts the request and calls
     `s.peer.Reports`.
   - In `server/rpc/rpc.go`, add `RPC.Reports`. Model it on `RPC.Log`: look up the step by UUID, then its
     workflow, pipeline and repo, and check that the calling agent owns the workflow (the same check as
     `Update`). Then call `testreport.Ingest(s.store, repo, pipeline, workflow, step, files)`.
   - Ingest takes a per-repo lock and touches the database, so run it synchronously and return its error. The
     agent then retries a store failure; parse warnings are only logged.
   - Add tests in `server/rpc/rpc_test.go` with the store mock.
6. **Deploy**: the server and every agent. Agents on Kubernetes ignore `reports:`.
7. **cognitiveos** (separate repo): `tools/woodpecker/generate-service-pipelines.py` emits, per Go service:
   - `go test -json ./... > $CI_WORKSPACE/.reports/go-test.json || status=$?` (the step must still fail on test
     failure);
   - `reports: [.reports/go-test.json, app/<svc>/go/go.mod]`.

## How to test

- Go: `go test -tags test -p 1 ./server/store/datastore/... ./server/testreport/ ./server/model/`, then `go vet`
  and `gofmt -l server/`. Mocks: mockery v3, with a scratch config limited to `server/store`.
- Before phase 2 exists, try Ingest end to end with a small `-tags test` program or test. It should open a sqlite
  store, create a repo and pipeline, call `testreport.Ingest` with a real `go test -json` file (for example from
  `go test -json ./server/model/`) and the matching `go.mod`, then check the cases, the runs and the code links.
- After phase 2: run a pipeline on repo 1 that writes `go test -json` output with one deliberately failing test.
  Check that the repo Tests tab and the pipeline Tests tab list it, that the link opens the right file and line in
  Forgejo, and that the history fills in over three pipelines.
- Web: `npx -y pnpm@12.15.0 exec vitest run`, `exec vue-tsc --noEmit` and `lint` in `web/`.

## Unverified and known limits

- The parsers and Ingest have no tests and have not been run on real reports. `apply()` compiles, but its logic
  is unexercised.
- Only sqlite has been tested. On MySQL and Postgres, check:
  - the raw counter `UPDATE`s in `testRunsDelete`;
  - the chunked `IN` lists;
  - `TEXT` for `test_key` and `message`;
  - the composite index `test_runs_repo_branch`.
- The group derivation deviates from the spec, as described above.
- Go tests get a file only when they fail, because the file comes from the failure output, and their `Line` stays
  0. A passing Go test links to its package directory plus a search link.
- Forgejo and Gitea search links work only when the forge's repository indexer is enabled (`[indexer]
  REPO_INDEXER_ENABLED = true`).
- Without a `go.mod` in the reports, the package → directory mapping is a guess.
- The per-repo lock assumes a single server instance, which is how Woodpecker runs.
- Pruning runs only after an ingest. A repo that stops reporting keeps its last data until the repo is deleted.
