# Upstream issue draft: squash pending pipelines

Repository: woodpecker-ci/woodpecker, template "New feature proposal".

**Title:** Squash pending pipelines into the newest one instead of dropping their changes

---

### Clear and concise description of the problem

As a user of Woodpecker with path-filtered workflows (`when: path: ...`) on a busy branch, I want a new push to replace the pipelines still waiting in the queue without losing their changes, so that every change is built and deployed exactly once without the queue piling up.

Today there are two options, and neither works for a monorepo with frequent pushes:

- **Without "cancel previous pipelines"**, every push queues its own pipeline. On a busy branch the queue grows faster than the agents drain it, and each pipeline builds a state of the branch that is already outdated.
- **With "cancel previous pipelines" for `push`**, a new push cancels every previous pipeline of the branch, including running ones, and the new pipeline evaluates its path filters only on its own changed files. If a canceled pipeline changed `app/**` and the new one only changed `docs/**`, the app workflow never runs for that change: it is not built, tested or deployed until someone touches `app/**` again. Running pipelines are also thrown away, often minutes before they would have finished.

#1785 asks for the underlying need ("include all changed files since last pipeline run").

I intend to submit a PR for this issue.

### Suggested solution

A repo setting **Squash pending pipelines**, below the "cancel previous pipelines" events and off by default. When enabled, a new pipeline of a selected event:

1. Only supersedes previous pipelines of the same event and context (branch for pushes, refspec otherwise) that have **not started yet** (pending, or blocked waiting for approval), and only older ones.
2. Adds their changed files to its own **before** its configuration is fetched and its path filters are evaluated, so the new pipeline runs every workflow any of them would have run. `CI_PIPELINE_FILES` lists all of them.
3. Cancels them only once it is itself queued. If it fails to start (config error, every workflow filtered out), they are left untouched.
4. Leaves running pipelines alone.
5. Takes the place in the queue of the oldest pipeline it supersedes, instead of going to the end of the queue behind every pipeline created since. Otherwise a branch with frequent pushes keeps losing its place, and its deploys starve behind later pull requests.

A branch then has at most one pending pipeline at any time, and it covers every change since the pipelines that already started. Combined with a workflow `concurrency` limit (#6671) or a single agent, it also has at most one running pipeline.

With the setting off, behaviour is unchanged.

### Alternative

- Compute changed files against the last successful pipeline (#1785). This needs a forge compare between arbitrary commits and a definition of "last" across failed, canceled and filtered pipelines. Squashing only needs the files the superseded pipelines already have.
- Only cancel older pipelines (#7345). This fixes an ordering problem but still drops the canceled pipelines' changes.
- A config extension cannot do this: it can neither change a pipeline's changed files nor cancel other pipelines atomically with queuing the new one.

### Additional context

Edge cases the implementation handles:

- Pipelines without changed files (cron, manual, or a forge that sent no file list) are only squashed with each other. Their path filters fall back to `on_empty`, and mixing them with pipelines that have files would change that decision.
- A pipeline waiting for approval squashes when it is approved. A previous pipeline waiting for approval is squashed like a pending one.
- Only changed files are carried over. Conditions on message, author and other metadata are evaluated for the newest commit.
- A restart squashes nothing.
- A superseded pipeline that started in the meantime is not canceled. Its changes then run twice, but none are lost.

### Validations

- [x] Checked that the feature isn't part of the `next` version already
- [x] Read the docs
- [x] Checked that there isn't already an issue that requests the same feature (related: #11, #904, #1785, #7345)
