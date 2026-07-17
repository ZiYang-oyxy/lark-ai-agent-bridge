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
  session_restart_context
  restart_queued_cancel
  restart_running_interrupted
  debounce_dm
  debounce_group
  busy_merge
  queue_full
  scope_parallel
  stop_preserves_queue
  recall_state
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
SESSION_STORE="$RUN_DIR/sessions.json"
FAKE_CLAUDE_LOG="$RUN_DIR/fake-claude.log"
CALLBACK_ADDR="${E2E_REAL_E2E_CALLBACK_ADDR:-127.0.0.1:$((20000 + RANDOM % 20000))}"
SERVER_QUEUE_MAX_PENDING=""

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
    echo "- callback_addr: $CALLBACK_ADDR"
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
set -eu
args="$(printf '%s' "$*" | tr '\n' ' ')"
printf 'pid=%s args=%s\n' "$$" "$args" >>"${FAKE_CLAUDE_LOG:?FAKE_CLAUDE_LOG is required}"
printf '%s\n' '{"type":"result","result":"FAKE_E2E_STARTED","model":"fake-claude-e2e","usage":{"output_tokens":1},"session_id":"fake-e2e-session"}'
case "$args" in
  *E2E_BLOCK*) sleep 300 ;;
esac
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
  local -a server_env
  server_env=("PATH=$FAKE_BIN_DIR:$PATH" "E2E_AUDIT_LOG=$AUDIT" "E2E_SESSION_STORE=$SESSION_STORE" "E2E_CALLBACK_ADDR=$CALLBACK_ADDR" "GOCACHE=$GOCACHE")
  if [[ "$USE_FAKE_CLAUDE" == "1" ]]; then
    server_env+=("E2E_CLAUDE_BIN=$FAKE_BIN_DIR/claude" "FAKE_CLAUDE_LOG=$FAKE_CLAUDE_LOG")
  fi
  if [[ -n "$SERVER_QUEUE_MAX_PENDING" ]]; then
    server_env+=("E2E_QUEUE_MAX_PENDING=$SERVER_QUEUE_MAX_PENDING")
  fi
  env "${server_env[@]}" go run ./cmd/lark-agent-bridge serve --default-workdir "$DEFAULT_WORKDIR" >>"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  summary "- bridge_pid: $SERVER_PID"
  sleep 5
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    echo "bridge exited during startup; see $SERVER_LOG" >&2
    cat "$SERVER_LOG" >&2 || true
    exit 1
  fi
}

stop_server() {
  local signal="${1:-TERM}"
  if [[ -z "${SERVER_PID:-}" ]]; then
    return
  fi
  if kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    kill "-$signal" "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" >/dev/null 2>&1 || true
  fi
  SERVER_PID=""
}

restart_server() {
  local signal="${1:-TERM}"
  stop_server "$signal"
  start_server_if_needed restart
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    echo "bridge did not survive restart; see $SERVER_LOG" >&2
    return 1
  fi
}

