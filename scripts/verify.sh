#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}"
mkdir -p "$GOCACHE"

require_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "verify failed: $label missing $needle" >&2
    return 1
  fi
}

require_not_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    echo "verify failed: $label unexpectedly contained $needle" >&2
    return 1
  fi
}

echo "== go test ./... =="
go test ./...

echo "== doctor =="
doctor_output="$(go run ./cmd/lark-agent-bridge doctor)"
echo "$doctor_output"
require_contains "$doctor_output" "ok tmux:" "doctor"
require_contains "$doctor_output" "ok default_workdir:" "doctor"
require_contains "$doctor_output" "ok audit_log:" "doctor"
require_contains "$doctor_output" "ok idle_reminder_after:" "doctor"
require_contains "$doctor_output" "ok idle_check_every:" "doctor"
require_contains "$doctor_output" "codex_config_hooks:" "doctor"
if [[ "${REQUIRE_LARK:-0}" == "1" ]]; then
  require_contains "$doctor_output" "ok LARK_APP_ID:" "doctor"
  require_contains "$doctor_output" "ok LARK_APP_SECRET:" "doctor"
fi

echo "== queue simulation =="
queue_output="$(go run ./cmd/lark-agent-bridge simulate -text "/claude first" -next-text "/claude second")"
require_contains "$queue_output" '"Message": "queued"' "queue simulation"
echo "queued reaction ok"

echo "== independent session tests =="
go test ./internal/bridge -run 'TestDifferentChatsCreateIndependentSessions|TestDifferentTopicsCreateIndependentSessions'
echo "independent sessions ok"

echo "== idle reminder termination tests =="
go test ./internal/session ./internal/bridge ./internal/card -run 'TestIdleReminderDueOnlyOnce|TestTouchResetsIdleReminder|TestRenderIdleReminders|TestPollOutputRefreshesIdleReminderClock|TestTerminateSessionActions|TestBuildLarkCardIncludesTerminateSessionAction'
echo "idle reminder termination ok"

echo "== stop button state tests =="
go test ./internal/bridge -run 'TestServiceStopActionInterruptsAndDisablesButton|TestServiceStopActionDequeuesNextInput'
echo "stop button state ok"

echo "== long connection card action tests =="
go test ./internal/feishu -run 'TestBuildCardActionFromLark|TestSDKLongConn|TestBotInfoClientGet'
echo "long connection card action ok"

echo "== group mention filter simulation =="
group_output="$(go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=false -text "hello")"
require_contains "$group_output" '"events": []' "group mention filter simulation"
echo "group mention filter ok"

echo "== agent launch mode simulation =="
default_agent_output="$(go run ./cmd/lark-agent-bridge simulate -text "hello")"
require_contains "$default_agent_output" '"SessionID": "claude:chat-demo"' "default agent simulation"
require_contains "$default_agent_output" '"claude"' "default agent simulation"
claude_auto_output="$(go run ./cmd/lark-agent-bridge simulate -text "/claude --approval auto hello")"
require_contains "$claude_auto_output" "claude --permission-mode acceptEdits" "claude auto approval simulation"
claude_full_output="$(go run ./cmd/lark-agent-bridge simulate -text "/claude --full hello")"
require_contains "$claude_full_output" "claude --dangerously-skip-permissions" "claude full approval simulation"
codex_auto_output="$(go run ./cmd/lark-agent-bridge simulate -text "/codex --approval auto inspect")"
require_contains "$codex_auto_output" "codex -c check_for_update_on_startup=false --ask-for-approval on-request" "codex auto approval simulation"
codex_full_output="$(go run ./cmd/lark-agent-bridge simulate -text "/codex --full inspect")"
require_contains "$codex_full_output" "codex -c check_for_update_on_startup=false --dangerously-bypass-approvals-and-sandbox" "codex full approval simulation"
echo "agent launch modes ok"

