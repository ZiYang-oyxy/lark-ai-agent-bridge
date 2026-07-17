#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SMOKE_CASES=(
  preflight
  new_basic
  streaming_card
  help
  status
  workdir_existing
  topic_reply_at
  topic_reply_without_at_negative
)

FULL_EXTRA_CASES=(
  message_revoke
  message_revoke_pending_workdir
  message_revoke_queued_input
)

MODE="smoke"
KEEP_SERVER_ON_FAIL=0
LIST_CASES=0
SELECTED_CASES=()
RUN_ID="$(date +%Y%m%d-%H%M%S)"
RUN_DIR="$ROOT/.cache/e2e/real-$RUN_ID"
DEFAULT_WORKDIR="${E2E_REAL_E2E_DEFAULT_WORKDIR:-/tmp/lark-agent-bridge-real-$RUN_ID}"
WAIT_TIMEOUT="${E2E_REAL_E2E_TIMEOUT_SEC:-420}"
USE_FAKE_CLAUDE="${E2E_REAL_E2E_FAKE_CLAUDE:-0}"
FAKE_BIN_DIR="$RUN_DIR/bin"
SERVER_PID=""
FAILURES=0
BOT_OPEN_ID=""

usage() {
  cat <<'USAGE'
Usage:
  scripts/e2e-real.sh [--mode smoke|full] [--case name ...] [--list-cases]

Options:
  --mode smoke|full           smoke runs core cases; full adds revoke cases.
  --case name                 run one case; repeat to run multiple cases.
  --list-cases                print supported cases and exit.
  --default-workdir path      default workdir passed to bridge serve.
  --run-dir path              evidence directory. Defaults to .cache/e2e/real-<timestamp>.
  --keep-server-on-fail       leave bridge running after a failure for diagnosis.
  -h, --help                  show this help.

Environment:
  .lark-agent-bridge/e2e.env is loaded automatically when present.
  Required for real execution: LARK_APP_ID, LARK_APP_SECRET, E2E_E2E_CHAT_ID.
  Optional: LARK_BOT_OPEN_ID, E2E_REAL_E2E_TIMEOUT_SEC, E2E_REAL_E2E_FAKE_CLAUDE=1.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)
      MODE="${2:-}"
      shift 2
      ;;
    --case)
      SELECTED_CASES+=("${2:-}")
      shift 2
      ;;
    --list-cases)
      LIST_CASES=1
      shift
      ;;
    --default-workdir)
      DEFAULT_WORKDIR="${2:-}"
      shift 2
      ;;
    --run-dir)
      RUN_DIR="${2:-}"
      shift 2
      ;;
    --keep-server-on-fail)
      KEEP_SERVER_ON_FAIL=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -f "$ROOT/.lark-agent-bridge/e2e.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$ROOT/.lark-agent-bridge/e2e.env"
  set +a
fi

case "$MODE" in
  smoke|full) ;;
  *)
    echo "invalid --mode: $MODE" >&2
    exit 2
    ;;
esac

all_cases() {
  printf '%s\n' "${SMOKE_CASES[@]}"
  printf '%s\n' "${FULL_EXTRA_CASES[@]}"
}

cases_for_mode() {
  if [[ "${#SELECTED_CASES[@]}" -gt 0 ]]; then
    printf '%s\n' "${SELECTED_CASES[@]}"
    return
  fi
  printf '%s\n' "${SMOKE_CASES[@]}"
  if [[ "$MODE" == "full" ]]; then
    printf '%s\n' "${FULL_EXTRA_CASES[@]}"
  fi
}

selected_cases_array() {
  RUN_CASES=()
  local case_name
  while IFS= read -r case_name; do
    RUN_CASES+=("$case_name")
  done < <(cases_for_mode)
}

if [[ "$LIST_CASES" -eq 1 ]]; then
  all_cases
  exit 0
fi

SUMMARY="$RUN_DIR/summary.md"
MESSAGES="$RUN_DIR/messages.jsonl"
AUDIT="$RUN_DIR/audit.jsonl"
MGET_DIR="$RUN_DIR/mget"
SERVER_LOG="$RUN_DIR/server.log"

mkdir -p "$RUN_DIR" "$MGET_DIR" "$ROOT/.cache/go-build"
: >"$MESSAGES"

export GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}"
export E2E_AUDIT_LOG="$AUDIT"

