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

simulate() {
  go run ./cmd/lark-agent-bridge simulate "$@"
}

