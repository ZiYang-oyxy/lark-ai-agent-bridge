#!/usr/bin/env bash
# Feishu interaction helpers: lark-cli wrapper, bot identity, message send/reply,
# media upload, audit-log waiters, and assertion primitives. Defines functions
# only; no side effects at source time.

lark_cli() {
  if [[ -n "$LARK_CLI_PROFILE" ]]; then
    command lark-cli --profile "$LARK_CLI_PROFILE" "$@"
  else
    command lark-cli "$@"
  fi
}

json_escape() {
  jq -Rn --arg v "$1" '$v'
}

fetch_bot_open_id() {
  if [[ -n "${LARK_BOT_OPEN_ID:-}" ]]; then
    BOT_OPEN_ID="$LARK_BOT_OPEN_ID"
    return
  fi
  local token_resp token info_resp open_id
  token_resp="$(curl -sS -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg app_id "$LARK_APP_ID" --arg app_secret "$LARK_APP_SECRET" '{app_id:$app_id, app_secret:$app_secret}')")"
  token="$(printf '%s' "$token_resp" | jq -r '.tenant_access_token // empty')"
  if [[ -z "$token" ]]; then
    echo "failed to fetch tenant access token" >&2
    exit 1
  fi
  info_resp="$(curl -sS 'https://open.feishu.cn/open-apis/bot/v3/info' -H "Authorization: Bearer $token")"
  open_id="$(printf '%s' "$info_resp" | jq -r '.bot.open_id // .data.open_id // .open_id // empty')"
  if [[ -z "$open_id" ]]; then
    echo "failed to fetch bot open_id" >&2
    exit 1
  fi
  BOT_OPEN_ID="$open_id"
}

send_at() {
  local text="$1"
  local content msg_id
  content="$(jq -nc --arg bot "$BOT_OPEN_ID" --arg text "$text" \
    '{zh_cn:{title:"",content:[[{tag:"at",user_id:$bot},{tag:"text",text:$text}]]}}')"
  msg_id="$(lark_cli im +messages-send --as user --chat-id "$E2E_E2E_CHAT_ID" \
    --msg-type post --content "$content" \
    --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to send message: $text" >&2
    exit 1
  fi
  printf '%s\n' "$msg_id"
}

send_text() {
  local text="$1"
  local msg_id
  msg_id="$(lark_cli im +messages-send --as user --chat-id "$E2E_E2E_CHAT_ID" --text "$text" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to send message: $text" >&2
    exit 1
  fi
  printf '%s\n' "$msg_id"
}

send_dm() {
  local text="$1"
  local msg_id
  msg_id="$(lark_cli im +messages-send --as user --user-id "$BOT_OPEN_ID" --text "$text" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to send direct message: $text" >&2
    return 1
  fi
  printf '%s\n' "$msg_id"
}

prepare_media_fixtures() {
  if [[ -f "$MEDIA_FIXTURE_DIR/.ready" ]]; then
    return
  fi
  require_cmd python3
  mkdir -p "$MEDIA_FIXTURE_DIR"
  python3 - "$MEDIA_FIXTURE_DIR" <<'PY'
from pathlib import Path
import sys
from PIL import Image

root = Path(sys.argv[1])
image = Image.new("RGB", (16, 16), (255, 0, 0))
for x in range(8, 16):
    for y in range(16):
        image.putpixel((x, y), (0, 128, 255))
for name, format_name in (("sample.jpg", "JPEG"), ("sample.png", "PNG"), ("sample.gif", "GIF"), ("sample.webp", "WEBP")):
    image.save(root / name, format=format_name)
PY
  printf 'MEDIA_TXT_%s\n' "$RUN_ID" >"$MEDIA_FIXTURE_DIR/sample.txt"
  printf '# MEDIA_MD_%s\n' "$RUN_ID" >"$MEDIA_FIXTURE_DIR/sample.md"
  jq -nc --arg marker "MEDIA_JSON_$RUN_ID" '{marker:$marker}' >"$MEDIA_FIXTURE_DIR/sample.json"
  printf 'kind,marker\nmedia,MEDIA_CSV_%s\n' "$RUN_ID" >"$MEDIA_FIXTURE_DIR/sample.csv"
  printf 'this is text disguised as an image\n' >"$MEDIA_FIXTURE_DIR/forged.png"
  dd if=/dev/zero bs=1048576 count=26 2>/dev/null | tr '\000' 'a' >"$MEDIA_FIXTURE_DIR/oversized.txt"
  printf '%%PDF-1.4\n%% E2E unsupported fixture\n' >"$MEDIA_FIXTURE_DIR/unsupported.pdf"
  printf 'PK\003\004E2E unsupported docx fixture\n' >"$MEDIA_FIXTURE_DIR/unsupported.docx"
  printf 'RIFF0000WAVEE2E unsupported audio fixture\n' >"$MEDIA_FIXTURE_DIR/unsupported.wav"
  printf '\000\001\002E2E unknown binary fixture\n' >"$MEDIA_FIXTURE_DIR/unsupported.bin"
  : >"$MEDIA_FIXTURE_DIR/.ready"
}

