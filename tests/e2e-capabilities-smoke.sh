#!/usr/bin/env bash
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
trap 'rm -rf "$TEST_ROOT"' EXIT

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
  printf 'profile=%s ' "$2" >>"${DOCTOR_LOG:?}"
  shift 2
fi
printf '%s\n' "$*" >>"${DOCTOR_LOG:?}"
case "$*" in
  'auth status --json --verify') printf '%s\n' '{"verified":true,"app_id":"cli_doctor_app"}' ;;
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

echo "e2e capability smoke ok"
