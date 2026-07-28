#!/usr/bin/env bash

if [[ -n "${BRIDGE_ASSERT_SH_LOADED:-}" ]]; then
  return 0
fi
BRIDGE_ASSERT_SH_LOADED=1

require_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if [[ "$haystack" != *"$needle"* ]]; then
    echo "verify failed: $label missing $needle" >&2
    return 1
  fi
}

require_not_contains() {
  local haystack="$1"
  local needle="$2"
  local label="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    echo "verify failed: $label unexpectedly contained $needle" >&2
    return 1
  fi
}

# 隔离 simulate 的 durable store 到套件级唯一临时目录,避免落到调用者的实时
# workspace(如 supervisor 的 ~/ws/.../.lark-agent-bridge/)读到生产 preferences
# 而污染断言(踩过:group intake 读到 supervisor 的 participated_topics override,
# all_group_messages 用例假失败)。
# 用显式 E2E_PREFERENCE_STORE 而非 E2E_DEFAULT_WORKDIR:后者会连带改写
# cfg.DefaultWorkDir 并与 simulate 的 --default-workdir flag 默认值交互,有副作用
# (实测会吞掉 group intake 的 all_group_messages);显式 store 只隔离 preference,
# 干净可靠。调用方若显式传 E2E_PREFERENCE_STORE(如 /config 用例)则以调用方为准。
BRIDGE_SIMULATE_WORKDIR="${BRIDGE_SIMULATE_WORKDIR:-$(mktemp -d "${TMPDIR:-/tmp}/bridge-simulate-XXXXXX")}"

simulate() {
  E2E_PREFERENCE_STORE="${E2E_PREFERENCE_STORE:-$BRIDGE_SIMULATE_WORKDIR/preferences.json}" \
    go run ./cmd/lark-agent-bridge simulate "$@"
}