media_relative_path() {
  local path="$1"
  case "$path" in
    "$ROOT"/*) printf './%s\n' "${path#"$ROOT/"}" ;;
    *) echo "media fixture is outside repo root: $path" >&2; return 1 ;;
  esac
}

upload_media_key() {
  local case_name="$1"
  local kind="$2"
  local path="$3"
  local relative msg_id file key
  relative="$(media_relative_path "$path")"
  case "$kind" in
    image)
      msg_id="$(lark_cli im +messages-send --as user --chat-id "$E2E_E2E_CHAT_ID" --image "$relative" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
      ;;
    file)
      msg_id="$(lark_cli im +messages-send --as user --chat-id "$E2E_E2E_CHAT_ID" --file "$relative" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
      ;;
    *) echo "unsupported media upload kind: $kind" >&2; return 1 ;;
  esac
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to upload media fixture: $path" >&2
    return 1
  fi
  file="$(mget "${case_name}_upload" "$msg_id")"
  key="$(jq -r '
    .data.messages[0] as $message |
    if $message.msg_type == "image" then
      ($message.content | capture("\\[Image: (?<key>img_[^]]+)\\]").key)
    elif $message.msg_type == "file" then
      ($message.content | capture("key=\\\"(?<key>file_[^\\\"]+)\\\"").key)
    else empty end
  ' "$file")"
  if [[ -z "$key" || "$key" == "null" ]]; then
    echo "uploaded media message has no resource key: $msg_id ($file)" >&2
    return 1
  fi
  record_message "$case_name" fixture_upload "$msg_id" "$file"
  printf '%s\n' "$key"
}

send_media_post() {
  local elements="$1"
  local content msg_id
  content="$(jq -nc --arg bot "$BOT_OPEN_ID" --argjson elements "$elements" \
    '{zh_cn:{title:"",content:[([{tag:"at",user_id:$bot}] + $elements)]}}')"
  msg_id="$(lark_cli im +messages-send --as user --chat-id "$E2E_E2E_CHAT_ID" --msg-type post --content "$content" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to send media post" >&2
    return 1
  fi
  printf '%s\n' "$msg_id"
}

require_media_p2p_chat() {
  if [[ -z "$MEDIA_P2P_CHAT_ID" ]]; then
    echo "media file cases require E2E_REAL_E2E_P2P_CHAT_ID for the user's direct chat with this bot" >&2
    return 1
  fi
}

send_direct_media() {
  local kind="$1"
  local path="$2"
  local relative msg_id
  require_media_p2p_chat
  relative="$(media_relative_path "$path")"
  case "$kind" in
    image)
      msg_id="$(lark_cli im +messages-send --as user --chat-id "$MEDIA_P2P_CHAT_ID" --image "$relative" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
      ;;
    file)
      msg_id="$(lark_cli im +messages-send --as user --chat-id "$MEDIA_P2P_CHAT_ID" --file "$relative" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
      ;;
    *) echo "unsupported direct media kind: $kind" >&2; return 1 ;;
  esac
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to send direct media fixture: $path" >&2
    return 1
  fi
  printf '%s\n' "$msg_id"
}

media_cache_path() {
  local path="$1"
  local extension="$2"
  local digest
  digest="$(shasum -a 256 "$path" | awk '{print $1}')"
  printf '%s/%s%s\n' "$MEDIA_CACHE_DIR" "$digest" "$extension"
}

send_group_pair() {
  local first_text="$1"
  local second_text="$2"
  local tag="${first_text//[^A-Za-z0-9._-]/_}"
  local first_out="$RUN_DIR/group-pair-${tag}-first.out"
  local second_out="$RUN_DIR/group-pair-${tag}-second.out"
  local first_err="$RUN_DIR/group-pair-${tag}-first.err"
  local second_err="$RUN_DIR/group-pair-${tag}-second.err"
  local first_pid second_pid first_status=0 second_status=0
  send_at "$first_text" >"$first_out" 2>"$first_err" &
  first_pid=$!
  send_at "$second_text" >"$second_out" 2>"$second_err" &
  second_pid=$!
  wait "$first_pid" || first_status=$?
  wait "$second_pid" || second_status=$?
  if [[ "$first_status" -ne 0 || "$second_status" -ne 0 ]]; then
    echo "concurrent group send failed: first=$first_status second=$second_status" >&2
    sed -n '1,80p' "$first_err" "$second_err" >&2
    return 1
  fi
  PAIR_FIRST="$(tail -n 1 "$first_out")"
  PAIR_SECOND="$(tail -n 1 "$second_out")"
  if [[ -z "$PAIR_FIRST" || "$PAIR_FIRST" == "null" || -z "$PAIR_SECOND" || "$PAIR_SECOND" == "null" ]]; then
    echo "concurrent group send returned an empty message id" >&2
    return 1
  fi
}

send_dm_pair() {
  local first_text="$1"
  local second_text="$2"
  local tag="${first_text//[^A-Za-z0-9._-]/_}"
  local first_out="$RUN_DIR/dm-pair-${tag}-first.out"
  local second_out="$RUN_DIR/dm-pair-${tag}-second.out"
  local first_err="$RUN_DIR/dm-pair-${tag}-first.err"
  local second_err="$RUN_DIR/dm-pair-${tag}-second.err"
  local first_pid second_pid first_status=0 second_status=0
  lark_cli im +messages-send --as user --user-id "$BOT_OPEN_ID" --text "$first_text" --jq '.data.message_id // .message_id // .data.message_id' >"$first_out" 2>"$first_err" &
  first_pid=$!
  lark_cli im +messages-send --as user --user-id "$BOT_OPEN_ID" --text "$second_text" --jq '.data.message_id // .message_id // .data.message_id' >"$second_out" 2>"$second_err" &
  second_pid=$!
  wait "$first_pid" || first_status=$?
  wait "$second_pid" || second_status=$?
  if [[ "$first_status" -ne 0 || "$second_status" -ne 0 ]]; then
    echo "real P2P send to bot open_id failed: first=$first_status second=$second_status; see $first_err and $second_err" >&2
    sed -n '1,80p' "$first_err" "$second_err" >&2
    return 1
  fi
  PAIR_FIRST="$(tail -n 1 "$first_out")"
  PAIR_SECOND="$(tail -n 1 "$second_out")"
  if [[ -z "$PAIR_FIRST" || "$PAIR_FIRST" == "null" || -z "$PAIR_SECOND" || "$PAIR_SECOND" == "null" ]]; then
    echo "real P2P send returned an empty message id; bot open_id may not be valid as --user-id" >&2
    return 1
  fi
}

reply_thread() {
  local root_msg="$1"
  local text="$2"
  local msg_id
  msg_id="$(lark_cli im +messages-reply --as user --message-id "$root_msg" --reply-in-thread --text "$text" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to reply thread: $root_msg" >&2
    exit 1
  fi
  printf '%s\n' "$msg_id"
}

# reply_quote 发一条 quote(引用回复)到 root_msg,不进 thread。用 rich content
# 承载 <at bot> + 用户文本,飞书渲染为 quote 卡片、bridge 侧解析到 QuotedText。
# 与 reply_thread 的差别:不带 --reply-in-thread,行为等价于用户在群里 hover 消息
# 选"回复引用"。
reply_quote() {
  local root_msg="$1"
  local text="$2"
  local content msg_id
  content="$(jq -nc --arg bot "$BOT_OPEN_ID" --arg text "$text" \
    '{zh_cn:{title:"",content:[[{tag:"at",user_id:$bot},{tag:"text",text:$text}]]}}')"
  msg_id="$(lark_cli im +messages-reply --as user --message-id "$root_msg" \
    --msg-type post --content "$content" \
    --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to reply-quote: $root_msg" >&2
    exit 1
  fi
  printf '%s\n' "$msg_id"
}

mget() {
  local case_name="$1"
  local msg_id="$2"
  local lookup_id="$msg_id"
  local reply_id=""
  local out="$MGET_DIR/$case_name-$msg_id.json"
  reply_id="$(audit_reply_message_id "$msg_id" 2>/dev/null || true)"
  if [[ -n "$reply_id" ]]; then
    lookup_id="$reply_id"
  fi
  lark_cli im +messages-mget --as user --message-ids "$lookup_id" --format json >"$out"
  printf '%s\n' "$out"
}

WAIT_MESSAGE_FILE=""
wait_message_contains() {
  local case_name="$1"
  local msg_id="$2"
  local needle="$3"
  local timeout="${4:-$WAIT_TIMEOUT}"
  local start file
  start="$(date +%s)"
  while true; do
    file="$(mget "$case_name" "$msg_id" 2>/dev/null || true)"
    if [[ -n "$file" && -f "$file" ]] && file_contains "$file" "$needle"; then
      WAIT_MESSAGE_FILE="$file"
      return 0
    fi
    if (( $(date +%s) - start >= timeout )); then
      echo "timed out waiting for message $msg_id to contain: $needle" >&2
      return 1
    fi
    sleep 1
  done
}

thread_id_for_message() {
  local case_name="$1"
  local msg_id="$2"
  local file thread_id
  file="$(mget "$case_name" "$msg_id")"
  thread_id="$(jq -r '.data.messages[0].thread_id // empty' "$file")"
  if [[ -z "$thread_id" || "$thread_id" == "null" ]]; then
    echo "message has no actual thread_id: $msg_id (evidence: $file)" >&2
    return 1
  fi
  printf '%s\n' "$thread_id"
}

message_link() {
  local file="$1"
  jq -r '.data.messages[0].message_app_link // empty' "$file"
}

record_message() {
  local case_name="$1"
  local role="$2"
  local msg_id="$3"
  local file="${4:-}"
  local link=""
  if [[ -n "$file" && -f "$file" ]]; then
    link="$(message_link "$file")"
  fi
  jq -nc --arg case "$case_name" --arg role "$role" --arg message_id "$msg_id" --arg link "$link" \
    '{case:$case, role:$role, message_id:$message_id, link:$link}' >>"$MESSAGES"
}

wait_audit() {
  local pattern="$1"
  local timeout="${2:-$WAIT_TIMEOUT}"
  local start
  start="$(date +%s)"
  while true; do
    if [[ -f "$AUDIT" ]] && grep -E "$pattern" "$AUDIT" >/dev/null 2>&1; then
      return 0
    fi
    if (( $(date +%s) - start >= timeout )); then
      echo "timed out waiting for audit pattern: $pattern" >&2
      return 1
    fi
    sleep 1
  done
}

audit_mark() {
  if [[ -f "$AUDIT" ]]; then
    wc -l <"$AUDIT" | tr -d ' '
  else
    printf '0\n'
  fi
}

audit_since() {
  local mark="$1"
  if [[ -f "$AUDIT" ]]; then
    tail -n "+$((mark + 1))" "$AUDIT"
  fi
}

wait_audit_since() {
  local mark="$1"
  local pattern="$2"
  local timeout="${3:-$WAIT_TIMEOUT}"
  local start
  start="$(date +%s)"
  while true; do
    if audit_since "$mark" | grep -E "$pattern" >/dev/null 2>&1; then
      return 0
    fi
    if (( $(date +%s) - start >= timeout )); then
      echo "timed out waiting for new audit pattern: $pattern" >&2
      return 1
    fi
    sleep 1
  done
}

wait_audit_expected_before_terminal() {
  local mark="$1"
  local expected_pattern="$2"
  local terminal_pattern="$3"
  local timeout="${4:-$WAIT_TIMEOUT}"
  local start line
  start="$(date +%s)"
  while true; do
    while IFS= read -r line; do
      if printf '%s\n' "$line" | grep -E "$expected_pattern" >/dev/null 2>&1; then
        return 0
      fi
      if printf '%s\n' "$line" | grep -E "$terminal_pattern" >/dev/null 2>&1; then
        echo "terminal event observed before expected event: expected=$expected_pattern terminal=$terminal_pattern" >&2
        return 1
      fi
    done < <(audit_since "$mark")
    if (( $(date +%s) - start >= timeout )); then
      echo "timed out waiting for expected audit pattern: $expected_pattern (terminal: $terminal_pattern)" >&2
      return 1
    fi
    sleep 1
  done
}

wait_audit_count_since() {
  local mark="$1"
  local pattern="$2"
  local want="$3"
  local timeout="${4:-$WAIT_TIMEOUT}"
  local start count
  start="$(date +%s)"
  while true; do
    count="$(audit_since "$mark" | grep -Ec "$pattern" || true)"
    if [[ "$count" -ge "$want" ]]; then
      return 0
    fi
    if (( $(date +%s) - start >= timeout )); then
      echo "timed out waiting for $want new audit records matching: $pattern (got $count)" >&2
      return 1
    fi
    sleep 1
  done
}

result_message_since() {
  local mark="$1"
  local first="$2"
  local second="$3"
  local line
  line="$(audit_since "$mark" | grep -E "($first|$second).*event=result" | tail -n 1)"
  if [[ "$line" == *"$first"* ]]; then
    printf '%s\n' "$first"
    return
  fi
  if [[ "$line" == *"$second"* ]]; then
    printf '%s\n' "$second"
    return
  fi
  echo "could not identify batch result anchor for $first / $second" >&2
  return 1
}

assert_file_contains() {
  local file="$1"
  local needle="$2"
  if file_contains "$file" "$needle"; then
    return 0
  fi
    echo "expected $file to contain: $needle" >&2
    return 1
}

assert_file_not_contains() {
  local file="$1"
  local needle="$2"
  if file_contains "$file" "$needle"; then
    echo "expected $file not to contain: $needle" >&2
    return 1
  fi
}

file_contains() {
  local file="$1"
  local needle="$2"
  if jq -e . "$file" >/dev/null 2>&1; then
    jq -r '.. | strings' "$file" | grep -F "$needle" >/dev/null 2>&1
    return $?
  fi
  grep -F "$needle" "$file" >/dev/null 2>&1
}

assert_no_error_event() {
  local msg_id="$1"
  if grep -E "$msg_id.*event=error" "$AUDIT" >/dev/null 2>&1; then
    echo "agent run ended with error for message: $msg_id" >&2
    grep -E "$msg_id.*event=error|$msg_id.*run_failed" "$AUDIT" >&2 || true
    return 1
  fi
}

wait_file_contains() {
  local file="$1"
  local needle="$2"
  local timeout="${3:-$WAIT_TIMEOUT}"
  local start
  start="$(date +%s)"
  while true; do
    if [[ -f "$file" ]] && grep -F -- "$needle" "$file" >/dev/null 2>&1; then
      return 0
    fi
    if (( $(date +%s) - start >= timeout )); then
      echo "timed out waiting for $file to contain: $needle" >&2
      return 1
    fi
    sleep 1
  done
}

require_fake_claude() {
  if [[ "$USE_FAKE_CLAUDE" != "1" ]]; then
    echo "case requires E2E_REAL_E2E_FAKE_CLAUDE=1 for deterministic process assertions" >&2
    return 1
  fi
}

assert_fake_batch_contains() {
  local mark="$1"
  local first="$2"
  local second="$3"
  if ! tail -n "+$((mark + 1))" "$FAKE_CLAUDE_LOG" | awk -v first="$first" -v second="$second" '
    index($0, first) && index($0, second) { found = 1 }
    END { exit found ? 0 : 1 }
  '; then
    echo "no fake Claude invocation contained both batch markers: $first, $second" >&2
    return 1
  fi
}

fake_log_mark() {
  if [[ -f "$FAKE_CLAUDE_LOG" ]]; then
    wc -l <"$FAKE_CLAUDE_LOG" | tr -d ' '
  else
    printf '0\n'
  fi
}

assert_fake_paths_since() {
  local mark="$1"
  shift
  local path
  for path in "$@"; do
    if ! tail -n "+$((mark + 1))" "$FAKE_CLAUDE_LOG" | grep -F -- "$path" >/dev/null 2>&1; then
      echo "fake Claude prompt did not contain accepted media path: $path" >&2
      return 1
    fi
  done
}

assert_fake_paths_absent_since() {
  local mark="$1"
  shift
  local path
  for path in "$@"; do
    if tail -n "+$((mark + 1))" "$FAKE_CLAUDE_LOG" 2>/dev/null | grep -F -- "$path" >/dev/null 2>&1; then
      echo "rejected media path reached fake Claude prompt: $path" >&2
      return 1
    fi
  done
}

assert_fake_log_unchanged() {
  local before="$1"
  local after
  after="$(fake_log_mark)"
  if [[ "$after" -ne "$before" ]]; then
    echo "rejected attachment unexpectedly started fake Claude: before=$before after=$after" >&2
    return 1
  fi
}

assert_fake_marker_not_started_after() {
  local marker="$1"
  local before="$2"
  local after
  after="$(grep -F -c -- "$marker" "$FAKE_CLAUDE_LOG" 2>/dev/null || true)"
  if [[ "$after" -ne "$before" ]]; then
    echo "cleared input unexpectedly started after restart: $marker" >&2
    return 1
  fi
}

fake_marker_count() {
  local marker="$1"
  grep -F -c -- "$marker" "$FAKE_CLAUDE_LOG" 2>/dev/null || true
}

revoke_message() {
  local msg_id="$1"
  local out="$RUN_DIR/revoke-$msg_id.json"
  lark_cli im messages delete --as user --yes --params "{\"message_id\":\"$msg_id\"}" >"$out"
}

stop_card() {
  local session_id="$1"
  local out="$RUN_DIR/stop-${session_id//[^A-Za-z0-9._-]/_}.json"
  local payload
  wait_callback_ready
  payload="$(jq -nc --arg session "$session_id" '{operator:{open_id:"e2e"},action:{value:{session:$session,action_id:"stop"}}}')"
  curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d "$payload" >"$out"
  jq -e '.ok == true and .card != null' "$out" >/dev/null
}

open_config() {
  local case_name="$1"
  local msg file
  msg="$(send_at "/config")"
  wait_message_contains "${case_name}_open" "$msg" "全局运行偏好" 60
  file="$WAIT_MESSAGE_FILE"
  record_message "$case_name" config "$msg" "$file"
  printf '%s\n' "$msg"
}

submit_config() {
  local case_name="$1"
  local session_id="$2"
  local model="$3"
  local effort="$4"
  local reply_mode="${5:-append}"
  local conversation_mode="${6:-chat}"
  local group_message_mode="${7:-mention_only}"
  local respond_to_bots="${8:-false}"
  local out="$RUN_DIR/${case_name}-${model}-${effort}-${reply_mode}-${conversation_mode}-${group_message_mode}-${respond_to_bots}.json"
  local payload mark
  wait_callback_ready
  payload="$(jq -nc --arg session "$session_id" --arg model "$model" --arg effort "$effort" --arg reply_mode "$reply_mode" --arg conversation_mode "$conversation_mode" --arg group_message_mode "$group_message_mode" --arg respond_to_bots "$respond_to_bots" \
    '{operator:{open_id:"e2e"},action:{value:{session:$session,action_id:"config.save"},form_value:{model:$model,effort:$effort,reply_mode:$reply_mode,conversation_mode:$conversation_mode,group_message_mode:$group_message_mode,respond_to_bots:$respond_to_bots}}}')"
  mark="$(audit_mark)"
  curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d "$payload" >"$out"
  jq -e '.ok == true and .card != null' "$out" >/dev/null
  wait_audit_since "$mark" '"Action":"config_saved"' 60
  assert_file_contains "$out" "model=\`$model\`"
  assert_file_contains "$out" "effort=\`$effort\`"
  assert_file_contains "$out" "reply mode=\`$reply_mode\`"
  assert_file_contains "$out" "conversation mode=\`$conversation_mode\`"
  assert_file_contains "$out" "group message mode=\`$group_message_mode\`"
  assert_file_contains "$out" "respond to bots=\`$respond_to_bots\`"
}

submit_invalid_config() {
  local case_name="$1"
  local session_id="$2"
  local out="$RUN_DIR/${case_name}-invalid.json"
  local payload mark
  wait_callback_ready
  payload="$(jq -nc --arg session "$session_id" \
    '{operator:{open_id:"e2e"},action:{value:{session:$session,action_id:"config.save"},form_value:{model:"not-allowed",effort:"extreme",reply_mode:"replace",conversation_mode:"invalid"}}}')"
  mark="$(audit_mark)"
  curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d "$payload" >"$out"
  jq -e '.ok == true and .card != null' "$out" >/dev/null
  wait_audit_since "$mark" '"Action":"config_save_failed"' 60
  assert_file_contains "$out" "偏好保存失败"
}

assert_persisted_config() {
  local model="$1"
  local effort="$2"
  local reply_mode="${3:-append}"
  local conversation_mode="${4:-chat}"
  jq -e --arg model "$model" --arg effort "$effort" --arg reply_mode "$reply_mode" --arg conversation_mode "$conversation_mode" \
    '.schema_version == 1 and .override.model == $model and .override.effort == $effort and .override.reply_mode == $reply_mode and .override.conversation_mode == $conversation_mode' "$PREFERENCE_STORE" >/dev/null
}

set_conversation_mode() {
  local case_name="$1"
  local mode="$2"
  local model="$CONFIG_DEFAULT_MODEL"
  local effort="$CONFIG_DEFAULT_EFFORT"
  local reply_mode="append"
  local config_msg
  if [[ -f "$PREFERENCE_STORE" ]] && jq -e '.override != null' "$PREFERENCE_STORE" >/dev/null 2>&1; then
    model="$(jq -r '.override.model' "$PREFERENCE_STORE")"
    effort="$(jq -r '.override.effort' "$PREFERENCE_STORE")"
    reply_mode="$(jq -r '.override.reply_mode // "append"' "$PREFERENCE_STORE")"
  fi
  config_msg="$(open_config "${case_name}_${mode}")"
  submit_config "$case_name" "config:message:${config_msg}" "$model" "$effort" "$reply_mode" "$mode"
}

set_group_message_mode() {
  local case_name="$1"
  local mode="$2"
  local respond_to_bots="${3:-false}"
  stop_server TERM
  rm -f "$PREFERENCE_STORE"
  SERVER_GROUP_MESSAGE_MODE="$mode"
  SERVER_RESPOND_TO_BOTS="$respond_to_bots"
  start_server_if_needed "$case_name"
}

audit_card_id_since() {
  local mark="$1"
  local message_id="$2"
  local line card_id
  line="$(audit_since "$mark" | jq -r --arg message_id "$message_id" \
    'select((.SessionID // "") | contains($message_id)) | select((.Action // "") | startswith("cardkit_")) | .Detail' | tail -n 1)"
  card_id="$(printf '%s\n' "$line" | sed -n 's/.*card_id=\([^ ]*\).*/\1/p')"
  if [[ -z "$card_id" ]]; then
    echo "no CardKit card id found for message $message_id after audit mark $mark" >&2
    return 1
  fi
  printf '%s\n' "$card_id"
}

