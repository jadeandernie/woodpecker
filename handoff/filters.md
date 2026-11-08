# Handoff: pipeline list filters

Branch `claude/pipeline-filters-kinds`, worktree `/Users/jade/dev/woodpecker-pipeline-kinds`.
Other agents share this worktree, so commit only your own paths with `git commit -- <paths>`.

## Done (committed)

- `af5f39782` The server filters the pipeline list by a query and lists the kinds of each pipeline.
  - `server/model/pipeline_query.go` (+ `_test.go`): `ParsePipelineQuery(q, now)`, `QueryGlobMatch`, `PipelineFacets`.
  - `server/model/pipeline.go`: `PipelineFilter.Query`, `WorkflowKinds()`.
  - `server/store/datastore/pipeline_query.go` (+ `_test.go`): turns a query into SQL, and `GetPipelineFacets`.
  - `server/store/datastore/pipeline.go`: `GetPipelineList` applies `f.Query`.
  - `server/store/store.go` and `server/store/mocks/mock_Store.go`: add `GetPipelineFacets`.
  - `server/api/pipeline.go` (+ `_test.go`): parses `q` (a bad query gets a 400 whose body is the message), fills `kinds`, and adds the `GetPipelineFacets` handler.
  - `server/router/api.go`: `GET /api/repos/{repo_id}/pipelines/filters`.
- `11a4a1ec6` The Go client filters the pipeline list by a query: `woodpecker-go/woodpecker/repo.go` adds `PipelineListOptions.Query`, sent as `q`.

## Endpoints

- `GET /api/repos/{repo_id}/pipelines?q=...`: returns 400 with a plain-text message for an invalid query, or for a commit prefix that matches no pipeline. Every pipeline now carries `kinds`.
- `GET /api/repos/{repo_id}/pipelines/filters`: returns `{branches, authors, events, kinds, groups, workflows}` taken from the newest 500 pipelines. Branches and authors are newest first; the rest are sorted. Lists are never null.
- `cmd/server/openapi/docs.go` was NOT regenerated. The swag annotations are accurate.

## Query grammar (as implemented in server/model/pipeline_query.go)

- Terms are separated by whitespace and ANDed together.
- `key:v1,v2` ORs its values.
- A leading `-` negates a term.
- `"..."` quotes text and handles `\"` and `\\` escapes.
- A bare word or a quoted phrase is free text. It matches the message or title (case-insensitive substring), or a commit prefix if it is hex.

Keys and aliases:

| Key | Aliases | Notes |
| --- | --- | --- |
| `status` | `is`, `state` | Value aliases: failed/failing→failure, errored→error, passed/succeeded→success, cancelled→canceled. |
| `event` | | `pr`/`pull`→pull_request, `deploy`→deployment. |
| `branch` | | |
| `author` | `sender` | |
| `ref` | | |
| `kind` | `type` | |
| `group` | `service`, `subgroup` | |
| `workflow` | `wf` | |
| `date` | `created` | |
| `after` | `since` | |
| `before` | `until` | |
| `duration` | `runtime` | |
| `commit` | `sha` | |
| `number` | `#N` | |

Value rules:

- **Globs:** `*` works in branch, author, ref, group and workflow. Without `*` the match is exact and case-insensitive (ref without `*` is a substring match).
- **kind:** matches a workflow's declared kind, or, when none is declared, the group derived by the repo's workflow group rules.
- **Ranges:** date, duration and number take `a..b`, `a..`, `..b`, `>x`, `>=x`, `<x` and `<=x`. Ranges include both ends.
- **Dates:**
  - `YYYY-MM-DD`, `today` and `yesterday` are whole UTC days.
  - RFC3339 and `2006-01-02T15:04[:05]` are instants.
  - A relative value (`7d`, `24h`) means "since then".
  - `after:X` means at or after X; `before:X` means before the start of X.
- **Duration:** units w/d/h/m/s, which can be combined (`1h30m`). A single value means "at least". It only matches finished pipelines, even when negated.
- **Commit:** a prefix, or `a..b`, which is a range in pipeline-number order, so the order of the two ends doesn't matter.
- **Errors:** unknown keys suggest a close match. Status and event values are validated.

