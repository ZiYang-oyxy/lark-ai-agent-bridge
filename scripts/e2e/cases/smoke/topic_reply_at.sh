#!/usr/bin/env bash
# smoke: topic_reply_at

case_topic_reply_at() {
  local root_marker="E2E_${RUN_ID}_TOPIC_ROOT"
  local reply_marker="E2E_${RUN_ID}_TOPIC_AT"
  local root reply file
  set_conversation_mode topic_reply_at topic
  root="$(send_at "/new 请只回复 ${root_marker}，不要调用工具。")"
  wait_audit "$root.*event=result"
  reply="$(reply_thread "$root" "<at user_id=\"${BOT_OPEN_ID}\"></at> 请只回复 ${reply_marker}，不要调用工具。")"
  wait_audit "$reply.*event=result"
  file="$(mget topic_reply_at "$reply")"
  assert_file_contains "$file" "$reply_marker"
  record_message topic_reply_at root "$root"
  record_message topic_reply_at reply "$reply" "$file"
  set_conversation_mode topic_reply_at_restore chat
}
