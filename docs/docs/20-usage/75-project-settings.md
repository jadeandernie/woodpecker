# Project settings

As the owner of a project in Woodpecker you can change project related settings via the web interface.

![project settings](./project-settings.png)

:::note
Woodpecker's permission handling is linked to the forge. A user on your forge that has admin access to the repo will also get admin permissions for the repository in Woodpecker and can then change the settings here.
:::

## Pipeline path

The path to the pipeline config file or folder. By default it is left empty which will use the following configuration resolution `.woodpecker/*.{yaml,yml}` -> `.woodpecker.yaml` -> `.woodpecker.yml`. If you set a custom path Woodpecker tries to load your configuration or fails if no configuration could be found at the specified location. To use a [multiple workflows](./25-workflows.md) with a custom path you have to change it to a folder path ending with a `/` like `.woodpecker/`.

## Repository hooks

Your Version-Control-System will notify Woodpecker about events via webhooks. If you want your pipeline to only run on specific webhooks, you can check them with this setting.

## Allow pull requests

Enables handling webhook's pull request event. If disabled, then pipeline won't run for pull requests.

## Allow deployments

Enables a pipeline to be started with the `deploy` event from a successful pipeline.

:::danger
Only activate this option if you trust all users who have push access to your repository.
Otherwise, these users will be able to steal secrets that are only available for `deploy` events.
:::

## Require approval for

To prevent malicious pipelines from extracting secrets or running harmful commands or to prevent accidental pipeline runs, you can require approval for an additional review process. Depending on the enabled option, a pipeline will be put on hold after creation and will only continue after approval. The default restrictive setting is `Approvals for forked repositories`.

Also see [security boundaries](./17-security-boundaries.md#approval-of-pipelines).

## Trusted

If you set your project to trusted, a pipeline step and by this the underlying containers gets access to escalated capabilities like mounting volumes.

:::note

Only server admins can set this option. If you are not a server admin this option won't be shown in your project settings.

:::

Also see [security boundaries](./17-security-boundaries.md#trusted-repositories).

## Custom trusted clone plugins

During the clone process, Git credentials (e.g., for private repositories) may be required.
These credentials are provided via [`netrc`](https://everything.curl.dev/usingcurl/netrc.html).

These credentials are injected only into trusted plugins specified in the environment variable `WOODPECKER_PLUGINS_TRUSTED_CLONE` (an instance-wide Woodpecker server setting) or declared in this repository-level setting.

With these credentials, it’s possible to perform any Git operations, including pushing changes back to the repo.
To prevent unauthorized access or misuse, a plugin allowlist is required, either on the instance level or the repository level.
Without an explicit allowlist, a malicious contributor could exploit a custom clone plugin in a Pull Request to reveal or transfer these credentials during the clone step.

:::info
This setting does not affect subsequent steps, nor does it allow direct pushes to the repository.
To enable pushing changes, you can inject Git credentials as a secret or use a dedicated plugin, such as [appleboy/drone-git-push](https://woodpecker-ci.org/plugins/git-push).
:::

Also see [security boundaries](./17-security-boundaries.md#git-credentials).

## Project visibility

You can change the visibility of your project by this setting. If a user has access to a project they can see all builds and their logs and artifacts. Settings, Secrets and Registries can only be accessed by owners.

- `Public` Every user can see your project without being logged in.
- `Internal` Only authenticated users of the Woodpecker instance can see this project.
- `Private` Only you and other owners of the repository can see this project.

## Timeout

After this timeout a pipeline has to finish or will be treated as timed out.

## Cancel previous pipelines

By enabling this option for a pipeline event previous pipelines of the same event and context will be canceled before starting the newly triggered one.

### Squash pending pipelines

By default a new pipeline cancels the previous ones even if they are already running, and only runs for its own changes. If [path filters](./20-workflow-syntax.md#path) decide which workflows run, the changes of a canceled pipeline are then never built or deployed.

With **Squash pending pipelines** enabled, a new pipeline only cancels previous pipelines (of an event selected above, with the same context) that have not started yet, and runs for their changes too: the files they changed are added to its own changed files before its configuration is fetched and its path filters are evaluated, and `CI_PIPELINE_FILES` lists all of them. A pipeline that is already running is left to finish. A branch with frequent pushes so has at most one pending pipeline at a time, and it covers every change since the pipelines that already started.

- Previous pipelines are only squashed into a newer one, never into a pipeline of an older commit, and a restarted pipeline squashes none.
- A pipeline waiting for approval squashes the previous pipelines when it is approved, and a previous pipeline waiting for approval is squashed like a pending one.
- The new pipeline takes the place in the queue of the oldest pipeline it squashes, or the earlier place that pipeline took over itself, so it does not wait behind pipelines created since. Its own creation time is unchanged.
- Only the changed files are carried over. Conditions on the commit message, author or other pipeline metadata are evaluated for the newest commit.
- Pipelines with unknown changes (no changed files, e.g. cron or manual pipelines and pushes where the forge reported no files) are only squashed with each other, as the `on_empty` setting of a path filter decides for them.
- If the new pipeline fails to start (e.g. its configuration cannot be loaded, or all its workflows are filtered out), the previous pipelines are not canceled.
- To also run at most one pipeline per branch at a time, limit the workflows' [concurrency](./20-workflow-syntax.md#concurrency) or use a single agent.
