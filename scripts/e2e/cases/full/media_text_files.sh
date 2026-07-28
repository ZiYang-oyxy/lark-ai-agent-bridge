#!/usr/bin/env bash
# full: media_text_files (parallel_group=media with media_images)

case_media_text_files() {
  require_fake_claude
  require_media_p2p_chat
  prepare_media_fixtures
  local source extension role msg file log_mark expected
  for extension in txt md json csv; do
    source="$MEDIA_FIXTURE_DIR/sample.$extension"
    role="text_file_$extension"
    expected="$(media_cache_path "$source" ".$extension")"
    log_mark="$(fake_log_mark)"
    msg="$(send_direct_media file "$source")"
    wait_audit "$msg.*event=result" 60
    wait_file_contains "$FAKE_CLAUDE_LOG" "$expected" 60
    assert_fake_paths_since "$log_mark" "$expected"
    file="$(mget "media_text_files_$extension" "$msg")"
    assert_file_contains "$file" "FAKE_E2E_STARTED"
    assert_no_error_event "$msg"
    record_message media_text_files "$role" "$msg" "$file"
  done
}
