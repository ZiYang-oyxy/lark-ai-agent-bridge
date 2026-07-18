#!/usr/bin/env bash
# Dynamic case dispatch intentionally invokes case_* helpers by constructed name.
# shellcheck disable=SC2329
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_ROOT="${E2E_STATE_ROOT:-$ROOT}"
cd "$ROOT"
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/e2e-profile.sh"
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/e2e-capabilities.sh"

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
  media_attachment_only
  media_images
  media_text_files
  media_partial
  media_rejected
  config_roundtrip
  config_reset
  config_frozen_queue
  requested_actual_model
  wrapper_preflight
)

MODE="smoke"
KEEP_SERVER_ON_FAIL=0
LIST_CASES=0
PROFILE_ARG=""
PROFILE_NAME=""
PROFILE_ENV=""
LARK_CLI_PROFILE=""
DOCTOR_MODE=0
PREFLIGHT_ONLY=0
STRICT_CAPABILITIES=0
SELECTED_CASES=()
RUN_ID="$(date +%Y%m%d-%H%M%S)"
RUN_DIR=""
RUN_DIR_SET=0
DEFAULT_WORKDIR="${E2E_REAL_E2E_DEFAULT_WORKDIR:-/tmp/lark-agent-bridge-real-$RUN_ID}"
WAIT_TIMEOUT="${E2E_REAL_E2E_TIMEOUT_SEC:-420}"
USE_FAKE_CLAUDE="${E2E_REAL_E2E_FAKE_CLAUDE:-0}"
MEDIA_P2P_CHAT_ID="${E2E_REAL_E2E_P2P_CHAT_ID:-}"
CONFIG_CUSTOM_MODEL="${E2E_REAL_E2E_CUSTOM_MODEL:-claude-e2e-custom}"
CONFIG_DEFAULT_MODEL="${E2E_MODEL:-default}"
CONFIG_DEFAULT_EFFORT="${E2E_EFFORT:-low}"
FAKE_BIN_DIR=""
SERVER_PID=""
FAILURES=0
BLOCKED_CASES=0
BOT_OPEN_ID=""

usage() {
  cat <<'USAGE'
Usage:
  scripts/e2e-real.sh [--profile name] [--doctor|--preflight-only]
                      [--mode smoke|full] [--case name ...] [--list-cases]

Options:
  --profile name              use one developer-local E2E profile.
  --doctor                    run static capability checks without sending messages.
  --preflight-only            run static checks and real active canaries, then exit.
  --strict-capabilities       return 3 when a required capability is blocked.
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
  Media file cases also require E2E_REAL_E2E_P2P_CHAT_ID for the user's direct chat with this bot.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)
      PROFILE_ARG="${2:-}"
      shift 2
      ;;
    --doctor)
      DOCTOR_MODE=1
      shift
      ;;
    --preflight-only)
      PREFLIGHT_ONLY=1
      shift
      ;;
    --strict-capabilities)
      STRICT_CAPABILITIES=1
      shift
      ;;
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
      RUN_DIR_SET=1
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

if [[ "$DOCTOR_MODE" -eq 1 && "$PREFLIGHT_ONLY" -eq 1 ]]; then
  echo "--doctor and --preflight-only are mutually exclusive" >&2
  exit 2
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

profile_selection="$(e2e_profile_select "$STATE_ROOT" "$PROFILE_ARG")" || exit 2
IFS=$'\t' read -r PROFILE_NAME PROFILE_ENV _ <<<"$profile_selection"
if [[ "$PROFILE_NAME" == "legacy" ]]; then
  e2e_profile_load_legacy "$PROFILE_ENV" || exit 2
  echo "notice: legacy .lark-agent-bridge/e2e.env is in use; run e2e-init.sh to create a named profile" >&2
else
  e2e_profile_load "$PROFILE_ENV" || exit 2
  LARK_CLI_PROFILE="${E2E_E2E_LARK_CLI_PROFILE:-lab-e2e-$PROFILE_NAME}"
fi
if [[ "$RUN_DIR_SET" -eq 0 ]]; then
  RUN_DIR="$STATE_ROOT/.cache/e2e/$PROFILE_NAME/real-$RUN_ID"
fi
WAIT_TIMEOUT="${E2E_REAL_E2E_TIMEOUT_SEC:-$WAIT_TIMEOUT}"
USE_FAKE_CLAUDE="${E2E_REAL_E2E_FAKE_CLAUDE:-$USE_FAKE_CLAUDE}"
MEDIA_P2P_CHAT_ID="${E2E_REAL_E2E_P2P_CHAT_ID:-}"

SUMMARY="$RUN_DIR/summary.md"
CAPABILITIES_JSON="$RUN_DIR/capabilities.json"
PROFILE_CAPABILITY_CACHE="$STATE_ROOT/.lark-agent-bridge/e2e/profiles/$PROFILE_NAME.capabilities.json"
MESSAGES="$RUN_DIR/messages.jsonl"
AUDIT="$RUN_DIR/audit.jsonl"
MGET_DIR="$RUN_DIR/mget"
SERVER_LOG="$RUN_DIR/server.log"
STATE_DIR="$RUN_DIR/state"
SESSION_STORE="$STATE_DIR/sessions.json"
PREFERENCE_STORE="$STATE_DIR/preferences.json"
FAKE_CLAUDE_LOG="$RUN_DIR/fake-claude.log"
MEDIA_CACHE_DIR="$RUN_DIR/media-cache"
MEDIA_FIXTURE_DIR="$RUN_DIR/media-fixtures"
FAKE_BIN_DIR="$RUN_DIR/bin"
SERVER_BIN="$RUN_DIR/lark-agent-bridge-e2e"
SERVER_PID_FILE="$RUN_DIR/server.pid"
RUN_TOKEN="${RUN_ID}-$RANDOM-$$"
CALLBACK_ADDR="${E2E_REAL_E2E_CALLBACK_ADDR:-127.0.0.1:$((20000 + RANDOM % 20000))}"
SERVER_QUEUE_MAX_PENDING=""

mkdir -p "$RUN_DIR" "$MGET_DIR" "$STATE_DIR" "$ROOT/.cache/go-build"
chmod 700 "$STATE_DIR"
if [[ -e "$SERVER_PID_FILE" ]]; then
  echo "run directory already contains server state; choose a new --run-dir: $SERVER_PID_FILE" >&2
  exit 1
fi
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

lark_cli() {
  if [[ -n "$LARK_CLI_PROFILE" ]]; then
    command lark-cli --profile "$LARK_CLI_PROFILE" "$@"
  else
    command lark-cli "$@"
  fi
}

require_env() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "missing required env: $name" >&2
    exit 1
  fi
}

