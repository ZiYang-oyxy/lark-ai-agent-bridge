#!/usr/bin/env bash
# Variables assigned by this contract are intentionally consumed by extracted
# harness functions through eval.
# shellcheck disable=SC2034
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/e2e-capabilities.sh"

fail() {
  echo "e2e-capabilities-smoke failed: $*" >&2
  exit 1
}

assert_eq() {
  local want="$1" got="$2" label="$3"
  [[ "$got" == "$want" ]] || fail "$label: got '$got', want '$want'"
}

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/lab-e2e-cap.XXXXXX")"
native_stop_pid=""
cleanup_smoke() {
  if [[ -n "${native_stop_pid:-}" ]]; then
    kill "$native_stop_pid" >/dev/null 2>&1 || true
    wait "$native_stop_pid" 2>/dev/null || true
  fi
  rm -rf "$TEST_ROOT"
}
trap cleanup_smoke EXIT INT TERM

e2e_cap_reset
e2e_cap_record credentials PASS ready "credentials ready" "" ""
e2e_cap_record dm_delivery BLOCKED open_id_cross_app "DM unavailable" "login through the bridge app" "dm.log"
assert_eq PASS "$(e2e_cap_status credentials)" "credentials status"
assert_eq BLOCKED "$(e2e_cap_status dm_delivery)" "DM status"
assert_eq SKIPPED "$(e2e_cap_status not_recorded)" "missing status"
assert_eq READY "$(e2e_cap_evaluate group_case credentials)" "ready prerequisite"
assert_eq BLOCKED "$(e2e_cap_evaluate debounce_dm credentials dm_delivery)" "blocked prerequisite"
assert_eq SKIPPED "$(e2e_cap_evaluate unknown_case credentials not_recorded)" "missing prerequisite"
assert_eq 0 "$(e2e_cap_exit_code 0)" "non-strict blocked exit"
assert_eq 3 "$(e2e_cap_exit_code 1)" "strict blocked exit"

e2e_cap_record wrapper FAIL bridge_crashed "bridge exited" "inspect server.log" "server.log"
assert_eq FAIL "$(e2e_cap_evaluate any_case wrapper dm_delivery)" "failure priority"
assert_eq 1 "$(e2e_cap_exit_code 0)" "non-strict failure exit"
assert_eq 1 "$(e2e_cap_exit_code 1)" "strict failure exit"

json_path="$TEST_ROOT/capabilities.json"
summary_path="$TEST_ROOT/summary.md"
e2e_cap_write_json "$json_path"
e2e_cap_write_summary "$summary_path"
jq -e '.schema_version == 1 and (.capabilities | length) == 3' "$json_path" >/dev/null
jq -e '.capabilities[] | has("name") and has("status") and has("reason_code") and has("summary") and has("remediation") and has("evidence_refs")' "$json_path" >/dev/null
rg -q 'dm_delivery.*BLOCKED.*open_id_cross_app' "$summary_path"

e2e_cap_reset
e2e_cap_import_json "$json_path" dm_delivery wrapper
assert_eq BLOCKED "$(e2e_cap_status dm_delivery)" "imported DM status"
assert_eq FAIL "$(e2e_cap_status wrapper)" "imported wrapper status"
assert_eq SKIPPED "$(e2e_cap_status credentials)" "unselected capability was not imported"

LOCK_ROOT="$TEST_ROOT/locks"
mkdir -p "$LOCK_ROOT"
chmod 700 "$LOCK_ROOT"
lock_path="$LOCK_ROOT/personal.lock"
e2e_profile_lock_acquire personal "$lock_path" run-one
if (
  # shellcheck disable=SC1091
  source "$ROOT/scripts/lib/e2e-capabilities.sh"
  e2e_profile_lock_acquire personal "$lock_path" run-two
) >/dev/null 2>&1; then
  fail "same profile acquired twice"
fi
if ! (
  # shellcheck disable=SC1091
  source "$ROOT/scripts/lib/e2e-capabilities.sh"
  e2e_profile_lock_acquire staging "$LOCK_ROOT/staging.lock" run-three
  e2e_profile_lock_release
); then
  fail "different profile lock did not run independently"
