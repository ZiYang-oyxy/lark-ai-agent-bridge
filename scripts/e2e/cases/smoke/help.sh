#!/usr/bin/env bash
# smoke: help

case_help() {
  local msg file
  msg="$(send_at "/help")"
  wait_message_contains help "$msg" "💡 命令帮助"
  file="$WAIT_MESSAGE_FILE"
  assert_file_contains "$file" "\`/new\`"
  record_message help root "$msg" "$file"
}
