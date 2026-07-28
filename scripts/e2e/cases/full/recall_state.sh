#!/usr/bin/env bash
# full: recall_state

case_recall_state() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_RECALL_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_RECALL_QUEUED_CANCEL"
  local active queued file before mark
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  before="$(fake_marker_count "$queued_marker")"
  mark="$(audit_mark)"
  queued="$(send_at "$queued_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  mark="$(audit_mark)"
  revoke_message "$queued"
  wait_audit_since "$mark" "message_recalled_queued_cancelled.*$queued" 60
  mark="$(audit_mark)"
  revoke_message "$active"
  wait_audit_since "$mark" "message_recalled_active_cancelled.*$active" 60
  sleep 2
  assert_fake_marker_not_started_after "$queued_marker" "$before"
  file="$(mget recall_state "$active")"
  assert_file_contains "$file" "已停止"
  record_message recall_state active_cancelled "$active" "$file"
  record_message recall_state queued_cancelled "$queued"
}