fi
e2e_profile_lock_release

mkdir "$LOCK_ROOT/stale.lock"
printf '%s\n%s\n%s\n' 999999 stale-run 2000-01-01T00:00:00Z >"$LOCK_ROOT/stale.lock/owner"
e2e_profile_lock_acquire stale "$LOCK_ROOT/stale.lock" fresh-run
assert_eq "$$" "$(sed -n '1p' "$LOCK_ROOT/stale.lock/owner")" "stale lock owner"
e2e_profile_lock_release

DOCTOR_STATE="$TEST_ROOT/doctor-state"
DOCTOR_BIN="$TEST_ROOT/doctor-bin"
DOCTOR_LOG="$TEST_ROOT/doctor-calls.log"
mkdir -p "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles" "$DOCTOR_BIN"
chmod 700 "$DOCTOR_STATE/.lark-agent-bridge" "$DOCTOR_STATE/.lark-agent-bridge/e2e" "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles"
cat >"$DOCTOR_BIN/lark-cli" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "--profile" ]]; then
  [[ "${2:-}" == lab-e2e-* ]] || exit 91
  cli_profile="$2"
  printf 'profile=%s ' "$2" >>"${DOCTOR_LOG:?}"
  shift 2
fi
printf '%s\n' "$*" >>"${DOCTOR_LOG:?}"
case "$*" in
  'auth status --json --verify') printf '%s\n' '{"verified":true,"app_id":"cli_doctor_app"}' ;;
  auth\ check\ --scope\ *\ --json)
    checked_scope="${4:-}"
    if [[ "${cli_profile:-}" == "lab-e2e-no-send" ]]; then
      printf '%s\n' "{\"ok\":false,\"granted\":null,\"missing\":[\"$checked_scope\"]}"
      exit 1
    fi
    printf '%s\n' "{\"ok\":true,\"granted\":[\"$checked_scope\"],\"missing\":null}"
    ;;
  *'im chats get'*'--chat-id oc_doctor_group'*) printf '%s\n' '{"data":{"chat_id":"oc_doctor_group"}}' ;;
  *'im chats get'*'--chat-id oc_doctor_p2p'*) printf '%s\n' '{"data":{"chat_id":"oc_doctor_p2p","chat_mode":"p2p"}}' ;;
  *) echo "unexpected fake lark-cli call" >&2; exit 1 ;;
esac
EOF
cat >"$DOCTOR_BIN/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "curl $*" >>"${DOCTOR_LOG:?}"
case "$*" in
  *tenant_access_token*) printf '%s\n' '{"tenant_access_token":"doctor-token"}' ;;
  *bot/v3/info*) printf '%s\n' '{"bot":{"open_id":"ou_doctor_bot"}}' ;;
  *) echo "unexpected fake curl call" >&2; exit 1 ;;
esac
EOF
cat >"$DOCTOR_BIN/claude" <<'EOF'
#!/usr/bin/env bash
echo "doctor unexpectedly invoked claude" >>"${DOCTOR_LOG:?}"
exit 1
EOF
cat >"$DOCTOR_BIN/go" <<'EOF'
#!/usr/bin/env bash
echo "preflight unexpectedly invoked go" >>"${DOCTOR_LOG:?}"
exit 1
EOF
chmod +x "$DOCTOR_BIN/lark-cli" "$DOCTOR_BIN/curl" "$DOCTOR_BIN/claude" "$DOCTOR_BIN/go"

write_doctor_profile() {
  local name="$1" p2p="$2" dir="$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles"
  cat >"$dir/$name.env" <<EOF
LARK_APP_ID=cli_doctor_app
LARK_APP_SECRET=doctor-secret
LARK_BOT_OPEN_ID=ou_doctor_bot
E2E_E2E_CHAT_ID=oc_doctor_group
E2E_REAL_E2E_P2P_CHAT_ID=$p2p
E2E_E2E_LARK_CLI_PROFILE=lab-e2e-$name
EOF
  printf '%s\n' '{"schema_version":1}' >"$dir/$name.json"
  chmod 600 "$dir/$name.env" "$dir/$name.json"
}

