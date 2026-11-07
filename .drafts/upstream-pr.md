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
- It is queued in the place of the oldest pipeline it supersedes, not at the end of the queue. Otherwise the branch's changes lose their place each time they are squashed, and on a busy repo its deploys wait behind every pipeline created since. The pipeline's own creation time is unchanged.

With the setting off, nothing changes: `cancelPreviousPipelines` runs exactly as before.

### How

- `model.Repo.CancelPreviousPipelineSquash`: a new column, `DEFAULT FALSE`.
- `pipeline.squashPreviousPipelines` selects the superseded pipelines and merges their changed files. It runs:
  - in `Create`, before the config fetch, unless the pipeline is blocked;
  - in `Approve`, once the pipeline is pending.
- `start` takes the superseded pipelines. It skips `cancelPreviousPipelines` when squashing, and calls `cancelSupersededPipelines` after `StartPipeline` succeeded. That function re-reads each pipeline and only cancels it if it is still pending or blocked.
- `Restart` passes no superseded pipelines.
- Queue place, its own commit, building on the queue pipeline order PR (`upstream-pr-queue-pipeline-order.md`, which adds `comparePipelineOrder`):
  - `model.Task.QueuedInPlaceOf`: a new column on `tasks`, `NOT NULL DEFAULT 0`. A task queued in the place of an earlier pipeline carries that pipeline's ID here and its creation time in `Created`, the existing ordering key.
  - `start` calls `takeOverQueuePlace` before `StartPipeline`. The place of a superseded pipeline is the one its queued tasks hold (from `Scheduler.Info`), so a chain of squashes keeps the place of the first; otherwise it is its creation time and ID.
  - `fifo.PushAtOnce` inserts such a task before the first pending task of a later pipeline instead of at the tail. `comparePipelineOrder` breaks ties in creation time by the ID of the pipeline whose place a task has. Other tasks are pushed to the tail as before.
  - The queue is restored from `TaskList` with the same `PushAtOnce`, so the place survives a restart whatever order the store lists the tasks in.
- The event and context comparison used by cancel previous pipelines moved into `sameEventAndContext`, so both paths share it. This refactor is its own commit with no behaviour change.
- API (`PATCH /repos/{id}`), Go SDK type, UI checkbox, English locale and docs (project settings).

### Edge cases

- Pipelines without changed files (cron, manual, or no files reported) are only squashed with each other, because `on_empty` decides their path filters.
- Blocked pipelines are squashed like pending ones. `Cancel` marks them killed, as it already does for blocked pipelines today.
- Only changed files carry over. Message, author and other metadata conditions use the newest commit.
- A superseded pipeline that started between selection and cancel is left running. Its changes then run twice, never zero times.
- A pipeline never takes a place behind its own, and superseded pipelines without a creation time and no queued tasks are ignored for the place.

### Tests

- `server/pipeline/cancel_test.go` covers selection by status, number, event, branch and changed-files kind; the files merge; skipping pipelines that started meanwhile; and that nothing is canceled when `StartPipeline` fails.
- Queue place:
  - `server/queue/fifo_test.go` (`TestFifoQueuedInPlaceOf`): a squashing pipeline's workflows go before those of pipelines created after the oldest superseded one, keep their own order, and a dependent workflow returns to that place; the superseded pipeline's ID breaks a tie in creation time; other pipelines still go to the tail.
  - `server/queue/persistent_test.go`: the place survives a restart, whether the store lists the tasks in insertion order or not.
  - `server/pipeline/queue_test.go` (`TestTakeOverQueuePlace`) and `cancel_test.go` (`TestStartSquash`): the oldest place is chosen, including one held in the queue through an earlier squash; a pipeline that supersedes none keeps its own; the pipeline's creation time is unchanged.
  - `server/store/datastore/task_test.go`: the column round-trips.
- `e2e/scenarios/squash_test.go` uses the in-process server, store and agent:
  - a pending pipeline squashed into a newer push runs both path-filtered workflows while the running one finishes;
  - gated pipelines squash on approval;
  - with the setting off, the running pipeline is still killed as before.
- `go test -race -tags test ./...` (server, pipeline, e2e) passes.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
