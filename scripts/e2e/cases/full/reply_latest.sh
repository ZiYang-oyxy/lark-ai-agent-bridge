#!/usr/bin/env bash
# full: reply_latest

case_reply_latest() {
  require_fake_claude
  local config_msg session_id first second first_mark second_mark first_card second_card first_file
  local topic_root topic_first topic_second topic_first_mark topic_second_mark topic_first_card topic_second_card
  config_msg="$(open_config reply_latest)"
  session_id="config:message:${config_msg}"
  submit_config reply_latest "$session_id" default low latest-card
  assert_persisted_config default low latest-card

  first_mark="$(audit_mark)"
  first="$(send_dm "/new E2E_${RUN_ID}_LATEST_DM_ONE E2E_REPLY_MODE_SEMANTICS")"
  wait_audit_since "$first_mark" "$first.*event=result" 60
  first_card="$(audit_card_id_since "$first_mark" "$first")"
  first_file="$(mget reply_latest_dm_first "$first")"
  assert_file_contains "$first_file" "E2E_FINAL_ANSWER"
  assert_file_not_contains "$first_file" "E2E_INTERMEDIATE_ANSWER"
  assert_file_not_contains "$first_file" "E2E_PRIVATE_THOUGHT"
  assert_file_not_contains "$first_file" "E2E_TOOL_CALL"
  second_mark="$(audit_mark)"
  second="$(send_dm "/new E2E_${RUN_ID}_LATEST_DM_TWO")"
  wait_audit_since "$second_mark" "$second.*event=result" 60
  second_card="$(audit_card_id_since "$second_mark" "$second")"
  assert_no_card_create_since "$second_mark" "$second"
  if [[ "$first_card" != "$second_card" ]]; then
    echo "latest mode changed DM card: $first_card -> $second_card" >&2
    return 1
  fi

  topic_root="$(reply_topic_root)"
  topic_first_mark="$(audit_mark)"
  topic_first="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_LATEST_TOPIC_ONE")"
  wait_audit_since "$topic_first_mark" "$topic_first.*event=result" 60
  topic_first_card="$(audit_card_id_since "$topic_first_mark" "$topic_first")"
  topic_second_mark="$(audit_mark)"
  topic_second="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_LATEST_TOPIC_TWO")"
  wait_audit_since "$topic_second_mark" "$topic_second.*event=result" 60
  topic_second_card="$(audit_card_id_since "$topic_second_mark" "$topic_second")"
  assert_no_card_create_since "$topic_second_mark" "$topic_second"
  if [[ "$topic_first_card" != "$topic_second_card" ]]; then
    echo "latest mode changed topic card: $topic_first_card -> $topic_second_card" >&2
    return 1
  fi
  record_message reply_latest dm_first "$first"
  record_message reply_latest dm_second "$second"
  record_message reply_latest topic_first "$topic_first"
  record_message reply_latest topic_second "$topic_second"
  summary "- dm_latest_card: $first_card"
  summary "- topic_latest_card: $topic_first_card"
}
