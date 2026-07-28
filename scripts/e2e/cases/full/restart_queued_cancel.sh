#!/usr/bin/env bash
# full: restart_queued_cancel

case_restart_queued_cancel() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_RESTART_QUEUE_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_RESTART_QUEUE_CANCEL"
  local follow_marker="E2E_${RUN_ID}_RESTART_QUEUE_FOLLOW"
  local active queued follow file before mark
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  mark="$(audit_mark)"
  queued="$(send_at "$queued_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  before="$(fake_marker_count "$queued_marker")"
  mark="$(audit_mark)"
  restart_server KILL
  wait_audit_since "$mark" '"Action":"session_recovery_cancelled"' 60
  sleep 2
  assert_fake_marker_not_started_after "$queued_marker" "$before"
  follow="$(send_at "/new ${follow_marker}")"
  wait_audit "$follow.*event=result" 60
  file="$(mget restart_queued_cancel "$follow")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message restart_queued_cancel active "$active"
  record_message restart_queued_cancel cleared_queue "$queued"
  record_message restart_queued_cancel followup "$follow" "$file"
}
