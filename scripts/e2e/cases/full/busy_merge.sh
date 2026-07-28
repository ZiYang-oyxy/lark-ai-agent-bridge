#!/usr/bin/env bash
# full: busy_merge

case_busy_merge() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_BUSY_ACTIVE_E2E_BLOCK"
  local first_marker="E2E_${RUN_ID}_BUSY_MERGE_ONE"
  local second_marker="E2E_${RUN_ID}_BUSY_MERGE_TWO"
  local active first second anchor file log_mark mark
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  log_mark="$(fake_log_mark)"
  mark="$(audit_mark)"
  send_group_pair "$first_marker" "$second_marker"
  first="$PAIR_FIRST"
  second="$PAIR_SECOND"
  wait_file_contains "$FAKE_CLAUDE_LOG" "$active_marker" 60
  wait_audit_count_since "$mark" '"Action":"queue_input"' 2 60
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit_since "$mark" '"Action":"batch_stop_requested"' 60
  wait_audit_since "$mark" "($first|$second).*event=result" 60
  assert_fake_batch_contains "$log_mark" "$first_marker" "$second_marker"
  anchor="$(result_message_since "$mark" "$first" "$second")"
  file="$(mget busy_merge "$anchor")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message busy_merge active "$active"
  record_message busy_merge first "$first"
  record_message busy_merge merged "$anchor" "$file"
}
