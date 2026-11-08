# Pipeline foundations: handoff

A foundation does shared setup once (one clone, one set of services) and runs
many member workflows inside it, with hooks between members. The builder is to
merge the foundation file and its members into ONE workflow (one agent).

## Done (branch `claude/pipeline-filters-kinds`)

- `84e731a28` Runtime failure scoping (agent side), tested.
  - `pipeline/backend/types/step.go`: `FailureScope string` and
    `FailureScopeInherits []string` (JSON `failure_scope`,
    `failure_scope_inherits`, omitempty).
  - `pipeline/runtime/scope.go`: `failureScopes` records the first failure per
    scope (and unscoped). A scoped step's status = unscoped failure, else own
    scope, else an inherited scope. Unscoped steps keep the workflow-wide `r.err`.
    Used by `shouldSkipStep` and `CI_PIPELINE_STATUS` (`step.go`).
  - `pipeline/runtime/workflow.go`: consecutive stages whose steps all have a
    scope run as one DAG segment (`runScopedSegment`), each step waiting only on
    its `DependsOn` within the segment. On cancel, unstarted steps are not run.
    The segment's first error goes to `r.err` (so teardown sees failure and the
    workflow fails).
  - Tests: `pipeline/runtime/scope_test.go`: member failure does not skip other
    members, normal semantics inside a member, setup failure skips all members
    (after hooks and teardown still run), inherited scopes, ignored failures,
    lanes not waiting on each other, cancel, and unit tests.
  - **Version skew:** no rpc bump. Old agents ignore the fields, so stages
    still run in order, but failures are workflow-wide: one failing member skips
    the rest (after hooks and teardown still run because they get
    `OnFailure`). Agents need upgrading to get per-member scoping.
- `1c2677a9a` YAML types: `pipeline/frontend/yaml/types/foundation.go`
  (`Foundation{Name, MaxParallel, Setup, Before, After, Teardown}`), plus
  `Workflow.Foundation` and `Workflow.UsesFoundation` in `workflow.go`. Nothing
  reads them yet.

## Left to do

