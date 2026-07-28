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
# Ordered case names, preserving registration order per tier.
E2E_SMOKE_ORDER=()
E2E_FULL_ORDER=()
E2E_FEATURE_ORDER=()

register_case() {
  # register_case <name> <tier> <needs_callback> <prereq_base> [prereq_optional] [real_agent_only]
  # real_agent_only=1: case 断言真 agent 行为(如 bridge 指令注入是否改变 AI 输出),
  #   fake claude 无法产生有意义结果,USE_FAKE_CLAUDE=1 时应 SKIP 而非 FAIL。
  local name="$1" tier="$2" needs_callback="$3" prereq_base="$4"
  local prereq_optional="${5:-}" real_agent_only="${6:-0}"
  E2E_CASE_TIER["$name"]="$tier"
  E2E_CASE_NEEDS_CALLBACK["$name"]="$needs_callback"
  E2E_CASE_PREREQ_BASE["$name"]="$prereq_base"
  E2E_CASE_PREREQ_OPTIONAL["$name"]="$prereq_optional"
  E2E_CASE_REAL_AGENT_ONLY["$name"]="$real_agent_only"
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
# SMOKE tier (order matches original SMOKE_CASES).
register_case new_basic                        smoke   0 "$_E2E_DEFAULT_PREREQ"
register_case streaming_card                   smoke   0 "$_E2E_DEFAULT_PREREQ"
register_case help                             smoke   0 "$_E2E_DEFAULT_PREREQ"
register_case status                           smoke   0 "$_E2E_DEFAULT_PREREQ"
register_case workdir_existing                 smoke   0 "$_E2E_DEFAULT_PREREQ"
register_case topic_reply_at                   smoke   0 "$_E2E_DEFAULT_PREREQ"
register_case topic_reply_without_at_negative  smoke   0 "$_E2E_DEFAULT_PREREQ"

# FULL tier (order matches original FULL_EXTRA_CASES).
register_case session_restart_context          full    0 "$_E2E_DEFAULT_PREREQ"
register_case restart_queued_cancel            full    0 "$_E2E_DEFAULT_PREREQ"
register_case restart_running_interrupted      full    0 "$_E2E_DEFAULT_PREREQ"
# debounce_dm: DM-only canary; no test_group, no optional downgrade.
register_case debounce_dm                      full    0 "credentials lark_cli_auth bot_identity wrapper exclusive_runtime"
register_case debounce_group                   full    0 "$_E2E_DEFAULT_PREREQ"
register_case busy_merge                       full    0 "$_E2E_DEFAULT_PREREQ"
register_case queue_full                       full    0 "$_E2E_DEFAULT_PREREQ"
register_case scope_parallel                   full    0 "$_E2E_DEFAULT_PREREQ"
# stop_preserves_queue / config_* / requested_actual_model: real card-action cases.
register_case stop_preserves_queue             full    1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime"
# recall family: optional recall_event_delivery downgrade.
register_case recall_state                     full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" recall_event_delivery
register_case message_revoke                   full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" recall_event_delivery
register_case message_revoke_pending_workdir   full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" recall_event_delivery
register_case message_revoke_queued_input      full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" recall_event_delivery
# media image family: base ends with test_group + wrapper + exclusive_runtime, optional media_image inserted before wrapper.
register_case media_attachment_only            full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" media_image
register_case media_images                     full    0 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" media_image
# media file family: p2p_chat based, optional media_file.
register_case media_text_files                 full    0 "credentials lark_cli_auth bot_identity p2p_chat wrapper exclusive_runtime" media_file
register_case media_partial                    full    0 "credentials lark_cli_auth bot_identity p2p_chat wrapper exclusive_runtime" media_file
register_case media_rejected                   full    0 "credentials lark_cli_auth bot_identity p2p_chat wrapper exclusive_runtime" media_file
register_case config_roundtrip                 full    1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime"
register_case config_reset                     full    1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime"
register_case config_frozen_queue              full    1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime"
register_case requested_actual_model           full    1 "credentials lark_cli_auth bot_identity test_group card_action wrapper exclusive_runtime"
# reply_append/reply_clean/reply_latest/preview_thresholds/latest_restart_fallback:
# DM-delivery cases. In the original case_prerequisites these matched the
# dm_delivery branch FIRST (line 2816), which short-circuited before the later
# card_action branch (line 2844) could ever apply to them -- so their reachable
# prerequisite was always the dm_delivery bundle with an optional dm_delivery
# downgrade, never card_action. Their function bodies confirm it: they deliver
# via send_dm / reply_in_topic and never click an interactive card (no
# stop_card), so dm_delivery is the correct and only prerequisite. They still
# need the injected callback (open_config/submit_config), hence needs_callback=1.
register_case reply_append                     full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery
register_case reply_clean                      full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery
register_case reply_latest                     full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery
register_case preview_thresholds               full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery
# native_text_stream: needs the injected callback but its prerequisites fell to
# the default branch in the original (never listed in the card_action prereq
# group), so it keeps the plain default bundle -- asymmetric on purpose.
register_case native_text_stream               full    1 "$_E2E_DEFAULT_PREREQ"
register_case reaction_lifecycle               full    0 "$_E2E_DEFAULT_PREREQ"
register_case latest_restart_fallback          full    1 "credentials lark_cli_auth bot_identity test_group wrapper exclusive_runtime" dm_delivery
register_case wrapper_preflight                full    0 "$_E2E_DEFAULT_PREREQ"
register_case quote_readback                   full    0 "$_E2E_DEFAULT_PREREQ"

# P3 · Bridge instructions injection behavior verification (real agent only).
# 验证 bridge 注入的 feishu-runtime-v3 指令对真 Claude 行为的实际影响。
# 图片 B 主题(B1/B2/B4/B8)+ 定时 C 主题(C1/C2/C3/C5)。fake claude 模式下 SKIP。
# 6-th positional field = real_agent_only=1。
register_case inject_image_intent              full    0 "$_E2E_DEFAULT_PREREQ" "" 1
register_case inject_image_no_intent           full    0 "$_E2E_DEFAULT_PREREQ" "" 1
register_case inject_schedule_propose          full    0 "$_E2E_DEFAULT_PREREQ" "" 1
register_case inject_schedule_timer            full    0 "$_E2E_DEFAULT_PREREQ" "" 1

# FEATURE tier (explicit-only).
register_case group_message_intake             feature 0 "$_E2E_DEFAULT_PREREQ"

# INTERNAL cases: never selected by a mode, but carry prerequisites used by the
# active capability preflight and its canaries. preflight has no wrapper prereq.
register_case preflight                        internal 0 "credentials lark_cli_auth bot_identity test_group exclusive_runtime"

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

# configure_callback_for_cases: enable the injected callback transport when any
# selected case is registered needs_callback=1.
configure_callback_for_cases() {
  local case_name
  CALLBACK_ADDR=""
  for case_name in "${RUN_CASES[@]}"; do
    if [[ "${E2E_CASE_NEEDS_CALLBACK[$case_name]:-0}" == "1" ]]; then
      enable_callback
      return
    fi
  done
}

# case_prerequisites: print the required capabilities for a case, one per line.
# Base capabilities come straight from the registry. When the case declares an
# optional capability AND `e2e_cap_index` reports it available at runtime, the
# optional capability is inserted immediately before `wrapper` (matching the
# original ordering, e.g. "... test_group dm_delivery wrapper exclusive_runtime"
# and "... p2p_chat media_file wrapper exclusive_runtime"). Cases whose base has
# no wrapper token (debounce_dm) never carry an optional capability, so the
# insertion point is always well defined.
case_prerequisites() {
  local name="$1"
  local base="${E2E_CASE_PREREQ_BASE[$name]:-$_E2E_DEFAULT_PREREQ}"
  local optional="${E2E_CASE_PREREQ_OPTIONAL[$name]:-}"
  if [[ -n "$optional" ]] && e2e_cap_index "$optional" >/dev/null 2>&1; then
    local token out=()
    for token in $base; do
      if [[ "$token" == "wrapper" ]]; then
        out+=("$optional")
      fi
      out+=("$token")
    done
    printf '%s\n' "${out[@]}"
  else
    printf '%s\n' $base
  fi
}
