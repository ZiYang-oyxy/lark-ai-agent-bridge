#!/usr/bin/env bash
# smoke: status

case_status() {
  local msg file
  msg="$(send_at "/status")"
  wait_message_contains status "$msg" "📊 会话状态"
  file="$WAIT_MESSAGE_FILE"
  assert_file_contains "$file" "\`claude_oneshot\`"
  record_message status root "$msg" "$file"
}
