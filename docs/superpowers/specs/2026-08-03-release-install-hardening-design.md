# Release and Installation Hardening Design

## Goal

Make a tagged RC safe to publish and consume directly from an installed Bridge while improving deployment diagnostics and credential handling.

## Confirmed Scope

- Keep Feishu SDK information logs, but redact credentials, tickets, tokens, signatures, and query values before they reach stderr.
- Store release-test evidence outside the checkout and give Go commands a writable cache even when the source tree is owned by another account.
- Publish `v0.1.14-rc.4` from the next tag. RC bundles update only the `prerelease` channel; stable releases update only `stable`.
- Host immutable binaries as GitHub Release assets and fixed channel manifests/install guides on GitHub Pages.
- Add `doctor --online --json`; `--strict` makes every failed online or static check fatal.
- Support `LARK_APP_ID_FILE` and `LARK_APP_SECRET_FILE` with strict regular-file, ownership, and permission checks. Direct environment values remain compatible and take precedence.
- Prevent two local Bridge processes from using the same App ID through a nonblocking advisory lock keyed by an App ID digest. Never persist the App ID itself.

## Interfaces

`bundle` accepts distinct immutable asset and channel bases. The compatibility `--base-url` form remains supported for external hosts. Generated asset URLs point at the GitHub Release; generated guide and channel URLs point at GitHub Pages.

`doctor --online` calls the minimum Feishu Open API needed to validate the application credentials and bot identity with a bounded timeout. JSON output contains structured check records and a summary, never raw credentials or authorization headers.

Secret files must be regular non-symlink files, owned by the current effective user, and inaccessible to group or others. Their trailing CR/LF is removed; an empty value is rejected.

The same-host App lock is held for the lifetime of `serve` and released by process exit. A conflicting process exits before opening the long connection.

## Release Ordering

The tag workflow builds and validates the version directory, creates the prerelease GitHub Release, uploads immutable assets, reads each asset back and verifies size/SHA-256, then publishes the channel manifest last. This prevents a channel manifest from referencing incomplete assets.

## Compatibility and Risk

Existing environment-only deployments and external `--base-url` publishing remain valid. The main behavioral correction is that an RC can no longer mutate the stable channel. Online doctor is opt-in to avoid network access in existing startup checks.
