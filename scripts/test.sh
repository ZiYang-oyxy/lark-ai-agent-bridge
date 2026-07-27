#!/usr/bin/env bash
set -euo pipefail

export GOCACHE="${GOCACHE:-$PWD/.cache/go-build}"
mkdir -p "$GOCACHE"
go test ./...
bash tests/e2e-profile-smoke.sh
bash tests/e2e-capabilities-smoke.sh
bash tests/release-regression-channel-smoke.sh
