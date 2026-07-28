#!/usr/bin/env bash
# Thin entrypoint for the Feishu real E2E harness.
#
# Responsibilities, in order:
#   1. Declare all globals (mirrors the original e2e-real.sh 62-93 + 256-276).
#   2. Parse CLI arguments (mirrors original 95-189).
#   3. Source the shared libraries, the case registry, and every case file so
#      all function definitions become available.
#   4. Static probe sentinel: when E2E_PROBE_NO_MAIN is set, return/exit before
#      running the main sequence so tooling can load definitions in isolation.
#   5. Run the main sequence (verbatim reproduction of original 3116-3202).
#
# Dynamic case dispatch intentionally invokes case_* helpers by constructed name.
# shellcheck disable=SC2329
set -euo pipefail

# run.sh lives in scripts/e2e/, so ROOT is two levels up.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_ROOT="${E2E_STATE_ROOT:-$ROOT}"
cd "$ROOT"

# --- Global declarations (original 62-93) -----------------------------------
MODE="smoke"
KEEP_SERVER_ON_FAIL=0
LIST_CASES=0
CAPABILITY_FILTER=""
PROFILE_ARG=""
PROFILE_NAME=""
PROFILE_ENV=""
LARK_CLI_PROFILE=""
DOCTOR_MODE=0
PREFLIGHT_ONLY=0
STRICT_CAPABILITIES=0
SELECTED_CASES=()
# P-OBSERVE §3.4:environment 声明(角色/audit 路径/sender App),不含 secret。
# 默认空,兼容 --profile 直连。当声明后,observe 模式 case 读 TEST_BOT_AUDIT
# 而非临时 bridge 的 audit。
ENVIRONMENT_NAME=""
ENVIRONMENT_FILE=""
# observe 模式下,常驻 Test bot 的 audit / workdir(由 environment 声明或 CLI 覆盖)。
# 空表示未声明,run.sh 主序列 fail-closed:observe case 拒绝在缺失时启动。
OBSERVE_AUDIT=""
OBSERVE_WORKDIR=""
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
SERVER_GROUP_MESSAGE_MODE="mention_only"
SERVER_RESPOND_TO_BOTS="false"
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
                      [--mode smoke|full] [--case name ...] [--capability name]
                      [--list-cases]

Options:
  --profile name              use one developer-local E2E profile.
  --environment name          load docs/environments/<name>.env (declares role
                              mapping / TEST_BOT_AUDIT / TEST_BOT_WORKDIR for
                              observe-mode cases). E2E_PROFILE from the env
                              becomes the default --profile if none is given.
  --doctor                    run static capability checks without sending messages.
  --preflight-only            run static checks and real active canaries, then exit.
  --strict-capabilities       return 3 when a required capability is blocked.
  --mode smoke|full           smoke runs core cases; full adds revoke cases.
  --case name                 run one case; repeat to run multiple cases.
  --capability name           filter cases to a single product capability group
                              (main_link|commands|config|lifecycle|queue|group|
                              media|recovery|render|reply_mode). Combines with
                              --list-cases to inspect the group. Mutually
                              exclusive with --case.
  --list-cases                print supported cases and exit.
  --default-workdir path      default workdir passed to bridge serve (controlled cases).
  --run-dir path              evidence directory. Defaults to .cache/e2e/real-<timestamp>.
  --keep-server-on-fail       leave bridge running after a failure for diagnosis.
  -h, --help                  show this help.

Environment:
  .lark-agent-bridge/e2e.env is loaded automatically when present.
  Required for real execution: LARK_APP_ID, LARK_APP_SECRET, E2E_E2E_CHAT_ID.
  Optional: LARK_BOT_OPEN_ID, E2E_REAL_E2E_TIMEOUT_SEC, E2E_REAL_E2E_FAKE_CLAUDE=1.
  Feature case group_message_intake is explicit-only and restores mention_only before exit.
  Media file cases also require E2E_REAL_E2E_P2P_CHAT_ID for the user's direct chat with this bot.
