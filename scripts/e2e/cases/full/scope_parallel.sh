#!/usr/bin/env bash
# full: scope_parallel

case_scope_parallel() {
  require_fake_claude
  local root_one root_two one two thread_one thread_two one_file two_file mark
  local one_marker="E2E_${RUN_ID}_SCOPE_ONE_E2E_BLOCK"
  local two_marker="E2E_${RUN_ID}_SCOPE_TWO_E2E_BLOCK"
  set_conversation_mode scope_parallel topic
  root_one="$(send_text "E2E_${RUN_ID}_SCOPE_ROOT_ONE")"
  root_two="$(send_text "E2E_${RUN_ID}_SCOPE_ROOT_TWO")"
  one="$(reply_thread "$root_one" "<at user_id=\"${BOT_OPEN_ID}\"></at> /new ${one_marker}")"
  two="$(reply_thread "$root_two" "<at user_id=\"${BOT_OPEN_ID}\"></at> /new ${two_marker}")"
  wait_audit "$one.*event=stream" 60
  wait_audit "$two.*event=stream" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$one_marker" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$two_marker" 60
  kill -0 "$SERVER_PID" >/dev/null 2>&1
  thread_one="$(thread_id_for_message scope_parallel_thread_one "$one")"
  thread_two="$(thread_id_for_message scope_parallel_thread_two "$two")"
  if [[ "$thread_one" == "$thread_two" ]]; then
    echo "parallel case resolved the same thread twice: $thread_one" >&2
    return 1
  fi
  mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:thread:${thread_one}:message:${one}"
  wait_audit_since "$mark" "batch_stop_requested.*thread:${thread_one}" 60
  mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:thread:${thread_two}:message:${two}"
  wait_audit_since "$mark" "batch_stop_requested.*thread:${thread_two}" 60
  one_file="$(mget scope_parallel "$one")"
  two_file="$(mget scope_parallel "$two")"
  assert_file_contains "$one_file" "已停止"
  assert_file_contains "$two_file" "已停止"
  record_message scope_parallel root_one "$root_one"
  record_message scope_parallel root_two "$root_two"
  record_message scope_parallel scope_one "$one" "$one_file"
  record_message scope_parallel scope_two "$two" "$two_file"
  set_conversation_mode scope_parallel_restore chat
}
