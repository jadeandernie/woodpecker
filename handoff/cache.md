# Handoff: built-in content-addressed cache (`cache:`)

Status: design only. No code is written yet. The previous agent read the code,
settled the design below and was stopped before implementing it.

Branch `claude/pipeline-filters-kinds` (based on `v3.20-fork`). Other agents
share this worktree: pipeline-list filters, "foundation" (types/foundation.go,
types/workflow.go, compiler/builder, backend/types/step.go, pipeline/runtime/*)
and test reports. Commit only your own paths (`git commit -- <paths>`), keep
hunks in shared files (schema.json, types/container.go, compiler/convert.go,
linter.go) small and commit them right away. Don't push.

## Goal

Carry dependency caches and build outputs between pipeline runs, keyed by a
hash of the inputs. Replace cognitiveos's hand-made caching: trusted named
volumes `cognitiveos-go-mod-cache:/go/pkg/mod` and
`cognitiveos-go-build-cache:/root/.cache/go-build` (mounted in 274 places), a
pnpm volume, host bind mounts for bazel, pip and ci-state, and
`ci-run-if-needed.sh`. That approach needs trusted repos, shares one writable
volume across every repo and branch, and has no keys, invalidation or eviction.

## Priority 1: step `cache:` key

```yaml
steps:
  test:
    image: golang:1.25
    cache:
      - key: go-{{ checksum "go.sum" }}   # also {{ arch }}, {{ os }}, {{ branch }}, {{ checksum "a" "b/**" }}
        restore_keys: [go-]               # prefix fallbacks, tried in order, newest match per prefix
        paths: [/go/pkg/mod, /root/.cache/go-build]   # absolute or relative to the workspace
        policy: pull-push                 # pull | push | pull-push (default)
    commands: [go test ./...]
```

### Mechanism (decided): the server-side compiler injects everything

The compiler does all of it, so **no agent upgrade is needed** for priority 1,
and the server enforces the security rules, not the YAML author.

Why not edit `pipeline/backend/common/script_posix.go`, as the brief
suggested: that code runs on the agent (docker `convert.go` calls
`GenerateContainerConf`). Using it would mean a new field on
`backend_types.Step` (step.go, which foundation is editing) and an agent
rollout before the feature works. The compiler can produce the same script
on its own.

1. **Volume.** In `compiler.Compile`, for each step of type
   `StepTypeCommands` with `cache:`, append
   `woodpecker-cache-<forgeID>-<repoID>:/woodpecker/cache` to `step.Volumes`.
   Use `metadata.Repo.ID` (int64) and the forge ID; check which forge-ID field
   metadata carries (`metadata.Forge` / `Repo`), and fall back to repo ID only.
   For pull-request events (`metadata.Curr.Event.IsPull()`, which covers
   pull_request, pull_request_closed and pull_request_metadata), mount it
   **read-only** (`...:/woodpecker/cache:ro`). Docker `Binds` accept `:ro`,
   since `convert.go` passes `step.Volumes` straight to `config.Binds`. This
   stops PR commands from writing to the volume directly, not just stopping
   our save step. Docker creates the named volume on first use, and the
   backend's teardown removes only `conf.Volume`, so the cache volume persists.
   Add the injection after `createProcess`, so it bypasses the linter's
   trusted-volumes check, which only looks at `container.Volumes`. That way
   `cache:` needs no trusted repo.
2. **Scripts.** Rewrite `step.Commands` to
   `["eval \"$CI_CACHE_RESTORE\"", <user commands...>, "eval \"$CI_CACHE_SAVE\""]`
   and put the generated POSIX sh bodies in `step.Environment["CI_CACHE_RESTORE"]`
   and `["CI_CACHE_SAVE"]`. Set them after the user's `environment:` is merged,
   so ours win. Using `eval` of env vars keeps the `▶` command lines in the log
   short. The step runs under `sh -e`, so the save line runs only if every
   user command succeeded. Omit the save line entirely for PR events and for
   `policy: pull`. Omit the restore for `policy: push`.
3. **Never fail the step.** Every cache action runs in a subshell with `set +e`
   and is invoked as `( ... ) || true`, or as `x=$( ... ) || rc=$?`, so `-e`
   can't trip. If `/woodpecker/cache` is missing (local backend, k8s) or not
   writable, or if `tar` or `sha256sum` is missing, log `cache: warning: ...`
   and carry on uncached.
4. **Key computation** happens once, in the restore, before the user commands
   run. The restore subshell prints one key per cache item on stdout (logs go
   to stderr), captured into `__wpc_keys`. Save takes item i's key with
   `sed -n "${i}p"`. An empty key means that item is skipped.
5. **Store layout.** All entries live on the same filesystem, so renames are
   atomic:
   - `/woodpecker/cache/v1/<key>.tar` holds the entries: an uncompressed tar
     made with `tar -cf … -C / <abs paths without leading />`, restored with
     `tar -xf … -C /`. Uncompressed is fastest on local disk.
   - `/woodpecker/cache/v1/<key>.used` is a last-used stamp. Touch it on every
     hit. It is kept separate from the tar's mtime, so "newest" still means
     newest written.
   - `/woodpecker/cache/v1/tmp/` holds temp tars. Write there, then `mv -f`
     into place.
   - `/woodpecker/cache/v1/locks/<key>/` is a `mkdir` lock. Only one
     concurrent saver per key; the others log "another step is saving" and
     skip.
6. **Restore order.** Try the exact key first. Then, for each `restore_keys`
   entry in order, use `ls -t v1/<prefix>*.tar | head -n1`. Log which entry
   was used, or "miss".
7. **Save.** Run only if the key is non-empty and `v1/<key>.tar` doesn't exist
   yet. Drop paths that don't exist and log them. Tar to tmp, then `mv`. On
   any tar error, remove the tmp file and log a warning.
8. **Eviction**, run after a save:
   - keep the newest **3** tars matching the item's literal key prefix (the
     text before the first `{{`), when that prefix is non-empty;
   - delete entries whose `.used` stamp is older than **14 days**
     (`find -mtime +14`);
   - delete locks older than 60 minutes (`find -mmin +60`).

   Caveat for the docs: prefix pruning overlaps when one prefix is a prefix of
   another (`go-` and `go-build-`), so advise distinct prefixes. N and the age
   could later become compiler options or server env vars.

### Key templates

Parse with `text/template/parse` and walk the nodes. Don't execute the
template. Each node maps to shell:
- `TextNode`: a literal. It must match `[A-Za-z0-9._-]` (linter error
  otherwise); the compiler replaces anything else with `-`.
- `{{ checksum "p1" "p2/**" }}` → `$(__wpc_checksum 'p1' 'p2/**')`. String
  args only, no whitespace. The function runs in `$CI_WORKSPACE`, a path the
  compiler bakes in with `path.Join(workspaceBase, workspacePath)`. It builds
  a file list:
  - an arg without `**` goes through a shell glob; a directory expands to
    `find dir -type f`;
  - an arg with `**`: base is the literal directory before the first glob
    character. Turn `**/` and `**` into `*`, then run
    `find <base> -type f -path '<pattern>'` (find's `*` matches `/`).

  Then `LC_ALL=C sort -u | tr '\n' '\0' | xargs -0 sha256sum | sha256sum | cut -c1-32`.
  If no files match, warn. The hash covers file names too.
- `{{ arch }}` → `uname -m` normalized: x86_64→amd64, aarch64→arm64,
  armv7l→arm.
- `{{ os }}` → `uname -s` lowercased.
- `{{ branch }}`: rendered at compile time from `metadata.Curr.Commit.Branch`,
  sanitized.

After rendering, the shell sanitizes the key with `tr -c 'A-Za-z0-9._-' '-'`
and truncates it to 200 characters. `restore_keys` use the same renderer.

### Where the code goes

- `pipeline/frontend/yaml/types/cache.go` (new): `type Cache struct { Key
  string; RestoreKeys []string \`yaml:"restore_keys"\`; Paths []string;
  Policy string }`. Then add one line to `Container`:
  `Cache []Cache \`yaml:"cache,omitempty"\``, and for priority 2
  `SkipIfCached bool \`yaml:"skip_if_cached,omitempty"\``. Both are small
  hunks in a shared file.
- `pipeline/frontend/yaml/cache/` (new package): template parsing and
  validation (`Validate(c types.Cache) error`), plus script generation:
  `Restore(items, opts) string` and `Save(items, opts) string`, with opts
  {Workspace, Root, Branch, ReadOnly, SkipIfCached}. The linter and the
  compiler both import it.
- Compiler (`compiler.go`, in the steps loop after `createProcess`): call an
  `injectCache(step, container)` helper kept in a new file
  `compiler/cache.go`, so the hunk in the shared file is one line.
- `schema.json`: add `cache` (array of objects; `key` and `paths` required;
  `policy` enum) and `skip_if_cached` (boolean) to the step definition.
- Linter, as a new `lintCache` called from `lintContainers`. Each of these is
  an error:
  - `cache` on a plugin step (no commands);
  - `cache` on services or clone;
  - an invalid template or literal;
  - empty `key` or `paths`;
  - a path of `/`, or one inside or containing `/woodpecker/cache`;
  - an unknown policy;
  - `skip_if_cached` without `cache`.

  Also an error: any user `volumes:` entry whose source starts with
  `woodpecker-cache-` or whose target is under `/woodpecker/cache`. Enforce
  that last rule in the compiler too, by rejecting it in `createProcess`, so
  it holds even when lint warnings are ignored.

### Backend limits (for the docs)

- **Docker:** supported. The volume is local to each agent host, so every host
  has its own cache. To share across hosts, pre-create the volume with a volume
  driver such as NFS. Images that run as non-root may not be able to write a
  fresh root-owned volume; that is logged and the step runs uncached.
- **Local backend:** volumes are ignored, the path is missing, and the step
  logs a warning and runs uncached.
- **Kubernetes:** unsupported. A `name:/path` volume becomes a PVC reference,
  and a missing PVC breaks the pod. Either document "don't use `cache:` on
  k8s", or add a small k8s-backend change that drops volumes named
  `woodpecker-cache-*` (that one needs an agent upgrade).
- **Windows:** unsupported; the scripts are POSIX sh.

### Security summary

- Each repo has its own volume, and the name is computed by the server.
- PR events mount the volume `:ro` and get no save line.
- Users can't mount the reserved name or path.
- `cache:` needs no trust, because the injection happens after lint.
- Pushes to any branch can write keys that main will read. Document adding
  `{{ branch }}` when that matters.

## Priority 2: `skip_if_cached`

```yaml
steps:
  unit:
    cache:
      - key: unit-{{ checksum "go.sum" "pkg/**" }}
        paths: [.ci-out/unit]
    skip_if_cached: true
```

The restore subshell exits **42** only if every item hit its exact key and
restored without error; anything else counts as a miss. Then the restore
line does:

```sh
echo "cache: exact hit for <keys>, skipping commands"
exit "${CI_STEP_SKIP_EXIT_CODE:-0}"
```

That is backward compatible:
- **Old agents** don't set `CI_STEP_SKIP_EXIT_CODE`, so the step exits 0 and
  passes with a clear "cached, skipped" log line.
- **New agents**: the runtime (pipeline/runtime, foundation's area) injects
  `CI_STEP_SKIP_EXIT_CODE=78` into command steps, and maps exit code 78 to
  skipped in the reported step state.

Check how `rpc.StepState` and the server's step status updater represent
`skipped`, and whether the UI already renders `StatusSkipped` for a step that
has started. Do this only after foundation's runtime changes
(`pipeline/runtime/{runtime,step,workflow,scope}.go`) are committed. Keep the
runtime change to a few lines. If mapping to skipped is too invasive, ship the
exit-0 behaviour alone and say so.

## Priority 3: warm clone (optional, only after 1 and 2 are solid)

This is not evaluated yet. The default clone is the external
`woodpeckerci/plugin-git` image, run as a plugin step with settings
`depth: "0"`, and it has no `--reference` or alternates option. A warm clone
would need either a fork of that plugin or a commands-based clone step that
keeps a bare mirror in the cache volume. The step would refresh the mirror
with `git fetch` under a lock, then run `git clone --reference`, or
`--dissociate` to stay safe when the mirror is evicted. Measure it against a
depth-1 clone on cognitiveos. If it doesn't clearly win, document why and
skip it.

## Docs to write

- `docs/docs/20-usage/71-caching.md` (new). Cover:
  - keys and templates, restore order, policies;
  - per-repo isolation and the PR rule;
  - eviction (3 per prefix, 14 days unused), backend limits;
  - clearing a cache: `docker volume rm woodpecker-cache-<forge>-<repo>` on
    each agent host, or delete files under `v1/` from a step;
  - **migrating from named volumes**, with a before/after for cognitiveos:
    `volumes: [cognitiveos-go-mod-cache:/go/pkg/mod, cognitiveos-go-build-cache:/root/.cache/go-build]`
    becomes `cache: [{key: go-mod-{{ checksum "go.sum" }}, restore_keys: [go-mod-], paths: [/go/pkg/mod]}, {key: go-build-{{ arch }}-{{ checksum "go.sum" "**/*.go" }}, restore_keys: [go-build-{{ arch }}-], paths: [/root/.cache/go-build]}]`.
    Note that restoring with tar costs time on each step, where a live volume
    mount cost nothing;
  - `ci-run-if-needed.sh` maps to `skip_if_cached`;
  - foundations: a foundation shares one clone and its services within a run;
    the cache carries things between runs.
- Mention `cache:` and `skip_if_cached` in
  `docs/docs/20-usage/20-workflow-syntax.md`.

## Next steps (in order)

1. Write `types/cache.go`, add the `Container` fields, and add the
   `yaml/cache` package with unit tests for template parsing and rendering,
   and for script generation per policy, PR read-only and skip.
2. Write `compiler/cache.go`, call it from `Compile`, add the reserved-volume
   rejection, and add compiler tests for volume injection, `:ro` on PRs,
   omitted saves, and env vars that override user values.
3. Add the linter rules and tests, and the schema.json entries with schema
   test fixtures.
4. Run a real e2e check with the CLI exec against a tiny pipeline twice, and
   show the second run restoring.
5. Do the `skip_if_cached` runtime mapping once foundation's runtime commit has
   landed.
6. Write the docs.
7. Decide on the warm clone.

## How to test

- `go test -tags test ./pipeline/frontend/yaml/... ./pipeline/backend/common/...`
- `go vet -tags test` on the same packages.
- The server build needs `web/dist/index.html`. It is gitignored; create it if
  it's missing.
- E2E needs docker. Build the CLI with `go build -o /tmp/wp ./cmd/cli`, then
  run `wp exec --backend-engine docker .woodpecker.yaml` twice on a repo with
  `go.sum` and a `cache:` step. Expect `cache: restored go-…` on the second
  run, and `docker volume ls | grep woodpecker-cache-` to show the volume.
  `exec` may have repo ID 0, so check the volume name it produces. Also run
  with `CI_PIPELINE_EVENT=pull_request` (check the exec flag for the event)
  and expect no save.
- Web checks, only if the web is touched: `npx -y pnpm@12.15.0` for vue-tsc,
  eslint and vitest.

## Operator notes (when shipped)

- Priority 1 needs only a server upgrade. Agents are unchanged.
- Priority 2 skipped status needs an agent upgrade. Without it, cached steps
  show as passed.
- Cache volumes `woodpecker-cache-*` build up on each agent host. Prune with
  `docker volume rm` when a repo is deleted.
