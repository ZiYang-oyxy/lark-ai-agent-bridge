#!/usr/bin/env bash
# full: preview_thresholds

case_preview_thresholds() {
  require_fake_claude
  require_cmd python3
  local config_msg session_id msg mark preview_file final_file
  config_msg="$(open_config preview_thresholds)"
  session_id="config:message:${config_msg}"
  submit_config preview_thresholds "$session_id" default low append
  mark="$(audit_mark)"
  msg="$(send_dm "/new E2E_${RUN_ID}_E2E_PREVIEW_THRESHOLDS")"
  wait_audit_count_since "$mark" '"Action":"cardkit_update".*event=stream' 3 60
  preview_file="$(mget preview_thresholds_preview "$msg")"
  assert_file_contains "$preview_file" "PREVIEW_FIRST_"
  assert_file_not_contains "$preview_file" "PREVIEW_TAIL_HIDDEN"
  python3 - "$AUDIT" "$mark" "$msg" <<'PY'
import datetime as dt
import json
import sys

path, mark, message_id = sys.argv[1], int(sys.argv[2]), sys.argv[3]
events = []
with open(path, encoding="utf-8") as source:
    for index, line in enumerate(source, 1):
        if index <= mark:
            continue
        event = json.loads(line)
        if event.get("Action") == "cardkit_update" and message_id in event.get("SessionID", "") and "event=stream" in event.get("Detail", ""):
            events.append(dt.datetime.fromisoformat(event["Time"].replace("Z", "+00:00")))
if len(events) < 2:
    raise SystemExit(f"expected at least two stream updates, got {len(events)}")
gap = (events[1] - events[0]).total_seconds()
if gap < 0.65:
    raise SystemExit(f"preview updates were not throttled: gap={gap:.3f}s")
print(f"first_preview_gap_sec={gap:.3f}")
PY
  wait_audit_since "$mark" "$msg.*event=result" 60
  final_file="$(mget preview_thresholds_final "$msg")"
  assert_file_contains "$final_file" "PREVIEW_FINAL_COMPLETE_"
  assert_file_contains "$final_file" "PREVIEW_TAIL_HIDDEN"
  record_message preview_thresholds result "$msg" "$final_file"
}
