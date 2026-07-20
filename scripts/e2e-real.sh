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
  new_basic
  streaming_card
)

FULL_EXTRA_CASES=(
  session_restart_context
  recall_state
  media_attachment_only
  media_images
  media_text_files
  native_text_stream
  latest_restart_fallback
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
RUN_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
RUN_STARTED_EPOCH="$(date +%s)"
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
SUMMARY_INITIALIZED=0
RUN_TIMING_FINALIZED=0

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

enable_callback() {
  if [[ -z "$CALLBACK_ADDR" ]]; then
    CALLBACK_ADDR="${E2E_REAL_E2E_CALLBACK_ADDR:-127.0.0.1:$((20000 + RANDOM % 20000))}"
  fi
}

configure_callback_for_cases() {
  local case_name
  CALLBACK_ADDR=""
  for case_name in "${RUN_CASES[@]}"; do
    case "$case_name" in
      native_text_stream|latest_restart_fallback)
        enable_callback
        return
        ;;
    esac
  done
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
REPLY_STORE="$STATE_DIR/replies.json"
FAKE_CLAUDE_LOG="$RUN_DIR/fake-claude.log"
MEDIA_CACHE_DIR="$RUN_DIR/media-cache"
MEDIA_FIXTURE_DIR="$RUN_DIR/media-fixtures"
FAKE_BIN_DIR="$RUN_DIR/bin"
SERVER_BIN="$RUN_DIR/lark-agent-bridge-e2e"
SERVER_PID_FILE="$RUN_DIR/server.pid"
RUN_TOKEN="${RUN_ID}-$RANDOM-$$"
CALLBACK_ADDR=""
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
    echo "- generated_at: $RUN_STARTED_AT"
    echo "- started_at: $RUN_STARTED_AT"
    echo "- repo: $ROOT"
    echo "- mode: $MODE"
    echo "- run_dir: $RUN_DIR"
    echo "- audit: $AUDIT"
    echo "- callback_addr: ${CALLBACK_ADDR:-disabled}"
    if [[ -n "$CALLBACK_ADDR" ]]; then
      echo "- action_transport: gateway_injected"
    else
      echo "- action_transport: not_applicable"
    fi
    echo "- default_workdir: $DEFAULT_WORKDIR"
    echo "- fake_claude: $USE_FAKE_CLAUDE"
    echo "- secrets: not printed"
    echo
  } >"$SUMMARY"
  SUMMARY_INITIALIZED=1
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
  local response auth_app scope_response scope_status scope_argument login_command
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
  scope_status=0
  scope_response="$(lark_cli auth check --scope "$scope_argument" --json 2>/dev/null)" || scope_status=$?
  if printf '%s' "$scope_response" | jq -e '(.missing | type) == "array" and (.missing | length > 0)' >/dev/null 2>&1; then
    e2e_cap_record lark_cli_auth BLOCKED user_e2e_scopes_missing "user OAuth is missing E2E observation or control scopes" "enable and publish the E2E scope bundle, then run $login_command"
    return
  fi
  if [[ "$scope_status" -ne 0 ]] || ! printf '%s' "$scope_response" | jq -e \
    '.ok == true and (((.missing // []) | type) != "array" or ((.missing // []) | length == 0))' >/dev/null 2>&1; then
    e2e_cap_record lark_cli_auth FAIL user_e2e_scope_check_failed "lark-cli could not verify the E2E scope bundle" "inspect lark-cli auth check output"
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
marker="$(printf '%s\n' "$args" | grep -Eo 'E2E_[A-Za-z0-9_-]+' | tail -n 1 || true)"
case "$args" in
  *E2E_*_NATIVE_TEXT_STREAM_STOP_E2E_BLOCK*)
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"native-stop-one "}}'
    sleep 1.2
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"native-stop-two "}}'
    sleep 1.2
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"native-stop-three "}}'
    exec sleep 300
    ;;
  *E2E_*_NATIVE_TEXT_STREAM_NORMAL*)
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"native-normal-one "}}'
    sleep 1.2
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"native-normal-two "}}'
    sleep 1.2
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"native-normal-three "}}'
    sleep 1.2
    printf '%s\n' '{"type":"result","result":"native-normal-final","model":"fake-claude-e2e","usage":{"output_tokens":1},"session_id":"fake-e2e-session"}'
    exit 0
    ;;
  *E2E_*_STREAM*)
    jq -nc --arg text "streaming ${marker}" '{type:"content_block_delta",delta:{type:"text_delta",text:$text}}'
    sleep 1.2
    jq -nc --arg result "FAKE_E2E_STARTED ${marker}" '{type:"result",result:$result,model:"fake-claude-e2e",usage:{output_tokens:1},session_id:"fake-e2e-session"}'
    exit 0
    ;;
  *E2E_PREVIEW_THRESHOLDS*)
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"PREVIEW_FIRST_"}}'
    sleep 0.2
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}}'
    sleep 1
    long="$(awk 'BEGIN { for (i = 0; i < 2100; i++) printf "C" }')PREVIEW_TAIL_HIDDEN"
    printf '{"type":"content_block_delta","delta":{"type":"text_delta","text":"%s"}}\n' "$long"
    sleep 3
    printf '{"type":"result","result":"PREVIEW_FINAL_COMPLETE_%s","model":"fake-claude-e2e","usage":{"output_tokens":1},"session_id":"fake-e2e-session"}\n' "$long"
    exit 0
    ;;
  *E2E_REPLY_MODE_SEMANTICS*)
    printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"E2E_INTERMEDIATE_ANSWER"}]}}'
    printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"E2E_PRIVATE_THOUGHT"},{"type":"tool_use","name":"Read","id":"e2e-tool-1","input":{"path":"E2E_TOOL_CALL"}}]}}'
    printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"E2E_FINAL_ANSWER"}]}}'
    printf '%s\n' '{"type":"result","result":"E2E_FINAL_ANSWER","model":"fake-claude-e2e","usage":{"output_tokens":2},"session_id":"fake-e2e-session"}'
    exit 0
    ;;
  *E2E_PROCESS_PANELS*)
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"E2E_PRIVATE_THOUGHT"}}'
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"E2E_TOOL_CALL"}}'
    printf '%s\n' '{"type":"content_block_delta","delta":{"type":"text_delta","text":"E2E_CLEAN_ANSWER"}}'
    printf '%s\n' '{"type":"result","result":"E2E_CLEAN_ANSWER","model":"fake-claude-e2e","usage":{"output_tokens":1},"session_id":"fake-e2e-session"}'
    exit 0
    ;;
