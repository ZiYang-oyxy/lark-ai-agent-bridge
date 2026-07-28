#!/usr/bin/env bash
# full: config_frozen_queue

case_config_frozen_queue() {
  require_fake_claude
  local config_msg session_id active old_queued new_queued mark log_mark old_file new_file
  local active_marker="E2E_${RUN_ID}_CONFIG_ACTIVE_E2E_BLOCK"
  local old_marker="E2E_${RUN_ID}_CONFIG_FROZEN_OLD"
  local new_marker="E2E_${RUN_ID}_CONFIG_FROZEN_NEW"
  config_msg="$(open_config config_frozen_queue)"
  session_id="config:message:${config_msg}"
  submit_config config_frozen_queue "$session_id" sonnet low
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$active_marker" 60
  log_mark="$(fake_log_mark)"
  mark="$(audit_mark)"
  old_queued="$(send_at "$old_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  submit_config config_frozen_queue "$session_id" opus high
  mark="$(audit_mark)"
  new_queued="$(send_at "$new_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit_since "$mark" '"Action":"batch_stop_requested"' 60
  wait_audit "$old_queued.*event=result" 60
  wait_audit "$new_queued.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$old_marker" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$new_marker" 60
  assert_fake_config_argv_since "$log_mark" "$old_marker" sonnet low
  assert_fake_config_argv_since "$log_mark" "$new_marker" opus high
  old_file="$(mget config_frozen_queue_old "$old_queued")"
  new_file="$(mget config_frozen_queue_new "$new_queued")"
  assert_file_contains "$old_file" "requested: sonnet"
  assert_file_contains "$new_file" "requested: opus"
  record_message config_frozen_queue active "$active"
  record_message config_frozen_queue frozen_old "$old_queued" "$old_file"
  record_message config_frozen_queue frozen_new "$new_queued" "$new_file"
}
