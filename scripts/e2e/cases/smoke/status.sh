#!/usr/bin/env bash
# smoke: status

case_status() {
  local msg file
  msg="$(send_at "/status")"
  wait_audit "reply_to=$msg event=message"
  file="$(mget status "$msg")"
  assert_file_contains "$file" "mode=claude_oneshot"
  assert_file_contains "$file" "default_workdir=$DEFAULT_WORKDIR"
  record_message status root "$msg" "$file"
}