audit_card_sequence_since() {
  local mark="$1"
  local message_id="$2"
  local line sequence
  line="$(audit_since "$mark" | jq -r --arg message_id "$message_id" \
    'select((.SessionID // "") | contains($message_id)) | select(.Action == "cardkit_update") | .Detail' | tail -n 1)"
  sequence="$(printf '%s\n' "$line" | sed -n 's/.*sequence=\([0-9][0-9]*\).*/\1/p')"
  if [[ -z "$sequence" ]]; then
    echo "no CardKit sequence found for message $message_id after audit mark $mark" >&2
    return 1
  fi
  printf '%s\n' "$sequence"
}

audit_card_reply_to() {
  local card_id="$1"
  local line reply_to
  line="$(jq -r --arg card_id "$card_id" \
    'select(.Action == "cardkit_create" or .Action == "cardkit_reply") | select((" " + (.Detail // "") + " ") | contains(" card_id=" + $card_id + " ")) | .Detail' \
    "$AUDIT" | sed -n '1p')"
  reply_to="$(printf '%s\n' "$line" | sed -n 's/.*reply_to=\([^ ]*\).*/\1/p')"
  if [[ -z "$reply_to" ]]; then
    echo "no reply target found for CardKit card $card_id" >&2
    return 1
  fi
  printf '%s\n' "$reply_to"
}

