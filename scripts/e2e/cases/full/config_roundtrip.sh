#!/usr/bin/env bash
# full: config_roundtrip

case_config_roundtrip() {
  require_fake_claude
  local config_msg session_id model effort marker msg file log_mark
  local -a models=(default sonnet opus haiku "$CONFIG_CUSTOM_MODEL")
  local -a efforts=(default low medium high high)
  config_msg="$(open_config config_roundtrip)"
  session_id="config:message:${config_msg}"
  for index in "${!models[@]}"; do
    model="${models[$index]}"
    effort="${efforts[$index]}"
    submit_config config_roundtrip "$session_id" "$model" "$effort"
    assert_persisted_config "$model" "$effort"
    marker="E2E_${RUN_ID}_CONFIG_${index}"
    log_mark="$(fake_log_mark)"
    msg="$(send_at "/new ${marker}")"
    wait_audit "$msg.*event=result" 60
    wait_file_contains "$FAKE_CLAUDE_LOG" "$marker" 60
    assert_fake_config_argv_since "$log_mark" "$marker" "$model" "$effort"
    file="$(mget "config_roundtrip_${index}" "$msg")"
    assert_file_contains "$file" "requested: $model"
    assert_file_contains "$file" "effort: $effort"
    record_message config_roundtrip "run_${model}_${effort}" "$msg" "$file"
  done
  submit_invalid_config config_roundtrip "$session_id"
  assert_persisted_config "$CONFIG_CUSTOM_MODEL" high
  summary "- combinations: default/default, sonnet/low, opus/medium, haiku/high, ${CONFIG_CUSTOM_MODEL}/high"
}