summary_init() {
  {
    echo "# Feishu Real E2E"
    echo
    echo "- run_id: $RUN_ID"
    echo "- generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "- repo: $ROOT"
    echo "- mode: $MODE"
    echo "- run_dir: $RUN_DIR"
    echo "- audit: $AUDIT"
    echo "- default_workdir: $DEFAULT_WORKDIR"
    echo "- fake_claude: $USE_FAKE_CLAUDE"
    echo "- secrets: not printed"
    echo
  } >"$SUMMARY"
}

summary() {
  printf '%s\n' "$*" >>"$SUMMARY"
}

validate_case() {
  local name="$1"
  if ! all_cases | grep -Fx "$name" >/dev/null 2>&1; then
    echo "unknown case: $name" >&2
    exit 2
  fi
}

validate_selected_cases() {
  for case_name in "${RUN_CASES[@]}"; do
    [[ -n "$case_name" ]] || continue
    validate_case "$case_name"
  done
}

log() {
  printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing command: $1" >&2
    exit 1
  fi
}

require_env() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "missing required env: $name" >&2
    exit 1
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

prepare_fake_claude_if_needed() {
  if [[ "$USE_FAKE_CLAUDE" != "1" ]]; then
    return
  fi
  mkdir -p "$FAKE_BIN_DIR"
  cat >"$FAKE_BIN_DIR/claude" <<'EOF'
#!/usr/bin/env sh
printf '%s\n' '{"type":"assistant","message":{"model":"fake-claude-e2e","content":[{"type":"text","text":"FAKE_E2E_STARTED"}],"usage":{"output_tokens":1},"session_id":"fake-e2e-session"}}'
sleep 300
EOF
  chmod +x "$FAKE_BIN_DIR/claude"
}

start_server_if_needed() {
  if [[ "$1" == "preflight" ]]; then
    return
  fi
  if [[ -n "$SERVER_PID" ]]; then
    return
  fi
  mkdir -p "$DEFAULT_WORKDIR"
  prepare_fake_claude_if_needed
  log "starting bridge serve"
  PATH="$FAKE_BIN_DIR:$PATH" E2E_AUDIT_LOG="$AUDIT" GOCACHE="$GOCACHE" go run ./cmd/lark-agent-bridge serve --default-workdir "$DEFAULT_WORKDIR" >"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  summary "- bridge_pid: $SERVER_PID"
  sleep 5
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    echo "bridge exited during startup; see $SERVER_LOG" >&2
    cat "$SERVER_LOG" >&2 || true
    exit 1
  fi
}

cleanup() {
  local status=$?
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    if [[ "$status" -ne 0 && "$KEEP_SERVER_ON_FAIL" -eq 1 ]]; then
      echo "keeping bridge process $SERVER_PID for diagnosis"
    else
      kill "$SERVER_PID" >/dev/null 2>&1 || true
      wait "$SERVER_PID" >/dev/null 2>&1 || true
    fi
  fi
}
trap cleanup EXIT

send_at() {
  local text="$1"
  local msg_id
  msg_id="$(lark-cli im +messages-send --as user --chat-id "$E2E_E2E_CHAT_ID" --text "<at user_id=\"$BOT_OPEN_ID\"></at> $text" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to send message: $text" >&2
    exit 1
  fi
  printf '%s\n' "$msg_id"
}

reply_thread() {
  local root_msg="$1"
  local text="$2"
  local msg_id
  msg_id="$(lark-cli im +messages-reply --as user --message-id "$root_msg" --reply-in-thread --text "$text" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
  if [[ -z "$msg_id" || "$msg_id" == "null" ]]; then
    echo "failed to reply thread: $root_msg" >&2
    exit 1
  fi
  printf '%s\n' "$msg_id"
}

