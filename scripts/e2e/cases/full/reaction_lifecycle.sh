#!/usr/bin/env bash
# full: reaction_lifecycle

case_reaction_lifecycle() {
  require_fake_claude
  local quick active queued quick_mark active_mark
  quick_mark="$(audit_mark)"
  quick="$(send_at "/new E2E_${RUN_ID}_REACTION_QUICK")"
  wait_audit_since "$quick_mark" "$quick.*event=result" 60
  wait_audit_since "$quick_mark" '"Action":"reaction_added".*"SessionID":"'"$quick"'".*type=Typing' 60
  wait_audit_since "$quick_mark" '"Action":"reaction_deleted".*"SessionID":"'"$quick"'".*type=Typing' 60
  if audit_since "$quick_mark" | grep -F '"SessionID":"'"$quick"'"' | grep -F '"Action":"reaction_added"' | grep -F 'type=OneSecond' >/dev/null 2>&1; then
    wait_audit_since "$quick_mark" '"Action":"reaction_deleted".*"SessionID":"'"$quick"'".*type=OneSecond' 60
  fi

  active_mark="$(audit_mark)"
  active="$(send_at "E2E_${RUN_ID}_REACTION_ACTIVE_E2E_BLOCK")"
  wait_audit_since "$active_mark" "$active.*event=stream" 60
  queued="$(send_at "E2E_${RUN_ID}_REACTION_WAIT")"
  wait_audit_since "$active_mark" '"Action":"reaction_added".*"SessionID":"'"$queued"'".*type=OneSecond' 60
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit_since "$active_mark" '"Action":"reaction_deleted".*"SessionID":"'"$queued"'".*type=OneSecond' 60
  wait_audit_since "$active_mark" '"Action":"reaction_added".*"SessionID":"'"$queued"'".*type=Typing' 60
  wait_audit_since "$active_mark" "$queued.*event=result" 60
  wait_audit_since "$active_mark" '"Action":"reaction_deleted".*"SessionID":"'"$queued"'".*type=Typing' 60
  record_message reaction_lifecycle quick "$quick"
  record_message reaction_lifecycle active "$active"
  record_message reaction_lifecycle delayed "$queued"
}
