#!/usr/bin/env bash
# full: media_rejected (run_media_rejection is its private helper)

run_media_rejection() {
  local label="$1"
  local source="$2"
  local expected_code="$3"
  local msg file log_mark rejected_path extension
  extension=".${source##*.}"
  rejected_path="$(media_cache_path "$source" "$extension")"
  log_mark="$(fake_log_mark)"
  msg="$(send_direct_media file "$source")"
  wait_audit "reply_to=$msg event=message" 60
  wait_message_contains "media_rejected_$label" "$msg" "附件处理失败，未执行：" 60
  file="$WAIT_MESSAGE_FILE"
  assert_file_contains "$file" "$expected_code"
  assert_fake_log_unchanged "$log_mark"
  assert_fake_paths_absent_since "$log_mark" "$rejected_path"
  record_message media_rejected "$label" "$msg" "$file"
}

case_media_rejected() {
  require_fake_claude
  require_media_p2p_chat
  prepare_media_fixtures
  run_media_rejection forged_mismatch "$MEDIA_FIXTURE_DIR/forged.png" content_mismatch
  run_media_rejection oversized "$MEDIA_FIXTURE_DIR/oversized.txt" file_too_large
  run_media_rejection pdf "$MEDIA_FIXTURE_DIR/unsupported.pdf" unsupported_media
  run_media_rejection docx "$MEDIA_FIXTURE_DIR/unsupported.docx" unsupported_media
  run_media_rejection audio "$MEDIA_FIXTURE_DIR/unsupported.wav" unsupported_media
  run_media_rejection attachment_only_total_failure "$MEDIA_FIXTURE_DIR/unsupported.bin" unsupported_media
}
