#!/usr/bin/env bash
# smoke: workdir_existing

case_workdir_existing() {
  local dir="$RUN_DIR/workdir-existing"
  local marker="PWD_RESULT:$dir"
  local msg file
  mkdir -p "$dir"
  msg="$(send_at "/new --workdir $dir 请使用 Bash(pwd) 查看当前目录，然后只回复 PWD_RESULT:<pwd输出>。")"
  wait_audit "$msg.*event=result"
  file="$(mget workdir_existing "$msg")"
  assert_file_contains "$file" "$marker"
  assert_file_contains "$file" "$dir"
  record_message workdir_existing root "$msg" "$file"
}
