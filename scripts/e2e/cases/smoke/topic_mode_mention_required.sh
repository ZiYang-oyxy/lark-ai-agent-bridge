#!/usr/bin/env bash
# smoke: topic_mode_mention_required

case_topic_mode_mention_required() {
  local root_marker="E2E_${RUN_ID}_TOPIC_ROOT"
  local reply_marker="E2E_${RUN_ID}_TOPIC_AT"
  local root reply file
  set_conversation_mode topic_mode_mention_required topic
  root="$(send_at "/new 请只回复 ${root_marker}，不要调用工具。")"
  wait_audit "$root.*event=result"
  reply="$(reply_thread "$root" "<at user_id=\"${BOT_OPEN_ID}\"></at> 请只回复 ${reply_marker}，不要调用工具。")"
  wait_audit "$reply.*event=result"
  file="$(mget topic_mode_mention_required "$reply")"
  assert_file_contains "$file" "$reply_marker"
  record_message topic_mode_mention_required root "$root"
  record_message topic_mode_mention_required reply "$reply" "$file"
  set_conversation_mode topic_mode_mention_required_restore chat
}
