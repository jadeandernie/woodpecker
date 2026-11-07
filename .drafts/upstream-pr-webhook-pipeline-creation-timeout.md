# Upstream PR draft: configurable webhook pipeline creation timeout

Repository: woodpecker-ci/woodpecker, base `main`.
Head: `jadeandernie:webhook-pipeline-creation-timeout`.
Enable "Allow edits by maintainers".

**Title:** Make the webhook pipeline creation timeout configurable

---

Part of #<issue number from upstream-issue-forgejo-dir-parallel.md>
Related #6781, #3403

Since #6781, a webhook-triggered pipeline is created under a fixed 2 minute cap. The cap also covers loading the config from the forge with all its retries. The defaults already let the config fetch alone use 90 s of it: `WOODPECKER_FORGE_RETRY` of 3 attempts, each with a `WOODPECKER_FORGE_TIMEOUT` of 30 s. Raising those settings past the cap has no effect, and nothing tells the admin why.

This adds `WOODPECKER_WEBHOOK_PIPELINE_CREATION_TIMEOUT` (`--webhook-pipeline-creation-timeout`):

- The default stays 2 minutes. If the value is not set or is not positive, the built-in default is used, so a server config without the field (as in tests) keeps the old behaviour.
- It is documented next to `WEBHOOK_SYNC_TIMEOUT`, with a pointer to `FORGE_TIMEOUT` and `FORGE_RETRY`.

### Tests

`TestHook` sets the timeout to one hour and checks that the context passed to the config service has a deadline one hour away. On the old code that deadline is 2 minutes away.

`go test -race -tags test ./server/api/ ./cmd/server/` passes.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
