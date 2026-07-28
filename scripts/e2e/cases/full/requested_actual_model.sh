#!/usr/bin/env bash
# full: requested_actual_model

case_requested_actual_model() {
  require_fake_claude
  local config_msg session_id marker msg file mark
  config_msg="$(open_config requested_actual_model)"
  session_id="config:message:${config_msg}"
  submit_config requested_actual_model "$session_id" opus high
  marker="E2E_${RUN_ID}_REQUESTED_ACTUAL"
  mark="$(audit_mark)"
  msg="$(send_at "/new ${marker}")"
  wait_audit "$msg.*event=result" 60
  wait_audit_since "$mark" '"Action":"model_requested_actual_mismatch".*requested=opus actual=fake-claude-e2e' 60
  file="$(mget requested_actual_model "$msg")"
  assert_file_contains "$file" "requested: opus"
  assert_file_contains "$file" "actual: fake-claude-e2e"
  assert_file_contains "$file" "effort: high"
  assert_file_not_contains "$file" "actual: opus"
  record_message requested_actual_model result "$msg" "$file"
}
