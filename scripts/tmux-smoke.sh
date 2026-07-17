#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SESSION="lark-agent-bridge-smoke"
WINDOW="agent-smoke"

cleanup() {
  tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cleanup
tmux new-session -d -s "$SESSION" -n control
tmux new-window -d -t "$SESSION" -n "$WINDOW" "cat"
tmux send-keys -t "$SESSION:$WINDOW" -- "tmux-smoke-ok" C-m
sleep 0.2
OUTPUT="$(tmux capture-pane -p -t "$SESSION:$WINDOW" -S -20)"

if [[ "$OUTPUT" != *"tmux-smoke-ok"* ]]; then
  echo "tmux smoke failed: expected output not found" >&2
  echo "$OUTPUT" >&2
  exit 1
fi

export GOCACHE="${GOCACHE:-$PWD/.cache/go-build}"
mkdir -p "$GOCACHE"
go test -tags tmux ./internal/tmux -run TestPaneWatcherRealTmuxCapturesExternalInput -count=1

echo "tmux smoke ok"
