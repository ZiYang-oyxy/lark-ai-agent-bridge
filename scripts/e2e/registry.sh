#!/usr/bin/env bash
# Single source of truth for E2E case metadata.
#
# The original e2e-real.sh scattered case facts across three places: the tier
# arrays (SMOKE_CASES / FULL_EXTRA_CASES / FEATURE_CASES), the
# configure_callback_for_cases white-list, and the case_prerequisites dispatch.
# This registry collapses them into one declaration per case; the array
# builders, callback wiring, and prerequisite resolution below all derive from
# it. Defines data + functions only; no side effects at source time.
#
# Per-case fields (declared via register_case):
#   tier            smoke | full | feature | internal
#                     - smoke/full drive --mode selection (SMOKE_CASES, FULL_EXTRA_CASES).
#                     - feature is explicit-only (--case), never auto-selected.
#                     - internal cases (preflight) are never listed by a mode but
#                       still carry prerequisites for the active capability probe.
#   needs_callback  1 when the case exercises the gateway-injected card action
#                     transport (configure_callback_for_cases must enable_callback).
#   prereq_base     space-separated capabilities always required.
#   prereq_optional single optional capability that is added ONLY when
#                     `e2e_cap_index <cap>` reports it available (runtime
#                     downgrade preserved from the original case_prerequisites).
#                     Empty when the case has no optional downgrade.
#   real_agent_only 1 when the case asserts real agent (Claude/Codex) behavior
#                     that fake claude cannot reproduce (bridge instruction
#                     injection etc.). SKIPPED in USE_FAKE_CLAUDE=1 rather than
#                     forced to FAIL.
#   execution_mode  observe | controlled(默认 observe)。
#                     - observe: 对外部常驻 Test bot 发消息 + 读 Test 的 audit,不起临时 bridge。
#                       消除 wss gateway 冷却、僵尸进程、E2E_* 巨块。适合纯观察类
#                       (help/status/quote_readback/inject_image_*)。
#                     - controlled: 沿用原临时 bridge 模式(e2e-real 起自己的 serve),
#                       用于需要 restart_server / mutate config / fake claude 定制回复的用例。
#                     决策依据见 docs/plans/2026-07-28-P-OBSERVE-case-classification.md。
#   capability      main_link | commands | config | lifecycle | queue | group |
#                   media | recovery | render | reply_mode | internal
#                     10 product capabilities from
#                     docs/plans/2026-07-28-test-framework-overhaul-P-CASES.md §3.
#                     Enables --capability <name> filtering. Case-to-capability
#                     mapping lives here so `--list-cases --capability <name>`
#                     is a single source of truth.
#
# The tier ORDER within smoke/full below reproduces the original arrays exactly
# (list order is load-bearing: --list-cases output and the media adjacency
# special-case both depend on it).

# --- Registry storage -------------------------------------------------------
declare -A E2E_CASE_TIER=()
declare -A E2E_CASE_NEEDS_CALLBACK=()
declare -A E2E_CASE_PREREQ_BASE=()
declare -A E2E_CASE_PREREQ_OPTIONAL=()
declare -A E2E_CASE_REAL_AGENT_ONLY=()
declare -A E2E_CASE_EXECUTION_MODE=()
declare -A E2E_CASE_CAPABILITY=()
# Ordered case names, preserving registration order per tier.
E2E_SMOKE_ORDER=()
E2E_FULL_ORDER=()
E2E_FEATURE_ORDER=()

# Known capability names. Mirrors P-CASES plan §3; keep in sync when adding
# new groups. Cases must declare a capability from this list.
_E2E_KNOWN_CAPABILITIES=(main_link commands config lifecycle queue group media recovery render reply_mode internal)

register_case() {
  # register_case <name> <tier> <needs_callback> <prereq_base> \
  #               [prereq_optional] [real_agent_only] [execution_mode] [capability]
  # real_agent_only=1: case 断言真 agent 行为(如 bridge 指令注入是否改变 AI 输出),
  #   fake claude 无法产生有意义结果,USE_FAKE_CLAUDE=1 时应 SKIP 而非 FAIL。
  # execution_mode:observe(默认) 走外部常驻 Test bot,controlled 起临时 bridge。
  # capability:P-CASES §3 十组能力之一(main_link/commands/.../internal),
  #   支持 --capability 过滤;不允许空值,防止归类空洞。
  local name="$1" tier="$2" needs_callback="$3" prereq_base="$4"
  local prereq_optional="${5:-}" real_agent_only="${6:-0}" execution_mode="${7:-observe}"
  local capability="${8:-}"
  case "$execution_mode" in
    observe|controlled) ;;
    *) echo "register_case: unknown execution_mode '$execution_mode' for '$name'" >&2; exit 2 ;;
  esac
  if [[ -z "$capability" ]]; then
    echo "register_case: capability is required for '$name'" >&2; exit 2
  fi
  local cap_valid=0
  for known in "${_E2E_KNOWN_CAPABILITIES[@]}"; do
    if [[ "$capability" == "$known" ]]; then cap_valid=1; break; fi
  done
  if (( cap_valid == 0 )); then
    echo "register_case: unknown capability '$capability' for '$name' (allowed: ${_E2E_KNOWN_CAPABILITIES[*]})" >&2; exit 2
  fi
  E2E_CASE_TIER["$name"]="$tier"
  E2E_CASE_NEEDS_CALLBACK["$name"]="$needs_callback"
  E2E_CASE_PREREQ_BASE["$name"]="$prereq_base"
  E2E_CASE_PREREQ_OPTIONAL["$name"]="$prereq_optional"
  E2E_CASE_REAL_AGENT_ONLY["$name"]="$real_agent_only"
  E2E_CASE_EXECUTION_MODE["$name"]="$execution_mode"
  E2E_CASE_CAPABILITY["$name"]="$capability"
  case "$tier" in
    smoke) E2E_SMOKE_ORDER+=("$name") ;;
    full) E2E_FULL_ORDER+=("$name") ;;
    feature) E2E_FEATURE_ORDER+=("$name") ;;
    internal) ;;
    *) echo "register_case: unknown tier '$tier' for '$name'" >&2; exit 2 ;;
  esac
}