esac
result="FAKE_E2E_STARTED${marker:+ $marker}"
jq -nc --arg result "$result" '{type:"result",result:$result,model:"fake-claude-e2e",usage:{output_tokens:1},session_id:"fake-e2e-session"}'
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
  local log_mark=0
  if [[ -f "$SERVER_LOG" ]]; then
    log_mark="$(wc -l <"$SERVER_LOG" | tr -d ' ')"
  fi
  local -a server_env
  server_env=(
    "PATH=$FAKE_BIN_DIR:$PATH"
    "E2E_AUDIT_LOG=$AUDIT"
    "E2E_SESSION_STORE=$SESSION_STORE"
    "E2E_PREFERENCE_STORE=$PREFERENCE_STORE"
    "E2E_REPLY_STORE=$REPLY_STORE"
    "E2E_MEDIA_CACHE_DIR=$MEDIA_CACHE_DIR"
    "E2E_ALLOWED_MODELS=${E2E_ALLOWED_MODELS:+$E2E_ALLOWED_MODELS,}$CONFIG_CUSTOM_MODEL"
    "GOCACHE=$GOCACHE"
  )
  if [[ -n "$CALLBACK_ADDR" ]]; then
    server_env+=("E2E_CALLBACK_ADDR=$CALLBACK_ADDR")
  fi
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
  local start
  start="$(date +%s)"
  while true; do
    if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
      echo "bridge exited during startup; see $SERVER_LOG" >&2
      tail -n 80 "$SERVER_LOG" >&2 || true
      SERVER_PID=""
      rm -f "$SERVER_PID_FILE"
      return 1
    fi
    if tail -n "+$((log_mark + 1))" "$SERVER_LOG" 2>/dev/null | grep -F 'connected to wss' >/dev/null 2>&1; then
      return 0
    fi
    if (( $(date +%s) - start >= 30 )); then
      echo "bridge WSS connection did not become ready; see $SERVER_LOG" >&2
      stop_server KILL
      return 1
    fi
    # The bridge is usually ready well under a second, so poll at a sub-second
    # interval to reclaim most of the startup wait without flooding the probe.
    sleep 0.3
  done
}

