#!/usr/bin/env bash
# Bridge `serve` process lifecycle: fake-claude fixture, PID sync, start/stop/
# restart, cleanup, and soft recovery. Defines functions only; the EXIT trap for
# cleanup is registered by run.sh's main sequence, not at source time.

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
previous=
instruction_file=
prompt=
for arg in "$@"; do
  if [ "$previous" = "--append-system-prompt-file" ]; then
    instruction_file="$arg"
  fi
  previous="$arg"
  prompt="$arg"
done
case "$prompt" in
  *'Reply with exactly OK. Do not use tools.'*) ;;
  *)
    test -n "$instruction_file" && test -r "$instruction_file"
    grep -q 'Feishu Bridge Runtime Instructions' "$instruction_file"
    case "$prompt" in *'Feishu Bridge Runtime Instructions'*) exit 92 ;; esac
    ;;
esac
marker="$(printf '%s\n' "$args" | grep -Eo 'E2E_[A-Za-z0-9_-]+' | tail -n 1 || true)"
write_test_image() {
  image_name="e2e-output-${marker}.png"
  image_base64='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII='
  if ! printf '%s' "$image_base64" | base64 --decode >"$image_name" 2>/dev/null; then
    printf '%s' "$image_base64" | base64 -D >"$image_name"
  fi
  }
case "$args" in
  *E2E_BRIDGE_IMAGE_NO_INTENT*)
    write_test_image
    jq -nc --arg result "已按要求生成，但不发送图片 ${marker}" '{type:"result",result:$result,model:"fake-claude-e2e",usage:{output_tokens:1},session_id:"fake-e2e-session"}'
    exit 0
    ;;
  *E2E_BRIDGE_IMAGE_AMBIGUOUS*)
    jq -nc --arg result "存在多个合理候选，请确认要发送哪一张。 ${marker}" '{type:"result",result:$result,model:"fake-claude-e2e",usage:{output_tokens:1},session_id:"fake-e2e-session"}'
    exit 0
    ;;
  *E2E_BRIDGE_IMAGE_INTENT*)
    write_test_image
    result="![${marker}](./${image_name})"
    jq -nc --arg result "$result" '{type:"result",result:$result,model:"fake-claude-e2e",usage:{output_tokens:1},session_id:"fake-e2e-session"}'
    exit 0
    ;;
  *E2E_*_OUTPUT_IMAGE*)
    write_test_image
    result="![${marker}](./${image_name})"
    jq -nc --arg result "$result" '{type:"result",result:$result,model:"fake-claude-e2e",usage:{output_tokens:1},session_id:"fake-e2e-session"}'
    exit 0
    ;;
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
