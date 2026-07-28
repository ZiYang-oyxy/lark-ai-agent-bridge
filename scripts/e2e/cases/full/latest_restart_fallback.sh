#!/usr/bin/env bash
# full: latest_restart_fallback

case_latest_restart_fallback() {
  require_fake_claude
  local config_msg session_id active queued follow stale_follow file mark active_card follow_card interrupted_sequence follow_sequence
  local active_message_file active_reply_to dm_chat_id
  local active_marker="E2E_${RUN_ID}_LATEST_RESTART_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_LATEST_RESTART_QUEUED"
  local follow_marker="E2E_${RUN_ID}_LATEST_RESTART_FOLLOW"
  local active_before queued_before invalid_card="e2e-invalid-card-id"
  config_msg="$(open_config latest_restart_fallback)"
  session_id="config:message:${config_msg}"
  submit_config latest_restart_fallback "$session_id" default low latest-card
  assert_persisted_config default low latest-card

  mark="$(audit_mark)"
  active="$(send_dm "/new ${active_marker}")"
  wait_audit_since "$mark" "$active.*event=stream" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$active_marker" 60
  active_card="$(audit_card_id_since "$mark" "$active")"
  active_reply_to="$(audit_card_reply_to "$active_card")"
  active_message_file="$(mget latest_restart_active "$active")"
  dm_chat_id="$(jq -r '.data.messages[0].chat_id // empty' "$active_message_file")"
  if [[ -z "$dm_chat_id" ]]; then
    echo "could not derive P2P chat id from active message $active" >&2
    return 1
  fi
  mark="$(audit_mark)"
  queued="$(send_dm "$queued_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  active_before="$(fake_marker_count "$active_marker")"
  queued_before="$(fake_marker_count "$queued_marker")"
  mark="$(audit_mark)"
  restart_server KILL
  wait_audit_since "$mark" '"Action":"session_recovery_interrupted"' 60
  wait_audit_since "$mark" '"Action":"session_recovery_cancelled"' 60
  wait_audit_since "$mark" "$active.*event=interrupted" 60
  interrupted_sequence="$(audit_card_sequence_since "$mark" "$active")"
  file="$(mget latest_restart_interrupted "$active_reply_to")"
  assert_file_contains "$file" "服务重启，已中断，请重新发送"
  assert_fake_marker_not_started_after "$active_marker" "$active_before"
  assert_fake_marker_not_started_after "$queued_marker" "$queued_before"

  mark="$(audit_mark)"
  follow="$(send_dm "/new ${follow_marker}")"
  wait_audit_since "$mark" "$follow.*event=result" 60
  follow_card="$(audit_card_id_since "$mark" "$follow")"
  follow_sequence="$(audit_card_sequence_since "$mark" "$follow")"
  assert_no_card_create_since "$mark" "$follow"
  if [[ "$follow_card" != "$active_card" || "$follow_sequence" -le "$interrupted_sequence" ]]; then
    echo "first latest run after restart did not advance old card: card $active_card/$follow_card sequence $interrupted_sequence/$follow_sequence" >&2
    return 1
  fi

  stop_server TERM
  jq --arg scope "claude:${dm_chat_id}" --arg card "$invalid_card" \
    '.latest_by_scope[$scope].CardID = $card' "$REPLY_STORE" >"$REPLY_STORE.tmp"
  chmod 600 "$REPLY_STORE.tmp"
  mv "$REPLY_STORE.tmp" "$REPLY_STORE"
  start_server_if_needed stale-restart
  mark="$(audit_mark)"
  stale_follow="$(send_dm "/new E2E_${RUN_ID}_LATEST_STALE_FALLBACK")"
  wait_audit_since "$mark" "$stale_follow.*event=result" 60
  wait_audit_since "$mark" '"Action":"cardkit_create"' 60
  follow_card="$(audit_card_id_since "$mark" "$stale_follow")"
  if [[ "$follow_card" == "$invalid_card" ]]; then
    echo "stale latest mapping was not replaced" >&2
    return 1
  fi
  record_message latest_restart_fallback interrupted "$active" "$file"
  record_message latest_restart_fallback queued_cancelled "$queued"
  record_message latest_restart_fallback resumed "$follow"
  record_message latest_restart_fallback stale_replaced "$stale_follow"
  summary "- recovered_card: $active_card sequence $interrupted_sequence -> $follow_sequence"
  summary "- stale_replacement_card: $follow_card"
}