wait_callback_ready() {
  local start response
  if [[ -z "$CALLBACK_ADDR" ]]; then
    echo "callback is disabled for the selected E2E cases" >&2
    return 1
  fi
  start="$(date +%s)"
  while true; do
    response="$(curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d '{"challenge":"e2e-ready"}' 2>/dev/null || true)"
    if [[ "$(printf '%s' "$response" | jq -r '.challenge // empty' 2>/dev/null)" == "e2e-ready" ]]; then
      return 0
    fi
    if (( $(date +%s) - start >= 10 )); then
      echo "bridge callback did not become ready; see $SERVER_LOG" >&2
      return 1
    fi
    sleep 0.3
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
  # Finalize evidence before process cleanup: stop_server can itself fail on a
  # wedged child, and that failure must not erase the run's timing evidence.
  append_run_timing >/dev/null 2>&1 || true
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
  local reply_mode="${5:-append}"
  local conversation_mode="${6:-chat}"
  local out="$RUN_DIR/${case_name}-${model}-${effort}-${reply_mode}-${conversation_mode}.json"
  local payload mark
  wait_callback_ready
  payload="$(jq -nc --arg session "$session_id" --arg model "$model" --arg effort "$effort" --arg reply_mode "$reply_mode" --arg conversation_mode "$conversation_mode" \
    '{operator:{open_id:"e2e"},action:{value:{session:$session,action_id:"config.save"},form_value:{model:$model,effort:$effort,reply_mode:$reply_mode,conversation_mode:$conversation_mode}}}')"
  mark="$(audit_mark)"
  curl -fsS -X POST "http://$CALLBACK_ADDR/card/callback" -H 'Content-Type: application/json' -d "$payload" >"$out"
  jq -e '.ok == true and .card != null' "$out" >/dev/null
  wait_audit_since "$mark" '"Action":"config_saved"' 60
  assert_file_contains "$out" "model=\`$model\`"
  assert_file_contains "$out" "effort=\`$effort\`"
  assert_file_contains "$out" "reply mode=\`$reply_mode\`"
  assert_file_contains "$out" "conversation mode=\`$conversation_mode\`"
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
    'select(.Action == "cardkit_reply" or .Action == "markdown_reply") | select((" " + (.Detail // "") + " ") | contains(" reply_to=" + $reply_to + " ")) | .Detail' \
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
  set_conversation_mode topic_reply_at topic
  root="$(send_at "/new 请只回复 ${root_marker}，不要调用工具。")"
  wait_audit "$root.*event=result"
  reply="$(reply_thread "$root" "<at user_id=\"${BOT_OPEN_ID}\"></at> 请只回复 ${reply_marker}，不要调用工具。")"
  wait_audit "$reply.*event=result"
  file="$(mget topic_reply_at "$reply")"
  assert_file_contains "$file" "$reply_marker"
  record_message topic_reply_at root "$root"
  record_message topic_reply_at reply "$reply" "$file"
  set_conversation_mode topic_reply_at_restore chat
}

