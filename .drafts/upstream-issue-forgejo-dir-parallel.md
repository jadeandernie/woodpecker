# Upstream issue draft: Forgejo/Gitea config folders load too slowly

Repository: woodpecker-ci/woodpecker, template "Bug report". Component: server.

**Title:** Forgejo/Gitea: a config folder with many files cannot load within the 2 minute pipeline creation cap

---

### Describe the bug

For a `.woodpecker/` folder, the Forgejo and Gitea forges' `Dir` lists the folder and then loads each file one after the other by calling `File`. `File` creates a new SDK client every time, and creating a client first requests `/api/v1/version`. A folder with N files therefore costs 2N + 2 sequential requests, each with a new HTTP client (and, with `WOODPECKER_*_SKIP_VERIFY`, a new transport and TLS handshake).

In a repository with about 200 workflow files, loading the folder took about 35 s on an idle forge and more than 60 s under load.

Since #6781, a webhook-triggered pipeline is created under a fixed 2 minute cap (`backgroundPipelineCreationTimeout` in `server/api/hook.go`). Every forge retry restarts the whole sequential `Dir`. So two slow attempts use up the cap, and the pipeline errors at exactly 120 s with `context deadline exceeded`.

Raising `WOODPECKER_FORGE_TIMEOUT` or `WOODPECKER_FORGE_RETRY` does not help, because they sit inside the cap. For example, a 60 s timeout with 5 retries can never complete.

GitHub had the same problem and fixed it in #7025: a GraphQL request, with a REST fallback that loads at most 10 files at once.

### Steps to reproduce

1. Use a Forgejo or Gitea repository with a few hundred files in `.woodpecker/`, on a forge with some latency.
2. Push.
3. The pipeline errors after 2 minutes while loading its config. The server log shows one `/api/v1/version` request per config file.

### Expected behavior

- The config folder loads in time proportional to its size divided by some parallelism, not 2 requests per file in sequence.
- The cap on pipeline creation can be raised by the admin, like the forge timeout and retries it contains.

### Suggested fix

1. In the Forgejo and Gitea forges, let `Dir` create one client for the listing and all files, so the version is checked once. Then load the files in parallel with a bounded `errgroup` (10, like the GitHub forge), keeping the listing order and cancelling on the first error.
2. Make the 2 minute cap a server setting, e.g. `WOODPECKER_WEBHOOK_PIPELINE_CREATION_TIMEOUT`, with the current 2 minutes as the default.

I have patches for both and will open PRs.

### Additional context

- #3403 added the forge timeout and retry settings for Gitea/Forgejo.
- #6781 moved slow pipeline creation into the background with the fixed cap.
- #7025 fixed the same per-file loading for GitHub.
- A config extension (`WOODPECKER_CONFIG_SERVICE_ENDPOINT`) does not avoid the cost. The forge is fetched first and its files are passed to the extension, unless the extension is exclusive.

### Validations

- [x] Read the docs
- [x] Checked that there isn't already an issue that reports the same bug
- [x] Checked that the bug isn't fixed in the `next` version already
