#!/usr/bin/env bash
set -euo pipefail

export GOCACHE="${GOCACHE:-$PWD/.cache/go-build}"
mkdir -p "$GOCACHE"
go test ./...
