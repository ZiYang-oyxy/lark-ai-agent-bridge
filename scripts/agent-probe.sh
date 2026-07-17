#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${E2E_AGENT_PROBE_WORKDIR:-$ROOT}"

TIMEOUT_SEC="${E2E_AGENT_PROBE_TIMEOUT_SEC:-6}"
AGENTS_CSV="${E2E_AGENT_PROBE_AGENTS:-claude,codex}"
REQUIRE_READY="${REQUIRE_AGENT_READY:-0}"

classify_output() {
  local agent="$1"
  local output="$2"
  local lower
  lower="$(printf '%s' "$output" | tr '[:upper:]' '[:lower:]')"

  if [[ "$lower" == *"not logged in"* || "$lower" == *"run /login"* || "$lower" == *"login required"* ]]; then
    echo "not_logged_in"
    return
  fi
  if [[ "$lower" == *"quick safety check"* || "$lower" == *"trust this folder"* || "$lower" == *"trust the contents of this directory"* || "$lower" == *"do you trust"* ]]; then
    echo "needs_trust"
    return
  fi
  if [[ "$lower" == *"ready for input"* || "$lower" == *"waiting for input"* || "$lower" == *"how can i help"* ]]; then
    echo "ready"
    return
  fi
  case "$agent" in
    codex)
      if [[ "$output" == *"›"* && "$lower" != *"update available"* ]]; then
        echo "ready"
        return
      fi
      ;;
    claude)
      if [[ "$output" == *"│"* && "$lower" == *"claude"* && "$lower" != *"quick safety check"* ]]; then
        echo "ready"
        return
      fi
      ;;
  esac
  echo "unknown"
}

command_for_agent() {
  case "$1" in
    claude) printf '%s\n' "claude" ;;
    codex) printf '%s\n' "codex -c check_for_update_on_startup=false" ;;
    *)
      echo "unsupported agent: $1" >&2
      return 1
      ;;
  esac
}

if ! command -v tmux >/dev/null 2>&1; then
  echo "fail tmux: not found"
  exit 1
fi

overall_status=0
IFS=',' read -r -a agents <<<"$AGENTS_CSV"
for raw_agent in "${agents[@]}"; do
  agent="$(printf '%s' "$raw_agent" | xargs)"
  [[ -n "$agent" ]] || continue
  cmd="$(command_for_agent "$agent")"
  session="lark-agent-bridge-agent-probe-${agent}-$$"
  output=""
  status="unknown"

  cleanup() {
    tmux kill-session -t "$session" >/dev/null 2>&1 || true
  }
  trap cleanup EXIT

  if ! tmux new-session -d -s "$session" "$cmd" >/dev/null 2>&1; then
    echo "fail $agent: cannot start tmux session"
    overall_status=1
    trap - EXIT
    cleanup
    continue
  fi

  sleep "$TIMEOUT_SEC"
  output="$(tmux capture-pane -t "$session" -p -S -200 2>&1 || true)"
  status="$(classify_output "$agent" "$output")"
  echo "ok $agent: $status"
  case "$status" in
    ready)
      ;;
    needs_trust)
      echo "hint $agent: approve the trust prompt in Feishu or run the agent once in this workdir and choose the trusted option"
      if [[ "$REQUIRE_READY" == "1" ]]; then
        overall_status=1
      fi
      ;;
    not_logged_in)
      echo "hint $agent: complete the agent login flow before real generation E2E"
      if [[ "$REQUIRE_READY" == "1" ]]; then
        overall_status=1
      fi
      ;;
    *)
      echo "hint $agent: first screen was not recognized; inspect with E2E_AGENT_PROBE_TIMEOUT_SEC=$TIMEOUT_SEC scripts/agent-probe.sh"
      if [[ "$REQUIRE_READY" == "1" ]]; then
        overall_status=1
      fi
      ;;
  esac

  trap - EXIT
  cleanup
done

exit "$overall_status"