echo "== command surface simulation =="
help_output="$(go run ./cmd/lark-agent-bridge simulate -text "/help")"
require_contains "$help_output" "/sessions - list active sessions" "help simulation"
command_output="$(go run ./cmd/lark-agent-bridge simulate -text "/claude hello" -next-text "/status" -next-text "/sessions" -next-text "/history")"
require_contains "$command_output" '"SessionID": "status"' "command surface simulation"
require_contains "$command_output" "attach=tmux attach -t lark-agent-bridge:agent-claude-chat-demo" "command surface simulation"
require_contains "$command_output" '"SessionID": "sessions"' "command surface simulation"
require_contains "$command_output" '"SessionID": "history"' "command surface simulation"
topic_output="$(go run ./cmd/lark-agent-bridge simulate -thread topic-a -text "/claude first" -next-text "/topic off" -next-text "/claude second" -next-text "/status")"
require_contains "$topic_output" "claude:chat-demo:thread:topic-a" "topic simulation"
require_contains "$topic_output" "topic mode disabled" "topic simulation"
require_contains "$topic_output" "topic_mode=false" "topic simulation"
attach_output="$(go run ./cmd/lark-agent-bridge simulate -text "/attach")"
require_contains "$attach_output" "tmux attach -t lark-agent-bridge:agent-claude-chat-demo" "attach simulation"
interrupt_output="$(go run ./cmd/lark-agent-bridge simulate -text "/claude hello" -next-text "/interrupt")"
require_contains "$interrupt_output" '"Message": "interrupted"' "interrupt simulation"
require_contains "$interrupt_output" '"C-c"' "interrupt simulation"
stop_output="$(go run ./cmd/lark-agent-bridge simulate -text "/claude hello" -next-text "/stop")"
require_contains "$stop_output" '"Message": "stopped"' "stop simulation"
require_contains "$stop_output" '"kill-window"' "stop simulation"
echo "command surface ok"

echo "== resume simulation =="
resume_output="$(go run ./cmd/lark-agent-bridge simulate -text "/resume codex --last")"
require_contains "$resume_output" '"Message": "resume session launched"' "resume simulation"
require_contains "$resume_output" "codex -c check_for_update_on_startup=false resume --last" "resume simulation"
echo "resume simulation ok"

echo "== workdir confirmation timeout simulation =="
missing_workdir="$ROOT/.cache/workdir-timeout-$RANDOM/missing"
workdir_timeout_output="$(go run ./cmd/lark-agent-bridge simulate -text "/claude --workdir $missing_workdir hello" -timeout-now)"
require_contains "$workdir_timeout_output" '"Message": "workdir creation timed out: cancelled"' "workdir confirmation timeout simulation"
require_not_contains "$workdir_timeout_output" '"new-window"' "workdir confirmation timeout simulation"
if [[ -e "$missing_workdir" ]]; then
  echo "verify failed: workdir timeout created $missing_workdir" >&2
  exit 1
fi
echo "workdir confirmation timeout ok"

echo "== workdir create and cancel simulation =="
create_workdir="$ROOT/.cache/workdir-create-$RANDOM/missing"
workdir_create_output="$(go run ./cmd/lark-agent-bridge simulate-action -prime-text "/claude --workdir $create_workdir hello" -action create_workdir -value "$create_workdir")"
require_contains "$workdir_create_output" '"Type": "workdir_confirm"' "workdir create simulation"
require_contains "$workdir_create_output" "workdir created: $create_workdir" "workdir create simulation"
require_contains "$workdir_create_output" '"Type": "stream"' "workdir create simulation"
require_contains "$workdir_create_output" "$create_workdir" "workdir create simulation"
require_contains "$workdir_create_output" '"new-window"' "workdir create simulation"
if [[ ! -d "$create_workdir" ]]; then
  echo "verify failed: workdir create did not create $create_workdir" >&2
  exit 1
