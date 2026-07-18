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
export E2E_E2E_LARK_CLI_PROFILE="lab-e2e-personal"
e2e_profile_write "$TEST_ROOT" personal

personal_env="$TEST_ROOT/.lark-agent-bridge/e2e/profiles/personal.env"
personal_json="$TEST_ROOT/.lark-agent-bridge/e2e/profiles/personal.json"
assert_eq 600 "$(stat -f '%Lp' "$personal_env")" "profile env mode"
assert_eq 600 "$(stat -f '%Lp' "$personal_json")" "profile metadata mode"
assert_eq 700 "$(stat -f '%Lp' "$(dirname "$personal_env")")" "profile directory mode"

selection="$(e2e_profile_select "$TEST_ROOT" personal)"
assert_eq $'personal\t'"$personal_env"$'\t'"$personal_json" "$selection" "explicit selection"

unset LARK_APP_ID LARK_APP_SECRET LARK_BOT_OPEN_ID E2E_E2E_CHAT_ID E2E_REAL_E2E_P2P_CHAT_ID E2E_E2E_LARK_CLI_PROFILE
e2e_profile_load "$personal_env"
assert_eq cli_test_app "$LARK_APP_ID" "loaded app id"
assert_eq secret-do-not-print "$LARK_APP_SECRET" "loaded secret"
assert_eq ou_profile_secret_bot "$LARK_BOT_OPEN_ID" "loaded bot id"
assert_eq lab-e2e-personal "$E2E_E2E_LARK_CLI_PROFILE" "loaded isolated CLI profile"

chmod 755 "$(dirname "$personal_env")"
assert_fail e2e_profile_load "$personal_env"
chmod 700 "$(dirname "$personal_env")"

old_env_contents="$(cat "$personal_env")"
FAILING_MV="$TEST_ROOT/failing-mv"
cat >"$FAILING_MV" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "$FAILING_MV"
export LARK_APP_ID="cli_replacement_app"
# shellcheck disable=SC2016
assert_fail env E2E_PROFILE_MV_BIN="$FAILING_MV" bash -c \
  'source "$1"; e2e_profile_write "$2" personal' _ "$ROOT/scripts/lib/e2e-profile.sh" "$TEST_ROOT"
assert_eq "$old_env_contents" "$(cat "$personal_env")" "failed atomic write preserved old env"
export LARK_APP_ID="cli_test_app"

export LARK_APP_SECRET=$'unsupported\nsecret'
assert_fail e2e_profile_write "$TEST_ROOT" unsafe-value
if find "$TEST_ROOT/.lark-agent-bridge/e2e/profiles" -maxdepth 1 -type f -name '.unsafe-value.*' | rg -q .; then
  fail "failed profile write left secret-bearing temporary files"
fi
export LARK_APP_SECRET="secret-do-not-print"

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
cat >"$TEST_ROOT/.lark-agent-bridge/e2e.env" <<'EOF'
export LARK_APP_ID="legacy_app"
export LARK_APP_SECRET="legacy_secret"
export E2E_E2E_CHAT_ID="oc_legacy_group"
export E2E_E2E_CHAT_TYPE="group"
EOF
chmod 600 "$TEST_ROOT/.lark-agent-bridge/e2e.env"
selection="$(e2e_profile_select "$TEST_ROOT" "")"
assert_eq $'legacy\t'"$TEST_ROOT/.lark-agent-bridge/e2e.env"$'\t-' "$selection" "legacy selection"
unset LARK_APP_ID LARK_APP_SECRET E2E_E2E_CHAT_ID E2E_E2E_CHAT_TYPE
e2e_profile_load_legacy "$TEST_ROOT/.lark-agent-bridge/e2e.env"
assert_eq legacy_app "$LARK_APP_ID" "legacy app load"
assert_eq group "$E2E_E2E_CHAT_TYPE" "legacy optional field load"

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
if [[ "${1:-}" == "--profile" ]]; then
  [[ "${2:-}" == "lab-e2e-developer" ]] || exit 91
  shift 2
elif [[ "${1:-} ${2:-}" == "profile add" ]]; then
  [[ "$*" == *'--name lab-e2e-developer'* ]] || exit 92
  [[ "$*" != *'--use'* ]] || exit 95
  IFS= read -r supplied_secret
  [[ "$supplied_secret" == "bootstrap-secret-do-not-print" ]] || exit 93
  exit 0