case_topic_reply_without_at_negative() {
  local root_marker="E2E_${RUN_ID}_TOPIC_NEG_ROOT"
  local neg_marker="E2E_${RUN_ID}_TOPIC_NEG"
  local root reply
  set_conversation_mode topic_reply_without_at_negative topic
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
  set_conversation_mode topic_reply_without_at_negative_restore chat
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
  set_conversation_mode scope_parallel topic
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
  set_conversation_mode scope_parallel_restore chat
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

case_reply_append() {
  require_fake_claude
  local config_msg session_id first second first_mark second_mark first_reply second_reply first_file second_file
  local topic_root topic_first topic_second topic_first_mark topic_second_mark topic_first_reply topic_second_reply
  config_msg="$(open_config reply_append)"
  session_id="config:message:${config_msg}"
  submit_config reply_append "$session_id" default low append
  assert_persisted_config default low append

  first_mark="$(audit_mark)"
  first="$(send_dm "/new E2E_${RUN_ID}_APPEND_DM_ONE E2E_REPLY_MODE_SEMANTICS")"
  wait_audit_since "$first_mark" "$first.*event=result" 60
  first_reply="$(audit_reply_message_id "$first")"
  first_file="$(mget reply_append_dm_first "$first")"
  assert_file_contains "$first_file" '"msg_type"'
  assert_file_contains "$first_file" '"post"'
  assert_file_contains "$first_file" "E2E_INTERMEDIATE_ANSWER"
  assert_file_contains "$first_file" "E2E_FINAL_ANSWER"
  assert_file_contains "$first_file" "E2E_TOOL_CALL"
  assert_file_contains "$first_file" "tokens:"
  assert_file_not_contains "$first_file" "E2E_PRIVATE_THOUGHT"
  assert_file_not_contains "$first_file" "collapsible_panel"
  second_mark="$(audit_mark)"
  second="$(send_dm "/new E2E_${RUN_ID}_APPEND_DM_TWO")"
  wait_audit_since "$second_mark" "$second.*event=result" 60
  second_reply="$(audit_reply_message_id "$second")"
  second_file="$(mget reply_append_dm_second "$second")"
  assert_file_contains "$second_file" '"msg_type"'
  assert_file_contains "$second_file" '"post"'
  if [[ "$first_reply" == "$second_reply" ]]; then
    echo "append mode reused DM reply $first_reply" >&2
    return 1
  fi

  topic_root="$(reply_topic_root)"
  topic_first_mark="$(audit_mark)"
  topic_first="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_APPEND_TOPIC_ONE")"
  wait_audit_since "$topic_first_mark" "$topic_first.*event=result" 60
  topic_first_reply="$(audit_reply_message_id "$topic_first")"
  topic_second_mark="$(audit_mark)"
  topic_second="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_APPEND_TOPIC_TWO")"
  wait_audit_since "$topic_second_mark" "$topic_second.*event=result" 60
  topic_second_reply="$(audit_reply_message_id "$topic_second")"
  if [[ "$topic_first_reply" == "$topic_second_reply" ]]; then
    echo "append mode reused topic reply $topic_first_reply" >&2
    return 1
  fi
  record_message reply_append dm_first "$first"
  record_message reply_append dm_second "$second"
  record_message reply_append topic_first "$topic_first"
  record_message reply_append topic_second "$topic_second"
  summary "- dm_replies: $first_reply, $second_reply"
  summary "- topic_replies: $topic_first_reply, $topic_second_reply"
}

case_reply_clean() {
  require_fake_claude
  local config_msg session_id dm topic_root topic file
  config_msg="$(open_config reply_clean)"
  session_id="config:message:${config_msg}"
  submit_config reply_clean "$session_id" default low append-clean-card
  assert_persisted_config default low append-clean-card

  dm="$(send_dm "/new E2E_${RUN_ID}_DM_E2E_REPLY_MODE_SEMANTICS")"
  wait_audit "$dm.*event=result" 60
  file="$(mget reply_clean_dm "$dm")"
  assert_file_contains "$file" "E2E_FINAL_ANSWER"
  assert_file_not_contains "$file" "E2E_INTERMEDIATE_ANSWER"
  assert_file_not_contains "$file" "E2E_PRIVATE_THOUGHT"
  assert_file_not_contains "$file" "E2E_TOOL_CALL"
  assert_file_not_contains "$file" "思考推理"
  assert_file_not_contains "$file" "工具调用"
  record_message reply_clean dm "$dm" "$file"

  topic_root="$(reply_topic_root)"
  topic="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_TOPIC_E2E_PROCESS_PANELS")"
  wait_audit "$topic.*event=result" 60
  file="$(mget reply_clean_topic "$topic")"
  assert_file_contains "$file" "E2E_CLEAN_ANSWER"
  assert_file_not_contains "$file" "E2E_PRIVATE_THOUGHT"
  assert_file_not_contains "$file" "E2E_TOOL_CALL"
  assert_file_not_contains "$file" "思考推理"
  assert_file_not_contains "$file" "工具调用"
  record_message reply_clean topic "$topic" "$file"
}