USAGE
}

# --- Argument parsing (original 95-189) -------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)
      PROFILE_ARG="${2:-}"
      shift 2
      ;;
    --environment)
      ENVIRONMENT_NAME="${2:-}"
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
    --capability)
      CAPABILITY_FILTER="${2:-}"
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

# --- Environment declaration(P-OBSERVE §3.4)---------------------------------
# environment 是"角色 + 路径"的结构声明,不含 secret。若声明,读入到本 shell,让
# observe 模式 case 直接引用 TEST_BOT_AUDIT / TEST_BOT_WORKDIR。E2E_PROFILE 字段
# 作为 --profile 的默认(仍可被显式 --profile 覆盖)。
if [[ -n "$ENVIRONMENT_NAME" ]]; then
  ENVIRONMENT_FILE="$ROOT/docs/environments/$ENVIRONMENT_NAME.env"
  if [[ ! -f "$ENVIRONMENT_FILE" ]]; then
    echo "unknown environment: $ENVIRONMENT_NAME (expected $ENVIRONMENT_FILE)" >&2
    exit 2
  fi
  # shellcheck disable=SC1090
  source "$ENVIRONMENT_FILE"
  # environment 声明的 audit / workdir 是**结构性**信息,写死 environment 里,
  # observe 模式 case 一律读它。
  OBSERVE_AUDIT="${TEST_BOT_AUDIT:-}"
  OBSERVE_WORKDIR="${TEST_BOT_WORKDIR:-}"
  # environment 引用的默认 profile,仅在未显式 --profile 时生效。
  if [[ -z "$PROFILE_ARG" && -n "${E2E_PROFILE:-}" ]]; then
    PROFILE_ARG="$E2E_PROFILE"
  fi
fi