1. **Builder merge.** Write `pipeline/frontend/builder/foundation.go` and hook
   it into `builder.go` `Build` and `utils.go` `selectYamls`.
   - Index: probe each raw file with `yaml.Unmarshal` into
     `{foundation: {name}, uses_foundation, matrix}`. Blocking errors for:
     - a duplicate foundation name;
     - a foundation name that equals another file's workflow name;
     - a file with both `foundation` and `uses_foundation`;
     - an unknown foundation;
     - `matrix` in a foundation or member file.
     The name defaults to `SanitizePath(file)`.
   - Selection: a foundation name selects its file and all members. A member
     name selects that member and its foundation file only. Hard dependencies
     are expanded the same way. Selected and filter checks must accept an item
     whose new `Item.Members []string` contains the name (add the field in
     `types.go`; don't change `ContainsItemWithName` semantics for the others).
   - Build loop: skip member files. A foundation file yields one item with the
     PID at its position. Use the foundation's metadata for the merged workflow
     and each member's metadata (Name = member) for substitution and `when`.
     Lint each file. Drop members whose `when` does not match. With no members
     left, return no item.
   - Dependencies between members:
     - A dependency on another member of the same foundation becomes step
       ordering. If that member is missing, drop an optional dependency; for a
       required one, drop the dependent member (repeat until stable).
     - Any other dependency becomes a dependency of the merged item, together
       with the foundation's own `depends_on`.
     - Members run in topological order, ties by name. A cycle is an error.
   - Lanes: `min(max(max_parallel,1), #members)`. A member with intra
     dependencies goes to the lane of its last-placed dependency. Others go to
     the least-loaded lane, lowest index first (round robin).
   - Step DAG and names:
     - Names are `setup/<n>`, then per member `<m>/before/<n>`, `<m>/<step>`,
       `<m>/after/<n>`, then `teardown/<n>`.
     - Pre-filter each group's containers by `when`, using the foundation's
       metadata and env = `b.Envs` + `AdditionalEnvs`. The compiler errors on
       dependencies of filtered steps.
     - Shallow-copy and rename containers, and prefix their in-group
       `depends_on`.
     - A group in sequence mode is chained. In a DAG group, steps without
       dependencies get the group entry.
     - Every step gets a non-nil `DependsOn`.
     - A group's exit is its sinks; an empty group passes its entry through.
     - A member's entry is its lane's exit plus the exits of the members it
       depends on.
     - Teardown's entry is the exits of all lanes.
   - The merged `yaml_types.Workflow` takes When, Workspace, Clone, SkipClone,
     Services, Labels, Concurrency and Kind from the foundation. Compile it with
     `toInternalRepresentation`.
   - After compiling, by step name:
     - Every step gets env `CI_FOUNDATION` and `CI_FOUNDATION_LANES`.
     - Before, member and after steps get `CI_FOUNDATION_MEMBER`,
       `CI_FOUNDATION_LANE`, `FailureScope=<member>` and `FailureScopeInherits`
       (the transitive intra dependencies).
     - After hooks and teardown get `OnFailure=true` unless their `when` sets a
       `status` (check `len(c.Status)` per constraint; `IncludesStatus*` cannot
       tell an unset status from `success`).
     - Reset `Networks[].Aliases` to the original short step name: `/` is not
       a valid DNS alias.
   - Item fields:
     - Kind: the foundation's kind, else the members' kind if they all set the
       same one, else empty.
     - RunsOn comes from the foundation's `when`.
     - Labels: refactor the defaulting and internal-label stamping at the end
       of `genItemForWorkflow` into a helper and reuse it.
   - After all items, when not in ListOnly mode, rewrite any dependency on an
     included member to its foundation item's name. Deduplicate, and a required
     dependency wins over an optional one. Then run
     `filterMissingDependencies`. Consider erroring on a dependency cycle that
     goes through a foundation item.
   - ListOnly mode emits the foundation item plus one item per member. Each
     member item's dependencies are its own plus the foundation's.
   - `server/pipeline/items.go` should need no change.
2. **Linter.** In `pipeline/frontend/yaml/linter/linter.go`:
   - Foundation files (`Workflow.Foundation != nil`) take a separate path. It
     drops the "steps required" error and adds errors for `steps`,
     `uses_foundation` and `matrix` (probe RawConfig). It lints clone, services
     and each hook group, using areas such as `foundation.setup`, and checks
     `depends_on` within each hook group (`lintDependsOn` looks at
     `Workflow.Steps` today). Then it runs the schema and deprecation checks.
   - Members (`UsesFoundation != ""`) get blocking errors for: clone,
     skip_clone, services, workspace, labels, concurrency, matrix and runs_on,
     and for a `when.status` that includes failure.
   - Add tests in `linter_test.go`.
3. **Schema.** In `pipeline/frontend/yaml/linter/schema/schema.json`:
   - Add a `foundation` definition: name pattern, `max_parallel` minimum 1, and
     setup, before, after and teardown as `step_list`, each with descriptions.
   - Add `uses_foundation`.
   - Replace `required: [steps]` with an `anyOf` that requires `steps` or
     `foundation`.
   - Add test files under `schema/.woodpecker/` and register them in
     `schema_test.go`.
4. **Tests.** In `pipeline/frontend/builder/`:
   - Table tests for the merge: DAG shape, lanes, env, naming, when filtering,
     depends_on rewriting and errors.
   - Selection tests for SelectedWorkflow and WorkflowFilter.
   - An end-to-end test from YAML files to items.
5. **Docs.** Write `docs/docs/20-usage/28-foundations.md`, optionally with a
   short mention in `60-services.md`. Cover:
   - The shape and run order.
   - Env vars and the per-lane database pattern. Use `$$` to escape, e.g.
     `createdb -T seeded test_lane_$${CI_FOUNDATION_LANE}`.
   - Failure scoping and the agent upgrade.
   - Limitations:
     - `CI_WORKFLOW_NAME` is the foundation's name at runtime.
     - `foundation.name` and `uses_foundation` are read before substitution.
     - Teardown does not run on cancel.
     - After hooks also run when setup failed, so make them idempotent.
     - Detached steps with the same name in two lanes share an alias.
     - CLI local mode is not pre-filtered.

## How to test

Run from the worktree:

```
gofmt -l pipeline/ server/pipeline/
go vet -tags test ./pipeline/... ./server/pipeline/...
go test -tags test ./pipeline/... ./server/pipeline/...
go build ./...
go test -tags test -race -count=10 -run Scope ./pipeline/runtime/
```

`go vet` already flags `pipeline/runtime/shutdown.go:37` (a discarded cancel
func). This was there before the foundation work and is not part of it.

## Unverified

- The runtime scoping has only been run with the dummy backend, not with
  Docker or Kubernetes agents, and not against an old agent.
- Nothing produces scoped steps yet, so there is no end-to-end run of a real
  foundation.
