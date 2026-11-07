# Upstream PR draft: squash pending pipelines

Repository: woodpecker-ci/woodpecker, base `main`.
Head: `jadeandernie:squash-pending-pipelines`.
Enable "Allow edits by maintainers".

**Title:** Add option to squash pending pipelines into the newest one

---

Closes #<issue number from upstream-issue.md>
Related #1785, #7345

### What

A new repo setting, **Squash pending pipelines**, below the "cancel previous pipelines" events and off by default (`cancel_previous_pipeline_squash` in the API).

When it is enabled, a new pipeline of a selected event changes how it cancels previous pipelines:

- It only cancels previous, older pipelines of the same event and context that have not started yet (pending or blocked). Running pipelines are left to finish.
- It adds their changed files to its own before its config is fetched and its path filters are evaluated. Path-filtered workflows of the superseded pipelines therefore still run, and `CI_PIPELINE_FILES` lists all of them.
- It cancels them, with "superseded by #N", only after it is queued itself. If it fails to start, nothing is canceled.

With the setting off, nothing changes: `cancelPreviousPipelines` runs exactly as before.

### How

- `model.Repo.CancelPreviousPipelineSquash`: a new column, `DEFAULT FALSE`.
- `pipeline.squashPreviousPipelines` selects the superseded pipelines and merges their changed files. It runs:
  - in `Create`, before the config fetch, unless the pipeline is blocked;
  - in `Approve`, once the pipeline is pending.
- `start` takes the superseded pipelines. It skips `cancelPreviousPipelines` when squashing, and calls `cancelSupersededPipelines` after `StartPipeline` succeeded. That function re-reads each pipeline and only cancels it if it is still pending or blocked.
- `Restart` passes no superseded pipelines.
- The event and context comparison used by cancel previous pipelines moved into `sameEventAndContext`, so both paths share it. This refactor is its own commit with no behaviour change.
- API (`PATCH /repos/{id}`), Go SDK type, UI checkbox, English locale and docs (project settings).

### Edge cases

- Pipelines without changed files (cron, manual, or no files reported) are only squashed with each other, because `on_empty` decides their path filters.
- Blocked pipelines are squashed like pending ones. `Cancel` marks them killed, as it already does for blocked pipelines today.
- Only changed files carry over. Message, author and other metadata conditions use the newest commit.
- A superseded pipeline that started between selection and cancel is left running. Its changes then run twice, never zero times.

### Tests

- `server/pipeline/cancel_test.go` covers selection by status, number, event, branch and changed-files kind; the files merge; skipping pipelines that started meanwhile; and that nothing is canceled when `StartPipeline` fails.
- `e2e/scenarios/squash_test.go` uses the in-process server, store and agent:
  - a pending pipeline squashed into a newer push runs both path-filtered workflows while the running one finishes;
  - gated pipelines squash on approval;
  - with the setting off, the running pipeline is still killed as before.
- `go test -race -tags test ./...` (server, pipeline, e2e) passes.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
