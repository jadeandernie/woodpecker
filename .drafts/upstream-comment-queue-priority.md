# Upstream comment draft: queue priority (#6986)

Post as a comment on https://github.com/woodpecker-ci/woodpecker/pull/6986 ("Add workflow-level queue priority").
Do not open a new issue or PR. #6986 and #6941 already cover the design space, and a third proposal would split the discussion.

---

Another use case for this, in case it helps the decision.

We deploy single services by promoting an existing pipeline of `main` to a `deployment` event (`deploy_to` set to the service). The queue is strict FIFO, so on a busy day that deployment waits behind every pending push workflow, often more than 150. A deploy that takes a minute starts an hour later. With this PR, the deploy workflow only needs:

```yaml
when:
  - event: deployment
    priority: 100
```

Two notes from reading the patch:

1. `canRunConcurrent` still reserves concurrency group slots in creation order (`taskOrderLess`). A high-priority task in a `concurrency` group can still wait behind older, lower-priority tasks of the same group. That is probably the right behaviour inside a group, but it might be worth a sentence in the docs.
2. On the concern about letting repository authors set their own priority: the deployment use case would be just as well served by an admin-side default per event, e.g. a server setting that ranks `deployment` above `push`. Task priority could then be the maximum of the event default and the workflow value. Any of these would solve the problem for us. The workflow-level field is the smallest change and is already here.

Until then, a workaround that works today for anyone with the same problem:

- Give one agent `WOODPECKER_AGENT_LABELS=!lane=deployment` and the build agents `lane=*`.
- Label the deploy workflows with `labels: { lane: ${CI_PIPELINE_EVENT} }`.

`assignToWorker` skips tasks that no idle agent matches, so deployments bypass the push backlog on the dedicated agent. Push workflows can still use any build agent.
