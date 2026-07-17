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
    echo "# Feishu Agent Bridge Evidence"
    echo
    echo "- generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "- workdir: $ROOT"
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
  local tmux_output
  local tmux_matches

  {
    echo "serve processes:"
    if ps_output="$(ps -ef 2>&1)"; then
      resource_ps_checked=1
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
    echo
    echo "tmux bridge sessions:"
    if tmux_output="$(tmux list-sessions 2>&1)"; then
      resource_tmux_checked=1
      tmux_matches="$(printf '%s\n' "$tmux_output" | grep -E '^(lark-agent-bridge|lark-agent-bridge-smoke|lark-agent-bridge-watch-smoke):' || true)"
      if [[ -n "$tmux_matches" ]]; then
        echo "$tmux_matches"
        status=1
      else
        echo "none"
      fi
    else
      echo "not checked: $tmux_output"
    fi
  } >"$log"
  append_command "$title" "ps/tmux resource residue check" "$log" "$status"
  return "$status"
}

append_header

local_status=0
preflight_status=0
tmux_status=0
agent_probe_status=0
resource_status=0
resource_ps_checked=0
resource_tmux_checked=0

run_section "local-verify" "./scripts/verify.sh" ./scripts/verify.sh || local_status=$?

if [[ "${RUN_TMUX_SMOKE:-0}" == "1" ]]; then
  run_section "tmux-smoke" "./scripts/tmux-smoke.sh" ./scripts/tmux-smoke.sh || tmux_status=$?
else
  {
    echo "## tmux-smoke"
    echo
    echo "- status: skipped"
    echo "- reason: set RUN_TMUX_SMOKE=1 to execute; this may need access to the system tmux socket"
    echo
  } >>"$REPORT"
fi

if [[ "${RUN_AGENT_PROBE:-0}" == "1" ]]; then
  run_section "agent-probe" "./scripts/agent-probe.sh" ./scripts/agent-probe.sh || agent_probe_status=$?
else
  {
    echo "## agent-probe"
    echo
    echo "- status: skipped"
    echo "- reason: set RUN_AGENT_PROBE=1 to execute; set REQUIRE_AGENT_READY=1 to fail when an agent is not ready"
    echo
  } >>"$REPORT"
fi

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
  if [[ "${RUN_TMUX_SMOKE:-0}" == "1" ]]; then
    if [[ "$tmux_status" -eq 0 ]]; then
      echo "- tmux smoke: passed"
    else
      echo "- tmux smoke: failed with status $tmux_status"
    fi
  else
    echo "- tmux smoke: skipped"
  fi
  if [[ "${RUN_AGENT_PROBE:-0}" == "1" ]]; then
    if [[ "$agent_probe_status" -eq 0 ]]; then
      echo "- agent probe: completed"
    else
      echo "- agent probe: failed with status $agent_probe_status"
    fi
  else
    echo "- agent probe: skipped"
  fi
  if [[ "$resource_status" -eq 0 ]]; then
    if [[ "$resource_ps_checked" -eq 1 && "$resource_tmux_checked" -eq 1 ]]; then
      echo "- resource status: clean"
    elif [[ "$resource_ps_checked" -eq 1 ]]; then
      echo "- resource status: serve clean; tmux not checked"
    elif [[ "$resource_tmux_checked" -eq 1 ]]; then
      echo "- resource status: tmux clean; serve not checked"
    else
      echo "- resource status: not fully checked"
    fi
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
if [[ "${RUN_TMUX_SMOKE:-0}" == "1" && "$tmux_status" -ne 0 ]]; then
  exit "$tmux_status"
fi
if [[ "${RUN_AGENT_PROBE:-0}" == "1" && "$agent_probe_status" -ne 0 ]]; then
  exit "$agent_probe_status"
fi
if [[ "${REQUIRE_E2E:-0}" == "1" && "$preflight_status" -ne 0 ]]; then
  exit "$preflight_status"
fi
