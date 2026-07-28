#!/usr/bin/env bash
# full: reply_clean

case_reply_clean() {
  require_fake_claude
  local config_msg session_id dm topic_root topic file
  config_msg="$(open_config reply_clean)"
  session_id="config:message:${config_msg}"
  submit_config reply_clean "$session_id" default low append-clean-card
  assert_persisted_config default low append-clean-card

  dm="$(send_dm "/new E2E_${RUN_ID}_DM_E2E_REPLY_MODE_SEMANTICS")"
  wait_audit "$dm.*event=result" 60
  file="$(mget reply_clean_dm "$dm")"
  assert_file_contains "$file" "E2E_FINAL_ANSWER"
  assert_file_not_contains "$file" "E2E_INTERMEDIATE_ANSWER"
  assert_file_not_contains "$file" "E2E_PRIVATE_THOUGHT"
  assert_file_not_contains "$file" "E2E_TOOL_CALL"
  assert_file_not_contains "$file" "思考推理"
  assert_file_not_contains "$file" "工具调用"
  record_message reply_clean dm "$dm" "$file"

  topic_root="$(reply_topic_root)"
  topic="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_TOPIC_E2E_PROCESS_PANELS")"
  wait_audit "$topic.*event=result" 60
  file="$(mget reply_clean_topic "$topic")"
  assert_file_contains "$file" "E2E_CLEAN_ANSWER"
  assert_file_not_contains "$file" "E2E_PRIVATE_THOUGHT"
  assert_file_not_contains "$file" "E2E_TOOL_CALL"
  assert_file_not_contains "$file" "思考推理"
  assert_file_not_contains "$file" "工具调用"
  record_message reply_clean topic "$topic" "$file"
}