run_expect_exit() {
  local want="$1"
  shift
  set +e
  "$@" >/dev/null 2>&1
  local got=$?
  set -e
  assert_eq "$want" "$got" "exit code for $*"
}

write_doctor_profile pass oc_doctor_p2p
: >"$DOCTOR_LOG"
run_expect_exit 0 env \
  E2E_STATE_ROOT="$DOCTOR_STATE" DOCTOR_LOG="$DOCTOR_LOG" PATH="$DOCTOR_BIN:$PATH" E2E_CLAUDE_BIN="$DOCTOR_BIN/claude" \
  bash "$ROOT/scripts/e2e-real.sh" --profile pass --doctor --run-dir "$TEST_ROOT/doctor-pass"
if rg -q 'messages-send|serve|unexpectedly invoked claude' "$DOCTOR_LOG"; then
  fail "static doctor performed an active operation"
fi
jq -e '.capabilities[] | select(.name == "p2p_chat" and .status == "PASS")' "$TEST_ROOT/doctor-pass/capabilities.json" >/dev/null
if rg -v '^profile=lab-e2e-pass ' "$DOCTOR_LOG" | rg -q .; then
  fail "named profile doctor used the global lark-cli profile"
fi

write_doctor_profile no-send oc_doctor_p2p
run_expect_exit 3 env \
  E2E_STATE_ROOT="$DOCTOR_STATE" DOCTOR_LOG="$DOCTOR_LOG" PATH="$DOCTOR_BIN:$PATH" E2E_CLAUDE_BIN="$DOCTOR_BIN/claude" \
  bash "$ROOT/scripts/e2e-real.sh" --profile no-send --doctor --strict-capabilities --run-dir "$TEST_ROOT/doctor-no-send"
jq -e '.capabilities[] | select(.name == "lark_cli_auth" and .status == "BLOCKED" and .reason_code == "user_e2e_scopes_missing")' \
  "$TEST_ROOT/doctor-no-send/capabilities.json" >/dev/null

write_doctor_profile blocked ''
run_expect_exit 0 env \
  E2E_STATE_ROOT="$DOCTOR_STATE" DOCTOR_LOG="$DOCTOR_LOG" PATH="$DOCTOR_BIN:$PATH" E2E_CLAUDE_BIN="$DOCTOR_BIN/claude" \
  bash "$ROOT/scripts/e2e-real.sh" --profile blocked --doctor --run-dir "$TEST_ROOT/doctor-blocked"
run_expect_exit 3 env \
  E2E_STATE_ROOT="$DOCTOR_STATE" DOCTOR_LOG="$DOCTOR_LOG" PATH="$DOCTOR_BIN:$PATH" E2E_CLAUDE_BIN="$DOCTOR_BIN/claude" \
  bash "$ROOT/scripts/e2e-real.sh" --profile blocked --doctor --strict-capabilities --run-dir "$TEST_ROOT/doctor-blocked-strict"

run_expect_exit 2 env E2E_STATE_ROOT="$DOCTOR_STATE" PATH="$DOCTOR_BIN:$PATH" \
  bash "$ROOT/scripts/e2e-real.sh" --profile missing --doctor --run-dir "$TEST_ROOT/doctor-missing"

write_doctor_profile static-blocked oc_doctor_p2p
awk '{if ($0 ~ /^LARK_APP_SECRET=/) print "LARK_APP_SECRET="; else print}' \
  "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/static-blocked.env" >"$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/static-blocked.env.tmp"
mv "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/static-blocked.env.tmp" "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/static-blocked.env"
chmod 600 "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/static-blocked.env"
: >"$DOCTOR_LOG"
run_expect_exit 3 env \
  E2E_STATE_ROOT="$DOCTOR_STATE" DOCTOR_LOG="$DOCTOR_LOG" PATH="$DOCTOR_BIN:$PATH" E2E_CLAUDE_BIN="$DOCTOR_BIN/claude" \
  bash "$ROOT/scripts/e2e-real.sh" --profile static-blocked --preflight-only --strict-capabilities --run-dir "$TEST_ROOT/preflight-static-blocked"
