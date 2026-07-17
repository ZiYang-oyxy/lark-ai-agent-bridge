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
require_contains "$doctor_output" "ok claude:" "doctor"
require_contains "$doctor_output" "ok default_workdir:" "doctor"
require_contains "$doctor_output" "ok audit_log:" "doctor"
require_contains "$doctor_output" "ok card_update_every:" "doctor"
require_contains "$doctor_output" "ok interaction_timeout:" "doctor"
require_contains "$doctor_output" "ok card_max_chars:" "doctor"
if [[ "${REQUIRE_LARK:-0}" == "1" ]]; then
  require_contains "$doctor_output" "ok LARK_APP_ID:" "doctor"
  require_contains "$doctor_output" "ok LARK_APP_SECRET:" "doctor"
fi

echo "== command surface simulation =="
help_output="$(go run ./cmd/lark-agent-bridge simulate -text "/help")"
require_contains "$help_output" "/new [--workdir" "help simulation"
require_contains "$help_output" "/status - show the current chat/topic session status" "help simulation"
require_not_contains "$help_output" "/resume" "help simulation"
require_not_contains "$help_output" "/codex" "help simulation"

plain_output="$(go run ./cmd/lark-agent-bridge simulate -text "hello")"
require_contains "$plain_output" '"SessionID": "claude:chat-demo:message:' "plain text simulation"
require_contains "$plain_output" "simulated answer: hello" "plain text simulation"

resume_output="$(go run ./cmd/lark-agent-bridge simulate -text "/resume")"
require_contains "$resume_output" "/resume 暂未实现" "resume disabled simulation"

codex_output="$(go run ./cmd/lark-agent-bridge simulate -text "/codex inspect")"
require_contains "$codex_output" "unknown command /codex" "codex disabled simulation"
echo "command surface ok"

echo "== group mention filter simulation =="
group_output="$(go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=false -text "hello")"
require_contains "$group_output" '"events": []' "group mention filter simulation"
echo "group mention filter ok"

echo "== session behavior tests =="
go test ./internal/bridge -run 'TestServiceQueuesSecondInputUntilFirstCompletes|TestDifferentTopicsRunInParallel|TestTopicPlainTextContinuesStoredClaudeSession|TestNewInTopicResetsStoredClaudeSession|TestServiceNewWithoutPromptCreatesReadySession|TestQueuedRunPreservesInputWorkDir'
go test ./internal/session -run 'TestEnqueueQueuesWhileRunning|TestResetClearsClaudeSessionAndHistory|TestQueuedResetClearsBeforeNextInput'
echo "session behavior ok"

echo "== claude one-shot runner tests =="
go test ./internal/agent ./internal/bridge -run 'TestBuildClaudeOneShotCommand|TestBuildClaudeOneShotCommandResumesInternalSession|TestBuildOneShotRejectsUnsupportedAgent|TestServiceNewRunsClaudeOneShotAndRendersResult|TestCLIExecRunnerUsesRequestedWorkDirAndPWD'
echo "claude one-shot ok"

echo "== stop button and workdir tests =="
go test ./internal/bridge ./internal/card -run 'TestServiceStopCancelsActiveOneShotRun|TestBuildLarkCardIncludesDisabledStopButton|TestServiceMissingWorkdirAsksThenRunsAfterCreate|TestWorkdirCancelDoesNotRun|TestWorkDirCreateActions|TestBuildLarkCardDisablesWorkdirTerminalActions'
missing_workdir="$ROOT/.cache/workdir-timeout-$RANDOM/missing"
workdir_timeout_output="$(go run ./cmd/lark-agent-bridge simulate -text "/new --workdir $missing_workdir hello" -timeout-now)"
require_contains "$workdir_timeout_output" '"Message": "workdir creation timed out: cancelled"' "workdir confirmation timeout simulation"
if [[ -e "$missing_workdir" ]]; then
  echo "verify failed: workdir timeout created $missing_workdir" >&2
  exit 1
fi
echo "stop and workdir ok"

echo "== card and long connection action tests =="
go test ./internal/card ./internal/feishu ./internal/bridge -run 'TestBuildLarkCardFormatsRichSegments|TestBuildLarkCardUsesDynamicHeaderAndStreamingMode|TestBuildLarkCardSupportsStoppedGreyHeader|TestBuildLarkCardUsesFinalStopButtonLabels|TestBuildCardActionFromLark|TestSDKLongConn|TestCallbackHTTPHandler|TestActionRequestFrom'
echo "card action ok"

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