# Convenience capability bundles (mirrors the strings the original printed).
_E2E_DEFAULT_PREREQ="credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime"

# --- Case declarations ------------------------------------------------------
# 每条尾部为 execution_mode + capability。
# execution_mode 归类判据见 docs/plans/2026-07-28-P-OBSERVE-case-classification.md。
# capability 归类判据见 docs/plans/2026-07-28-test-framework-overhaul-P-CASES.md §3-§4。
#
# SMOKE tier (order matches original SMOKE_CASES; 4 case removed and 2 renamed
# in P-CASES Phase 2, see plan §4.2-§4.3 for the mapping table).
register_case new_basic                        smoke   0 "$_E2E_DEFAULT_PREREQ" "" 0 observe    main_link
register_case streaming_card                   smoke   0 "$_E2E_DEFAULT_PREREQ" "" 0 observe    render
register_case help                             smoke   0 "$_E2E_DEFAULT_PREREQ" "" 0 observe    commands
register_case status                           smoke   0 "$_E2E_DEFAULT_PREREQ" "" 0 observe    commands
register_case workdir_existing                 smoke   0 "$_E2E_DEFAULT_PREREQ" "" 0 observe    lifecycle
# topic_mode_*:用 set_conversation_mode 改 preference → open_config → submit_config
# 需要 callback (P-OBSERVE 分类冲突,Phase 2 修正为 controlled)。见 P-CASES §4.5。
register_case topic_mode_mention_required      smoke   1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime" "" 0 controlled group
register_case topic_mode_non_mention_ignored   smoke   1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime" "" 0 controlled group

# FULL tier (order matches original FULL_EXTRA_CASES).
# controlled:require_fake_claude + restart_server + assert_fake_batch_contains
register_case session_restart_context          full    0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled recovery
register_case revoke_queued_on_restart         full    0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled recovery
register_case interrupt_running_on_restart     full    0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled recovery
# debounce_dm: DM-only canary; no test_group, no optional downgrade.
register_case debounce_dm                      full    0 "credentials lark_cli_auth bot_identity wrapper exclusive_runtime" "" 0 controlled queue
register_case debounce_group                   full    0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled queue
register_case busy_merge                       full    0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled queue
register_case queue_full                       full    0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled queue
register_case scope_parallel                   full    0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled queue
# stop_preserves_queue / config_*: real card-action cases.
register_case stop_preserves_queue             full    1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime" "" 0 controlled queue
# recall family: optional recall_event_delivery downgrade.
register_case recall_state                     full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" recall_event_delivery 0 controlled recovery
register_case message_revoke                   full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" recall_event_delivery 0 controlled recovery
register_case revoke_during_workdir_prompt     full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" recall_event_delivery 0 controlled recovery
register_case revoke_during_queue              full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" recall_event_delivery 0 controlled recovery
# media image family: base ends with test_group + wrapper + exclusive_runtime, optional media_image inserted before wrapper.
# media_attachment_only removed in P-CASES Phase 2 (media_images/text_files 已覆盖此边界).
register_case media_images                     full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" media_image 0 controlled media
# media file family: p2p_chat based, optional media_file.
register_case media_text_files                 full    0 "credentials lark_cli_auth bot_identity p2p_chat wrapper exclusive_runtime" media_file 0 controlled media
register_case media_partial                    full    0 "credentials lark_cli_auth bot_identity p2p_chat wrapper exclusive_runtime" media_file 0 controlled media
register_case media_rejected                   full    0 "credentials lark_cli_auth bot_identity p2p_chat wrapper exclusive_runtime" media_file 0 controlled media
# config_frozen_queue removed in P-CASES Phase 2 (队列语义,由 busy_merge/queue_full 覆盖).
# requested_actual_model removed in P-CASES Phase 2 (status card 顶栏已由 status case 覆盖).
register_case config_roundtrip                 full    1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime" "" 0 controlled config
register_case config_reset                     full    1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime" "" 0 controlled config
# reply_append/reply_clean/reply_latest/preview_thresholds/session_resume_after_restart:
# DM-delivery cases. In the original case_prerequisites these matched the
# dm_delivery branch FIRST (line 2816), which short-circuited before the later
# card_action branch (line 2844) could ever apply to them -- so their reachable
# prerequisite was always the dm_delivery bundle with an optional dm_delivery
# downgrade, never card_action. Their function bodies confirm it: they deliver
# via send_dm / reply_in_topic and never click an interactive card (no
# stop_card), so dm_delivery is the correct and only prerequisite. They still
# need the injected callback (open_config/submit_config), hence needs_callback=1.
register_case reply_append                     full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery 0 controlled reply_mode
register_case reply_clean                      full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery 0 controlled reply_mode
register_case reply_latest                     full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery 0 controlled reply_mode
register_case preview_thresholds               full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery 0 controlled render
# native_text_stream: needs the injected callback but its prerequisites fell to
# the default branch in the original (never listed in the card_action prereq
# group), so it keeps the plain default bundle -- asymmetric on purpose.
register_case native_text_stream               full    1 "$_E2E_DEFAULT_PREREQ" "" 0 controlled render
register_case reaction_lifecycle               full    0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled render
register_case session_resume_after_restart     full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery 0 controlled recovery
# wrapper_preflight removed in P-CASES Phase 2 (capability check 已在 doctor 阶段覆盖).
register_case quote_readback                   full    0 "$_E2E_DEFAULT_PREREQ" "" 0 observe    group