if rg -q 'preflight unexpectedly invoked go|messages-send|serve|unexpectedly invoked claude' "$DOCTOR_LOG"; then
  fail "preflight ignored a blocked static prerequisite"
fi

write_doctor_profile fail-fast oc_doctor_p2p
awk '{if ($0 ~ /^E2E_E2E_CHAT_ID=/) print "E2E_E2E_CHAT_ID=oc_missing_group"; else print}' \
  "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/fail-fast.env" >"$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/fail-fast.env.tmp"
mv "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/fail-fast.env.tmp" "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/fail-fast.env"
chmod 600 "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/fail-fast.env"
cat >"$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/fail-fast.capabilities.json" <<'EOF'
{"schema_version":1,"generated_at":"2099-01-01T00:00:00Z","capabilities":[{"name":"media_image","status":"PASS","reason_code":"ready","summary":"previous image canary passed","remediation":"","evidence_refs":[]}]}
EOF
chmod 600 "$DOCTOR_STATE/.lark-agent-bridge/e2e/profiles/fail-fast.capabilities.json"
: >"$DOCTOR_LOG"
fail_fast_summary="$TEST_ROOT/fail-fast/summary.md"
run_expect_exit 0 env \
  E2E_STATE_ROOT="$DOCTOR_STATE" DOCTOR_LOG="$DOCTOR_LOG" PATH="$DOCTOR_BIN:$PATH" E2E_CLAUDE_BIN="$DOCTOR_BIN/claude" \
  bash "$ROOT/scripts/e2e-real.sh" --profile fail-fast --case media_attachment_only --run-dir "$TEST_ROOT/fail-fast"
if rg -q 'preflight unexpectedly invoked go|messages-send|serve|unexpectedly invoked claude' "$DOCTOR_LOG"; then
  fail "runner started active work although every selected case was blocked"
fi
rg -q '^## E2E capabilities$' "$fail_fast_summary"
rg -q '^## media_attachment_only$' "$fail_fast_summary"
rg -q 'test_group.*BLOCKED' "$fail_fast_summary"
if [[ -e "$DOCTOR_STATE/.lark-agent-bridge/e2e/locks/fail-fast.lock" ]]; then
  fail "fully blocked run acquired an active profile lock"
fi
run_expect_exit 3 env \
  E2E_STATE_ROOT="$DOCTOR_STATE" DOCTOR_LOG="$DOCTOR_LOG" PATH="$DOCTOR_BIN:$PATH" E2E_CLAUDE_BIN="$DOCTOR_BIN/claude" \
  bash "$ROOT/scripts/e2e-real.sh" --profile fail-fast --case media_attachment_only --strict-capabilities --run-dir "$TEST_ROOT/fail-fast-strict"

LEGACY_STATE="$TEST_ROOT/legacy-state"
mkdir -p "$LEGACY_STATE/.lark-agent-bridge"
chmod 700 "$LEGACY_STATE/.lark-agent-bridge"
cat >"$LEGACY_STATE/.lark-agent-bridge/e2e.env" <<'EOF'
export LARK_APP_ID="cli_doctor_app"
export LARK_APP_SECRET="legacy-doctor-secret"
export LARK_BOT_OPEN_ID="ou_doctor_bot"
export E2E_E2E_CHAT_ID="oc_doctor_group"
export E2E_REAL_E2E_P2P_CHAT_ID="oc_doctor_p2p"
export E2E_REAL_E2E_FAKE_CLAUDE="1"
EOF
chmod 600 "$LEGACY_STATE/.lark-agent-bridge/e2e.env"
legacy_before="$(shasum -a 256 "$LEGACY_STATE/.lark-agent-bridge/e2e.env")"
legacy_output="$TEST_ROOT/legacy-doctor.out"
set +e
E2E_STATE_ROOT="$LEGACY_STATE" DOCTOR_LOG="$DOCTOR_LOG" PATH="$DOCTOR_BIN:$PATH" E2E_CLAUDE_BIN="$DOCTOR_BIN/claude" \
  bash "$ROOT/scripts/e2e-real.sh" --doctor --run-dir "$TEST_ROOT/legacy-doctor" >"$legacy_output" 2>&1
