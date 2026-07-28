#!/usr/bin/env bash
# full: stop_preserves_queue

case_stop_preserves_queue() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_STOP_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_STOP_QUEUE_PRESERVED"
  local active queued active_file queued_file mark
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  mark="$(audit_mark)"
  queued="$(send_at "$queued_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit_since "$mark" '"Action":"batch_stop_requested"' 60
  wait_audit "$queued.*event=result" 60
  queued_file="$(mget stop_preserves_queue "$queued")"
  assert_file_contains "$queued_file" "FAKE_E2E_STARTED"
  wait_file_contains "$FAKE_CLAUDE_LOG" "$queued_marker" 60
  active_file="$(mget stop_preserves_queue "$active")"
  assert_file_contains "$active_file" "已停止"
  record_message stop_preserves_queue stopped_active "$active" "$active_file"
  record_message stop_preserves_queue preserved_queue "$queued" "$queued_file"
}
