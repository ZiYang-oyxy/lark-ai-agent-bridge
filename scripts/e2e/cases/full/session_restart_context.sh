#!/usr/bin/env bash
# full: session_restart_context

case_session_restart_context() {
  require_fake_claude
  local first_marker="E2E_${RUN_ID}_RESTART_CONTEXT_FIRST"
  local second_marker="E2E_${RUN_ID}_RESTART_CONTEXT_SECOND"
  local first second file log_mark
  first="$(send_at "/new ${first_marker}")"
  wait_audit "$first.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$first_marker" 60
  restart_server TERM
  log_mark="$(fake_log_mark)"
  second="$(send_at "$second_marker")"
  wait_audit "$second.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$second_marker" 60
  assert_fake_batch_contains "$log_mark" "$second_marker" "--resume fake-e2e-session"
  file="$(mget session_restart_context "$second")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message session_restart_context first "$first"
  record_message session_restart_context resumed "$second" "$file"
}
