#!/usr/bin/env bash
# full: reply_append

case_reply_append() {
  require_fake_claude
  local config_msg session_id first second first_mark second_mark first_reply second_reply first_file second_file
  local topic_root topic_first topic_second topic_first_mark topic_second_mark topic_first_reply topic_second_reply
  config_msg="$(open_config reply_append)"
  session_id="config:message:${config_msg}"
  submit_config reply_append "$session_id" default low append
  assert_persisted_config default low append

  first_mark="$(audit_mark)"
  first="$(send_dm "/new E2E_${RUN_ID}_APPEND_DM_ONE E2E_REPLY_MODE_SEMANTICS")"
  wait_audit_since "$first_mark" "$first.*event=result" 60
  first_reply="$(audit_reply_message_id "$first")"
  first_file="$(mget reply_append_dm_first "$first")"
  assert_file_contains "$first_file" '"msg_type"'
  assert_file_contains "$first_file" '"interactive"'
  assert_file_contains "$first_file" "E2E_INTERMEDIATE_ANSWER"
  assert_file_contains "$first_file" "E2E_FINAL_ANSWER"
  assert_file_contains "$first_file" "E2E_TOOL_CALL"
  assert_file_contains "$first_file" "tokens:"
  assert_file_not_contains "$first_file" "E2E_PRIVATE_THOUGHT"
  assert_file_not_contains "$first_file" "collapsible_panel"
  second_mark="$(audit_mark)"
  second="$(send_dm "/new E2E_${RUN_ID}_APPEND_DM_TWO")"
  wait_audit_since "$second_mark" "$second.*event=result" 60
  second_reply="$(audit_reply_message_id "$second")"
  second_file="$(mget reply_append_dm_second "$second")"
  assert_file_contains "$second_file" '"msg_type"'
  assert_file_contains "$second_file" '"interactive"'
  if [[ "$first_reply" == "$second_reply" ]]; then
    echo "append mode reused DM reply $first_reply" >&2
    return 1
  fi

  topic_root="$(reply_topic_root)"
  topic_first_mark="$(audit_mark)"
  topic_first="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_APPEND_TOPIC_ONE")"
  wait_audit_since "$topic_first_mark" "$topic_first.*event=result" 60
  topic_first_reply="$(audit_reply_message_id "$topic_first")"
  topic_second_mark="$(audit_mark)"
  topic_second="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_APPEND_TOPIC_TWO")"
  wait_audit_since "$topic_second_mark" "$topic_second.*event=result" 60
  topic_second_reply="$(audit_reply_message_id "$topic_second")"
  if [[ "$topic_first_reply" == "$topic_second_reply" ]]; then
    echo "append mode reused topic reply $topic_first_reply" >&2
    return 1
  fi
  record_message reply_append dm_first "$first"
  record_message reply_append dm_second "$second"
  record_message reply_append topic_first "$topic_first"
  record_message reply_append topic_second "$topic_second"
  summary "- dm_replies: $first_reply, $second_reply"
  summary "- topic_replies: $topic_first_reply, $topic_second_reply"
}