# P3 · Bridge instructions injection behavior verification (real agent only).
# 验证 bridge 注入的 feishu-runtime-v3 指令对真 Claude 行为的实际影响。
# 图片 B 主题(B1/B2/B4/B8)+ 定时 C 主题(C1/C2/C3/C5)。fake claude 模式下 SKIP。
# inject_image_*:纯观察 output_images_completed audit,不污染 supervisor → observe。
# inject_schedule_*:会创建 draft 到 supervisor 生产 schedule store,污染 → controlled(P-OBSERVE §5 已知坑决策)。
register_case inject_image_intent              full    0 "$_E2E_DEFAULT_PREREQ" "" 1 observe    main_link
register_case inject_image_no_intent           full    0 "$_E2E_DEFAULT_PREREQ" "" 1 observe    main_link
register_case inject_schedule_propose          full    0 "$_E2E_DEFAULT_PREREQ" "" 1 controlled main_link
register_case inject_schedule_timer            full    0 "$_E2E_DEFAULT_PREREQ" "" 1 controlled main_link

# FEATURE tier (explicit-only).
# group_message_intake 改 SERVER_GROUP_MESSAGE_MODE + rm PREFERENCE_STORE → controlled。
register_case group_message_intake             feature 0 "$_E2E_DEFAULT_PREREQ" "" 0 controlled group

# INTERNAL cases: never selected by a mode, but carry prerequisites used by the
# active capability preflight and its canaries. preflight has no wrapper prereq.
# preflight 是环境预检 utility,不属常规二分,标 controlled 表示由临时 bridge 承载。
register_case preflight                        internal 0 "credentials lark_cli_auth bot_identity test_group exclusive_runtime" "" 0 controlled internal

# --- Derivations ------------------------------------------------------------
# all_cases: every case selectable via --case (smoke + full + feature), in the
# original union order. Internal cases are intentionally excluded (they were
# never members of the three arrays).
all_cases() {
  printf '%s\n' "${E2E_SMOKE_ORDER[@]}"
  printf '%s\n' "${E2E_FULL_ORDER[@]}"
  printf '%s\n' "${E2E_FEATURE_ORDER[@]}"
}

cases_for_mode() {
  if [[ "${#SELECTED_CASES[@]}" -gt 0 ]]; then
    printf '%s\n' "${SELECTED_CASES[@]}"
    return
  fi
  printf '%s\n' "${E2E_SMOKE_ORDER[@]}"
  if [[ "$MODE" == "full" ]]; then
    printf '%s\n' "${E2E_FULL_ORDER[@]}"
  fi
}

# case_execution_mode: print the case's execution_mode (observe|controlled).
# Defaults to observe when the case is unknown (matches register_case default).
case_execution_mode() {
  local name="$1"
  printf '%s\n' "${E2E_CASE_EXECUTION_MODE[$name]:-observe}"
}

# case_capability: print the case's capability group (or empty when unknown).
case_capability() {
  local name="$1"
  printf '%s\n' "${E2E_CASE_CAPABILITY[$name]:-}"
}

# cases_for_capability: print all cases (smoke+full+feature order) matching
# the given capability. Empty argument prints nothing (never a wildcard).
cases_for_capability() {
  local want="$1"
  [[ -z "$want" ]] && return
  local name
  while IFS= read -r name; do
    if [[ "${E2E_CASE_CAPABILITY[$name]:-}" == "$want" ]]; then
      printf '%s\n' "$name"
    fi
  done < <(all_cases)
}
