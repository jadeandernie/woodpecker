# Upstream PR draft: keep a pipeline's place for workflows whose dependencies clear

Repository: woodpecker-ci/woodpecker, base `main`.
Head: `jadeandernie:queue-pipeline-order` (not created yet).
Enable "Allow edits by maintainers".

Before posting: create the head branch from upstream `main` and port c260e2a5 from `v3.17-squash` onto it. On `main`, `filterWaiting` also builds the `pendingIDs` index from #7303, and `fifo.go` no longer imports `slices`. Re-run the tests listed below on the branch.

**Title:** Keep a pipeline's place in the queue for workflows whose dependencies clear

---

Closes #<issue number from upstream-issue-queue-pipeline-order.md>
Related #6671, #7322, #6986

When a workflow's dependencies finished, `filterWaiting` moved it back to the end of pending, behind every workflow of every pipeline queued after its own. Now it goes back at its pipeline's place:

- before the first pending task of a pipeline created after its own, ordering pipelines by `Created` and then by pipeline ID for pipelines created in the same second;
- behind the pending tasks of its own pipeline, so the workflows of one pipeline keep the order they were queued in;
- in one walk over pending: the waiting tasks are sorted first, and each one's place is at or after the place of the one before it.

Everything else stays the same. New pipelines are still appended, expired tasks still go to the front, and `assignToWorker` still hands out pending from the front. Tasks without `Created` or a pipeline ID compare equal, so they still go to the back.

The order uses the pipeline ID rather than `taskOrderLess`'s name tiebreak. Within a pipeline, ordering by name would reorder workflows by their names instead of the order they were queued in. Across pipelines created in the same second, it would interleave their workflows.

### Tests

`TestFifoPipelineOrder`:

- `dependent workflow keeps its pipeline's place`: pipeline 1 has a build and a deploy that depends on it, pipeline 2 (created later) has three independent workflows. After the build is done, the deploy is first in pending and is the next task an agent gets. On the old code it is last.
- `workflows of one pipeline keep their order`: the workflow names sort against the queued order. After the build is done, pending holds lint, deploy-b and deploy-a, then the later pipeline, and agents get them in that order.
- `resubmitted workflows merge in by pipeline`: calls `filterWaiting` directly with unsorted waiting tasks from five pipelines. One of them was created in the same second as a pipeline already pending, so the pipeline ID decides.

All three fail on the old code. `go test -race ./server/queue/...` and `go test -tags test ./e2e/...` pass.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
