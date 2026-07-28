#!/usr/bin/env bash
# full: queue_full (mutates SERVER_QUEUE_MAX_PENDING and restarts; resets it after)

case_queue_full() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_QUEUE_FULL_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_QUEUE_FULL_QUEUED"
  local rejected_marker="E2E_${RUN_ID}_QUEUE_FULL_REJECTED"
  local active queued rejected file mark stop_mark
  SERVER_QUEUE_MAX_PENDING=2
  restart_server TERM
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  mark="$(audit_mark)"
  queued="$(send_at "$queued_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$active_marker" 60
  mark="$(audit_mark)"
  rejected="$(send_at "$rejected_marker")"
  wait_audit_since "$mark" '"Action":"queue_rejected"' 60
  file="$(mget queue_full "$rejected")"
  assert_file_contains "$file" "队列已满，未执行。"
  if grep -F -- "$rejected_marker" "$FAKE_CLAUDE_LOG" >/dev/null 2>&1; then
    echo "queue-full input unexpectedly started a fake Claude process" >&2
    return 1
  fi
  stop_mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit_since "$stop_mark" '"Action":"batch_stop_requested"' 60
  wait_audit "$queued.*event=result" 60
  SERVER_QUEUE_MAX_PENDING=""
  restart_server TERM
  record_message queue_full active "$active"
  record_message queue_full queued "$queued"
  record_message queue_full rejected "$rejected" "$file"
}
