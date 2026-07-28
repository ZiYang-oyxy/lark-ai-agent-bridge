#!/usr/bin/env bash
# smoke: new_basic

case_new_basic() {
  local marker="E2E_${RUN_ID}_NEW_BASIC"
  local msg file
  msg="$(send_at "/new 请只回复 ${marker}，不要调用工具。")"
  wait_audit "$msg.*event=result"
  file="$(mget new_basic "$msg")"
  assert_file_contains "$file" "$marker"
  assert_file_contains "$file" "[已完成 ✗]"
  record_message new_basic root "$msg" "$file"
}
