#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}"
OUT_DIR="${EVIDENCE_DIR:-$ROOT/.cache/evidence}"
mkdir -p "$GOCACHE" "$OUT_DIR"

STAMP="$(date +%Y%m%d-%H%M%S)"
REPORT="$OUT_DIR/evidence-$STAMP.md"

append_header() {
  {
    echo "# Feishu AI Agent Bridge Evidence"
    echo
    echo "- generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "- workdir: $ROOT"
    echo "- mode: claude one-shot card bridge"
    echo "- note: generated reports live under .cache and are intentionally ignored by git"
    echo
  } >"$REPORT"
}

append_command() {
  local title="$1"
  local display="$2"
  local log="$3"
  local status="$4"
  {
    echo "## $title"
    echo
    echo "- command: \`$display\`"
    echo "- exit_status: $status"
    echo
    echo '```text'
    cat "$log"
    echo '```'
    echo
  } >>"$REPORT"
}

run_section() {
  local title="$1"
  local display="$2"
  shift 2
  local log="$OUT_DIR/$STAMP-$title.log"
  set +e
  "$@" >"$log" 2>&1
  local status=$?
  set -e
  append_command "$title" "$display" "$log" "$status"
  return "$status"
}

append_resource_status() {
  local title="resource-status"
  local log="$OUT_DIR/$STAMP-$title.log"
  local status=0
  local serve_matches
  local ps_output

  {
    echo "serve processes:"
    if ps_output="$(ps -ef 2>&1)"; then
      serve_matches="$(printf '%s\n' "$ps_output" | grep -E 'lark-agent-bridge serve|go run \./cmd/lark-agent-bridge serve' | grep -v 'grep -E' || true)"
      if [[ -n "$serve_matches" ]]; then
        echo "$serve_matches"
        status=1
      else
        echo "none"
      fi
    else
      echo "not checked: $ps_output"
    fi
  } >"$log"
  append_command "$title" "ps resource residue check" "$log" "$status"
  return "$status"
}

append_header

local_status=0
preflight_status=0
resource_status=0

run_section "local-verify" "./scripts/verify.sh" ./scripts/verify.sh || local_status=$?

run_section "ignore-check" "git check-ignore reference/.cache/runtime artifacts" \
  git -C "$ROOT" check-ignore -v reference/lark-agent-workspace .cache .lark-agent-bridge .DS_Store || local_status=$?

run_section "git-status" "git status --short" git status --short || local_status=$?

append_resource_status || resource_status=$?
if [[ "$resource_status" -ne 0 ]]; then
  local_status=$resource_status
fi

if [[ -n "${LARK_APP_ID:-}" && -n "${LARK_APP_SECRET:-}" ]]; then
  run_section "e2e-preflight" "./scripts/e2e-preflight.sh" ./scripts/e2e-preflight.sh || preflight_status=$?
else
  {
    echo "## e2e-preflight"
    echo
    echo "- status: pending"
    echo "- missing: LARK_APP_ID or LARK_APP_SECRET"
    echo "- next: set real Feishu app credentials and run ./scripts/e2e-preflight.sh"
    echo
  } >>"$REPORT"
  preflight_status=1
fi

{
  echo "## Summary"
  echo
  if [[ "$local_status" -eq 0 ]]; then
    echo "- local evidence: passed"
  else
    echo "- local evidence: failed with status $local_status"
  fi
  if [[ "$resource_status" -eq 0 ]]; then
    echo "- resource status: clean"
  else
    echo "- resource status: residue found with status $resource_status"
  fi
  if [[ "$preflight_status" -eq 0 ]]; then
    echo "- Feishu E2E preflight: passed"
  else
    echo "- Feishu E2E preflight: pending or failed"
  fi
  echo "- report: $REPORT"
} >>"$REPORT"

echo "$REPORT"

if [[ "$local_status" -ne 0 ]]; then
  exit "$local_status"
fi
if [[ "${REQUIRE_E2E:-0}" == "1" && "$preflight_status" -ne 0 ]]; then
  exit "$preflight_status"
fi