legacy_status=$?
set -e
assert_eq 0 "$legacy_status" "legacy doctor exit"
assert_eq "$legacy_before" "$(shasum -a 256 "$LEGACY_STATE/.lark-agent-bridge/e2e.env")" "legacy profile remained unchanged"
rg -q 'legacy .lark-agent-bridge/e2e.env is in use' "$legacy_output"
if rg -q 'legacy-doctor-secret|ou_doctor_bot|oc_doctor_' "$legacy_output"; then
  fail "legacy doctor output leaked a secret or full ID"
fi

send_at_source="$(sed -n '/^send_at() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$send_at_source" | rg -q -- '--msg-type post'; then
  fail "group trigger must use a real post mention, not text that resembles an @ tag"
fi
if ! printf '%s\n' "$send_at_source" | rg -q 'tag:"at"'; then
  fail "group trigger post must contain a Feishu at element"
fi

debounce_prerequisite_source="$(sed -n '/^case_prerequisites() {/,/^}/p' "$ROOT/scripts/e2e-real.sh" | sed -n '/debounce_dm)/,/;;/p')"
if printf '%s\n' "$debounce_prerequisite_source" | rg -q 'p2p_chat'; then
  fail "debounce_dm must be able to create the P2P chat through bot open_id"
fi
if printf '%s\n' "$debounce_prerequisite_source" | rg -q 'dm_delivery'; then
  fail "debounce_dm is the DM delivery probe and must not depend on a cached DM result"
fi
active_dm_source="$(sed -n '/^run_active_capability_preflight() {/,/^}/p' "$ROOT/scripts/e2e-real.sh" | sed -n '/dm_delivery credentials/,/fi/p')"
if printf '%s\n' "$active_dm_source" | head -n 1 | rg -q 'p2p_chat'; then
  fail "active DM canary must not require a pre-existing P2P chat_id"
fi

startup_source="$(sed -n '/^start_server_if_needed() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$startup_source" | rg -F 'connected to wss' >/dev/null; then
  fail "bridge readiness must use the WSS connection log"
fi
if printf '%s\n' "$startup_source" | rg -F '/card/callback' >/dev/null; then
  fail "bridge readiness must not depend on the callback endpoint"
fi
callback_probe_source="$(sed -n '/^wait_callback_ready() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$callback_probe_source" | rg -F '"challenge":"e2e-ready"' >/dev/null; then
  fail "action cases must retain the callback challenge probe"
fi
callback_selection_source="$(sed -n '/^configure_callback_for_cases() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$callback_selection_source" | rg -F 'native_text_stream|latest_restart_fallback' >/dev/null; then
  fail "callback must be enabled only for selected action cases"
fi
server_start_source="$(sed -n '/^start_server_if_needed() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$server_start_source" | rg -F '[[ -n "$CALLBACK_ADDR" ]]' >/dev/null; then
  fail "non-action E2E servers must not receive E2E_CALLBACK_ADDR"
fi
if ! rg -F 'svc.OutputImages = sender' "$ROOT/cmd/lark-agent-bridge/main.go" >/dev/null; then
  fail "production serve must wire SDKSender as the output image sender"
fi
mget_source="$(sed -n '/^mget() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$mget_source" | rg -F 'audit_reply_message_id' >/dev/null; then
  fail "mget must resolve non-thread CardKit replies from the reply audit"
fi
fake_claude_source="$(sed -n '/^prepare_fake_claude_if_needed() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$fake_claude_source" | rg -F "grep -Eo 'E2E_[A-Za-z0-9_-]+'" >/dev/null; then
  fail "fake Claude must preserve the prompt marker for smoke/full assertions"
fi
if ! printf '%s\n' "$fake_claude_source" | rg -F '*E2E_*_STREAM*)' >/dev/null; then
  fail "fake Claude must emit an intermediate delta for streaming_card"
