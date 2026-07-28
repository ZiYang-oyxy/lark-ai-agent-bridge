#!/usr/bin/env bash
# Bridge `serve` process lifecycle: fake-claude fixture, PID sync, start/stop/
# restart, cleanup, and soft recovery. Defines functions only; the EXIT trap for
# cleanup is registered by run.sh's main sequence, not at source time.

prepare_fake_claude_if_needed() {
  if [[ "$USE_FAKE_CLAUDE" != "1" ]]; then
    return
  fi
  mkdir -p "$FAKE_BIN_DIR"

  # P-OBSERVE §3.6 Step 6b:替换原 100+ 行 heredoc shim。fake claude 现在由
  # cmd/lark-agent-fake-claude 二进制承接,fixture 定义在 scripts/e2e/fixtures/。
  # 编译该二进制到 FAKE_BIN_DIR/claude,PATH 优先命中它。
  local fake_bin="$FAKE_BIN_DIR/claude"
  local fixture_dir="$ROOT/scripts/e2e/fixtures"
  local go_bin="${GO:-$(command -v go || echo /usr/local/go/bin/go)}"
  test -x "$go_bin" || { echo "prepare_fake_claude_if_needed: go binary not executable: $go_bin" >&2; return 1; }
  test -d "$fixture_dir" || { echo "prepare_fake_claude_if_needed: fixture dir missing: $fixture_dir" >&2; return 1; }
  GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}" "$go_bin" build -o "$fake_bin" "$ROOT/cmd/lark-agent-fake-claude" \
    || { echo "prepare_fake_claude_if_needed: build failed" >&2; return 1; }

  # LAB_FAKE_FIXTURE_DIR 通过 server_env 注入到 bridge serve 进程,fake claude 二进制
  # 从 PATH 命中并读到指定 fixture 目录。fixture 引擎负责所有 marker 分派、image
  # side-effect、hang 语义、指令合规性校验。
  export LAB_FAKE_FIXTURE_DIR="$fixture_dir"
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

seed_controlled_bridge_access() {
  # e2e 注入的 card action 里 operator.open_id 硬编码为 "e2e"（见 lib/lark.sh:submit_config),
  # 不是真实飞书 user open_id;而 controlled bridge 每次都是全新 workdir、空的 access.json,
  # 主动 refresh 到的 owner 是 bot app owner 而不是 "e2e"。为让 config.save 等 admin-only
  # action 能过 canRunAdminCommand,把 "e2e" 预写进 policy.Admins。fake_claude 场景下这只是
  # 测试脚手架,不影响真实生产的 access 语义。
  local access_dir="$DEFAULT_WORKDIR/.lark-agent-bridge"
  local access_file="$access_dir/access.json"
  mkdir -p "$access_dir"
  cat >"$access_file" <<'ACCESS_JSON'
{
  "schema_version": 2,
  "revision": 0,
  "access": {
    "allowed_users": [],
    "allowed_chats": [],
    "admins": ["e2e"]
  }
}
ACCESS_JSON
  chmod 600 "$access_file"
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
  seed_controlled_bridge_access
  prepare_fake_claude_if_needed
  log "starting bridge serve"
  local log_mark=0
  if [[ -f "$SERVER_LOG" ]]; then
    log_mark="$(wc -l <"$SERVER_LOG" | tr -d ' ')"
  fi
  local -a server_env
  build_server_env
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
    media_text_files|session_resume_after_restart)
      reset_message="$(send_dm "/new")"
      ;;
    *)
      reset_message="$(send_at "/new")"
      ;;
  esac
  wait_audit_since "$mark" "$reset_message.*event=result" 60
  summary "- recovery: soft_reset"
}
