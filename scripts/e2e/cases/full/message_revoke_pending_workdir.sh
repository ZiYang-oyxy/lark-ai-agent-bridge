#!/usr/bin/env bash
# full: message_revoke_pending_workdir

case_message_revoke_pending_workdir() {
  local dir="$RUN_DIR/revoke-pending-workdir"
  local marker="E2E_${RUN_ID}_REVOKE_PENDING"
  local msg file
  msg="$(send_at "/new --workdir ${dir} ${marker}")"
  wait_audit "$msg.*event=workdir_confirm" 60
  revoke_message "$msg"
  wait_audit "message_recalled_pending_cancelled.*$msg" 60
  wait_audit "$msg.*event=workdir_cancelled" 60
  [[ ! -e "$dir" ]] || { echo "workdir should not be created after revoke: $dir" >&2; return 1; }
  if grep -F "$marker" "$AUDIT" | grep -F "run_input" >/dev/null 2>&1; then
    echo "revoked pending workdir unexpectedly started Claude run" >&2
    return 1
  fi
  file="$(mget message_revoke_pending_workdir "$msg")"
  assert_file_contains "$file" "[Cancel ✗]"
  record_message message_revoke_pending_workdir revoked "$msg" "$file"
}