fi
if ! printf '%s\n' "$fake_claude_source" | rg -F '*E2E_*_OUTPUT_IMAGE*)' >/dev/null; then
  fail "fake Claude must expose an explicit local image output contract"
fi
native_fake_source="$(printf '%s\n' "$fake_claude_source" | sed -n '/\*E2E_\*_NATIVE_TEXT_STREAM_NORMAL\*)/,/;;/p')"
if ! printf '%s\n' "$fake_claude_source" | rg -F '*E2E_*_NATIVE_TEXT_STREAM_NORMAL*)' >/dev/null || \
  ! printf '%s\n' "$fake_claude_source" | rg -F '*E2E_*_NATIVE_TEXT_STREAM_STOP_E2E_BLOCK*)' >/dev/null; then
  fail "native text stream fake patterns must include the run-id segment"
fi
if ! printf '%s\n' "$native_fake_source" | awk '
  /native-normal-three/ { third = NR }
  third && /sleep / { pause = NR }
  /"type":"result"/ { result = NR }
  END { exit !(third && pause > third && result > pause) }
'; then
  fail "native text stream fake must leave a preview interval between its final delta and terminal result"
fi

# Execute the fake generated by the real harness so marker routing is covered as
# behavior, not only as source text. Representative markers include a dynamic
# run-id segment, which is where the native routing regression occurred.
FAKE_CONTRACT_DIR="$TEST_ROOT/fake-contract"
RUN_DIR="$FAKE_CONTRACT_DIR"
FAKE_BIN_DIR="$FAKE_CONTRACT_DIR/bin"
FAKE_CLAUDE_LOG="$FAKE_CONTRACT_DIR/fake-claude.log"
USE_FAKE_CLAUDE=1
export FAKE_CLAUDE_LOG
mkdir -p "$FAKE_CONTRACT_DIR"
eval "$fake_claude_source"
prepare_fake_claude_if_needed

"$FAKE_BIN_DIR/claude" 'E2E_20260719-123456_NATIVE_TEXT_STREAM_NORMAL' >"$FAKE_CONTRACT_DIR/native-normal.jsonl"
rg -q 'native-normal-one' "$FAKE_CONTRACT_DIR/native-normal.jsonl"
rg -q 'native-normal-final' "$FAKE_CONTRACT_DIR/native-normal.jsonl"

"$FAKE_BIN_DIR/claude" 'E2E_20260719-123456_STREAM' >"$FAKE_CONTRACT_DIR/stream.jsonl"
rg -q 'content_block_delta' "$FAKE_CONTRACT_DIR/stream.jsonl"
rg -q 'E2E_20260719-123456_STREAM' "$FAKE_CONTRACT_DIR/stream.jsonl"

"$FAKE_BIN_DIR/claude" 'E2E_20260719-123456_DEFAULT' >"$FAKE_CONTRACT_DIR/default.jsonl"
rg -q 'FAKE_E2E_STARTED E2E_20260719-123456_DEFAULT' "$FAKE_CONTRACT_DIR/default.jsonl"

(
  cd "$FAKE_CONTRACT_DIR"
  "$FAKE_BIN_DIR/claude" 'E2E_20260719-123456_OUTPUT_IMAGE' >"$FAKE_CONTRACT_DIR/output-image.jsonl"
)
test -s "$FAKE_CONTRACT_DIR/e2e-output-E2E_20260719-123456_OUTPUT_IMAGE.png"
rg -Fq '![E2E_20260719-123456_OUTPUT_IMAGE](./e2e-output-E2E_20260719-123456_OUTPUT_IMAGE.png)' "$FAKE_CONTRACT_DIR/output-image.jsonl"

: >"$FAKE_CONTRACT_DIR/native-stop.jsonl"
"$FAKE_BIN_DIR/claude" 'E2E_20260719-123456_NATIVE_TEXT_STREAM_STOP_E2E_BLOCK' >"$FAKE_CONTRACT_DIR/native-stop.jsonl" &
native_stop_pid=$!
for _ in 1 2 3 4 5 6 7; do
  [[ "$(wc -l <"$FAKE_CONTRACT_DIR/native-stop.jsonl")" -ge 3 ]] && break
  sleep 0.5