else
  exit 94
fi
case "$*" in
  'config show')
    if [[ "${CLI_PROFILE_SCENARIO:-missing}" == "mismatch" ]]; then
      printf '%s\n' 'App ID: cli_another_app'
      exit 0
    fi
    exit 1
    ;;
  'auth status --json --verify') printf '%s\n' '{"verified":true,"app_id":"cli_bootstrap_app","user":{"open_id":"ou_bootstrap_secret_user"}}' ;;
  'auth check --scope im:message.send_as_user --json')
    if [[ "${SEND_SCOPE_SCENARIO:-granted}" == "missing" ]]; then
      printf '%s\n' '{"ok":false,"granted":null,"missing":["im:message.send_as_user"]}'
      exit 1
    fi
    printf '%s\n' '{"ok":true,"granted":true,"missing":[]}'
    ;;
  *'im chats get'*'--chat-id oc_bootstrap_secret_group'*) printf '%s\n' '{"data":{"chat_id":"oc_bootstrap_secret_group"}}' ;;
  *'im +chat-list'*'--types p2p'*)
    case "${P2P_SCENARIO:-unique}" in
      none) printf '%s\n' '{"items":[{"chat_id":"oc_unrelated"}]}' ;;
      multiple) printf '%s\n' '{"items":[{"chat_id":"oc_bootstrap_secret_p2p"},{"chat_id":"oc_bootstrap_secret_p2p_two"}]}' ;;
      *) printf '%s\n' '{"items":[{"chat_id":"oc_unrelated"},{"chat_id":"oc_bootstrap_secret_p2p"}]}' ;;
    esac
    ;;
  *'im +chat-members-list'*'--chat-id oc_unrelated'*) printf '%s\n' '{"bots":[]}' ;;
  *'im +chat-members-list'*'--chat-id oc_wrong_p2p'*) printf '%s\n' '{"bots":[]}' ;;
  *'im +chat-members-list'*'--chat-id oc_bootstrap_secret_p2p_two'*) printf '%s\n' '{"bots":[{"member_id":"ou_bootstrap_secret_bot"}]}' ;;
  *'im +chat-members-list'*'--chat-id oc_bootstrap_secret_p2p'*) printf '%s\n' '{"bots":[{"member_id":"ou_bootstrap_secret_bot"}]}' ;;
  *) echo "unexpected fake lark-cli call: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$FAKE_BIN/curl" "$FAKE_BIN/lark-cli"

bootstrap_output="$TEST_ROOT/bootstrap-output"
unset E2E_E2E_LARK_CLI_PROFILE
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

unset LARK_APP_ID LARK_APP_SECRET LARK_BOT_OPEN_ID E2E_E2E_CHAT_ID E2E_REAL_E2E_P2P_CHAT_ID E2E_E2E_LARK_CLI_PROFILE
PATH="$FAKE_BIN:$PATH" E2E_REPO_ROOT="$BOOT_ROOT" \
  bash "$ROOT/scripts/e2e-init.sh" --profile developer --non-interactive >"$bootstrap_output" 2>&1
assert_ok rg -q '^LARK_APP_ID=cli_bootstrap_app$' "$bootstrap_env"

mismatch_root="$TEST_ROOT/bootstrap-cli-profile-mismatch"
mismatch_output="$TEST_ROOT/bootstrap-cli-profile-mismatch.out"
mkdir -p "$mismatch_root"
set +e
PATH="$FAKE_BIN:$PATH" E2E_REPO_ROOT="$mismatch_root" CLI_PROFILE_SCENARIO=mismatch \
  LARK_APP_ID="cli_bootstrap_app" LARK_APP_SECRET="bootstrap-secret-do-not-print" \
  LARK_BOT_OPEN_ID="" E2E_E2E_CHAT_ID="oc_bootstrap_secret_group" E2E_REAL_E2E_P2P_CHAT_ID="" \
  bash "$ROOT/scripts/e2e-init.sh" --profile developer --non-interactive >"$mismatch_output" 2>&1
