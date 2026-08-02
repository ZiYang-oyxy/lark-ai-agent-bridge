#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Supervisor/serve environments inject durable state paths. They are runtime
# inputs, not verifier inputs: leaving them set makes default-path config tests
# read the live workspace instead of the verifier's temporary workdir.
unset E2E_PREFERENCE_STORE E2E_REPLY_STORE E2E_MEDIA_CACHE_DIR E2E_SESSION_STORE

source "$ROOT/scripts/lib/runtime-paths.sh"

source "$ROOT/scripts/lib/assert.sh"
source "$ROOT/scripts/lib/simulate-suite.sh"

echo "== group intake documentation contracts =="
readme_output="$(<README.md)"
help_source="$(<internal/bridge/command.go)"
capability_source="$(find scripts/e2e -type f -name '*.sh' -print0 | xargs -0 cat)"
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
smoke_command_surface
echo "command surface ok"

echo "== group mention filter simulation =="
smoke_group_intake
echo "group mention filter ok"

echo "== 冒烟测试套件 =="
go run ./cmd/lark-bridge-test --source . --smoke
echo "冒烟测试通过"

echo "== group intake mode simulation =="
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

if [[ -z "${LAB_LARK_APP_ID:-${LARK_APP_ID:-}}" || -z "${LAB_LARK_APP_SECRET:-${LARK_APP_SECRET:-}}" ]]; then
  echo "== serve credential guard =="
  set +e
  serve_output="$(go run ./cmd/lark-agent-bridge serve 2>&1)"
  serve_status=$?
  set -e
  if [[ $serve_status -eq 0 ]]; then
    echo "verify failed: serve succeeded without LARK_APP_ID/LARK_APP_SECRET" >&2
    exit 1
  fi
  require_contains "$serve_output" "LAB_LARK_APP_ID and LAB_LARK_APP_SECRET are required for serve" "serve credential guard"
  echo "serve credential guard ok"
else
  echo "== serve credential guard skipped: LARK_APP_ID/LARK_APP_SECRET are set =="
fi

echo "local verify ok"
