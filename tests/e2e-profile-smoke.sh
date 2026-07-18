#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/e2e-profile.sh"

fail() {
  echo "e2e-profile-smoke failed: $*" >&2
  exit 1
}

assert_eq() {
  local want="$1" got="$2" label="$3"
  [[ "$got" == "$want" ]] || fail "$label: got '$got', want '$want'"
}

assert_ok() {
  "$@" >/dev/null 2>&1 || fail "expected success: $*"
}

assert_fail() {
  if "$@" >/dev/null 2>&1; then
    fail "expected failure: $*"
  fi
}

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/lab-e2e-profile.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT
mkdir -p "$TEST_ROOT/.lark-agent-bridge/e2e/profiles"
chmod 700 "$TEST_ROOT/.lark-agent-bridge" "$TEST_ROOT/.lark-agent-bridge/e2e" "$TEST_ROOT/.lark-agent-bridge/e2e/profiles"

assert_ok e2e_profile_validate_name personal
assert_ok e2e_profile_validate_name dev.alice-1
assert_fail e2e_profile_validate_name .
assert_fail e2e_profile_validate_name ..
assert_fail e2e_profile_validate_name ../escape
assert_fail e2e_profile_validate_name 'bad/name'
assert_fail e2e_profile_validate_name 'bad name'

export LARK_APP_ID="cli_test_app"
export LARK_APP_SECRET="secret-do-not-print"
export LARK_BOT_OPEN_ID="ou_profile_secret_bot"
export E2E_E2E_CHAT_ID="oc_profile_secret_group"
export E2E_REAL_E2E_P2P_CHAT_ID="oc_profile_secret_p2p"
e2e_profile_write "$TEST_ROOT" personal

personal_env="$TEST_ROOT/.lark-agent-bridge/e2e/profiles/personal.env"
personal_json="$TEST_ROOT/.lark-agent-bridge/e2e/profiles/personal.json"
assert_eq 600 "$(stat -f '%Lp' "$personal_env")" "profile env mode"
assert_eq 600 "$(stat -f '%Lp' "$personal_json")" "profile metadata mode"
assert_eq 700 "$(stat -f '%Lp' "$(dirname "$personal_env")")" "profile directory mode"

selection="$(e2e_profile_select "$TEST_ROOT" personal)"
assert_eq $'personal\t'"$personal_env"$'\t'"$personal_json" "$selection" "explicit selection"

unset LARK_APP_ID LARK_APP_SECRET LARK_BOT_OPEN_ID E2E_E2E_CHAT_ID E2E_REAL_E2E_P2P_CHAT_ID
e2e_profile_load "$personal_env"
assert_eq cli_test_app "$LARK_APP_ID" "loaded app id"
assert_eq secret-do-not-print "$LARK_APP_SECRET" "loaded secret"
assert_eq ou_profile_secret_bot "$LARK_BOT_OPEN_ID" "loaded bot id"

selection="$(e2e_profile_select "$TEST_ROOT" "")"
assert_eq $'personal\t'"$personal_env"$'\t'"$personal_json" "$selection" "unique profile selection"

export E2E_E2E_PROFILE=personal
selection="$(e2e_profile_select "$TEST_ROOT" "")"
assert_eq $'personal\t'"$personal_env"$'\t'"$personal_json" "$selection" "environment selection"
unset E2E_E2E_PROFILE

cp "$personal_env" "$TEST_ROOT/.lark-agent-bridge/e2e/profiles/staging.env"
cp "$personal_json" "$TEST_ROOT/.lark-agent-bridge/e2e/profiles/staging.json"
assert_fail e2e_profile_select "$TEST_ROOT" ""
selection="$(e2e_profile_select "$TEST_ROOT" staging)"
assert_eq $'staging\t'"$TEST_ROOT/.lark-agent-bridge/e2e/profiles/staging.env"$'\t'"$TEST_ROOT/.lark-agent-bridge/e2e/profiles/staging.json" "$selection" "explicit multiple-profile selection"

chmod 644 "$personal_env"
load_output="$TEST_ROOT/load-output"
if e2e_profile_load "$personal_env" >"$load_output" 2>&1; then
  fail "insecure profile mode unexpectedly loaded"
fi
if rg -e 'secret-do-not-print|ou_profile_secret_bot|oc_profile_secret' "$load_output" >/dev/null 2>&1; then
  fail "profile load error leaked a secret or full ID"