done
kill "$native_stop_pid" >/dev/null 2>&1 || true
wait "$native_stop_pid" 2>/dev/null || true
native_stop_pid=""
assert_eq 3 "$(wc -l <"$FAKE_CONTRACT_DIR/native-stop.jsonl" | tr -d ' ')" "native stop fake delta count"

fail_fast_source="$(sed -n '/^wait_audit_expected_before_terminal() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if [[ -z "$fail_fast_source" ]]; then
  fail "audit waiter must fail fast when a terminal event precedes the expected event"
fi
audit_since_source="$(sed -n '/^audit_since() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
eval "$audit_since_source"
eval "$fail_fast_source"
AUDIT="$TEST_ROOT/fail-fast-audit.log"
: >"$AUDIT"
printf '%s\n' 'message=<FEISHU_MESSAGE_ID> event=result' >>"$AUDIT"
SECONDS=0
if wait_audit_expected_before_terminal 0 'cardkit_text_stream' 'event=result' 5 2>"$TEST_ROOT/fail-fast.err"; then
  fail "terminal-first audit unexpectedly satisfied the native stream waiter"
fi
if (( SECONDS >= 2 )); then
  fail "terminal-first audit did not fail fast"
fi
rg -q 'terminal event observed before expected event' "$TEST_ROOT/fail-fast.err"
: >"$AUDIT"
printf '%s\n' 'session=current Action=cardkit_text_stream' >>"$AUDIT"
wait_audit_expected_before_terminal 0 'session=current.*cardkit_text_stream' 'session=current.*event=result' 5
: >"$AUDIT"
printf '%s\n' \
  'session=current event=result' \
  'session=current Action=cardkit_text_stream' >>"$AUDIT"
if wait_audit_expected_before_terminal 0 'session=current.*cardkit_text_stream' 'session=current.*event=result' 5 2>"$TEST_ROOT/ordered-fail-fast.err"; then
  fail "terminal-first audit in one snapshot was incorrectly accepted"
fi
: >"$AUDIT"
printf '%s\n' \
  'session=unrelated Action=cardkit_text_stream' \
  'session=current event=result' >>"$AUDIT"
if wait_audit_expected_before_terminal 0 'session=current.*cardkit_text_stream' 'session=current.*event=result' 5 2>"$TEST_ROOT/scoped-fail-fast.err"; then
  fail "unrelated stream incorrectly satisfied the current-session waiter"
fi

summary_init_source="$(sed -n '/^summary_init() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
summary_footer_source="$(sed -n '/^append_normal_summary_footer() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
summary_timing_source="$(sed -n '/^append_run_timing() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if [[ -z "$summary_timing_source" ]]; then
  fail "real E2E timing must have an idempotent exit-path finalizer"
fi
cleanup_source="$(sed -n '/^cleanup() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$cleanup_source" | rg -F 'append_run_timing' >/dev/null; then
  fail "real E2E cleanup must finalize timing on failure exits"
fi
if ! printf '%s\n' "$cleanup_source" | awk '
  /append_run_timing/ { timing = NR }
  /stop_server TERM/ { stop = NR }
  END { exit !(timing && stop && timing < stop) }
'; then
  fail "real E2E cleanup must finalize timing before fallible process cleanup"
fi
for required in 'started_at' 'finished_at' 'wall_clock_sec'; do
  if ! printf '%s\n%s\n%s\n' "$summary_init_source" "$summary_timing_source" "$summary_footer_source" | rg -F -- "$required" >/dev/null; then
    fail "real E2E summary must record authoritative timing field: $required"
  fi
