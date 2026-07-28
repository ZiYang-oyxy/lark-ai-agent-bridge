#!/usr/bin/env bash
# full: media_images (parallel_group=media with media_text_files)

case_media_images() {
  require_fake_claude
  prepare_media_fixtures
  local jpg="$MEDIA_FIXTURE_DIR/sample.jpg"
  local png="$MEDIA_FIXTURE_DIR/sample.png"
  local webp="$MEDIA_FIXTURE_DIR/sample.webp"
  local gif="$MEDIA_FIXTURE_DIR/sample.gif"
  local jpg_key png_key webp_key gif_key elements msg file log_mark
  local jpg_path png_path webp_path gif_path
  jpg_key="$(upload_media_key media_images image "$jpg")"
  png_key="$(upload_media_key media_images image "$png")"
  webp_key="$(upload_media_key media_images image "$webp")"
  gif_key="$(upload_media_key media_images image "$gif")"
  elements="$(jq -nc --arg jpg "$jpg_key" --arg png "$png_key" --arg webp "$webp_key" --arg gif "$gif_key" \
    '[{tag:"img",image_key:$jpg},{tag:"img",image_key:$png},{tag:"img",image_key:$webp},{tag:"img",image_key:$gif}]')"
  jpg_path="$(media_cache_path "$jpg" .jpg)"
  png_path="$(media_cache_path "$png" .png)"
  webp_path="$(media_cache_path "$webp" .webp)"
  gif_path="$(media_cache_path "$gif" .gif)"
  log_mark="$(fake_log_mark)"
  msg="$(send_media_post "$elements")"
  wait_audit "$msg.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$gif_path" 60
  assert_fake_paths_since "$log_mark" "$jpg_path" "$png_path" "$webp_path" "$gif_path"
  file="$(mget media_images "$msg")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  assert_no_error_event "$msg"
  record_message media_images image_matrix "$msg" "$file"
}
