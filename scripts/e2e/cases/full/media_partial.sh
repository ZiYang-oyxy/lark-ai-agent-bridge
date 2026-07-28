#!/usr/bin/env bash
# full: media_partial

case_media_partial() {
  require_fake_claude
  require_media_p2p_chat
  prepare_media_fixtures
  local image="$MEDIA_FIXTURE_DIR/sample.png"
  local rejected="$MEDIA_FIXTURE_DIR/unsupported.pdf"
  local marker="E2E_${RUN_ID}_MEDIA_MIXED_PARTIAL"
  local image_key elements msg file log_mark image_path rejected_path rejected_msg rejected_file
  image_key="$(upload_media_key media_partial image "$image")"
  elements="$(jq -nc --arg marker "$marker" --arg image "$image_key" \
    '[{tag:"text",text:$marker},{tag:"img",image_key:$image}]')"
  image_path="$(media_cache_path "$image" .png)"
  rejected_path="$(media_cache_path "$rejected" .pdf)"
  log_mark="$(fake_log_mark)"
  msg="$(send_media_post "$elements")"
  wait_audit "$msg.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$image_path" 60
  assert_fake_paths_since "$log_mark" "$image_path"
  file="$(mget media_partial "$msg")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  assert_no_error_event "$msg"
  record_message media_partial mixed_text_image "$msg" "$file"

  log_mark="$(fake_log_mark)"
  rejected_msg="$(send_direct_media file "$rejected")"
  wait_audit "reply_to=$rejected_msg event=message" 60
  wait_message_contains media_partial_rejected "$rejected_msg" "附件处理失败，未执行：" 60
  rejected_file="$WAIT_MESSAGE_FILE"
  assert_file_contains "$rejected_file" "unsupported_media"
  assert_fake_log_unchanged "$log_mark"
  assert_fake_paths_absent_since "$log_mark" "$rejected_path"
  record_message media_partial rejected_peer "$rejected_msg" "$rejected_file"
}