done
SUMMARY="$TEST_ROOT/timing-summary.md"
RUN_ID="timing-contract"
RUN_STARTED_AT="2026-07-19T00:00:00Z"
RUN_STARTED_EPOCH=$(( $(date +%s) - 2 ))
SUMMARY_INITIALIZED=0
RUN_TIMING_FINALIZED=0
MODE="smoke"
RUN_DIR="$TEST_ROOT/timing-run"
AUDIT="$RUN_DIR/audit.jsonl"
CALLBACK_ADDR=""
DEFAULT_WORKDIR="$TEST_ROOT/workdir"
USE_FAKE_CLAUDE=1
FAILURES=0
BLOCKED_CASES=0
MESSAGES="$RUN_DIR/messages.jsonl"
SERVER_LOG="$RUN_DIR/server.log"
eval "$summary_init_source"
eval "$(sed -n '/^summary() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
eval "$summary_timing_source"
eval "$summary_footer_source"
summary_init
append_normal_summary_footer
append_run_timing
rg -q '^- started_at: 2026-07-19T00:00:00Z$' "$SUMMARY"
rg -q '^- finished_at: [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' "$SUMMARY"
wall_clock_value="$(sed -n 's/^- wall_clock_sec: //p' "$SUMMARY")"
if [[ ! "$wall_clock_value" =~ ^[0-9]+$ || "$wall_clock_value" -lt 2 ]]; then
  fail "real E2E summary wall_clock_sec is not an authoritative non-negative duration: $wall_clock_value"
fi
assert_eq 1 "$(rg -c '^- finished_at:' "$SUMMARY")" "run timing finalizer count"

native_cases="$(bash "$ROOT/scripts/e2e-real.sh" --list-cases)"
if ! printf '%s\n' "$native_cases" | rg -Fx 'native_text_stream' >/dev/null; then
  fail "native text stream must be an explicitly selectable E2E case"
fi
expected_l2_cases="$(cat <<'EOF'
new_basic
streaming_card
session_restart_context
recall_state
media_attachment_only
media_images
media_text_files
native_text_stream
latest_restart_fallback
EOF
)"
assert_eq "$expected_l2_cases" "$native_cases" "real E2E must contain only the L2 platform subset"

latest_restart_source="$(sed -n '/^case_latest_restart_fallback() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if ! printf '%s\n' "$latest_restart_source" | rg -F '服务重启，已中断，请重新发送' >/dev/null; then
  fail "latest restart evidence must assert the rendered Chinese punctuation"
fi

native_case_source="$(sed -n '/^case_native_text_stream() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
for required in \
  'cardkit_text_stream' \
  'cardkit_update' \
  'cardkit_sequence_unknown' \
  'event=stopped' \
  'streaming_mode=false' \
  'buttons=disabled' \
  'callback_elapsed > 3'; do
  if ! printf '%s\n' "$native_case_source" | rg -F -- "$required" >/dev/null; then
    fail "native text stream contract is missing: $required"
  fi
done

run_case_source="$(sed -n '/^run_case() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
if printf '%s\n' "$run_case_source" | rg -F 'SERVER_CARD_UPDATE_MS' >/dev/null || \
  printf '%s\n' "$run_case_source" | rg -F 'SERVER_CARD_MIN_DELTA_CHARS' >/dev/null; then
  fail "native text stream must run with the shared server timing instead of restarting for case-local parameters"
fi
if ! printf '%s\n' "$run_case_source" | rg -F 'soft_recover_after_failure "$name"' >/dev/null; then
  fail "failed cases must use soft recovery instead of restarting the bridge"
fi
if printf '%s\n' "$run_case_source" | rg -F 'start_server_if_needed recovery' >/dev/null; then
  fail "failed case recovery must not restart the bridge"
fi
parallel_source="$(sed -n '/^run_parallel_media_pair() {/,/^}/p' "$ROOT/scripts/e2e-real.sh")"
for required in 'media_images' 'media_text_files' 'wait "$group_pid"' 'wait "$p2p_pid"'; do
  if ! printf '%s\n' "$parallel_source" | rg -F -- "$required" >/dev/null; then
    fail "isolated media pair parallel contract is missing: $required"
  fi
done

if rg -F 'SERVER_REAL_CARDKIT' "$ROOT/scripts/e2e-real.sh" >/dev/null || rg -F 'E2E_REAL_CARDKIT=$SERVER_REAL_CARDKIT' "$ROOT/scripts/e2e-real.sh" >/dev/null; then
  fail "retired native validation gate is still present"
fi

echo "e2e capability smoke ok"