case_reply_latest() {
  require_fake_claude
  local config_msg session_id first second first_mark second_mark first_card second_card first_file
  local topic_root topic_first topic_second topic_first_mark topic_second_mark topic_first_card topic_second_card
  config_msg="$(open_config reply_latest)"
  session_id="config:message:${config_msg}"
  submit_config reply_latest "$session_id" default low latest-card
  assert_persisted_config default low latest-card

  first_mark="$(audit_mark)"
  first="$(send_dm "/new E2E_${RUN_ID}_LATEST_DM_ONE E2E_REPLY_MODE_SEMANTICS")"
  wait_audit_since "$first_mark" "$first.*event=result" 60
  first_card="$(audit_card_id_since "$first_mark" "$first")"
  first_file="$(mget reply_latest_dm_first "$first")"
  assert_file_contains "$first_file" "E2E_FINAL_ANSWER"
  assert_file_not_contains "$first_file" "E2E_INTERMEDIATE_ANSWER"
  assert_file_not_contains "$first_file" "E2E_PRIVATE_THOUGHT"
  assert_file_not_contains "$first_file" "E2E_TOOL_CALL"
  second_mark="$(audit_mark)"
  second="$(send_dm "/new E2E_${RUN_ID}_LATEST_DM_TWO")"
  wait_audit_since "$second_mark" "$second.*event=result" 60
  second_card="$(audit_card_id_since "$second_mark" "$second")"
  assert_no_card_create_since "$second_mark" "$second"
  if [[ "$first_card" != "$second_card" ]]; then
    echo "latest mode changed DM card: $first_card -> $second_card" >&2
    return 1
  fi

  topic_root="$(reply_topic_root)"
  topic_first_mark="$(audit_mark)"
  topic_first="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_LATEST_TOPIC_ONE")"
  wait_audit_since "$topic_first_mark" "$topic_first.*event=result" 60
  topic_first_card="$(audit_card_id_since "$topic_first_mark" "$topic_first")"
  topic_second_mark="$(audit_mark)"
  topic_second="$(reply_in_topic "$topic_root" "/new E2E_${RUN_ID}_LATEST_TOPIC_TWO")"
  wait_audit_since "$topic_second_mark" "$topic_second.*event=result" 60
  topic_second_card="$(audit_card_id_since "$topic_second_mark" "$topic_second")"
  assert_no_card_create_since "$topic_second_mark" "$topic_second"
  if [[ "$topic_first_card" != "$topic_second_card" ]]; then
    echo "latest mode changed topic card: $topic_first_card -> $topic_second_card" >&2
    return 1
  fi
  record_message reply_latest dm_first "$first"
  record_message reply_latest dm_second "$second"
  record_message reply_latest topic_first "$topic_first"
  record_message reply_latest topic_second "$topic_second"
  summary "- dm_latest_card: $first_card"
  summary "- topic_latest_card: $topic_first_card"
}

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

