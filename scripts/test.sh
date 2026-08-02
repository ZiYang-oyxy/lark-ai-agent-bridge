#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
source "$ROOT/scripts/lib/runtime-paths.sh"
cd "$ROOT"
go test ./...
bash tests/e2e-profile-smoke.sh
bash tests/e2e-capabilities-smoke.sh
bash tests/release-regression-channel-smoke.sh
