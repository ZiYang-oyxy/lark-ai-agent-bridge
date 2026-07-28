#!/usr/bin/env bash
# full: debounce_group

case_debounce_group() {
  require_fake_claude
  local first_marker="E2E_${RUN_ID}_GROUP_DEBOUNCE_ONE"
  local second_marker="E2E_${RUN_ID}_GROUP_DEBOUNCE_TWO"
  local first second anchor file log_mark mark
  log_mark="$(fake_log_mark)"
  mark="$(audit_mark)"
  send_group_pair "$first_marker" "$second_marker"
  first="$PAIR_FIRST"
  second="$PAIR_SECOND"
  wait_audit_since "$mark" "($first|$second).*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$second_marker" 60
  assert_fake_batch_contains "$log_mark" "$first_marker" "$second_marker"
  anchor="$(result_message_since "$mark" "$first" "$second")"
  file="$(mget debounce_group "$anchor")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message debounce_group first "$first"
  record_message debounce_group second "$second"
  record_message debounce_group result_anchor "$anchor" "$file"
}