case_native_text_stream() {
  local normal_marker="E2E_${RUN_ID}_NATIVE_TEXT_STREAM_NORMAL"
  local stop_marker="E2E_${RUN_ID}_NATIVE_TEXT_STREAM_STOP_E2E_BLOCK"
  local normal stop normal_mark stop_mark callback_mark callback_elapsed stop_response
  local normal_stream_pattern normal_terminal_pattern

  normal_mark="$(audit_mark)"
  normal="$(send_at "/new ${normal_marker} 请只回复一篇至少四段的简短说明，每段至少两句，不要调用工具；必须在正文中包含该标记。")"
  normal_stream_pattern="($normal.*\"Action\":\"cardkit_text_stream\"|\"Action\":\"cardkit_text_stream\".*$normal)"
  normal_terminal_pattern="($normal.*event=result|event=result.*$normal)"
  wait_audit_expected_before_terminal "$normal_mark" "$normal_stream_pattern" "$normal_terminal_pattern" 60
  wait_audit_count_since "$normal_mark" "($normal.*(\"Action\":\"cardkit_text_stream\"|event=stream)|(\"Action\":\"cardkit_text_stream\"|event=stream).*$normal)" 3 60
  wait_audit_since "$normal_mark" "$normal.*event=result" 60
  if ! audit_since "$normal_mark" | jq -s -e --arg message_id "$normal" '
    [ .[] | select((.SessionID // "") | contains($message_id)) ] | to_entries as $events
    | ([ $events[] | select(.value.Action == "cardkit_text_stream") | .key ] | first) as $native
    | ([ $events[] | select(.value.Action == "cardkit_update" and (.value.Detail | contains("event=result"))) | .key ] | first) as $terminal
    | $native != null and $terminal != null and $native < $terminal
  ' >/dev/null; then
    echo "native text stream was not followed by a terminal CardKit update: $normal" >&2
    return 1
  fi

  stop_mark="$(audit_mark)"
  stop="$(send_at "/new ${stop_marker} 请只连续输出一篇至少一千字的中文说明，不要调用工具；必须在正文中包含该标记。")"
  wait_audit_since "$stop_mark" '"Action":"cardkit_text_stream"' 60
  SECONDS=0
  stop_card "claude:${E2E_E2E_CHAT_ID}:message:${stop}"
  callback_elapsed="$SECONDS"
  if (( callback_elapsed > 3 )); then
    echo "stop callback exceeded three seconds: ${callback_elapsed}s" >&2
    return 1
  fi
  stop_response="$RUN_DIR/stop-claude_${E2E_E2E_CHAT_ID//[^A-Za-z0-9._-]/_}_message_${stop//[^A-Za-z0-9._-]/_}.json"
  jq -e '
    .ok == true
    and .card.config.streaming_mode == false
    and ([.. | objects | select(.tag? == "button") | .disabled] | length > 0 and all(.[]; . == true))
  ' "$stop_response" >/dev/null
  callback_mark="$(audit_mark)"
  wait_audit_since "$stop_mark" "$stop.*event=stopped" 60
  sleep 3
  if audit_since "$callback_mark" | jq -e --arg message_id "$stop" '
    select((.SessionID // "") | contains($message_id)) | select(.Action == "cardkit_text_stream")
  ' >/dev/null; then
    echo "native preview occurred after the stop callback completed: $stop" >&2
    return 1
  fi
  if ! audit_since "$stop_mark" | jq -e --arg message_id "$stop" '
    select((.SessionID // "") | contains($message_id))
    | select(.Action == "cardkit_sequence_unknown")
  ' >/dev/null; then
    if ! audit_since "$stop_mark" | jq -e --arg message_id "$stop" '
      select((.SessionID // "") | contains($message_id))
      | select(.Action == "cardkit_update")
      | select(.Detail | contains("event=stopped") and contains("streaming=false") and contains("stop_disabled=true"))
    ' >/dev/null; then
      echo "stopped terminal card did not disable buttons or streaming: $stop" >&2
      return 1
    fi
  fi
  record_message native_text_stream normal "$normal"
  record_message native_text_stream stopped "$stop"
  summary "- native_text_stream: normal=$normal stop=$stop callback_sec=$callback_elapsed buttons=disabled streaming_mode=false"
}

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
    E2E_REPLY_STORE="$REPLY_STORE" \
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

soft_recover_after_failure() {
  local case_name="$1"
  local child_pids child_pid deadline mark reset_message
  sync_server_pid || return 1
  child_pids="$(pgrep -P "$SERVER_PID" 2>/dev/null || true)"
  if [[ -n "$child_pids" ]]; then
    # child_pids intentionally contains one whitespace-separated PID per line.
    # shellcheck disable=SC2086
    kill -TERM $child_pids >/dev/null 2>&1 || true
    deadline=$(( $(date +%s) + 10 ))
    for child_pid in $child_pids; do
      while kill -0 "$child_pid" >/dev/null 2>&1 && (( $(date +%s) < deadline )); do
        sleep 0.2
      done
      if kill -0 "$child_pid" >/dev/null 2>&1; then
        kill -KILL "$child_pid" >/dev/null 2>&1 || true
      fi
    done
  fi
  mark="$(audit_mark)"
  case "$case_name" in
    media_text_files|latest_restart_fallback)
      reset_message="$(send_dm "/new")"
      ;;
    *)
      reset_message="$(send_at "/new")"
      ;;
  esac
  wait_audit_since "$mark" "$reset_message.*event=result" 60
  summary "- recovery: soft_reset"
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
  if [[ "$status" -eq 0 && "${E2E_E2E_FORCE_FAIL_CASE:-}" == "$name" ]]; then
    echo "case forced to fail after completion for soft-recovery verification" >>"$RUN_DIR/$name.log"
    status=97
  fi
  sync_server_pid >/dev/null 2>&1 || true
  if [[ "$name" != "preflight" && "$status" -ne 0 && "$KEEP_SERVER_ON_FAIL" -eq 0 ]]; then
    set +e
    # Recovery output intentionally joins the case-specific evidence log.
    # shellcheck disable=SC2129
    echo "case failed; soft-resetting its session without restarting the bridge" >>"$RUN_DIR/$name.log"
    soft_recover_after_failure "$name" >>"$RUN_DIR/$name.log" 2>&1
    local recovery_status=$?
    set -e
    if [[ "$recovery_status" -ne 0 ]]; then
      echo "bridge soft recovery after failed case also failed" >>"$RUN_DIR/$name.log"
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
    reply_append|reply_clean|reply_latest|preview_thresholds|latest_restart_fallback)
      if e2e_cap_index dm_delivery >/dev/null 2>&1; then
        printf '%s\n' credentials lark_cli_auth bot_identity test_group dm_delivery wrapper exclusive_runtime
      else
        printf '%s\n' credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime
      fi
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

append_run_timing() {
  if [[ "$SUMMARY_INITIALIZED" -ne 1 || "$RUN_TIMING_FINALIZED" -eq 1 ]]; then
    return
  fi
  local finished_at wall_clock_sec
  finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  wall_clock_sec=$(( $(date +%s) - RUN_STARTED_EPOCH ))
  summary "- finished_at: $finished_at"
  summary "- wall_clock_sec: $wall_clock_sec"
  RUN_TIMING_FINALIZED=1
}

append_normal_summary_footer() {
  summary "## Summary"
  summary
  summary "- failures: $FAILURES"
  summary "- blocked: $BLOCKED_CASES"
  append_run_timing
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

case_capabilities_ready() {
  local name="$1" capability
  local prerequisites=()
  while IFS= read -r capability; do
    [[ -n "$capability" ]] && prerequisites+=("$capability")
  done < <(case_prerequisites "$name")
  [[ "$(e2e_cap_evaluate "$name" "${prerequisites[@]}")" == "READY" ]]
}

run_parallel_media_pair() {
  local group_case="media_images" p2p_case="media_text_files"
  local group_pid p2p_pid group_status=0 p2p_status=0 start elapsed recovery_status
  start_server_if_needed parallel-media
  prepare_media_fixtures
  log "cases $group_case + $p2p_case start in parallel"
  start="$(date +%s)"
  set +e
  ( set -e; case_media_images ) >"$RUN_DIR/$group_case.log" 2>&1 &
  group_pid=$!
  ( set -e; case_media_text_files ) >"$RUN_DIR/$p2p_case.log" 2>&1 &
  p2p_pid=$!
  wait "$group_pid" || group_status=$?
  wait "$p2p_pid" || p2p_status=$?
  set -e
  if [[ "$group_status" -eq 0 && "${E2E_E2E_FORCE_FAIL_CASE:-}" == "$group_case" ]]; then
    group_status=97
  fi
  if [[ "$p2p_status" -eq 0 && "${E2E_E2E_FORCE_FAIL_CASE:-}" == "$p2p_case" ]]; then
    p2p_status=97
  fi
  elapsed=$(( $(date +%s) - start ))
  for case_name in "$group_case" "$p2p_case"; do
    local status="$group_status"
    [[ "$case_name" == "$p2p_case" ]] && status="$p2p_status"
    if [[ "$status" -ne 0 && "$KEEP_SERVER_ON_FAIL" -eq 0 ]]; then
      set +e
      soft_recover_after_failure "$case_name" >>"$RUN_DIR/$case_name.log" 2>&1
      recovery_status=$?
      set -e
      if [[ "$recovery_status" -ne 0 ]]; then
        echo "bridge soft recovery after failed parallel case also failed" >>"$RUN_DIR/$case_name.log"
      fi
    fi
    summary "## $case_name"
    summary
    summary "- execution: parallel_isolated_media"
    if [[ "$status" -eq 0 ]]; then
      log "case $case_name passed"
      summary "- status: passed"
    else
      log "case $case_name failed; see $RUN_DIR/$case_name.log"
      summary "- status: failed"
      FAILURES=$((FAILURES + 1))
    fi
    summary "- elapsed_sec: $elapsed"
    summary "- log: $RUN_DIR/$case_name.log"
    summary
  done
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
  enable_callback
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
configure_callback_for_cases
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
  append_capability_summary
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

case_index=0
while (( case_index < ${#RUN_CASES[@]} )); do
  case_name="${RUN_CASES[$case_index]}"
  [[ -n "$case_name" ]] || { case_index=$((case_index + 1)); continue; }
  if [[ "$case_name" == "media_images" && $((case_index + 1)) -lt ${#RUN_CASES[@]} && "${RUN_CASES[$((case_index + 1))]}" == "media_text_files" ]] && \
    case_capabilities_ready media_images && case_capabilities_ready media_text_files; then
    run_parallel_media_pair
    case_index=$((case_index + 2))
    continue
  fi
  run_case_with_capabilities "$case_name"
  case_index=$((case_index + 1))
done

append_normal_summary_footer

echo "$SUMMARY"
exit "$(normal_exit_code)"
