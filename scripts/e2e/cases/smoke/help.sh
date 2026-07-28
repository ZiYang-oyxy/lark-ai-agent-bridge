#!/usr/bin/env bash
# smoke: help

case_help() {
  local msg file
  msg="$(send_at "/help")"
  wait_audit "reply_to=$msg event=message"
  file="$(mget help "$msg")"
  assert_file_contains "$file" "/new [--workdir <path>] [prompt]"
  record_message help root "$msg" "$file"
}
