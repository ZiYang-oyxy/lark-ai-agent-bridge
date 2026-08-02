# Release and Installation Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a verifiable RC and make production installation diagnostics, secrets, and process ownership safe.

**Architecture:** Add focused helpers at the SDK logging, credential-loading, doctor, locking, and release boundaries. Preserve current CLI compatibility while separating immutable Release asset URLs from mutable channel URLs.

**Tech Stack:** Go 1.26.3, Bash, systemd user services, GitHub Actions, GitHub Releases, GitHub Pages.

---

### Task 1: Redacted SDK Logging

**Files:** `internal/feishu/sdk_logger.go`, `internal/feishu/sdk_logger_test.go`, `internal/feishu/sdk_longconn.go`

- [ ] Add a failing test proving secrets and sensitive query values are removed while level and non-sensitive context remain.
- [ ] Run `go test ./internal/feishu -run RedactingLogger` and confirm the secret is present before the implementation.
- [ ] Implement the larkcore logger adapter and inject it into the WebSocket client.
- [ ] Re-run the focused and package tests.

### Task 2: Portable Release Evidence

**Files:** `cmd/lark-bridge-release/evidence_test.go`, `cmd/lark-bridge-release/evidence.go`, `scripts/test.sh`, `scripts/verify.sh`, `scripts/release.sh`, `scripts/evidence.sh`, `scripts/e2e-preflight.sh`, `scripts/release-regression.sh`

- [ ] Add failing tests for a cache directory outside the checkout and an evidence directory writable independently of the repository owner.
- [ ] Run the focused release-tool tests and confirm the old source-local paths fail.
- [ ] Resolve cache/state through XDG cache/state directories with secure modes and explicit overrides.
- [ ] Run the release-tool tests from a read-only source copy.

### Task 3: RC-Safe Release Publishing

**Files:** `cmd/lark-bridge-release/main_test.go`, `cmd/lark-bridge-release/main.go`, `tests/release-bundle-smoke.sh`, `.github/workflows/release.yml`, `README.md`, `docs/releases/README.md`

- [ ] Add failing tests for channel selection and split asset/channel URLs.
- [ ] Confirm an RC currently writes `stable` and uses one URL base.
- [ ] Implement compatibility argument parsing, channel-aware output, and split metadata URLs.
- [ ] Add a tag workflow that verifies assets before publishing the channel last.
- [ ] Run Go and shell release smoke tests for both RC and stable tags.

### Task 4: Online Doctor, Secret Files, and App Lock

**Files:** `internal/credentials/credentials.go`, `internal/credentials/credentials_test.go`, `internal/doctor/online.go`, `internal/doctor/online_test.go`, `internal/applock/lock_unix.go`, `internal/applock/lock_unix_test.go`, `cmd/lark-agent-bridge/main.go`, `cmd/lark-agent-bridge/main_test.go`, `README.md`, `docs/framework/architecture.md`

- [ ] Add failing tests for secure file loading, precedence, JSON output, online API failures, lock conflicts, and cleanup.
- [ ] Run each focused test and confirm failure for the missing behavior.
- [ ] Implement credentials before config loading, structured doctor output, bounded online checks, and the lifetime serve lock.
- [ ] Re-run focused tests and `go test ./...`.

### Task 5: Release and Local Upgrade

**Files:** `docs/releases/v0.1.14-rc.4.md`, `tasks.md`, generated release artifacts outside Git tracking, local systemd configuration outside the repo

- [ ] Run the full repository verification and release evidence gate.
- [ ] Review all diffs for correctness and secret leakage, then remove completed items from `tasks.md`.
- [ ] Commit, push main, create annotated `v0.1.14-rc.4`, and let the tag workflow publish it.
- [ ] Verify the GitHub release, manifests, checksums, and remote binary version.
- [ ] Install the verified Linux asset locally, configure both update channel URLs, restart the service, and perform a real `/status` roundtrip.

### Task 6: Dada Bare Codex Default

**Files:** `/root/ws/dada-workspace/tests/test_ai_command_profiles.sh`, `/root/ws/dada-workspace/bin/agent-defaults`, `/root/ws/dada-workspace/bin/codex`

- [ ] Add a failing regression for `codexSuffix: ""` in diagnostic and execution paths.
- [ ] Confirm the validator rejects the documented onboard output.
- [ ] Accept the empty suffix and run the official Codex CLI without a named `cxN` profile while retaining managed workspace flags.
- [ ] Run the focused workspace test and the relevant installer/onboard suites.
