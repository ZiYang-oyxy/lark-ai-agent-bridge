#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}"
mkdir -p "$GOCACHE"

usage() {
  cat <<'USAGE'
Usage: scripts/smoke-local.sh [-h|--help]

Local seconds-level smoke: assembly-level go test + doctor + core command-surface
simulate. No network, no deploy. Prints SMOKE_LOCAL_OK on success.
USAGE
}

case "${1:-}" in
  -h|--help) usage; exit 0 ;;
esac

require_contains() {
  local haystack="$1" needle="$2" label="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "smoke-local failed: $label missing $needle" >&2
    exit 1
  fi
}

echo "== assembly go test =="
go test ./internal/config ./internal/bridge ./internal/card ./internal/agent

echo "== doctor =="
doctor_output="$(go run ./cmd/lark-agent-bridge doctor)"
echo "$doctor_output"
require_contains "$doctor_output" "ok claude:" "doctor"
require_contains "$doctor_output" "ok default_workdir:" "doctor"

echo "== command surface =="
help_output="$(go run ./cmd/lark-agent-bridge simulate -text "/help")"
require_contains "$help_output" '"Type": "help"' "help simulation"

plain_output="$(go run ./cmd/lark-agent-bridge simulate -text "hello")"
require_contains "$plain_output" "simulated answer: hello" "plain text simulation"

group_output="$(go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=false -text "hello")"
require_contains "$group_output" '"events": []' "group mention filter"

stop_output="$(go run ./cmd/lark-agent-bridge simulate -text "/stop")"
require_contains "$stop_output" "当前会话没有正在运行的任务。" "idle stop"

echo "SMOKE_LOCAL_OK"
