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

echo "e2e capability smoke ok"
