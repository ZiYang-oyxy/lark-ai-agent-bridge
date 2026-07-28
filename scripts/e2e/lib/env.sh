#!/usr/bin/env bash
# server_env construction, isolated for capability sandboxing (see 5b).
# Defines functions only; no side effects at source time.

# build_server_env populates the caller-declared `server_env` array with the
# environment the bridge `serve` process runs under. Extracted verbatim from the
# original start_server_if_needed body so callers keep identical semantics.
build_server_env() {
  server_env=(
    "PATH=$FAKE_BIN_DIR:$PATH"
    "E2E_AUDIT_LOG=$AUDIT"
    "E2E_SESSION_STORE=$SESSION_STORE"
    "E2E_PREFERENCE_STORE=$PREFERENCE_STORE"
    "E2E_REPLY_STORE=$REPLY_STORE"
    "E2E_PARTICIPATED_TOPICS_STORE=$PARTICIPATED_TOPICS_STORE"
    "E2E_GROUP_MESSAGE_MODE=$SERVER_GROUP_MESSAGE_MODE"
    "E2E_RESPOND_TO_BOTS=$SERVER_RESPOND_TO_BOTS"
    "E2E_MEDIA_CACHE_DIR=$MEDIA_CACHE_DIR"
    "E2E_ALLOWED_MODELS=${E2E_ALLOWED_MODELS:+$E2E_ALLOWED_MODELS,}$CONFIG_CUSTOM_MODEL"
    "GOCACHE=$GOCACHE"
  )
  if [[ -n "$CALLBACK_ADDR" ]]; then
    server_env+=("E2E_CALLBACK_ADDR=$CALLBACK_ADDR")
  fi
  if [[ "$USE_FAKE_CLAUDE" == "1" ]]; then
    server_env+=(
      "E2E_CLAUDE_BIN=$FAKE_BIN_DIR/claude"
      "FAKE_CLAUDE_LOG=$FAKE_CLAUDE_LOG"
      # P-OBSERVE §3.6 Step 6b:告诉 fake claude 二进制去哪里读 fixture。
      # 空则二进制 fail-closed(不自动猜路径)。
      "LAB_FAKE_FIXTURE_DIR=${LAB_FAKE_FIXTURE_DIR:-$ROOT/scripts/e2e/fixtures}"
    )
  fi
  if [[ -n "$SERVER_QUEUE_MAX_PENDING" ]]; then
    server_env+=("E2E_QUEUE_MAX_PENDING=$SERVER_QUEUE_MAX_PENDING")
  fi
}
