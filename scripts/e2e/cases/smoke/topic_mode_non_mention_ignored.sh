#!/usr/bin/env bash
# smoke: topic_mode_non_mention_ignored

case_topic_mode_non_mention_ignored() {
  local root_marker="E2E_${RUN_ID}_TOPIC_NEG_ROOT"
  local neg_marker="E2E_${RUN_ID}_TOPIC_NEG"
  local root reply
  set_conversation_mode topic_mode_non_mention_ignored topic
  root="$(send_at "/new 请只回复 ${root_marker}，不要调用工具。")"
  wait_audit "$root.*event=result"
  reply="$(reply_thread "$root" "请只回复 ${neg_marker}，不要调用工具。")"
  sleep 8
  if grep -F "$reply" "$AUDIT" >/dev/null 2>&1; then
    echo "negative topic reply unexpectedly reached bridge: $reply" >&2
    return 1
  fi
  record_message topic_mode_non_mention_ignored root "$root"
  record_message topic_mode_non_mention_ignored reply_without_at "$reply"
  set_conversation_mode topic_mode_non_mention_ignored_restore chat
}
