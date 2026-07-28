#!/usr/bin/env bash
# feature: group_message_intake (explicit-only; installs a private EXIT trap that
# overrides the top-level cleanup trap, then restores it — trap hand-off must stay
# intact). Mutates SERVER_GROUP_MESSAGE_MODE / SERVER_RESPOND_TO_BOTS and restores
# them via group_message_intake_cleanup.

group_message_intake_cleanup() {
  set +e
  stop_server TERM
  rm -f "$PREFERENCE_STORE"
  rm -f "$PARTICIPATED_TOPICS_STORE"
  SERVER_GROUP_MESSAGE_MODE="mention_only"
  SERVER_RESPOND_TO_BOTS="false"
  start_server_if_needed group_message_intake_restore
}

case_group_message_intake() {
  require_fake_claude
  local mention_marker="E2E_${RUN_ID}_MENTION_ONLY"
  local topic_marker="E2E_${RUN_ID}_PARTICIPATED"
  local restart_marker="E2E_${RUN_ID}_PARTICIPATED_RESTART"
  local other_marker="E2E_${RUN_ID}_OTHER_TOPIC"
  local all_marker="E2E_${RUN_ID}_ALL_GROUP"
  local rollback_marker="E2E_${RUN_ID}_ROLLBACK"
  local root other_root msg
  trap group_message_intake_cleanup EXIT

  set_group_message_mode group_message_intake_mention mention_only false
  msg="$(send_text "$mention_marker")"
  sleep 8
  if grep -F "$msg" "$AUDIT" >/dev/null 2>&1; then
    echo "mention_only unexpectedly accepted non-mention message: $msg" >&2
    return 1
  fi

  set_group_message_mode group_message_intake_participated participated_topics false
  root="$(send_at "/new E2E_${RUN_ID}_TOPIC_ROOT")"
  wait_audit "$root.*event=result" 60
  msg="$(reply_thread "$root" "<at user_id=\"${BOT_OPEN_ID}\"></at> $topic_marker")"
  wait_audit "$msg.*event=result" 60
  msg="$(reply_thread "$root" "$topic_marker-followup")"
  wait_audit "$msg.*event=result" 60

  other_root="$(send_text "${other_marker}-root")"
  msg="$(reply_thread "$other_root" "$other_marker")"
  sleep 8
  if grep -F "$msg" "$AUDIT" >/dev/null 2>&1; then
    echo "participated_topics unexpectedly accepted another topic: $msg" >&2
    return 1
  fi

  restart_server TERM
  msg="$(reply_thread "$root" "$restart_marker")"
  wait_audit "$msg.*event=result" 60

  set_group_message_mode group_message_intake_all all_group_messages false
  msg="$(send_text "$all_marker")"
  wait_audit "$msg.*event=result" 60

  set_group_message_mode group_message_intake_rollback mention_only false
  msg="$(send_text "$rollback_marker")"
  sleep 8
  if grep -F "$msg" "$AUDIT" >/dev/null 2>&1; then
    echo "mention_only rollback unexpectedly accepted non-mention message: $msg" >&2
    return 1
  fi

  group_message_intake_cleanup
  trap - EXIT
  summary "- group_message_modes: mention_only, participated_topics, all_group_messages"
  summary "- participation_restart: passed"
  summary "- bot_sender: not exercised (requires a separately controlled bot identity)"
  summary "- scope_incremental_grant: not exercised unless this profile initially lacks im:message.group_msg"
}
