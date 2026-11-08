# Handoff: pipeline filters, kinds, foundations, test reports, cache

Branch `claude/pipeline-filters-kinds`, based on `v3.20-fork`, which is the branch the Woodpecker at
http://192.168.1.155:8091 runs (not `main`). It is pushed to nas-forgejo (`jade/woodpecker`) and to GitHub
`jadeandernie/woodpecker`. Never push to `origin`, which is upstream `woodpecker-ci`.

## What Jade asked for (2026-11-08)

1. Filters on the repo pipeline list (`/repos/1`) that support complex queries: status, date range, run time, commit
   range, service group, type (build/test/deploy), branch, author, and more.
2. Build, test and deploy as a fundamental kind of workflow, each shown with its own symbol.
3. A "pipeline foundation": one clone and one postgres/clickhouse set up once and shared by every member workflow,
   with `before`/`after` hooks (reset or seed the db) and lanes for parallel members, so less work is wasted.
4. A Tests tab:
   - failing tests listed one by one, each linked to its code in Forgejo;
   - each test's history (last run, pass rate, run time, date added);
   - tests grouped by service, with search and filters.
5. A cache between runs keyed by a hash of the inputs: dependency caches, build outputs, and skipping steps whose
   inputs already passed. It replaces cognitiveos's trusted named volumes and `ci-run-if-needed.sh`.

## Status by area

Each area has its own file here, written by the agent that worked on it.

| Area | State | Notes |
| --- | --- | --- |
| Kind field (`kind:` in YAML, `workflows.kind`, `kinds` on the pipeline API) | done | commits a2889225a, 31e572e46 |
| Kind symbols in the web UI, more test name words, docs | done | 71660ea98, 2263c5268, 2db9b0a8d; not checked visually |
| Query filters | see `filters.md` | |
| Foundations | see `foundation.md` | runtime failure scoping may need an agent upgrade |
| Test reports / Tests tab | see `tests.md` | phase 1 is server and UI; phase 2 is `reports:` collection on agents plus RPC |
| Cache | see `cache.md` | |

If an area's file is missing, its agent stopped before writing one: read `git log` and `git status` for that area.

## Before merging into v3.20-fork

- Regenerate the OpenAPI docs: `cmd/server/openapi/docs.go`, via swag through the Makefile.
- Run `go test -tags test ./...` on the touched packages. `web/dist/index.html` must exist for the server to build;
  it is gitignored.
- In `web/`: `npx -y pnpm@12.15.0 install --frozen-lockfile`, then `vue-tsc --noEmit`, `lint`, `vitest run` and
  `build`. Use pnpm 12: the lockfile has two YAML documents.
- Deploy the server, and also the agents if runtime or agent code changed.
- Check `/repos/1` in a browser. It needs a Forgejo sign-in.
- Delete this `handoff/` directory before the merge.

## Follow-up in cognitiveos (a separate repo)

`tools/woodpecker/generate-service-pipelines.py` should:
- emit a foundation (one clone and one postgres/clickhouse);
- emit `kind:` on each workflow;
- emit `reports:` (`go test -json` or JUnit);
- emit `cache:` in place of the named go-mod/go-build volumes.
