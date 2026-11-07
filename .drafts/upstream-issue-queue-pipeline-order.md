# Upstream issue draft: a workflow whose dependencies clear goes to the back of the queue

Repository: woodpecker-ci/woodpecker, template "Bug report". Component: server.

**Title:** A workflow whose `depends_on` clears is queued behind every later pipeline

---

### Describe the bug

A workflow with `depends_on` waits in the queue's `waitingOnDeps` list until its dependencies finish. On every tick, `filterWaiting` in `server/queue/fifo.go` moves all waiting tasks back with `q.pending.PushBack(task)`, then takes out the ones that are still blocked. So when a workflow's dependencies finish, it lands at the back of pending, behind every workflow of every pipeline queued after its own. `assignToWorker` hands out pending from the front, so the workflow waits for all of them.

A common case is a deploy workflow that depends on a build in the same pipeline. On a busy repository the build starts soon, but the deploy then waits behind the builds of all later pushes. On 3.17.0 we saw a deploy at position 478 of 506 pending tasks, behind hundreds of workflows from three pipelines created after its own. The time from merge to deploy grows with the backlog, and an older pipeline's deploy can start after a newer pipeline's.

The task's `Created` field is documented as the queue ordering across pipelines, and `canRunConcurrent` already reserves concurrency group slots in that order (`taskOrderLess`). The pending list itself does not keep that order for these tasks.

### Steps to reproduce

1. Use one agent that runs one workflow at a time.
2. Queue pipeline A with workflows `build` and `deploy` (`depends_on: [build]`).
3. Queue pipeline B after it, with several workflows without dependencies.
4. When A's `build` finishes, all of B's workflows run before A's `deploy`.

### Expected behavior

A workflow whose dependencies finish keeps its pipeline's place in the queue. It runs before the workflows of pipelines created after its own.

### Suggested fix

Move waiting tasks back in pipeline order instead of to the back. Insert each one before the first pending task of a later pipeline, ordering pipelines by `Created` and then by pipeline ID. Within a pipeline, keep the order workflows were queued in. If the waiting tasks are sorted first, one walk over pending places all of them, so a tick stays linear in the size of the queue.

I have a patch and will open a PR.

### Additional context

- #6671 added `Created`, `taskOrderLess` and the creation-order reservation for concurrency groups.
- #7322 rewrites `filterWaiting` but still moves released tasks with `q.pending.PushBack(task)`. The same change applies there.
- #6986 and #6941 add queue priority. #6986 relies on the pending order (creation order) to break ties between equal priorities, which this bug breaks.

### Validations

- [x] Read the docs
- [x] Checked that there isn't already an issue that reports the same bug
- [x] Checked that the bug isn't fixed in the `next` version already