mget() {
  local case_name="$1"
  local msg_id="$2"
  local out="$MGET_DIR/$case_name-$msg_id.json"
  lark-cli im +messages-mget --as user --message-ids "$msg_id" --format json >"$out"
  printf '%s\n' "$out"
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

revoke_message() {
  local msg_id="$1"
  local out="$RUN_DIR/revoke-$msg_id.json"
  lark-cli im messages delete --as user --yes --params "{\"message_id\":\"$msg_id\"}" >"$out"
}

case_preflight() {
  ./scripts/e2e-preflight.sh >"$RUN_DIR/preflight.log" 2>&1
  summary "- preflight: passed"
}

case_new_basic() {
  local marker="E2E_${RUN_ID}_NEW_BASIC"
  local msg file
  msg="$(send_at "/new 请只回复 ${marker}，不要调用工具。")"
  wait_audit "$msg.*event=result"
  file="$(mget new_basic "$msg")"
  assert_file_contains "$file" "$marker"
  assert_file_contains "$file" "[已完成 ✗]"
  record_message new_basic root "$msg" "$file"
}

case_streaming_card() {
  local marker="E2E_${RUN_ID}_STREAM"
  local msg file
  msg="$(send_at "/new 请使用 Bash 工具执行 sleep 3; echo ${marker}，然后只回复 ${marker}。")"
  wait_audit "$msg.*event=stream sequence="
  wait_audit "$msg.*event=result"
  file="$(mget streaming_card "$msg")"
  assert_file_contains "$file" "$marker"
  record_message streaming_card root "$msg" "$file"
}

case_help() {
  local msg file
  msg="$(send_at "/help")"
  wait_audit "reply_to=$msg event=message"
  file="$(mget help "$msg")"
  assert_file_contains "$file" "/new [--workdir <path>] [prompt]"
  record_message help root "$msg" "$file"
}

case_status() {
  local msg file
  msg="$(send_at "/status")"
  wait_audit "reply_to=$msg event=message"
  file="$(mget status "$msg")"
  assert_file_contains "$file" "mode=claude_oneshot"
  assert_file_contains "$file" "default_workdir=$DEFAULT_WORKDIR"
  record_message status root "$msg" "$file"
}

case_workdir_existing() {
  local dir="$RUN_DIR/workdir-existing"
  local marker="PWD_RESULT:$dir"
  local msg file
  mkdir -p "$dir"
  msg="$(send_at "/new --workdir $dir 请使用 Bash(pwd) 查看当前目录，然后只回复 PWD_RESULT:<pwd输出>。")"
  wait_audit "$msg.*event=result"
  file="$(mget workdir_existing "$msg")"
  assert_file_contains "$file" "$marker"
  assert_file_contains "$file" "$dir"
  record_message workdir_existing root "$msg" "$file"
}

case_topic_reply_at() {
  local root_marker="E2E_${RUN_ID}_TOPIC_ROOT"
  local reply_marker="E2E_${RUN_ID}_TOPIC_AT"
  local root reply file
  root="$(send_at "/new 请只回复 ${root_marker}，不要调用工具。")"
  wait_audit "$root.*event=result"
  reply="$(reply_thread "$root" "<at user_id=\"${BOT_OPEN_ID}\"></at> 请只回复 ${reply_marker}，不要调用工具。")"
  wait_audit "$reply.*event=result"
  file="$(mget topic_reply_at "$reply")"
  assert_file_contains "$file" "$reply_marker"
  record_message topic_reply_at root "$root"
  record_message topic_reply_at reply "$reply" "$file"
}

case_topic_reply_without_at_negative() {
  local root_marker="E2E_${RUN_ID}_TOPIC_NEG_ROOT"
  local neg_marker="E2E_${RUN_ID}_TOPIC_NEG"
  local root reply
  root="$(send_at "/new 请只回复 ${root_marker}，不要调用工具。")"
  wait_audit "$root.*event=result"
  reply="$(reply_thread "$root" "请只回复 ${neg_marker}，不要调用工具。")"
  sleep 8
  if grep -F "$reply" "$AUDIT" >/dev/null 2>&1; then
    echo "negative topic reply unexpectedly reached bridge: $reply" >&2
    return 1
  fi
  record_message topic_reply_without_at_negative root "$root"
  record_message topic_reply_without_at_negative reply_without_at "$reply"
}

case_message_revoke() {
  local marker="E2E_${RUN_ID}_REVOKE_SHOULD_CANCEL"
  local after="E2E_${RUN_ID}_AFTER_REVOKE"
  local msg follow
  msg="$(send_at "/new ${marker}")"
  wait_audit "$msg.*event=stream" 60
  revoke_message "$msg"
  wait_audit "message_recalled_active_cancelled.*$msg" 60
  wait_audit "$msg.*event=stopped" 60
  follow="$(send_at "/new ${after}")"
  wait_audit "$follow.*event=stream" 60
  if grep -F "$after" "$AUDIT" | grep -F "queue_input" >/dev/null 2>&1; then
    echo "follow-up after revoke was queued" >&2
    return 1
  fi
  revoke_message "$follow"
  wait_audit "message_recalled_active_cancelled.*$follow" 60
  record_message message_revoke revoked "$msg"
  record_message message_revoke followup "$follow"
}