cleanup() {
  local status=$?
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    if [[ "$status" -ne 0 && "$KEEP_SERVER_ON_FAIL" -eq 1 ]]; then
      echo "keeping bridge process $SERVER_PID for diagnosis"
    else
      stop_server TERM
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

send_text() {
  local text="$1"
  local msg_id
  msg_id="$(lark-cli im +messages-send --as user --chat-id "$E2E_E2E_CHAT_ID" --text "$text" --jq '.data.message_id // .message_id // .data.message_id' | tail -n 1)"
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
  local first="$1"
  local second="$2"
  if ! awk -v first="$first" -v second="$second" '
    index($0, first) { seen_first = 1 }
    index($0, second) { seen_second = 1 }
    seen_first && seen_second { found = 1; exit }
    END { exit found ? 0 : 1 }
  ' "$FAKE_CLAUDE_LOG"; then
    echo "no fake Claude invocation contained both batch markers: $first, $second" >&2
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
  lark-cli im messages delete --as user --yes --params "{\"message_id\":\"$msg_id\"}" >"$out"
}

stop_card() {
  local session_id="$1"
  local out="$RUN_DIR/stop-${session_id//[^A-Za-z0-9._-]/_}.json"
  local payload
  payload="$(jq -nc --arg session "$session_id" '{operator:{open_id:"e2e"},action:{value:{session:$session,action_id:"stop"}}}')"
  curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d "$payload" >"$out"
  jq -e '.ok == true and .card != null' "$out" >/dev/null
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

case_session_restart_context() {
  require_fake_claude
  local first_marker="E2E_${RUN_ID}_RESTART_CONTEXT_FIRST"
  local second_marker="E2E_${RUN_ID}_RESTART_CONTEXT_SECOND"
  local first second file
  first="$(send_at "/new ${first_marker}")"
  wait_audit "$first.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$first_marker" 60
  restart_server TERM
  second="$(send_at "$second_marker")"
  wait_audit "$second.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$second_marker" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "--resume fake-e2e-session" 60
  file="$(mget session_restart_context "$second")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message session_restart_context first "$first"
  record_message session_restart_context resumed "$second" "$file"
}

case_restart_queued_cancel() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_RESTART_QUEUE_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_RESTART_QUEUE_CANCEL"
  local follow_marker="E2E_${RUN_ID}_RESTART_QUEUE_FOLLOW"
  local active queued follow file before
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  queued="$(send_at "$queued_marker")"
  wait_audit "queue_input.*position=" 60
  before="$(fake_marker_count "$queued_marker")"
  restart_server KILL
  wait_audit "session_recovery_cancelled" 60
  sleep 2
  assert_fake_marker_not_started_after "$queued_marker" "$before"
  follow="$(send_at "/new ${follow_marker}")"
  wait_audit "$follow.*event=result" 60
  file="$(mget restart_queued_cancel "$follow")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message restart_queued_cancel active "$active"
  record_message restart_queued_cancel cleared_queue "$queued"
  record_message restart_queued_cancel followup "$follow" "$file"
}

case_restart_running_interrupted() {
  require_fake_claude
  local running_marker="E2E_${RUN_ID}_RESTART_RUNNING_E2E_BLOCK"
  local follow_marker="E2E_${RUN_ID}_RESTART_RUNNING_FOLLOW"
  local running follow file before
  running="$(send_at "/new ${running_marker}")"
  wait_audit "$running.*event=stream" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$running_marker" 60
  before="$(fake_marker_count "$running_marker")"
  restart_server KILL
  wait_audit "session_recovery_interrupted" 60
  sleep 2
  assert_fake_marker_not_started_after "$running_marker" "$before"
  follow="$(send_at "/new ${follow_marker}")"
  wait_audit "$follow.*event=result" 60
  file="$(mget restart_running_interrupted "$follow")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message restart_running_interrupted interrupted "$running"
  record_message restart_running_interrupted followup "$follow" "$file"
}

case_debounce_dm() {
  require_fake_claude
  # The available real account is group-only. Keep the 250 ms DM rule covered
  # locally, then prove the same batch lifecycle through a real CardKit run.
  go test ./internal/bridge -run '^TestDebounceForMessage$'
  local first_marker="E2E_${RUN_ID}_DM_DEBOUNCE_ONE"
  local second_marker="E2E_${RUN_ID}_DM_DEBOUNCE_TWO"
  local first second file
  first="$(send_at "/new ${first_marker}")"
  second="$(send_at "$second_marker")"
  wait_audit "$second.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$second_marker" 60
  assert_fake_batch_contains "$first_marker" "$second_marker"
  file="$(mget debounce_dm "$second")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message debounce_dm first "$first"
  record_message debounce_dm second "$second" "$file"
}

case_debounce_group() {
  require_fake_claude
  local first_marker="E2E_${RUN_ID}_GROUP_DEBOUNCE_ONE"
  local second_marker="E2E_${RUN_ID}_GROUP_DEBOUNCE_TWO"
  local first second file
  first="$(send_at "/new ${first_marker}")"
  second="$(send_at "$second_marker")"
  wait_audit "$second.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$second_marker" 60
  assert_fake_batch_contains "$first_marker" "$second_marker"
  file="$(mget debounce_group "$second")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message debounce_group first "$first"
  record_message debounce_group second "$second" "$file"
}

case_busy_merge() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_BUSY_ACTIVE_E2E_BLOCK"
  local first_marker="E2E_${RUN_ID}_BUSY_MERGE_ONE"
  local second_marker="E2E_${RUN_ID}_BUSY_MERGE_TWO"
  local active first second file
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  first="$(send_at "$first_marker")"
  second="$(send_at "$second_marker")"
  wait_file_contains "$FAKE_CLAUDE_LOG" "$active_marker" 60
  revoke_message "$active"
  wait_audit "message_recalled_active_cancelled.*$active" 60
  wait_audit "$second.*event=result" 60
  assert_fake_batch_contains "$first_marker" "$second_marker"
  file="$(mget busy_merge "$second")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message busy_merge active "$active"
  record_message busy_merge first "$first"
  record_message busy_merge merged "$second" "$file"
}

