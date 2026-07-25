#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repo_root"
export GOCACHE="${GOCACHE:-$repo_root/.cache/go-build}"
# Release commands run validation internally and must not inherit live runtime stores.
unset E2E_PREFERENCE_STORE E2E_REPLY_STORE E2E_MEDIA_CACHE_DIR E2E_SESSION_STORE
export LAB_RELEASE_GO_BIN="${LAB_RELEASE_GO_BIN:-$(command -v go)}"
exec go run ./cmd/lark-bridge-release "$@"
