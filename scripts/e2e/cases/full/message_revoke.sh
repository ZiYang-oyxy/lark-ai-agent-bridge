#!/usr/bin/env bash
# full: message_revoke

case_message_revoke() {
  local marker="E2E_${RUN_ID}_REVOKE_SHOULD_CANCEL"
  local after="E2E_${RUN_ID}_AFTER_REVOKE"
  local msg follow
  msg="$(send_at "/new ${marker}")"
  wait_audit "$msg.*event=stream" 60
  revoke_message "$msg"
  wait_audit "message_recalled_active_cancelled.*$msg" 60
  wait_audit "$msg.*event=stopped" 60
  follow="$(send_at "/new ${after}")"
  wait_audit "$follow.*event=stream" 60
  if grep -F "$after" "$AUDIT" | grep -F "queue_input" >/dev/null 2>&1; then
    echo "follow-up after revoke was queued" >&2
    return 1
  fi
  revoke_message "$follow"
  wait_audit "message_recalled_active_cancelled.*$follow" 60
  record_message message_revoke revoked "$msg"
  record_message message_revoke followup "$follow"
}
