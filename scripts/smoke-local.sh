#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
source "$ROOT/scripts/lib/runtime-paths.sh"
source "$ROOT/scripts/lib/assert.sh"
source "$ROOT/scripts/lib/simulate-suite.sh"

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

echo "== assembly go test =="
go test ./internal/config ./internal/bridge ./internal/card ./internal/agent ./internal/session ./internal/schedule

echo "== doctor =="
doctor_output="$(go run ./cmd/lark-agent-bridge doctor)"
echo "$doctor_output"
require_contains "$doctor_output" "ok claude:" "doctor"
require_contains "$doctor_output" "ok default_workdir:" "doctor"

echo "== command surface =="
smoke_command_surface
smoke_group_intake

stop_output="$(simulate -text "/stop")"
require_contains "$stop_output" "当前会话没有正在运行的任务。" "idle stop"

echo "== equivalent write commands =="
smoke_tmp="$(mktemp -d)"
trap 'rm -rf "$smoke_tmp"' EXIT
preference_store="$smoke_tmp/preferences.json"

config_output="$(E2E_PREFERENCE_STORE="$preference_store" simulate -text "/config set effort=high")"
require_contains "$config_output" '"Action": "config_saved"' "config set audit"
require_contains "$config_output" "effort=high" "config set value"

local_output="$(E2E_PREFERENCE_STORE="$preference_store" simulate -group=true -mentioned=true -text "/local-config set effort=high")"
require_contains "$local_output" '"Action": "local_config_saved"' "local config set audit"

mkdir_target="$smoke_tmp/workspace/new-dir"
mkdir -p "$smoke_tmp/workspace"
mkdir_output="$(E2E_DEFAULT_WORKDIR="$smoke_tmp/workspace" simulate -text "/mkdir $mkdir_target")"
require_contains "$mkdir_output" '"Type": "workdir_created"' "mkdir event"
test -d "$mkdir_target"

cron_output="$(simulate -text "/cron confirm")"
require_contains "$cron_output" "定时任务服务尚未配置" "cron confirm routing"

echo "SMOKE_LOCAL_OK"
