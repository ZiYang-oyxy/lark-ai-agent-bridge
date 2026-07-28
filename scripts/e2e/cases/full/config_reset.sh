#!/usr/bin/env bash
# full: config_reset

case_config_reset() {
  require_fake_claude
  local config_msg session_id reset_msg reset_file reopened marker msg log_mark
  config_msg="$(open_config config_reset)"
  session_id="config:message:${config_msg}"
  submit_config config_reset "$session_id" opus high
  assert_persisted_config opus high
  reset_msg="$(send_at "/config reset")"
  wait_audit '"Action":"config_reset"' 60
  wait_audit "reply_to=$reset_msg event=message" 60
  reset_file="$(mget config_reset "$reset_msg")"
  assert_file_contains "$reset_file" "已恢复环境默认"
  jq -e '.schema_version == 1 and .override == null' "$PREFERENCE_STORE" >/dev/null
  restart_server TERM
  reopened="$(open_config config_reset_after_restart)"
  jq -e '.schema_version == 1 and .override == null' "$PREFERENCE_STORE" >/dev/null
  marker="E2E_${RUN_ID}_CONFIG_RESET_DEFAULTS"
  log_mark="$(fake_log_mark)"
  msg="$(send_at "/new ${marker}")"
  wait_audit "$msg.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$marker" 60
  assert_fake_config_argv_since "$log_mark" "$marker" "$CONFIG_DEFAULT_MODEL" "$CONFIG_DEFAULT_EFFORT"
  record_message config_reset reset "$reset_msg" "$reset_file"
  record_message config_reset reopened "$reopened"
  record_message config_reset defaults_run "$msg"
  summary "- environment_defaults: model=$CONFIG_DEFAULT_MODEL effort=$CONFIG_DEFAULT_EFFORT"
}