case_message_revoke_pending_workdir() {
  local dir="$RUN_DIR/revoke-pending-workdir"
  local marker="E2E_${RUN_ID}_REVOKE_PENDING"
  local msg file
  msg="$(send_at "/new --workdir ${dir} ${marker}")"
  wait_audit "$msg.*event=workdir_confirm" 60
  revoke_message "$msg"
  wait_audit "message_recalled_pending_cancelled.*$msg" 60
  wait_audit "$msg.*event=workdir_cancelled" 60
  [[ ! -e "$dir" ]] || { echo "workdir should not be created after revoke: $dir" >&2; return 1; }
  if grep -F "$marker" "$AUDIT" | grep -F "run_input" >/dev/null 2>&1; then
    echo "revoked pending workdir unexpectedly started Claude run" >&2
    return 1
  fi
  file="$(mget message_revoke_pending_workdir "$msg")"
  assert_file_contains "$file" "[Cancel ✗]"
  record_message message_revoke_pending_workdir revoked "$msg" "$file"
}

case_message_revoke_queued_input() {
  local first_marker="E2E_${RUN_ID}_QUEUE_FIRST"
  local second_marker="E2E_${RUN_ID}_QUEUE_SECOND"
  local follow_marker="E2E_${RUN_ID}_QUEUE_FOLLOW"
  local first second follow
  first="$(send_at "/new ${first_marker}")"
  wait_audit "$first.*event=stream" 60
  second="$(send_at "${second_marker}")"
  wait_audit "queue_input.*${second_marker}" 60
  revoke_message "$second"
  wait_audit "message_recalled_queued_cancelled.*${second_marker}" 60
  revoke_message "$first"
  wait_audit "message_recalled_active_cancelled.*$first" 60
  follow="$(send_at "/new ${follow_marker}")"
  wait_audit "$follow_marker" 30
  if grep -F "$second_marker" "$AUDIT" | grep -F "dequeue_input" >/dev/null 2>&1; then
    echo "revoked queued input was dequeued" >&2
    return 1
  fi
  if grep -F "$follow_marker" "$AUDIT" | grep -F "queue_input" >/dev/null 2>&1; then
    echo "follow-up after queued revoke was queued" >&2
    return 1
  fi
  record_message message_revoke_queued_input active "$first"
  record_message message_revoke_queued_input queued_revoked "$second"
  record_message message_revoke_queued_input followup "$follow"
}

run_case() {
  local name="$1"
  start_server_if_needed "$name"
  log "case $name start"
  summary "## $name"
  summary
  local start status
  start="$(date +%s)"
  set +e
  ( set -e; "case_$name" ) >"$RUN_DIR/$name.log" 2>&1
  status=$?
  set -e
  local elapsed=$(( $(date +%s) - start ))
  if [[ "$status" -eq 0 ]]; then
    log "case $name passed"
    summary "- status: passed"
  else
    log "case $name failed; see $RUN_DIR/$name.log"
    summary "- status: failed"
    FAILURES=$((FAILURES + 1))
  fi
  summary "- elapsed_sec: $elapsed"
  summary "- log: $RUN_DIR/$name.log"
  summary
}

RUN_CASES=()
selected_cases_array
summary_init
validate_selected_cases
require_cmd jq
require_cmd go
require_cmd lark-cli
require_cmd curl
require_env LARK_APP_ID
require_env LARK_APP_SECRET
require_env E2E_E2E_CHAT_ID
fetch_bot_open_id
summary "- bot_open_id: ${BOT_OPEN_ID:0:6}...${BOT_OPEN_ID: -4}"
summary

for case_name in "${RUN_CASES[@]}"; do
  [[ -n "$case_name" ]] || continue
  run_case "$case_name"
done

summary "## Summary"
summary
summary "- failures: $FAILURES"
summary "- messages: $MESSAGES"
summary "- server_log: $SERVER_LOG"

echo "$SUMMARY"
if [[ "$FAILURES" -ne 0 ]]; then
  exit 1
fi