audit_reply_message_id() {
  local reply_to="$1"
  local line message_id
  if [[ ! -f "$AUDIT" ]]; then
    return 1
  fi
  line="$(jq -r --arg reply_to "$reply_to" \
    'select(.Action == "cardkit_reply") | select((" " + (.Detail // "") + " ") | contains(" reply_to=" + $reply_to + " ")) | .Detail' \
    "$AUDIT" | tail -n 1)"
  message_id="$(printf '%s\n' "$line" | sed -n 's/.* message_id=\([^ ]*\).*/\1/p')"
  if [[ -z "$message_id" ]]; then
    return 1
  fi
  printf '%s\n' "$message_id"
}

assert_no_card_create_since() {
  local mark="$1"
  local message_id="$2"
  if audit_since "$mark" | jq -e --arg message_id "$message_id" \
    'select(.Action == "cardkit_create") | select((.SessionID // "") | contains($message_id))' >/dev/null; then
    echo "latest-card unexpectedly created a new card for $message_id" >&2
    return 1
  fi
}

reply_topic_root() {
  send_text "E2E_${RUN_ID}_TOPIC_ROOT_$RANDOM"
}

reply_in_topic() {
  local root_message_id="$1"
  local text="$2"
  reply_thread "$root_message_id" "<at user_id=\"$BOT_OPEN_ID\"></at> $text"
}

