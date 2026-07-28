#!/usr/bin/env bash
# full: revoke_during_queue

case_revoke_during_queue() {
  local first_marker="E2E_${RUN_ID}_QUEUE_FIRST"
  local second_marker="E2E_${RUN_ID}_QUEUE_SECOND"
  local follow_marker="E2E_${RUN_ID}_QUEUE_FOLLOW"
  local first second follow mark
  first="$(send_at "/new ${first_marker}")"
  wait_audit "$first.*event=stream" 60
  mark="$(audit_mark)"
  second="$(send_at "${second_marker}")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  mark="$(audit_mark)"
  revoke_message "$second"
  wait_audit_since "$mark" "message_recalled_queued_cancelled.*$second" 60
  revoke_message "$first"
  wait_audit "message_recalled_active_cancelled.*$first" 60
  follow="$(send_at "/new ${follow_marker}")"
  wait_audit "$follow.*event=stream" 30
  if grep -F "$second" "$AUDIT" | grep -F "event=result" >/dev/null 2>&1; then
    echo "revoked queued input unexpectedly produced a result card" >&2
    return 1
  fi
  record_message revoke_during_queue active "$first"
  record_message revoke_during_queue queued_revoked "$second"
  record_message revoke_during_queue followup "$follow"
}