# --- Source libraries, registry, and cases ----------------------------------
# The original single-file script sourced only the profile/capability libs at
# the top; every other helper was defined inline. After the split, those helpers
# live under scripts/e2e/lib and scripts/e2e/cases and are assembled here.
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/e2e-profile.sh"
# shellcheck disable=SC1091
source "$ROOT/scripts/lib/e2e-capabilities.sh"
# shellcheck disable=SC1091
source "$ROOT/scripts/e2e/lib/env.sh"
# shellcheck disable=SC1091
source "$ROOT/scripts/e2e/lib/server.sh"
# shellcheck disable=SC1091
source "$ROOT/scripts/e2e/lib/lark.sh"
# shellcheck disable=SC1091
source "$ROOT/scripts/e2e/registry.sh"
for _case_file in "$ROOT"/scripts/e2e/cases/*/*.sh; do
  # shellcheck disable=SC1090
  source "$_case_file"
done
unset _case_file

# --- Orchestration helpers ---------------------------------------------------
# These functions coordinate case selection, capability gating, summary
# authoring, and the parallel media special-case. They stay in run.sh because
# they read and mutate the global run state declared above (RUN_CASES, MODE,
# FAILURES, BLOCKED_CASES, SUMMARY, ...) and drive the main sequence directly.
# all_cases / cases_for_mode / configure_callback_for_cases / case_prerequisites
# are registry-derived and live in registry.sh instead.

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

summary_init() {
  {
    echo "# Feishu Real E2E"
    echo
    echo "- run_id: $RUN_ID"
    echo "- generated_at: $RUN_STARTED_AT"
    echo "- started_at: $RUN_STARTED_AT"
    echo "- repo: $ROOT"
    echo "- mode: $MODE"
    echo "- environment: ${ENVIRONMENT_NAME:-<none>}"
    echo "- run_dir: $RUN_DIR"
    echo "- audit_controlled: $AUDIT"
    if [[ -n "$OBSERVE_AUDIT" ]]; then
      echo "- audit_observe: $OBSERVE_AUDIT"
    else
      echo "- audit_observe: <none>"
    fi
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

record_static_group_intake_features() {
  e2e_cap_record group_message_intake SKIPPED explicit_case_not_run "real group intake was not exercised by static doctor" "run --case group_message_intake with fake Claude"
  e2e_cap_record scope_incremental_grant SKIPPED dedicated_profile_required "grant completion needs a profile that initially lacks im:message.group_msg" "use a dedicated missing-scope profile and complete its authorization card"
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
  record_static_group_intake_features
  e2e_cap_write_json "$CAPABILITIES_JSON"
  e2e_cap_write_summary "$SUMMARY"
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

run_case() {
  local name="$1"
  # real_agent_only case 在 fake claude 模式下无法产生有意义结果(fake 强制吐罐头
  # JSON,断言 AI 实际行为无从谈起)。SKIP 而非 FAIL,让 L2 full 回归(fake)干净放行,
  # L3 canary(env -u E2E_REAL_E2E_FAKE_CLAUDE)才真跑。
  if [[ "${E2E_CASE_REAL_AGENT_ONLY[$name]:-0}" == "1" && "$USE_FAKE_CLAUDE" == "1" ]]; then
    log "case $name skipped (real_agent_only, fake claude enabled)"
    summary "## $name"
    summary "- status: skipped_real_agent_only"
    return 0
  fi
  # P-OBSERVE §3.3:按 execution_mode 分派。
  # observe:纯观察常驻 Test bot,不起临时 bridge;AUDIT 指向 Test 的 audit.jsonl。
  # controlled:原路径,e2e 起自己的临时 bridge。
  local exec_mode saved_audit="" saved_default_workdir=""
  exec_mode="$(case_execution_mode "$name")"
  if [[ "$exec_mode" == "observe" ]]; then
    if [[ -z "$OBSERVE_AUDIT" ]]; then
      log "case $name(observe)需要 environment 提供 TEST_BOT_AUDIT;--environment 未声明,SKIP"
      summary "## $name"
      summary "- status: skipped_observe_env_missing"
      summary "- reason: environment 未声明 TEST_BOT_AUDIT(观察者模式无法读常驻 Test audit)"
      summary "- fix: 加 --environment <name>,该 env 里写 TEST_BOT_AUDIT"
      return 0
    fi
    # 切换 lark.sh 全套 helper 依赖的 $AUDIT 到常驻 Test。全局变量恢复由函数末尾处理。
    saved_audit="$AUDIT"
    AUDIT="$OBSERVE_AUDIT"
    # observe 模式不起临时 bridge,也不改临时 workdir(case 只发消息、读 audit)。
    log "case $name start (observe: audit=$AUDIT)"
    summary "## $name"
    summary
    summary "- execution_mode: observe"
    summary "- audit: $AUDIT"
  else
    start_server_if_needed "$name"
    log "case $name start"
    summary "## $name"
    summary
    summary "- execution_mode: controlled"
  fi
  local start status
  start="$(date +%s)"
  set +e
  ( set -e; "case_$name" ) >"$RUN_DIR/$name.log" 2>&1
  status=$?
  set -e
  # 无论成功失败,先恢复全局 AUDIT(observe 模式借用了 Test 的 audit,后续 case 可能是 controlled 要用回临时 bridge 的)。
  if [[ "$exec_mode" == "observe" ]]; then
    AUDIT="$saved_audit"
  fi
  if [[ "$status" -eq 0 && "${E2E_E2E_FORCE_FAIL_CASE:-}" == "$name" ]]; then
    echo "case forced to fail after completion for soft-recovery verification" >>"$RUN_DIR/$name.log"
    status=97
  fi
  # observe 模式不管临时 bridge 生命周期;controlled 保留原有 sync + soft_recover。
  if [[ "$exec_mode" == "controlled" ]]; then
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
    run_capability_case media_image media_images || true
  fi
  if capability_prerequisites_ready media_file dm_delivery p2p_chat; then
    run_capability_case media_file media_text_files || true
  fi
}

# --capability 与 --case 互斥:capability 是"按能力组批量选",case 是"逐个选",
# 混用会让预期不明。检测早于 --list-cases 分支,便于 --list-cases --capability
# 直接列该组下的案例。
if [[ -n "$CAPABILITY_FILTER" && "${#SELECTED_CASES[@]}" -gt 0 ]]; then
  echo "错误: --capability 与 --case 互斥,只能使用其中一个" >&2
  exit 2
fi

# --capability filter: expand to the case names matching that capability, then
# feed them into SELECTED_CASES so the rest of the pipeline (list-cases, mode
# selection, prerequisite dispatch) reuses the existing --case path.
if [[ -n "$CAPABILITY_FILTER" ]]; then
  while IFS= read -r _cap_case; do
    SELECTED_CASES+=("$_cap_case")
  done < <(cases_for_capability "$CAPABILITY_FILTER")
  if (( ${#SELECTED_CASES[@]} == 0 )); then
    echo "错误: --capability '$CAPABILITY_FILTER' 未匹配任何 case (核对拼写: main_link|commands|config|lifecycle|queue|group|media|recovery|render|reply_mode|internal)" >&2
    exit 2
  fi
fi

# --list-cases prints the case union and exits (kept here, next to the array
# helpers it depends on; matches the original placement before profile setup).
# When --capability 也在,SELECTED_CASES 已被填充,这里直接列过滤后的集合。
if [[ "$LIST_CASES" -eq 1 ]]; then
  if (( ${#SELECTED_CASES[@]} > 0 )); then
    printf '%s\n' "${SELECTED_CASES[@]}"
  else
    all_cases
  fi
  exit 0
fi

# --- Static probe sentinel ---------------------------------------------------
# Tooling that only needs the function/registry definitions (e.g. the P1
# equivalence probe) sources this file with E2E_PROBE_NO_MAIN set and stops
# here, before any profile selection or side effects run.
if [[ -n "${E2E_PROBE_NO_MAIN:-}" ]]; then
  return 0 2>/dev/null || exit 0
fi

# --- Cleanup trap (原巨石 e2e-real.sh:823) -----------------------------------
# 必须紧跟哨兵之后、任何会 fork bridge serve 的操作之前注册。原巨石在函数区
# 结束后的顶层直接 `trap cleanup EXIT`;P1 拆分时 cleanup 函数搬进 lib/server.sh
# 但主 trap 注册漏了,导致外壳 SIGTERM/超时时 bridge serve 子进程成僵尸继续占
# App 的 wss 连接,后续 run 抢同 App wss 时飞书 gateway 消息投递紊乱(实测踩过)。
trap cleanup EXIT

# --- Profile selection and derived paths (original 240-287) ------------------
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
PARTICIPATED_TOPICS_STORE="$STATE_DIR/participated-topics.json"
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

# --- Main sequence (verbatim reproduction of original 3116-3202) -------------
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
require_env LARK_APP_ID
require_env LARK_APP_SECRET
require_env E2E_E2E_CHAT_ID
# 只在存在 controlled 用例时才编译临时 bridge binary,并起 pgrep/ps 依赖。
# observe-only 跑一律走常驻 Test,零构建、零本地 bridge 进程管理。
if any_selected_case_is_controlled; then
  require_cmd go
  require_cmd pgrep
  require_cmd ps
  go build -o "$SERVER_BIN" ./cmd/lark-agent-bridge
fi
fetch_bot_open_id
summary "- bot_open_id: ${BOT_OPEN_ID:0:6}...${BOT_OPEN_ID: -4}"
summary
if any_selected_case_is_controlled; then
  summary "- controlled_bridge_binary: $SERVER_BIN"
else
  summary "- controlled_bridge_binary: not built (observe-only run)"
fi
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
