#!/usr/bin/env bash
# full: media_attachment_only

case_media_attachment_only() {
  require_fake_claude
  prepare_media_fixtures
  local source="$MEDIA_FIXTURE_DIR/sample.jpg"
  local key elements msg file log_mark expected
  key="$(upload_media_key media_attachment_only image "$source")"
  elements="$(jq -nc --arg key "$key" '[{tag:"img",image_key:$key}]')"
  expected="$(media_cache_path "$source" .jpg)"
  log_mark="$(fake_log_mark)"
  msg="$(send_media_post "$elements")"
  wait_audit "$msg.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$expected" 60
  assert_fake_paths_since "$log_mark" "$expected"
  file="$(mget media_attachment_only "$msg")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  assert_no_error_event "$msg"
  record_message media_attachment_only attachment_only_jpeg "$msg" "$file"
}