record_static_credentials() {
  if [[ -n "${LARK_APP_ID:-}" && -n "${LARK_APP_SECRET:-}" ]]; then
    e2e_cap_record credentials PASS ready "bridge app credentials are configured" ""
  else
    e2e_cap_record credentials BLOCKED credentials_missing "bridge app credentials are incomplete" "rerun e2e-init.sh for this profile"
  fi
}

record_static_user_auth() {
  local response auth_app scope_response scope scope_argument missing_scopes=() login_command
  scope_argument="$(e2e_user_auth_scope_argument)"
  login_command="lark-cli auth login --scope '$scope_argument'"
  if [[ -n "$LARK_CLI_PROFILE" ]]; then
    login_command="lark-cli --profile $LARK_CLI_PROFILE auth login --scope '$scope_argument'"
  fi
  if ! response="$(lark_cli auth status --json --verify 2>/dev/null)"; then
    e2e_cap_record lark_cli_auth BLOCKED lark_cli_auth_missing "lark-cli user authentication is unavailable" "run $login_command"
    e2e_cap_record oauth_same_app SKIPPED auth_unavailable "OAuth app identity was not checked" "restore lark-cli authentication"
    return
  fi
  if ! printf '%s' "$response" | jq -e . >/dev/null 2>&1; then
    e2e_cap_record lark_cli_auth FAIL auth_response_invalid "lark-cli returned invalid auth JSON" "inspect lark-cli auth status --json --verify"
    e2e_cap_record oauth_same_app SKIPPED auth_invalid "OAuth app identity was not checked" "repair lark-cli"
    return
  fi
  auth_app="$(printf '%s' "$response" | jq -r '.appId // .app_id // .data.app_id // .auth.app_id // empty')"
  if [[ -z "$auth_app" ]]; then
    e2e_cap_record oauth_same_app BLOCKED oauth_app_unverifiable "lark-cli did not expose its OAuth app identity" "run active DM preflight to prove app compatibility"
  elif [[ "$auth_app" == "${LARK_APP_ID:-}" ]]; then
    e2e_cap_record oauth_same_app PASS ready "user OAuth and bridge app identities match" ""
  else
    e2e_cap_record oauth_same_app BLOCKED oauth_app_mismatch "user OAuth belongs to another app" "run $login_command"
  fi
  while IFS= read -r scope; do
    if scope_response="$(lark_cli auth check --scope "$scope" --json 2>/dev/null)" && \
      printf '%s' "$scope_response" | jq -e --arg scope "$scope" \
        '.ok == true and ((.granted == true) or ((.granted | type) == "array" and (.granted | index($scope) != null)))' >/dev/null 2>&1; then
      continue
    fi
    if printf '%s' "${scope_response:-}" | jq -e --arg scope "$scope" \
      '(.missing | type) == "array" and (.missing | index($scope) != null)' >/dev/null 2>&1; then
      missing_scopes+=("$scope")
      continue
    fi
    e2e_cap_record lark_cli_auth FAIL user_e2e_scope_check_failed "lark-cli could not verify $scope" "inspect lark-cli auth check output"
    return
  done < <(e2e_user_auth_scopes)
  if (( ${#missing_scopes[@]} > 0 )); then
    e2e_cap_record lark_cli_auth BLOCKED user_e2e_scopes_missing "user OAuth is missing E2E observation or control scopes" "enable and publish the E2E scope bundle, then run $login_command"
    return
  fi
  e2e_cap_record lark_cli_auth PASS ready "lark-cli user authentication covers E2E send, observation and control" ""
}

record_static_bot() {
  if [[ -n "${LARK_BOT_OPEN_ID:-}" ]]; then
    BOT_OPEN_ID="$LARK_BOT_OPEN_ID"
    e2e_cap_record bot_identity PASS ready "bot identity is configured" ""
  else
    e2e_cap_record bot_identity BLOCKED bot_not_initialized "bot identity is missing from the profile" "rerun e2e-init.sh"
  fi
}

record_static_chats() {
  if [[ -z "${E2E_E2E_CHAT_ID:-}" ]]; then
    e2e_cap_record test_group BLOCKED group_missing "test group is not configured" "rerun e2e-init.sh with a test group"
  elif lark_cli im chats get --as user --chat-id "$E2E_E2E_CHAT_ID" --json >/dev/null 2>&1; then
    e2e_cap_record test_group PASS ready "test group is readable by the current user" ""
  else
    e2e_cap_record test_group BLOCKED group_unavailable "test group is not readable by the current user" "check membership and user OAuth"
  fi

  if [[ -z "${E2E_REAL_E2E_P2P_CHAT_ID:-}" ]]; then
    e2e_cap_record p2p_chat BLOCKED p2p_not_found "P2P chat is not configured" "start a direct chat with the bot and rerun e2e-init.sh"
  elif lark_cli im chats get --as user --chat-id "$E2E_REAL_E2E_P2P_CHAT_ID" --json >/dev/null 2>&1; then
    e2e_cap_record p2p_chat PASS ready "P2P chat is readable by the current user" ""
  else
    e2e_cap_record p2p_chat BLOCKED p2p_unavailable "P2P chat is not readable by the current user" "rerun e2e-init.sh and select the intended P2P chat"
  fi
}

record_static_wrapper() {
  local claude_bin="${E2E_CLAUDE_BIN:-claude}"
  if [[ "$claude_bin" == */* ]]; then
    if [[ -x "$claude_bin" ]]; then
      e2e_cap_record wrapper PASS ready "configured Claude wrapper is executable" ""
    else
      e2e_cap_record wrapper BLOCKED wrapper_unavailable "configured Claude wrapper is not executable" "fix E2E_CLAUDE_BIN for this profile"
    fi
  elif command -v "$claude_bin" >/dev/null 2>&1; then
    e2e_cap_record wrapper PASS ready "Claude executable is available" ""
  else
    e2e_cap_record wrapper BLOCKED wrapper_unavailable "Claude executable is unavailable" "install Claude or configure E2E_CLAUDE_BIN"
  fi
}

record_static_exclusivity() {
  local lock_path="$STATE_ROOT/.lark-agent-bridge/e2e/locks/$PROFILE_NAME.lock" owner_pid=""
  if [[ ! -d "$lock_path" ]]; then
    e2e_cap_record exclusive_runtime PASS ready "profile has no active local owner" ""
    return
  fi
  owner_pid="$(sed -n '1p' "$lock_path/owner" 2>/dev/null || true)"
  if [[ "$owner_pid" =~ ^[0-9]+$ ]] && kill -0 "$owner_pid" >/dev/null 2>&1; then
    e2e_cap_record exclusive_runtime BLOCKED profile_busy "another local run owns this profile" "wait for the active run or select another profile"
  elif [[ "$owner_pid" =~ ^[0-9]+$ ]]; then
    e2e_cap_record exclusive_runtime PASS stale_lock "profile lock is stale and can be reclaimed by an active run" ""
  else
    e2e_cap_record exclusive_runtime BLOCKED profile_busy "profile lock owner cannot be verified" "inspect the local gitignored lock directory"
  fi
}

run_static_capability_doctor() {
  mkdir -p "$RUN_DIR"
  e2e_cap_reset
  record_static_credentials
  record_static_user_auth
  record_static_bot
  record_static_chats
  record_static_wrapper
  record_static_exclusivity
  e2e_cap_write_json "$CAPABILITIES_JSON"
  e2e_cap_write_summary "$SUMMARY"
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
  *E2E_BLOCK*) exec sleep 300 ;;
esac
EOF
  chmod +x "$FAKE_BIN_DIR/claude"
}

sync_server_pid() {
  if [[ -f "$SERVER_PID_FILE" ]]; then
    local stored_token=""
    read -r SERVER_PID stored_token <"$SERVER_PID_FILE" || true
    if [[ "$stored_token" != "$RUN_TOKEN" ]]; then
      echo "refusing server state owned by another E2E run: $SERVER_PID_FILE" >&2
      SERVER_PID=""
      return 2
    fi
    if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      local command=""
      command="$(ps -ww -p "$SERVER_PID" -o command= 2>/dev/null || true)"
      if [[ "$command" == "$SERVER_BIN serve "* ]]; then
        return 0
      fi
      echo "discarding server state whose PID is not this run's bridge: $SERVER_PID" >&2
    fi
    rm -f "$SERVER_PID_FILE"
  fi
  SERVER_PID=""
  return 1
}

start_server_if_needed() {
  if [[ "$1" == "preflight" ]]; then
    return
  fi
  local sync_status=0
  sync_server_pid || sync_status=$?
  if [[ "$sync_status" -eq 0 ]]; then
    return 0
  fi
  if [[ "$sync_status" -eq 2 ]]; then
    return 1
  fi
  mkdir -p "$DEFAULT_WORKDIR"
  prepare_fake_claude_if_needed
  log "starting bridge serve"
  local -a server_env
  server_env=(
    "PATH=$FAKE_BIN_DIR:$PATH"
    "E2E_AUDIT_LOG=$AUDIT"
    "E2E_SESSION_STORE=$SESSION_STORE"
    "E2E_PREFERENCE_STORE=$PREFERENCE_STORE"
    "E2E_MEDIA_CACHE_DIR=$MEDIA_CACHE_DIR"
    "E2E_CALLBACK_ADDR=$CALLBACK_ADDR"
    "E2E_ALLOWED_MODELS=${E2E_ALLOWED_MODELS:+$E2E_ALLOWED_MODELS,}$CONFIG_CUSTOM_MODEL"
    "GOCACHE=$GOCACHE"
  )
  if [[ "$USE_FAKE_CLAUDE" == "1" ]]; then
    server_env+=("E2E_CLAUDE_BIN=$FAKE_BIN_DIR/claude" "FAKE_CLAUDE_LOG=$FAKE_CLAUDE_LOG")
  fi
  if [[ -n "$SERVER_QUEUE_MAX_PENDING" ]]; then
    server_env+=("E2E_QUEUE_MAX_PENDING=$SERVER_QUEUE_MAX_PENDING")
  fi
  env "${server_env[@]}" "$SERVER_BIN" serve --default-workdir "$DEFAULT_WORKDIR" >>"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  printf '%s %s\n' "$SERVER_PID" "$RUN_TOKEN" >"$SERVER_PID_FILE"
  summary "- bridge_pid: $SERVER_PID"
  local start response
  start="$(date +%s)"
  while true; do
    if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      echo "bridge exited during startup; see $SERVER_LOG" >&2
      tail -n 80 "$SERVER_LOG" >&2 || true
      SERVER_PID=""
      rm -f "$SERVER_PID_FILE"
      return 1
    fi
    response="$(curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d '{"challenge":"e2e-ready"}' 2>/dev/null || true)"
    if [[ "$(printf '%s' "$response" | jq -r '.challenge // empty' 2>/dev/null)" == "e2e-ready" ]]; then
      return 0
    fi
    if (( $(date +%s) - start >= 30 )); then
      echo "bridge callback did not become ready; see $SERVER_LOG" >&2
      stop_server KILL
      return 1
    fi
    sleep 1
  done
}

stop_server() {
  local signal="${1:-TERM}"
  local sync_status=0
  sync_server_pid || sync_status=$?
  if [[ "$sync_status" -ne 0 ]]; then
    return
  fi
  if kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    local child_pids="" force_kill_children=0
    child_pids="$(pgrep -P "$SERVER_PID" 2>/dev/null || true)"
    if [[ "$signal" == "KILL" ]]; then
      force_kill_children=1
    fi
    kill "-$signal" "$SERVER_PID" >/dev/null 2>&1 || true
    local deadline=$(( $(date +%s) + 15 ))
    while kill -0 "$SERVER_PID" >/dev/null 2>&1; do
      if (( $(date +%s) >= deadline )); then
        kill -KILL "$SERVER_PID" >/dev/null 2>&1 || true
        force_kill_children=1
        deadline=$(( $(date +%s) + 5 ))
        while kill -0 "$SERVER_PID" >/dev/null 2>&1 && (( $(date +%s) < deadline )); do
          sleep 1
        done
        break
      fi
      sleep 1
    done
    if kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      echo "bridge process did not exit: $SERVER_PID" >&2
      return 1
    fi
    if [[ "$force_kill_children" -eq 1 && -n "$child_pids" ]]; then
      # child_pids intentionally contains one whitespace-separated PID per line.
      # shellcheck disable=SC2086
      kill -KILL $child_pids >/dev/null 2>&1 || true
      local child_pid
      for child_pid in $child_pids; do
        deadline=$(( $(date +%s) + 5 ))
        while kill -0 "$child_pid" >/dev/null 2>&1 && (( $(date +%s) < deadline )); do
          sleep 1
        done
        if kill -0 "$child_pid" >/dev/null 2>&1; then
          echo "fake Claude child did not exit: $child_pid" >&2
          return 1
        fi
      done
    fi
  fi
  SERVER_PID=""
  rm -f "$SERVER_PID_FILE"
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
  local status=$? kept_server=0
  sync_server_pid >/dev/null 2>&1 || true
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    if [[ "$status" -ne 0 && "$KEEP_SERVER_ON_FAIL" -eq 1 ]]; then
      echo "keeping bridge process $SERVER_PID for diagnosis"
      kept_server=1
    else
      stop_server TERM
    fi
  fi
  if [[ "$kept_server" -eq 1 && -n "${E2E_PROFILE_LOCK_PATH:-}" ]]; then
    printf '%s\n%s\n%s\n' "$SERVER_PID" "$RUN_TOKEN" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$E2E_PROFILE_LOCK_PATH/owner"
  else
    e2e_profile_lock_release >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

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

mget() {
  local case_name="$1"
  local msg_id="$2"
  local out="$MGET_DIR/$case_name-$msg_id.json"
  lark_cli im +messages-mget --as user --message-ids "$msg_id" --format json >"$out"
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
  payload="$(jq -nc --arg session "$session_id" '{operator:{open_id:"e2e"},action:{value:{session:$session,action_id:"stop"}}}')"
  curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d "$payload" >"$out"
  jq -e '.ok == true and .card != null' "$out" >/dev/null
}

open_config() {
  local case_name="$1"
  local msg file
  msg="$(send_at "/config")"
  wait_audit "$msg.*event=config" 60
  file="$(mget "${case_name}_open" "$msg")"
  assert_file_contains "$file" "个人运行偏好"
  record_message "$case_name" config "$msg" "$file"
  printf '%s\n' "$msg"
}

submit_config() {
  local case_name="$1"
  local session_id="$2"
  local model="$3"
  local effort="$4"
  local out="$RUN_DIR/${case_name}-${model}-${effort}.json"
  local payload mark
  payload="$(jq -nc --arg session "$session_id" --arg model "$model" --arg effort "$effort" \
    '{operator:{open_id:"e2e"},action:{value:{session:$session,action_id:"config.save"},form_value:{model:$model,effort:$effort}}}')"
  mark="$(audit_mark)"
  curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d "$payload" >"$out"
  jq -e '.ok == true and .card != null' "$out" >/dev/null
  wait_audit_since "$mark" '"Action":"config_saved"' 60
  assert_file_contains "$out" "model=\`$model\`"
  assert_file_contains "$out" "effort=\`$effort\`"
}

submit_invalid_config() {
  local case_name="$1"
  local session_id="$2"
  local out="$RUN_DIR/${case_name}-invalid.json"
  local payload mark
  payload="$(jq -nc --arg session "$session_id" \
    '{operator:{open_id:"e2e"},action:{value:{session:$session,action_id:"config.save"},form_value:{model:"not-allowed",effort:"extreme"}}}')"
  mark="$(audit_mark)"
  curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d "$payload" >"$out"
  jq -e '.ok == true and .card != null' "$out" >/dev/null
  wait_audit_since "$mark" '"Action":"config_save_failed"' 60
  assert_file_contains "$out" "偏好保存失败"
}

assert_persisted_config() {
  local model="$1"
  local effort="$2"
  jq -e --arg model "$model" --arg effort "$effort" \
    '.schema_version == 1 and .override.model == $model and .override.effort == $effort' "$PREFERENCE_STORE" >/dev/null
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
  local first second follow mark
  first="$(send_at "/new ${first_marker}")"
  wait_audit "$first.*event=stream" 60
  mark="$(audit_mark)"
  second="$(send_at "${second_marker}")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  mark="$(audit_mark)"
  revoke_message "$second"
  wait_audit_since "$mark" "message_recalled_queued_cancelled.*$second" 60
  revoke_message "$first"
  wait_audit "message_recalled_active_cancelled.*$first" 60
  follow="$(send_at "/new ${follow_marker}")"
  wait_audit "$follow.*event=stream" 30
  if grep -F "$second" "$AUDIT" | grep -F "event=result" >/dev/null 2>&1; then
    echo "revoked queued input unexpectedly produced a result card" >&2
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
  local first second file log_mark
  first="$(send_at "/new ${first_marker}")"
  wait_audit "$first.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$first_marker" 60
  restart_server TERM
  log_mark="$(fake_log_mark)"
  second="$(send_at "$second_marker")"
  wait_audit "$second.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$second_marker" 60
  assert_fake_batch_contains "$log_mark" "$second_marker" "--resume fake-e2e-session"
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
  local active queued follow file before mark
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  mark="$(audit_mark)"
  queued="$(send_at "$queued_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  before="$(fake_marker_count "$queued_marker")"
  mark="$(audit_mark)"
  restart_server KILL
  wait_audit_since "$mark" '"Action":"session_recovery_cancelled"' 60
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
  local running follow file before mark
  running="$(send_at "/new ${running_marker}")"
  wait_audit "$running.*event=stream" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$running_marker" 60
  before="$(fake_marker_count "$running_marker")"
  mark="$(audit_mark)"
  restart_server KILL
  wait_audit_since "$mark" '"Action":"session_recovery_interrupted"' 60
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
  local first_marker="E2E_${RUN_ID}_DM_DEBOUNCE_ONE"
  local second_marker="E2E_${RUN_ID}_DM_DEBOUNCE_TWO"
  local first second anchor file log_mark mark
  log_mark="$(fake_log_mark)"
  mark="$(audit_mark)"
  send_dm_pair "$first_marker" "$second_marker"
  first="$PAIR_FIRST"
  second="$PAIR_SECOND"
  wait_audit_since "$mark" "($first|$second).*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$second_marker" 60
  assert_fake_batch_contains "$log_mark" "$first_marker" "$second_marker"
  anchor="$(result_message_since "$mark" "$first" "$second")"
  file="$(mget debounce_dm "$anchor")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message debounce_dm first "$first"
  record_message debounce_dm second "$second"
  record_message debounce_dm result_anchor "$anchor" "$file"
}

case_debounce_group() {
  require_fake_claude
  local first_marker="E2E_${RUN_ID}_GROUP_DEBOUNCE_ONE"
  local second_marker="E2E_${RUN_ID}_GROUP_DEBOUNCE_TWO"
  local first second anchor file log_mark mark
  log_mark="$(fake_log_mark)"
  mark="$(audit_mark)"
  send_group_pair "$first_marker" "$second_marker"
  first="$PAIR_FIRST"
  second="$PAIR_SECOND"
  wait_audit_since "$mark" "($first|$second).*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$second_marker" 60
  assert_fake_batch_contains "$log_mark" "$first_marker" "$second_marker"
  anchor="$(result_message_since "$mark" "$first" "$second")"
  file="$(mget debounce_group "$anchor")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message debounce_group first "$first"
  record_message debounce_group second "$second"
  record_message debounce_group result_anchor "$anchor" "$file"
}

case_busy_merge() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_BUSY_ACTIVE_E2E_BLOCK"
  local first_marker="E2E_${RUN_ID}_BUSY_MERGE_ONE"
  local second_marker="E2E_${RUN_ID}_BUSY_MERGE_TWO"
  local active first second anchor file log_mark mark
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  log_mark="$(fake_log_mark)"
  mark="$(audit_mark)"
  send_group_pair "$first_marker" "$second_marker"
  first="$PAIR_FIRST"
  second="$PAIR_SECOND"
  wait_file_contains "$FAKE_CLAUDE_LOG" "$active_marker" 60
  wait_audit_count_since "$mark" '"Action":"queue_input"' 2 60
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit_since "$mark" '"Action":"batch_stop_requested"' 60
  wait_audit_since "$mark" "($first|$second).*event=result" 60
  assert_fake_batch_contains "$log_mark" "$first_marker" "$second_marker"
  anchor="$(result_message_since "$mark" "$first" "$second")"
  file="$(mget busy_merge "$anchor")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  record_message busy_merge active "$active"
  record_message busy_merge first "$first"
  record_message busy_merge merged "$anchor" "$file"
}

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

case_scope_parallel() {
  require_fake_claude
  local root_one root_two one two thread_one thread_two one_file two_file mark
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
  thread_one="$(thread_id_for_message scope_parallel_thread_one "$one")"
  thread_two="$(thread_id_for_message scope_parallel_thread_two "$two")"
  if [[ "$thread_one" == "$thread_two" ]]; then
    echo "parallel case resolved the same thread twice: $thread_one" >&2
    return 1
  fi
  mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:thread:${thread_one}:message:${one}"
  wait_audit_since "$mark" "batch_stop_requested.*thread:${thread_one}" 60
  mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:thread:${thread_two}:message:${two}"
  wait_audit_since "$mark" "batch_stop_requested.*thread:${thread_two}" 60
  one_file="$(mget scope_parallel "$one")"
  two_file="$(mget scope_parallel "$two")"
  assert_file_contains "$one_file" "已停止"
  assert_file_contains "$two_file" "已停止"
  record_message scope_parallel root_one "$root_one"
  record_message scope_parallel root_two "$root_two"
  record_message scope_parallel scope_one "$one" "$one_file"
  record_message scope_parallel scope_two "$two" "$two_file"
}

case_stop_preserves_queue() {
  require_fake_claude
  local active_marker="E2E_${RUN_ID}_STOP_ACTIVE_E2E_BLOCK"
  local queued_marker="E2E_${RUN_ID}_STOP_QUEUE_PRESERVED"
  local active queued active_file queued_file mark
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  mark="$(audit_mark)"
  queued="$(send_at "$queued_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit_since "$mark" '"Action":"batch_stop_requested"' 60
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
  local active queued file before mark
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  before="$(fake_marker_count "$queued_marker")"
  mark="$(audit_mark)"
  queued="$(send_at "$queued_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  mark="$(audit_mark)"
  revoke_message "$queued"
  wait_audit_since "$mark" "message_recalled_queued_cancelled.*$queued" 60
  mark="$(audit_mark)"
  revoke_message "$active"
  wait_audit_since "$mark" "message_recalled_active_cancelled.*$active" 60
  sleep 2
  assert_fake_marker_not_started_after "$queued_marker" "$before"
  file="$(mget recall_state "$active")"
  assert_file_contains "$file" "已停止"
  record_message recall_state active_cancelled "$active" "$file"
  record_message recall_state queued_cancelled "$queued"
}

case_media_attachment_only() {
  require_fake_claude
  prepare_media_fixtures
  local source="$MEDIA_FIXTURE_DIR/sample.jpg"
  local key elements msg file log_mark expected
  key="$(upload_media_key media_attachment_only image "$source")"
  elements="$(jq -nc --arg key "$key" '[{tag:"img",image_key:$key}]')"
  expected="$(media_cache_path "$source" .jpg)"
  log_mark="$(fake_log_mark)"
  msg="$(send_media_post "$elements")"
  wait_audit "$msg.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$expected" 60
  assert_fake_paths_since "$log_mark" "$expected"
  file="$(mget media_attachment_only "$msg")"
  assert_file_contains "$file" "FAKE_E2E_STARTED"
  assert_no_error_event "$msg"
  record_message media_attachment_only attachment_only_jpeg "$msg" "$file"
}

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

case_media_text_files() {
  require_fake_claude
  require_media_p2p_chat
  prepare_media_fixtures
  local source extension role msg file log_mark expected
  for extension in txt md json csv; do
    source="$MEDIA_FIXTURE_DIR/sample.$extension"
    role="text_file_$extension"
    expected="$(media_cache_path "$source" ".$extension")"
    log_mark="$(fake_log_mark)"
    msg="$(send_direct_media file "$source")"
    wait_audit "$msg.*event=result" 60
    wait_file_contains "$FAKE_CLAUDE_LOG" "$expected" 60
    assert_fake_paths_since "$log_mark" "$expected"
    file="$(mget "media_text_files_$extension" "$msg")"
    assert_file_contains "$file" "FAKE_E2E_STARTED"
    assert_no_error_event "$msg"
    record_message media_text_files "$role" "$msg" "$file"
  done
}

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

case_config_roundtrip() {
  require_fake_claude
  local config_msg session_id model effort marker msg file log_mark
  local -a models=(default sonnet opus haiku "$CONFIG_CUSTOM_MODEL")
  local -a efforts=(default low medium high high)
  config_msg="$(open_config config_roundtrip)"
  session_id="config:message:${config_msg}"
  for index in "${!models[@]}"; do
    model="${models[$index]}"
    effort="${efforts[$index]}"
    submit_config config_roundtrip "$session_id" "$model" "$effort"
    assert_persisted_config "$model" "$effort"
    marker="E2E_${RUN_ID}_CONFIG_${index}"
    log_mark="$(fake_log_mark)"
    msg="$(send_at "/new ${marker}")"
    wait_audit "$msg.*event=result" 60
    wait_file_contains "$FAKE_CLAUDE_LOG" "$marker" 60
    assert_fake_config_argv_since "$log_mark" "$marker" "$model" "$effort"
    file="$(mget "config_roundtrip_${index}" "$msg")"
    assert_file_contains "$file" "requested: $model"
    assert_file_contains "$file" "effort: $effort"
    record_message config_roundtrip "run_${model}_${effort}" "$msg" "$file"
  done
  submit_invalid_config config_roundtrip "$session_id"
  assert_persisted_config "$CONFIG_CUSTOM_MODEL" high
  summary "- combinations: default/default, sonnet/low, opus/medium, haiku/high, ${CONFIG_CUSTOM_MODEL}/high"
}

case_config_reset() {
  require_fake_claude
  local config_msg session_id reset_msg reset_file reopened marker msg log_mark
  config_msg="$(open_config config_reset)"
  session_id="config:message:${config_msg}"
  submit_config config_reset "$session_id" opus high
  assert_persisted_config opus high
  reset_msg="$(send_at "/config reset")"
  wait_audit '"Action":"config_reset"' 60
  wait_audit "reply_to=$reset_msg event=message" 60
  reset_file="$(mget config_reset "$reset_msg")"
  assert_file_contains "$reset_file" "已恢复环境默认"
  jq -e '.schema_version == 1 and .override == null' "$PREFERENCE_STORE" >/dev/null
  restart_server TERM
  reopened="$(open_config config_reset_after_restart)"
  jq -e '.schema_version == 1 and .override == null' "$PREFERENCE_STORE" >/dev/null
  marker="E2E_${RUN_ID}_CONFIG_RESET_DEFAULTS"
  log_mark="$(fake_log_mark)"
  msg="$(send_at "/new ${marker}")"
  wait_audit "$msg.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$marker" 60
  assert_fake_config_argv_since "$log_mark" "$marker" "$CONFIG_DEFAULT_MODEL" "$CONFIG_DEFAULT_EFFORT"
  record_message config_reset reset "$reset_msg" "$reset_file"
  record_message config_reset reopened "$reopened"
  record_message config_reset defaults_run "$msg"
  summary "- environment_defaults: model=$CONFIG_DEFAULT_MODEL effort=$CONFIG_DEFAULT_EFFORT"
}

case_config_frozen_queue() {
  require_fake_claude
  local config_msg session_id active old_queued new_queued mark log_mark old_file new_file
  local active_marker="E2E_${RUN_ID}_CONFIG_ACTIVE_E2E_BLOCK"
  local old_marker="E2E_${RUN_ID}_CONFIG_FROZEN_OLD"
  local new_marker="E2E_${RUN_ID}_CONFIG_FROZEN_NEW"
  config_msg="$(open_config config_frozen_queue)"
  session_id="config:message:${config_msg}"
  submit_config config_frozen_queue "$session_id" sonnet low
  active="$(send_at "/new ${active_marker}")"
  wait_audit "$active.*event=stream" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$active_marker" 60
  log_mark="$(fake_log_mark)"
  mark="$(audit_mark)"
  old_queued="$(send_at "$old_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  submit_config config_frozen_queue "$session_id" opus high
  mark="$(audit_mark)"
  new_queued="$(send_at "$new_marker")"
  wait_audit_since "$mark" '"Action":"queue_input"' 60
  mark="$(audit_mark)"
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${active}"
  wait_audit_since "$mark" '"Action":"batch_stop_requested"' 60
  wait_audit "$old_queued.*event=result" 60
  wait_audit "$new_queued.*event=result" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$old_marker" 60
  wait_file_contains "$FAKE_CLAUDE_LOG" "$new_marker" 60
  assert_fake_config_argv_since "$log_mark" "$old_marker" sonnet low
  assert_fake_config_argv_since "$log_mark" "$new_marker" opus high
  old_file="$(mget config_frozen_queue_old "$old_queued")"
  new_file="$(mget config_frozen_queue_new "$new_queued")"
  assert_file_contains "$old_file" "requested: sonnet"
  assert_file_contains "$new_file" "requested: opus"
  record_message config_frozen_queue active "$active"
  record_message config_frozen_queue frozen_old "$old_queued" "$old_file"
  record_message config_frozen_queue frozen_new "$new_queued" "$new_file"
}

case_requested_actual_model() {
  require_fake_claude
  local config_msg session_id marker msg file mark
  config_msg="$(open_config requested_actual_model)"
  session_id="config:message:${config_msg}"
  submit_config requested_actual_model "$session_id" opus high
  marker="E2E_${RUN_ID}_REQUESTED_ACTUAL"
  mark="$(audit_mark)"
  msg="$(send_at "/new ${marker}")"
  wait_audit "$msg.*event=result" 60
  wait_audit_since "$mark" '"Action":"model_requested_actual_mismatch".*requested=opus actual=fake-claude-e2e' 60
  file="$(mget requested_actual_model "$msg")"
  assert_file_contains "$file" "requested: opus"
  assert_file_contains "$file" "actual: fake-claude-e2e"
  assert_file_contains "$file" "effort: high"
  assert_file_not_contains "$file" "actual: opus"
  record_message requested_actual_model result "$msg" "$file"
}

case_wrapper_preflight() {
  require_fake_claude
  local out="$RUN_DIR/wrapper-preflight.out"
  local log_mark line
  log_mark="$(fake_log_mark)"
  env \
    E2E_CLAUDE_BIN="$FAKE_BIN_DIR/claude" \
    FAKE_CLAUDE_LOG="$FAKE_CLAUDE_LOG" \
    E2E_AUDIT_LOG="$AUDIT" \
    E2E_SESSION_STORE="$SESSION_STORE" \
    E2E_PREFERENCE_STORE="$PREFERENCE_STORE" \
    E2E_MEDIA_CACHE_DIR="$MEDIA_CACHE_DIR" \
    E2E_ALLOWED_MODELS="${E2E_ALLOWED_MODELS:+$E2E_ALLOWED_MODELS,}$CONFIG_CUSTOM_MODEL" \
    "$SERVER_BIN" doctor --strict --default-workdir "$DEFAULT_WORKDIR" >"$out"
  assert_file_contains "$out" "ok wrapper-preflight: passed"
  line="$(tail -n "+$((log_mark + 1))" "$FAKE_CLAUDE_LOG" | tail -n 1)"
  if ! printf '%s\n' "$line" | grep -F -- "-p --output-format stream-json --verbose --effort low Reply with exactly OK" >/dev/null 2>&1; then
    echo "wrapper preflight did not use the bounded harmless invocation: $line" >&2
    return 1
  fi
  if grep -F -- "$LARK_APP_SECRET" "$out" >/dev/null 2>&1; then
    echo "wrapper preflight output leaked LARK_APP_SECRET" >&2
    return 1
  fi
  summary "- doctor: strict wrapper preflight passed"
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
  sync_server_pid >/dev/null 2>&1 || true
  if [[ "$name" != "preflight" && "$status" -ne 0 && "$KEEP_SERVER_ON_FAIL" -eq 0 ]]; then
    set +e
    # Recovery output intentionally joins the case-specific evidence log.
    # shellcheck disable=SC2129
    echo "case failed; restarting bridge to clear active processes and pending input" >>"$RUN_DIR/$name.log"
    stop_server TERM >>"$RUN_DIR/$name.log" 2>&1
    start_server_if_needed recovery >>"$RUN_DIR/$name.log" 2>&1
    local recovery_status=$?
    set -e
    if [[ "$recovery_status" -ne 0 ]]; then
      echo "bridge recovery after failed case also failed" >>"$RUN_DIR/$name.log"
    fi
  fi
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

case_prerequisites() {
  local name="$1"
  case "$name" in
    debounce_dm)
      printf '%s\n' credentials lark_cli_auth bot_identity wrapper exclusive_runtime
      ;;
    media_attachment_only|media_images)
      if e2e_cap_index media_image >/dev/null 2>&1; then
        printf '%s\n' credentials lark_cli_auth bot_identity test_group media_image wrapper exclusive_runtime
      else
        printf '%s\n' credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime
      fi
      ;;
    media_text_files|media_partial|media_rejected)
      if e2e_cap_index media_file >/dev/null 2>&1; then
        printf '%s\n' credentials lark_cli_auth bot_identity p2p_chat media_file wrapper exclusive_runtime
      else
        printf '%s\n' credentials lark_cli_auth bot_identity p2p_chat wrapper exclusive_runtime
      fi
      ;;
    message_revoke|message_revoke_pending_workdir|message_revoke_queued_input|recall_state)
      if e2e_cap_index recall_event_delivery >/dev/null 2>&1; then
        printf '%s\n' credentials lark_cli_auth bot_identity test_group recall_event_delivery wrapper exclusive_runtime
      else
        printf '%s\n' credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime
      fi
      ;;
    preflight)
      printf '%s\n' credentials lark_cli_auth bot_identity test_group exclusive_runtime
      ;;
    *)
      printf '%s\n' credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime
      ;;
  esac
}

selected_cases_have_ready() {
  local case_name capability state
  local prerequisites=()
  for case_name in "${RUN_CASES[@]}"; do
    prerequisites=()
    while IFS= read -r capability; do
      [[ -n "$capability" ]] && prerequisites+=("$capability")
    done < <(case_prerequisites "$case_name")
    state="$(e2e_cap_evaluate "$case_name" "${prerequisites[@]}")"
    [[ "$state" == "READY" ]] && return 0
  done
  return 1
}

append_capability_summary() {
  local capability_summary="$RUN_DIR/.capability-summary.md"
  e2e_cap_write_summary "$capability_summary"
  cat "$capability_summary" >>"$SUMMARY"
  rm -f "$capability_summary"
}

append_normal_summary_footer() {
  summary "## Summary"
  summary
  summary "- failures: $FAILURES"
  summary "- blocked: $BLOCKED_CASES"
  summary "- messages: $MESSAGES"
  summary "- server_log: $SERVER_LOG"
}

normal_exit_code() {
  if [[ "$FAILURES" -ne 0 ]]; then
    printf '1\n'
  elif [[ "$STRICT_CAPABILITIES" -eq 1 && "$BLOCKED_CASES" -ne 0 ]]; then
    printf '3\n'
  else
    printf '0\n'
  fi
}

run_case_with_capabilities() {
  local name="$1" state capability
  local prerequisites=()
  while IFS= read -r capability; do
    [[ -n "$capability" ]] && prerequisites+=("$capability")
  done < <(case_prerequisites "$name")
  state="$(e2e_cap_evaluate "$name" "${prerequisites[@]}")"
  case "$state" in
    READY)
      run_case "$name"
      ;;
    BLOCKED)
      log "case $name blocked by profile capabilities"
      summary "## $name"
      summary
      summary "- status: blocked"
      summary "- reason: prerequisite_blocked"
      summary
      BLOCKED_CASES=$((BLOCKED_CASES + 1))
      ;;
    *)
      log "case $name skipped because a prerequisite failed or was not evaluated"
      summary "## $name"
      summary
      summary "- status: skipped"
      summary "- reason: prerequisite_$state"
      summary
      FAILURES=$((FAILURES + 1))
      ;;
  esac
}

capability_prerequisites_ready() {
  local capability="$1"
  shift
  local state
  state="$(e2e_cap_evaluate "$capability" "$@")"
  case "$state" in
    READY) return 0 ;;
    BLOCKED)
      e2e_cap_record "$capability" BLOCKED prerequisite_blocked "one or more prerequisites are blocked" "resolve the blocked static capability first"
      ;;
    FAIL)
      e2e_cap_record "$capability" SKIPPED prerequisite_failed "a prerequisite failed" "repair the failed capability first"
      ;;
    *)
      e2e_cap_record "$capability" SKIPPED prerequisite_skipped "a prerequisite was not evaluated" "run static doctor first"
      ;;
  esac
  return 1
}

run_capability_case() {
  local capability="$1" case_name="$2" blocked_pattern="${3:-}" blocked_reason="${4:-external_prerequisite}" remediation="${5:-inspect the local evidence}"
  local log_file="$RUN_DIR/capability-$capability.log" status=0
  set +e
  ( set -e; "case_$case_name" ) >"$log_file" 2>&1
  status=$?
  set -e
  if [[ "$status" -eq 0 ]]; then
    e2e_cap_record "$capability" PASS ready "active canary passed" "" "$log_file"
    return 0
  fi
  if [[ -n "$blocked_pattern" ]] && grep -Eiq "$blocked_pattern" "$log_file"; then
    e2e_cap_record "$capability" BLOCKED "$blocked_reason" "active canary hit an external prerequisite" "$remediation" "$log_file"
    return 0
  fi
  e2e_cap_record "$capability" FAIL canary_failed "active canary failed" "inspect the capability log" "$log_file"
  return 1
}

run_recall_capabilities() {
  local log_file="$RUN_DIR/capability-recall.log" status=0
  set +e
  ( set -e; case_message_revoke ) >"$log_file" 2>&1
  status=$?
  set -e
  if [[ "$status" -eq 0 ]]; then
    e2e_cap_record message_recall_api PASS ready "message deletion API passed" "" "$log_file"
    e2e_cap_record recall_event_delivery PASS ready "recall event reached the bridge" "" "$log_file"
    return
  fi
  if grep -Eiq 'permission|forbidden|scope|delete.*failed|revoke.*failed' "$log_file"; then
    e2e_cap_record message_recall_api BLOCKED recall_permission_missing "message deletion API is unavailable" "grant the user message recall permission" "$log_file"
    e2e_cap_record recall_event_delivery SKIPPED recall_api_blocked "recall event was not tested" "resolve message recall API access" "$log_file"
  elif grep -Eiq 'message_recalled|timed out.*recall|recall.*timed out' "$log_file"; then
    e2e_cap_record message_recall_api PASS ready "message deletion API passed" "" "$log_file"
    e2e_cap_record recall_event_delivery BLOCKED event_not_delivered "recall event did not reach the bridge" "enable im.message.recalled_v1 for this app" "$log_file"
  else
    e2e_cap_record message_recall_api FAIL canary_failed "recall canary failed before it could be classified" "inspect the recall capability log" "$log_file"
    e2e_cap_record recall_event_delivery SKIPPED recall_probe_failed "recall event was not evaluated" "repair the recall canary" "$log_file"
  fi
}

run_active_capability_preflight() {
  local lock_path="$STATE_ROOT/.lark-agent-bridge/e2e/locks/$PROFILE_NAME.lock"
  local readiness
  readiness="$(e2e_cap_evaluate active_preflight credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime)"
  if [[ "$readiness" != "READY" ]]; then
    e2e_cap_record group_delivery SKIPPED static_prerequisite_blocked "active preflight did not start" "resolve static doctor results first"
    e2e_cap_record dm_delivery SKIPPED static_prerequisite_blocked "active preflight did not start" "resolve static doctor results first"
    e2e_cap_record card_action SKIPPED static_prerequisite_blocked "active preflight did not start" "resolve static doctor results first"
    e2e_cap_record message_recall_api SKIPPED static_prerequisite_blocked "active preflight did not start" "resolve static doctor results first"
    e2e_cap_record recall_event_delivery SKIPPED static_prerequisite_blocked "active preflight did not start" "resolve static doctor results first"
    e2e_cap_record media_image SKIPPED static_prerequisite_blocked "active preflight did not start" "resolve static doctor results first"
    e2e_cap_record media_file SKIPPED static_prerequisite_blocked "active preflight did not start" "resolve static doctor results first"
    return 0
  fi
  mkdir -p "$(dirname "$lock_path")"
  chmod 700 "$(dirname "$lock_path")"
  e2e_profile_lock_acquire "$PROFILE_NAME" "$lock_path" "$RUN_TOKEN" || {
    e2e_cap_record exclusive_runtime BLOCKED profile_busy "another local run owns this profile" "wait or select another profile"
    return 3
  }
  e2e_cap_record exclusive_runtime PASS ready "active run acquired the local profile lock" ""
  USE_FAKE_CLAUDE=1
  go build -o "$SERVER_BIN" ./cmd/lark-agent-bridge
  fetch_bot_open_id
  start_server_if_needed capability

  if capability_prerequisites_ready group_delivery credentials bot_identity test_group; then
    run_capability_case group_delivery new_basic || true
  fi
  if capability_prerequisites_ready dm_delivery credentials lark_cli_auth bot_identity; then
    run_capability_case dm_delivery debounce_dm 'cross app|P2P send|user-id' open_id_cross_app "login lark-cli through the bridge app" || true
    case "$(e2e_cap_status dm_delivery)" in
      PASS) e2e_cap_record oauth_same_app PASS active_probe "DM canary proved OAuth compatibility" "" "$RUN_DIR/capability-dm_delivery.log" ;;
      BLOCKED) e2e_cap_record oauth_same_app BLOCKED oauth_app_mismatch "DM canary proved an app identity mismatch" "login lark-cli through the bridge app" "$RUN_DIR/capability-dm_delivery.log" ;;
    esac
  fi
  if capability_prerequisites_ready card_action group_delivery; then
    run_capability_case card_action stop_preserves_queue || true
  fi
  if capability_prerequisites_ready message_recall_api group_delivery; then
    run_recall_capabilities
  else
    e2e_cap_record recall_event_delivery SKIPPED recall_prerequisite_blocked "recall event was not tested" "resolve group delivery first"
  fi
  if capability_prerequisites_ready media_image group_delivery; then
    run_capability_case media_image media_attachment_only || true
  fi
  if capability_prerequisites_ready media_file dm_delivery p2p_chat; then
    run_capability_case media_file media_text_files || true
  fi
}

RUN_CASES=()
selected_cases_array
validate_selected_cases
require_cmd jq
require_cmd lark-cli
require_cmd curl
run_static_capability_doctor
if [[ "$DOCTOR_MODE" -eq 1 ]]; then
  echo "$SUMMARY"
  exit "$(e2e_cap_exit_code "$STRICT_CAPABILITIES")"
fi
if [[ "$PREFLIGHT_ONLY" -eq 1 ]]; then
  require_cmd go
  require_cmd pgrep
  require_cmd ps
  run_active_capability_preflight || true
  e2e_cap_write_json "$CAPABILITIES_JSON"
  e2e_cap_write_summary "$SUMMARY"
  if [[ "$PROFILE_NAME" != "legacy" ]]; then
    cp "$CAPABILITIES_JSON" "$PROFILE_CAPABILITY_CACHE"
    chmod 600 "$PROFILE_CAPABILITY_CACHE"
  fi
  echo "$SUMMARY"
  exit "$(e2e_cap_exit_code "$STRICT_CAPABILITIES")"
fi
if [[ "$PROFILE_NAME" != "legacy" && -f "$PROFILE_CAPABILITY_CACHE" && "$PROFILE_CAPABILITY_CACHE" -nt "$PROFILE_ENV" ]]; then
  e2e_cap_import_json "$PROFILE_CAPABILITY_CACHE" \
    group_delivery dm_delivery card_action message_recall_api recall_event_delivery media_image media_file
fi
summary_init
if ! selected_cases_have_ready; then
  e2e_cap_write_json "$CAPABILITIES_JSON"
  append_capability_summary
  for case_name in "${RUN_CASES[@]}"; do
    [[ -n "$case_name" ]] || continue
    run_case_with_capabilities "$case_name"
  done
  append_normal_summary_footer
  echo "$SUMMARY"
  exit "$(normal_exit_code)"
fi
normal_lock_path="$STATE_ROOT/.lark-agent-bridge/e2e/locks/$PROFILE_NAME.lock"
mkdir -p "$(dirname "$normal_lock_path")"
chmod 700 "$(dirname "$normal_lock_path")"
if ! e2e_profile_lock_acquire "$PROFILE_NAME" "$normal_lock_path" "$RUN_TOKEN"; then
  e2e_cap_record exclusive_runtime BLOCKED profile_busy "another local run owns this profile" "wait or select another profile"
  e2e_cap_write_json "$CAPABILITIES_JSON"
  e2e_cap_write_summary "$SUMMARY"
  echo "$SUMMARY"
  if [[ "$STRICT_CAPABILITIES" -eq 1 ]]; then
    exit 3
  fi
  exit 0
fi
e2e_cap_record exclusive_runtime PASS ready "active run acquired the local profile lock" ""
e2e_cap_write_json "$CAPABILITIES_JSON"
append_capability_summary
require_cmd go
require_cmd pgrep
require_cmd ps
require_env LARK_APP_ID
require_env LARK_APP_SECRET
require_env E2E_E2E_CHAT_ID
go build -o "$SERVER_BIN" ./cmd/lark-agent-bridge
fetch_bot_open_id
summary "- bot_open_id: ${BOT_OPEN_ID:0:6}...${BOT_OPEN_ID: -4}"
summary

for case_name in "${RUN_CASES[@]}"; do
  [[ -n "$case_name" ]] || continue
  run_case_with_capabilities "$case_name"
done

append_normal_summary_footer

echo "$SUMMARY"
exit "$(normal_exit_code)"
