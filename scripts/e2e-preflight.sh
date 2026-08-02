#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

source "$ROOT/scripts/lib/runtime-paths.sh"

require_env() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "missing required env: $name" >&2
    return 1
  fi
  echo "ok $name: set"
}

require_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "preflight failed: $label missing $needle" >&2
    return 1
  fi
}

echo "== required Feishu credentials =="
require_env LARK_APP_ID
require_env LARK_APP_SECRET

if [[ -n "${E2E_CALLBACK_ADDR:-}" ]]; then
  echo "ok E2E_CALLBACK_ADDR: $E2E_CALLBACK_ADDR (legacy local HTTP callback enabled)"
else
  echo "ok E2E_CALLBACK_ADDR: not set; CardKit buttons use long connection card.action.trigger"
fi

echo "== doctor =="
doctor_output="$(go run ./cmd/lark-agent-bridge doctor)"
echo "$doctor_output"
require_contains "$doctor_output" "ok claude:" "doctor"
require_contains "$doctor_output" "ok LARK_APP_ID:" "doctor"
require_contains "$doctor_output" "ok LARK_APP_SECRET:" "doctor"
require_contains "$doctor_output" "ok default_workdir:" "doctor"
require_contains "$doctor_output" "ok audit_log:" "doctor"
require_contains "$doctor_output" "ok card_update_every:" "doctor"
require_contains "$doctor_output" "ok interaction_timeout:" "doctor"
require_contains "$doctor_output" "ok card_max_chars:" "doctor"

echo "== long connection action and CardKit unit coverage =="
go test ./internal/bridge -run 'TestCallbackHTTPHandler|TestActionRequestFrom'
go test ./internal/feishu -run 'TestBotInfo|TestBuildCardAction|TestSDKLongConn|TestCardKit|TestReactionCardRenderer'

echo "== local behavior evidence =="
REQUIRE_LARK=1 ./scripts/verify.sh

echo "e2e preflight ok"
echo "next: start the bridge without E2E_CALLBACK_ADDR for long connection button E2E, then execute docs/workflow/testing.md Feishu E2E steps."