mismatch_status=$?
set -e
assert_eq 3 "$mismatch_status" "existing CLI profile app mismatch status"
if rg -e 'bootstrap-secret-do-not-print|ou_bootstrap_secret_bot|oc_bootstrap_secret_group' "$mismatch_output" >/dev/null 2>&1; then
  fail "CLI profile mismatch output leaked a secret or full ID"
fi

missing_scope_root="$TEST_ROOT/bootstrap-missing-send-scope"
missing_scope_output="$TEST_ROOT/bootstrap-missing-send-scope.out"
mkdir -p "$missing_scope_root"
set +e
PATH="$FAKE_BIN:$PATH" E2E_REPO_ROOT="$missing_scope_root" SEND_SCOPE_SCENARIO=missing \
  LARK_APP_ID="cli_bootstrap_app" LARK_APP_SECRET="bootstrap-secret-do-not-print" \
  LARK_BOT_OPEN_ID="" E2E_E2E_CHAT_ID="oc_bootstrap_secret_group" E2E_REAL_E2E_P2P_CHAT_ID="" \
  bash "$ROOT/scripts/e2e-init.sh" --profile developer --non-interactive >"$missing_scope_output" 2>&1
missing_scope_status=$?
set -e
assert_eq 3 "$missing_scope_status" "missing send-as-user scope bootstrap status"
rg -q 'BLOCKED user_send_scope_missing' "$missing_scope_output"
if rg -e 'bootstrap-secret-do-not-print|ou_bootstrap_secret_bot|oc_bootstrap_secret_group' "$missing_scope_output" >/dev/null 2>&1; then
  fail "missing scope output leaked a secret or full ID"
fi

for scenario in none multiple; do
  blocked_root="$TEST_ROOT/bootstrap-$scenario"
  blocked_output="$TEST_ROOT/bootstrap-$scenario.out"
  mkdir -p "$blocked_root"
  set +e
  PATH="$FAKE_BIN:$PATH" E2E_REPO_ROOT="$blocked_root" P2P_SCENARIO="$scenario" \
    LARK_APP_ID="cli_bootstrap_app" LARK_APP_SECRET="bootstrap-secret-do-not-print" \
    LARK_BOT_OPEN_ID="" E2E_E2E_CHAT_ID="oc_bootstrap_secret_group" E2E_REAL_E2E_P2P_CHAT_ID="" \
    bash "$ROOT/scripts/e2e-init.sh" --profile developer --non-interactive >"$blocked_output" 2>&1
  blocked_status=$?
  set -e
  assert_eq 3 "$blocked_status" "$scenario P2P bootstrap status"
  if rg -e 'bootstrap-secret-do-not-print|ou_bootstrap_secret_bot|oc_bootstrap_secret_group|oc_bootstrap_secret_p2p' "$blocked_output" >/dev/null 2>&1; then
    fail "$scenario P2P bootstrap leaked a secret or full ID"
  fi
done

wrong_p2p_root="$TEST_ROOT/bootstrap-wrong-p2p"
wrong_p2p_output="$TEST_ROOT/bootstrap-wrong-p2p.out"
mkdir -p "$wrong_p2p_root"
set +e
PATH="$FAKE_BIN:$PATH" E2E_REPO_ROOT="$wrong_p2p_root" \
  LARK_APP_ID="cli_bootstrap_app" LARK_APP_SECRET="bootstrap-secret-do-not-print" \
  LARK_BOT_OPEN_ID="" E2E_E2E_CHAT_ID="oc_bootstrap_secret_group" E2E_REAL_E2E_P2P_CHAT_ID="" \
  bash "$ROOT/scripts/e2e-init.sh" --profile developer --non-interactive --p2p-chat-id oc_wrong_p2p \
  >"$wrong_p2p_output" 2>&1
wrong_p2p_status=$?
set -e
assert_eq 3 "$wrong_p2p_status" "explicit P2P without target bot status"
if rg -e 'bootstrap-secret-do-not-print|ou_bootstrap_secret_bot|oc_bootstrap_secret_group|oc_wrong_p2p' "$wrong_p2p_output" >/dev/null 2>&1; then
  fail "explicit P2P validation leaked a secret or full ID"
fi

echo "e2e profile smoke ok"
