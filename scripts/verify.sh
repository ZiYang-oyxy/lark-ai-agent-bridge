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

echo "== group intake documentation contracts =="
readme_output="$(<README.md)"
help_source="$(<internal/bridge/command.go)"
capability_source="$(<scripts/e2e-real.sh)"
for mode in mention_only participated_topics all_group_messages; do
  require_contains "$readme_output" "$mode" "README group intake modes"
done
require_contains "$readme_output" "im:message.group_msg" "README group scope"
require_contains "$readme_output" "E2E_PARTICIPATED_TOPICS_STORE" "README participation store"
require_contains "$readme_output" "默认关闭" "README bot sender default"
require_contains "$help_source" "participated topics" "help group intake"
require_contains "$capability_source" "group_message_intake" "real E2E capability"
require_contains "$capability_source" "scope_incremental_grant" "real E2E capability"
echo "group intake documentation contracts ok"

echo "== go test ./... =="
go test ./...

echo "== doctor =="
doctor_output="$(go run ./cmd/lark-agent-bridge doctor)"
echo "$doctor_output"
require_contains "$doctor_output" "ok claude:" "doctor"
require_contains "$doctor_output" "ok default_workdir:" "doctor"
require_contains "$doctor_output" "ok audit_log:" "doctor"
require_contains "$doctor_output" "ok session_catalog:" "doctor"
require_contains "$doctor_output" "ok card_update_every:" "doctor"
require_contains "$doctor_output" "ok interaction_timeout:" "doctor"
require_contains "$doctor_output" "ok card_max_chars:" "doctor"
if [[ "${REQUIRE_LARK:-0}" == "1" ]]; then
  require_contains "$doctor_output" "ok LARK_APP_ID:" "doctor"
  require_contains "$doctor_output" "ok LARK_APP_SECRET:" "doctor"
fi

echo "== version and release tooling =="
version_output="$(go run ./cmd/lark-agent-bridge version --json)"
require_contains "$version_output" '"version":"dev"' "development version"
require_contains "$version_output" '"goos":"' "version platform"
release_help="$(go run ./cmd/lark-bridge-release help)"
require_contains "$release_help" "prepare vMAJOR.MINOR.PATCH" "release prepare help"
require_contains "$release_help" "bundle vMAJOR.MINOR.PATCH" "release bundle help"
echo "version and release tooling ok"

echo "== command surface simulation =="
	help_output="$(go run ./cmd/lark-agent-bridge simulate -text "/help")"
	require_contains "$help_output" '"Type": "help"' "help simulation"
	require_contains "$help_output" "当前版本：dev" "help current version"
	require_contains "$help_output" '**`/new`** `[--workdir path]' "help simulation"
	require_contains "$help_output" '**`/config`** 全局运行偏好' "help simulation"
	require_contains "$help_output" '**`/resume`** `[session-id]' "help simulation"
require_not_contains "$help_output" "/codex" "help simulation"

plain_output="$(go run ./cmd/lark-agent-bridge simulate -text "hello")"
require_contains "$plain_output" '"SessionID": "claude:chat-demo:message:' "plain text simulation"
require_contains "$plain_output" "simulated answer: hello" "plain text simulation"

resume_output="$(go run ./cmd/lark-agent-bridge simulate -text "/resume")"
require_contains "$resume_output" "Session 历史存储不可用" "resume simulation without durable catalog"

codex_output="$(go run ./cmd/lark-agent-bridge simulate -text "/codex inspect")"
require_contains "$codex_output" "unknown command /codex" "codex disabled simulation"
echo "command surface ok"

echo "== group mention filter simulation =="
group_output="$(go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=false -text "hello")"
require_contains "$group_output" '"events": []' "group mention filter simulation"
echo "group mention filter ok"

echo "== group intake mode simulation =="
all_group_output="$(E2E_GROUP_MESSAGE_MODE=all_group_messages go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=false -text "hello all")"
require_contains "$all_group_output" "simulated answer: hello all" "all group messages simulation"

bot_off_output="$(E2E_GROUP_MESSAGE_MODE=all_group_messages go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=true -sender-type bot -text "/help")"
require_contains "$bot_off_output" '"events": []' "bot sender default off simulation"
bot_on_output="$(E2E_GROUP_MESSAGE_MODE=all_group_messages E2E_RESPOND_TO_BOTS=true go run ./cmd/lark-agent-bridge simulate -group=true -mentioned=true -sender-type bot -text "/help")"
require_contains "$bot_on_output" '**`/new`** `[--workdir path]' "bot sender enabled simulation"

topics_store="$ROOT/.cache/verify-participated-topics-$RANDOM.json"
E2E_GROUP_MESSAGE_MODE=participated_topics E2E_PARTICIPATED_TOPICS_STORE="$topics_store" go run ./cmd/lark-agent-bridge simulate -group=true -thread=topic-verify -mentioned=true -text "/help" >/dev/null
topic_followup_output="$(E2E_GROUP_MESSAGE_MODE=participated_topics E2E_PARTICIPATED_TOPICS_STORE="$topics_store" go run ./cmd/lark-agent-bridge simulate -group=true -thread=topic-verify -mentioned=false -text "/help")"
require_contains "$topic_followup_output" '**`/new`** `[--workdir path]' "participated topic restart simulation"
rm -f "$topics_store"
echo "group intake modes ok"

echo "== session behavior tests =="
go test ./internal/bridge -run 'TestServiceResume|TestServiceRecordsCompletedSession|TestServiceQueuesSecondInputUntilFirstCompletes|TestDifferentTopicsRunInParallel|TestTopicPlainTextContinuesStoredClaudeSession|TestNewInTopicResetsStoredClaudeSession|TestServiceNewWithoutPromptCreatesReadySession|TestQueuedRunPreservesInputWorkDir|TestServiceSkipsDuplicateRunAndOldDelivery|TestServiceRejectsTwentyFirstPendingInput|TestServiceRestoreDoesNotRunClearedQueue|TestServiceMergesBusyTopicInputsIntoNextBatch|TestServiceStopKeepsLaterQueue|TestMessageRecallRemovesQueuedInput'
go test ./internal/session -run 'TestCatalog|TestCanonicalWorkDir|TestManagerResume|TestEnqueueQueuesWhileRunning|TestResetClearsClaudeSessionAndHistory|TestQueuedResetClearsBeforeNextInput|TestRestoreKeepsContextButClearsPending|TestRestoreDebouncingInputIsCancelled|TestRestoreQueuedInputIsCancelled|TestRestoreStartingInputIsCancelled|TestRestoreRunningInputIsInterrupted|TestAcceptAndEnqueueDuplicateDoesNotAdvanceRevision|TestAcceptAndEnqueueQueueFullDoesNotRecordReceipt|TestAcceptAndEnqueueExtendsCompatibleDebounceCohort'
echo "session behavior ok"

echo "== agent one-shot runner tests =="
go test ./internal/agent ./internal/config ./internal/bridge -run 'TestBuildClaudeOneShotCommand|TestBuildClaudeOneShotCommandResumesInternalSession|TestBuildCodexOneShotCommand|TestParseKindAcceptsCodex|TestBuildOneShotRejectsUnsupportedAgent|TestAgentsConfigAcceptsCodexAndCxPresets|TestServiceNewRunsClaudeOneShotAndRendersResult|TestCLIExecRunnerUsesRequestedWorkDirAndPWD|TestCLIExecRunnerRunsCodexWithStdinImagesAndConfiguredWorkDir|TestServiceRunsConfiguredCodexPresetWithImagesAndResumesThread|TestParseCodexStream'
echo "agent one-shot ok"

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