fi
chmod 600 "$personal_env"

rm -f "$TEST_ROOT/.lark-agent-bridge/e2e/profiles/"*.env "$TEST_ROOT/.lark-agent-bridge/e2e/profiles/"*.json
printf '%s\n' 'LARK_APP_ID=legacy_app' >"$TEST_ROOT/.lark-agent-bridge/e2e.env"
chmod 600 "$TEST_ROOT/.lark-agent-bridge/e2e.env"
selection="$(e2e_profile_select "$TEST_ROOT" "")"
assert_eq $'legacy\t'"$TEST_ROOT/.lark-agent-bridge/e2e.env"$'\t-' "$selection" "legacy selection"

assert_ok git -C "$ROOT" check-ignore -q .lark-agent-bridge/e2e/profiles/personal.env
assert_ok git -C "$ROOT" check-ignore -q .cache/e2e/personal/run/capabilities.json

BOOT_ROOT="$TEST_ROOT/bootstrap-repo"
FAKE_BIN="$TEST_ROOT/fake-bin"
mkdir -p "$BOOT_ROOT" "$FAKE_BIN"
cat >"$FAKE_BIN/curl" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *tenant_access_token*) printf '%s\n' '{"tenant_access_token":"tenant-token"}' ;;
  *bot/v3/info*) printf '%s\n' '{"bot":{"open_id":"ou_bootstrap_secret_bot"}}' ;;
  *) printf '%s\n' '{"code":0}' ;;
esac
EOF
cat >"$FAKE_BIN/lark-cli" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  'auth status --json --verify') printf '%s\n' '{"verified":true,"user":{"open_id":"ou_bootstrap_secret_user"}}' ;;
  *'im chats get'*'--chat-id oc_bootstrap_secret_group'*) printf '%s\n' '{"data":{"chat_id":"oc_bootstrap_secret_group"}}' ;;
  *'im +chat-list'*'--types p2p'*) printf '%s\n' '{"items":[{"chat_id":"oc_unrelated"},{"chat_id":"oc_bootstrap_secret_p2p"}]}' ;;
  *'im +chat-members-list'*'--chat-id oc_unrelated'*) printf '%s\n' '{"bots":[]}' ;;
  *'im +chat-members-list'*'--chat-id oc_bootstrap_secret_p2p'*) printf '%s\n' '{"bots":[{"member_id":"ou_bootstrap_secret_bot"}]}' ;;
  *) echo "unexpected fake lark-cli call: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$FAKE_BIN/curl" "$FAKE_BIN/lark-cli"

bootstrap_output="$TEST_ROOT/bootstrap-output"
if ! PATH="$FAKE_BIN:$PATH" \
  E2E_REPO_ROOT="$BOOT_ROOT" \
  LARK_APP_ID="cli_bootstrap_app" \
  LARK_APP_SECRET="bootstrap-secret-do-not-print" \
  LARK_BOT_OPEN_ID="" \
  E2E_E2E_CHAT_ID="oc_bootstrap_secret_group" \
  E2E_REAL_E2E_P2P_CHAT_ID="" \
    bash "$ROOT/scripts/e2e-init.sh" --profile developer --non-interactive >"$bootstrap_output" 2>&1; then
  sed -E 's/(secret|ou_|oc_)[A-Za-z0-9._-]*/[redacted]/g' "$bootstrap_output" >&2
  fail "bootstrap command failed"
fi

bootstrap_env="$BOOT_ROOT/.lark-agent-bridge/e2e/profiles/developer.env"
assert_eq 600 "$(stat -f '%Lp' "$bootstrap_env")" "bootstrap env mode"
assert_ok rg -q '^LARK_BOT_OPEN_ID=ou_bootstrap_secret_bot$' "$bootstrap_env"
assert_ok rg -q '^E2E_REAL_E2E_P2P_CHAT_ID=oc_bootstrap_secret_p2p$' "$bootstrap_env"
if rg -e 'bootstrap-secret-do-not-print|ou_bootstrap_secret_bot|oc_bootstrap_secret_group|oc_bootstrap_secret_p2p' "$bootstrap_output" >/dev/null 2>&1; then
  fail "bootstrap output leaked a secret or full ID"
fi

echo "e2e profile smoke ok"