fi
cancel_workdir="$ROOT/.cache/workdir-cancel-$RANDOM/missing"
workdir_cancel_output="$(go run ./cmd/lark-agent-bridge simulate-action -prime-text "/claude --workdir $cancel_workdir hello" -action cancel_workdir -value "$cancel_workdir")"
require_contains "$workdir_cancel_output" '"Type": "workdir_confirm"' "workdir cancel simulation"
require_contains "$workdir_cancel_output" '"Message": "workdir creation cancelled"' "workdir cancel simulation"
require_not_contains "$workdir_cancel_output" '"new-window"' "workdir cancel simulation"
if [[ -e "$cancel_workdir" ]]; then
  echo "verify failed: workdir cancel created $cancel_workdir" >&2
  exit 1
fi
echo "workdir create and cancel ok"

echo "== rich segment simulation =="
rich_output="$(go run ./cmd/lark-agent-bridge simulate-output -output "plain\nThinking: inspect plan\nTool: Bash ls\nfinal")"
require_contains "$rich_output" '"Kind": "thought"' "rich segment simulation"
require_contains "$rich_output" '"Kind": "tool"' "rich segment simulation"
echo "rich segments ok"

echo "== interaction timeout simulation =="
timeout_output="$(go run ./cmd/lark-agent-bridge simulate-output -output "Tool permission required\nAllow once\nReject" -timeout-now)"
require_contains "$timeout_output" '"Message": "interaction timed out: sent reject"' "interaction timeout simulation"
require_contains "$timeout_output" '"reject"' "interaction timeout simulation"
echo "interaction timeout ok"

echo "== choice and resume interaction simulation =="
choice_output="$(go run ./cmd/lark-agent-bridge simulate-output -output "请选择:\n1. repo top3\n2. AI only" -timeout-now)"
require_contains "$choice_output" '"Type": "choice"' "choice interaction simulation"
require_contains "$choice_output" '"ID": "choice_1"' "choice interaction simulation"
require_contains "$choice_output" '"Message": "interaction timed out: sent cancel"' "choice interaction simulation"
resume_choice_output="$(go run ./cmd/lark-agent-bridge simulate-output -output "resume session\n1 34ccac3d 0s ago query\n2 def456 1m ago inspect" -timeout-now)"
require_contains "$resume_choice_output" '"Type": "resume"' "resume interaction simulation"
require_contains "$resume_choice_output" '"ID": "resume_1"' "resume interaction simulation"
require_contains "$resume_choice_output" '"ID": "resume_cancel"' "resume interaction simulation"
require_contains "$resume_choice_output" '"Message": "interaction timed out: sent cancel"' "resume interaction simulation"
echo "choice and resume interactions ok"

echo "== long stream pagination simulation =="
printf -v long_stream_text '%*s' 200 ''
long_stream_text="${long_stream_text// /x}"
long_stream_output="$(E2E_CARD_MAX_CHARS=40 go run ./cmd/lark-agent-bridge simulate-output -output "$long_stream_text")"
require_contains "$long_stream_output" '"Message": "page 5/5"' "long stream pagination simulation"
echo "long stream pagination ok"

if [[ -z "${LARK_APP_ID:-}" || -z "${LARK_APP_SECRET:-}" ]]; then
  echo "== serve credential guard =="
  set +e
  serve_output="$(go run ./cmd/lark-agent-bridge serve 2>&1)"
  serve_status=$?
  set -e
  if [[ $serve_status -eq 0 ]]; then
    echo "verify failed: serve succeeded without LARK_APP_ID/LARK_APP_SECRET" >&2
    exit 1
  fi
  require_contains "$serve_output" "LARK_APP_ID and LARK_APP_SECRET are required for serve" "serve credential guard"
  echo "serve credential guard ok"
else
  echo "== serve credential guard skipped: LARK_APP_ID/LARK_APP_SECRET are set =="
fi

echo "local verify ok"
