#!/usr/bin/env bash
# smoke: streaming_card

case_streaming_card() {
  local marker="E2E_${RUN_ID}_STREAM"
  local msg file
  msg="$(send_at "/new 请使用 Bash 工具执行 sleep 3; echo ${marker}，然后只回复 ${marker}。")"
  wait_audit "$msg.*event=stream sequence="
  wait_audit "$msg.*event=result"
  file="$(mget streaming_card "$msg")"
  assert_file_contains "$file" "$marker"
  record_message streaming_card root "$msg" "$file"
}
