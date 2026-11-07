# Concurrency groups

A concurrency group without a limit makes a repository run one pipeline's workflows of the group at a time, and keeps at most one more pipeline's waiting: when a newer pipeline comes, it replaces the waiting one. This suits deployments, where only the newest change needs to go out and the changes in between can be skipped. It works like a [GitHub Actions concurrency group](https://docs.github.com/en/actions/using-jobs/using-concurrency) with `cancel-in-progress: false`.

For a limit on how many workflows may run at once, which keeps every waiting pipeline, see [Concurrency](./25-workflows.md#concurrency).

## Syntax

Give each workflow of the group the same `group`, without a `limit`:

```yaml title=".woodpecker/deploy.yaml"
steps:
  - name: deploy
    image: debian:stable-slim
    commands:
      - echo deploying

depends_on:
  - publish

concurrency:
  group: deploy-production
```

The group supports [environment variable substitution](./50-environment.md), for example `group: deploy-${CI_COMMIT_BRANCH}`. Groups are per repository: the same group name in two repositories are two groups.

## How it works

A pipeline's workflows in a group are its **unit** in the group. A unit is

- **queued** while none of its workflows has started, including while they wait for the workflows they depend on,
- **in progress** once one of them has started, until all of them are done.

In each group of a repository there is at most one unit in progress and at most one queued:

- While a pipeline's unit is in progress, the workflows of other pipelines in the group are not handed to agents. They start once all workflows of the unit in progress are done, whether they succeeded or not.
- A pipeline's own workflows in the group never wait for each other beyond their `depends_on`: a `publish` and a `deploy` workflow in the same group run one after the other in the same pipeline.
- When a pipeline with workflows in the group starts, the queued units of older pipelines in the group are superseded: their workflows and steps end as skipped, with the error `superseded by #N` naming the newer pipeline. The pipeline's other workflows are not affected.
- An older pipeline never supersedes a newer one. If an older pipeline starts after a newer one, for example when it is approved later, its own unit is superseded if the newer pipeline's unit is queued or in progress.
- A unit in progress is never canceled or superseded.

All events are treated the same. Workflows without a `concurrency` group are not affected.

### Example

Pipelines #1, #2 and #3 are pushed one after the other. Each builds an image, then publishes and deploys it in the group `deploy-production`:

1. #1 publishes and deploys.
2. #2 builds, then its publish waits until #1's deploy is done.
3. #3 comes while #1 still deploys: #2's publish and deploy are skipped, `superseded by #3`.
4. Once #1's deploy is done, #3 publishes and deploys.

## Notes

- If the newest pipeline's unit fails, for example because its build fails, the older units it superseded do not run either.
- The state is kept in the database, so it survives a server restart, and the first pipeline of a group also supersedes the queued workflows of older pipelines of the same branch that have the same names but no group yet, for example those created before the group was added.
- Workflows of a unit should not depend on workflows of another group's unit: two pipelines could each wait for the other.
- Use a group either with or without a limit, not both.
