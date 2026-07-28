#!/usr/bin/env bash
# full: wrapper_preflight

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
