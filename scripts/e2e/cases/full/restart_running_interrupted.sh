#!/usr/bin/env bash
# full: restart_running_interrupted

case_restart_running_interrupted() {
  require_fake_claude
  local running_marker="E2E_${RUN_ID}_RESTART_RUNNING_E2E_BLOCK"
  local follow_marker="E2E_${RUN_ID}_RESTART_RUNNING_FOLLOW"
  local running follow file before mark
  running="$(send_at "/new ${running_marker}")"
  wait_audit "$running.*event=stream" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$running_marker" 60
  before="$(fake_marker_count "$running_marker")"
  mark="$(audit_mark)"
  restart_server KILL
  wait_audit_since "$mark" '"Action":"session_recovery_interrupted"' 60
  sleep 2
  assert_fake_marker_not_started_after "$running_marker" "$before"
  follow="$(send_at "/new ${follow_marker}")"
  wait_audit "$follow.*event=result" 60
  file="$(mget restart_running_interrupted "$follow")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message restart_running_interrupted interrupted "$running"
  record_message restart_running_interrupted followup "$follow" "$file"
}