## Left to do

1. **Web API client.**
   - In `web/src/lib/api/index.ts`, add `q?: string` to the `getPipelineList` opts. Add `getPipelineFacets(repoId)`, which calls `/api/repos/${repoId}/pipelines/filters`.
   - Add a `PipelineFacets` type in a new `web/src/lib/api/types/pipelineFilter.ts`. Don't change the Pipeline or Workflow types.
2. **TS query helpers.**
   - Add `web/src/components/repo/pipeline/filter/query.ts`: tokenize terms (same rules as the server), add/remove/replace a term by key, and quote values that contain spaces, commas or quotes. These feed the chips and the builder.
   - Add a vitest file next to it.
3. **Query bar.** Add `filter/PipelineQueryBar.vue`:
   - a text input where Enter applies the query;
   - the error shown inline;
   - removable chips, one per term.
   - The API client throws `Error("Bad Request: <msg>")`, so strip the `Bad Request: ` prefix before showing it. A global toast will still appear unless `client.ts` changes, and that file isn't ours.
4. **Filter builder.** Add `filter/PipelineFilterBuilder.vue`, a "Filters" popover with:
   - status multi-select;
   - kind toggles: `SvgIcon` with `@mdi/js` `mdiHammerWrench`/`mdiFlask`/`mdiRocketLaunchOutline`, or reuse `~/lib/pipeline-kind` and `kind/KindIcon.vue`;
   - date from/to;
   - duration min/max;
   - branch, author, group and workflow pickers fed by facets;
   - commit from/to;
   - event.
   - It must work in dark mode and at narrow widths. Follow the tile and label classes in `web/src/views/repo/RepoInsights.vue`.
5. **Wire it up.** In `web/src/views/repo/RepoPipelines.vue`:
   - Sync `?q=` with `router.replace`, using the same pattern as RepoInsights' `setQuery`.
   - When q is set, fetch `getPipelineList(repo.id, { q, page, perPage: 50 })` from the server, with "load more" (`hasMore` = a full page came back).
   - When q is empty, keep today's injected `pipelines` and store behaviour.
   - `PipelineList.vue` may need a "no pipelines match" empty state.
6. **i18n.** Add keys only under `repo.pipeline.filter.*` in `web/src/assets/locales/en.json`. Another agent shares that file, so re-read it right before you edit.
7. **Docs.** Add `docs/docs/20-usage/27-pipeline-filters.md`, covering the grammar above with examples.
8. **Optional:** add a CLI `--query` flag in `cli/pipeline/list.go`, and `Kinds` on the woodpecker-go `Pipeline` plus a facets client method.

## How to test

- `go test -tags test ./server/api/ ./server/model/ ./server/router/...`
- `go test -tags test -p 1 ./server/store/datastore/` runs on sqlite. For postgres, set `WOODPECKER_DATABASE_DRIVER=postgres` and `WOODPECKER_DATABASE_DATASOURCE=postgres://...`.
- `go test ./woodpecker-go/woodpecker/ -run TestPipelineList`
- Web, from `web/`:
  - `npx -y pnpm@12.15.0 exec vitest run <file>`
  - `npx -y pnpm@12.15.0 exec vue-tsc --noEmit`
  - `npx -y pnpm@12.15.0 lint`
- To regenerate store mocks, use mockery v3.9.0: `go install github.com/vektra/mockery/v3@v3.9.0`.

All of the Go tests above passed when they were committed: sqlite, plus the datastore tests on postgres 18. `gofmt` and `go vet -tags test` were clean.

## Known broken or unverified

- MySQL is not tested. The SQL uses `LIKE ? ESCAPE '!'`, `LOWER`, `COALESCE`, a quoted `commit` column and `EXISTS` subqueries.
- `go build ./server/...` currently fails in `server/testreport/report.go` (invalid BOM). That file belongs to another agent's uncommitted work, not this feature.
- No web UI yet. Nothing has been tried against the deploy at 192.168.1.155:8091.
- `kind`, `group` and `workflow` filters load the repo's distinct workflow names once per request and match them in Go. That is fine for about 200 names but not checked at large scale.
