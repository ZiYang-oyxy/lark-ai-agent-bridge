#!/usr/bin/env bash
# full: native_text_stream

case_native_text_stream() {
  local normal_marker="E2E_${RUN_ID}_NATIVE_TEXT_STREAM_NORMAL"
  local stop_marker="E2E_${RUN_ID}_NATIVE_TEXT_STREAM_STOP_E2E_BLOCK"
  local normal stop normal_mark stop_mark callback_mark callback_elapsed stop_response
  local normal_stream_pattern normal_terminal_pattern

  normal_mark="$(audit_mark)"
  normal="$(send_at "/new ${normal_marker} 请只回复一篇至少四段的简短说明，每段至少两句，不要调用工具；必须在正文中包含该标记。")"
  normal_stream_pattern="($normal.*\"Action\":\"cardkit_text_stream\"|\"Action\":\"cardkit_text_stream\".*$normal)"
  normal_terminal_pattern="($normal.*event=result|event=result.*$normal)"
  wait_audit_expected_before_terminal "$normal_mark" "$normal_stream_pattern" "$normal_terminal_pattern" 60
  wait_audit_count_since "$normal_mark" "($normal.*(\"Action\":\"cardkit_text_stream\"|event=stream)|(\"Action\":\"cardkit_text_stream\"|event=stream).*$normal)" 3 60
  wait_audit_since "$normal_mark" "$normal.*event=result" 60
  if ! audit_since "$normal_mark" | jq -s -e --arg message_id "$normal" '
    [ .[] | select((.SessionID // "") | contains($message_id)) ] | to_entries as $events
    | ([ $events[] | select(.value.Action == "cardkit_text_stream") | .key ] | first) as $native
    | ([ $events[] | select(.value.Action == "cardkit_update" and (.value.Detail | contains("event=result"))) | .key ] | first) as $terminal
    | $native != null and $terminal != null and $native < $terminal
  ' >/dev/null; then
    echo "native text stream was not followed by a terminal CardKit update: $normal" >&2
    return 1
  fi

  stop_mark="$(audit_mark)"
  stop="$(send_at "/new ${stop_marker} 请只连续输出一篇至少一千字的中文说明，不要调用工具；必须在正文中包含该标记。")"
  wait_audit_since "$stop_mark" '"Action":"cardkit_text_stream"' 60
  SECONDS=0
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${stop}"
  callback_elapsed="$SECONDS"
  if (( callback_elapsed > 3 )); then
    echo "stop callback exceeded three seconds: ${callback_elapsed}s" >&2
    return 1
  fi
  stop_response="$RUN_DIR/stop-claude_${E2E_E2E_CHAT_ID//[^A-Za-z0-9._-]/_}_message_${stop//[^A-Za-z0-9._-]/_}.json"
  jq -e '
    .ok == true
    and .card.config.streaming_mode == false
    and ([.. | objects | select(.tag? == "button") | .disabled] | length > 0 and all(.[]; . == true))
  ' "$stop_response" >/dev/null
  callback_mark="$(audit_mark)"
  wait_audit_since "$stop_mark" "$stop.*event=stopped" 60
  sleep 3
  if audit_since "$callback_mark" | jq -e --arg message_id "$stop" '
    select((.SessionID // "") | contains($message_id)) | select(.Action == "cardkit_text_stream")
  ' >/dev/null; then
    echo "native preview occurred after the stop callback completed: $stop" >&2
    return 1
  fi
  if ! audit_since "$stop_mark" | jq -e --arg message_id "$stop" '
    select((.SessionID // "") | contains($message_id))
    | select(.Action == "cardkit_sequence_unknown")
  ' >/dev/null; then
    if ! audit_since "$stop_mark" | jq -e --arg message_id "$stop" '
      select((.SessionID // "") | contains($message_id))
      | select(.Action == "cardkit_update")
      | select(.Detail | contains("event=stopped") and contains("streaming=false") and contains("stop_disabled=true"))
    ' >/dev/null; then
      echo "stopped terminal card did not disable buttons or streaming: $stop" >&2
      return 1
    fi
  fi
  record_message native_text_stream normal "$normal"
  record_message native_text_stream stopped "$stop"
  summary "- native_text_stream: normal=$normal stop=$stop callback_sec=$callback_elapsed buttons=disabled streaming_mode=false"
}