assert_fake_config_argv_since() {
  local mark="$1"
  local marker="$2"
  local model="$3"
  local effort="$4"
  local line
  line="$(tail -n "+$((mark + 1))" "$FAKE_CLAUDE_LOG" | grep -F -- "$marker" | tail -n 1)"
  if [[ -z "$line" ]]; then
    echo "no fake Claude invocation found for config marker: $marker" >&2
    return 1
  fi
  if [[ "$model" == "default" ]]; then
    if printf '%s\n' "$line" | grep -E -- '(^| )--model( |$)' >/dev/null 2>&1; then
      echo "default model unexpectedly emitted --model: $line" >&2
      return 1
    fi
  elif ! printf '%s\n' "$line" | grep -F -- "--model $model" >/dev/null 2>&1; then
    echo "fake Claude argv missing model $model: $line" >&2
    return 1
  fi
  if [[ "$effort" == "default" ]]; then
    if printf '%s\n' "$line" | grep -E -- '(^| )--effort( |$)' >/dev/null 2>&1; then
      echo "default effort unexpectedly emitted --effort: $line" >&2
      return 1
    fi
  elif ! printf '%s\n' "$line" | grep -F -- "--effort $effort" >/dev/null 2>&1; then
    echo "fake Claude argv missing effort $effort: $line" >&2
    return 1
  fi
}