case_queue_full() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_QUEUE_FULL_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_QUEUE_FULL_QUEUED"
  local rejected_marker="E2E_${RUN_ID}_QUEUE_FULL_REJECTED"
  local active queued rejected file
  SERVER_QUEUE_MAX_PENDING=2
  restart_server TERM
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  queued="$(send_at "$queued_marker")"
  wait_file_contains "$FAKE_CLAUDE_LOG" "$active_marker" 60
  rejected="$(send_at "$rejected_marker")"
  wait_audit "queue_rejected" 60
  file="$(mget queue_full "$rejected")"
  assert_file_contains "$file" "队列已满，未执行。"
  if grep -F -- "$rejected_marker" "$FAKE_CLAUDE_LOG" >/dev/null 2>&1; then
    echo "queue-full input unexpectedly started a fake Claude process" >&2
    return 1
  fi
  revoke_message "$active"
  wait_audit "message_recalled_active_cancelled.*$active" 60
  wait_audit "$queued.*event=result" 60
  SERVER_QUEUE_MAX_PENDING=""
  restart_server TERM
  record_message queue_full active "$active"
  record_message queue_full queued "$queued"
  record_message queue_full rejected "$rejected" "$file"
}

case_scope_parallel() {
  require_fake_claude
  local root_one root_two one two
  local one_marker="E2E_${RUN_ID}_SCOPE_ONE_E2E_BLOCK"
  local two_marker="E2E_${RUN_ID}_SCOPE_TWO_E2E_BLOCK"
  root_one="$(send_text "E2E_${RUN_ID}_SCOPE_ROOT_ONE")"
  root_two="$(send_text "E2E_${RUN_ID}_SCOPE_ROOT_TWO")"
  one="$(reply_thread "$root_one" "<at user_id=\"${BOT_OPEN_ID}\"></at> /new ${one_marker}")"
  two="$(reply_thread "$root_two" "<at user_id=\"${BOT_OPEN_ID}\"></at> /new ${two_marker}")"
  wait_audit "$one.*event=stream" 60
  wait_audit "$two.*event=stream" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$one_marker" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$two_marker" 60
  kill -0 "$SERVER_PID" >/dev/null 2>&1
  revoke_message "$one"
  revoke_message "$two"
  wait_audit "message_recalled_active_cancelled.*$one" 60
  wait_audit "message_recalled_active_cancelled.*$two" 60
  record_message scope_parallel root_one "$root_one"
  record_message scope_parallel root_two "$root_two"
  record_message scope_parallel scope_one "$one"
  record_message scope_parallel scope_two "$two"
}

case_stop_preserves_queue() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_STOP_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_STOP_QUEUE_PRESERVED"
  local active queued active_file queued_file
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  queued="$(send_at "$queued_marker")"
  wait_audit "queue_input" 60
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit "batch_stop_requested" 60
  wait_audit "$queued.*event=result" 60
  queued_file="$(mget stop_preserves_queue "$queued")"
  assert_file_contains "$queued_file" "FAKE_E2E_STARTED"
  wait_file_contains "$FAKE_CLAUDE_LOG" "$queued_marker" 60
  active_file="$(mget stop_preserves_queue "$active")"
  assert_file_contains "$active_file" "已停止"
  record_message stop_preserves_queue stopped_active "$active" "$active_file"
  record_message stop_preserves_queue preserved_queue "$queued" "$queued_file"
}

case_recall_state() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_RECALL_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_RECALL_QUEUED_CANCEL"
  local active queued file
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  queued="$(send_at "$queued_marker")"
  wait_audit "queue_input" 60
  revoke_message "$queued"
  wait_audit "message_recalled_queued_cancelled.*$queued" 60
  revoke_message "$active"
  wait_audit "message_recalled_active_cancelled.*$active" 60
  if grep -F "$queued_marker" "$AUDIT" | grep -F "event=result" >/dev/null 2>&1; then
    echo "recalled queued input unexpectedly produced a result card" >&2
    return 1
  fi
  file="$(mget recall_state "$active")"
  assert_file_contains "$file" "已停止"
  record_message recall_state active_cancelled "$active" "$file"
  record_message recall_state queued_cancelled "$queued"
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
