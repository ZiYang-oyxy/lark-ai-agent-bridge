#!/usr/bin/env bash

if [[ -n "${BRIDGE_SIMULATE_SUITE_SH_LOADED:-}" ]]; then
  return 0
fi
BRIDGE_SIMULATE_SUITE_SH_LOADED=1

smoke_command_surface() {
  local help_output plain_output resume_output codex_output
  help_output="$(simulate -text "/help")"
  require_contains "$help_output" '"Type": "help"' "help simulation"
  require_contains "$help_output" '"CurrentVersion": "dev"' "help current version"
  require_contains "$help_output" '**`/new`** `[--workdir path]' "help simulation"
  require_contains "$help_output" '**`/config`** 全局运行偏好 · **`/config set`**' "help simulation"
  require_contains "$help_output" '**`/local-config`** `[reset]` 本群覆盖 · **`/local-config set`**' "help simulation"
  require_contains "$help_output" '**`/mkdir`** `[path]`' "help simulation"
  require_contains "$help_output" '**`/resume`** `[session-id]' "help simulation"
  require_not_contains "$help_output" "/codex" "help simulation"

  plain_output="$(simulate -text "hello")"
  require_contains "$plain_output" '"SessionID": "claude:chat-demo:message:' "plain text simulation"
  require_contains "$plain_output" "simulated answer: hello" "plain text simulation"

  resume_output="$(simulate -text "/resume")"
  require_contains "$resume_output" "Session 历史存储不可用" "resume simulation without durable catalog"

  codex_output="$(simulate -text "/codex inspect")"
  require_contains "$codex_output" "unknown command /codex" "codex disabled simulation"
}

smoke_group_intake() {
  local group_output all_group_output bot_off_output bot_on_output topics_store topic_followup_output
  group_output="$(simulate -group=true -mentioned=false -text "hello")"
  require_contains "$group_output" '"events": []' "group mention filter simulation"

  all_group_output="$(E2E_GROUP_MESSAGE_MODE=all_group_messages simulate -group=true -mentioned=false -text "hello all")"
  require_contains "$all_group_output" "simulated answer: hello all" "all group messages simulation"

  bot_off_output="$(E2E_GROUP_MESSAGE_MODE=all_group_messages simulate -group=true -mentioned=true -sender-type bot -text "/help")"
  require_contains "$bot_off_output" '"events": []' "bot sender default off simulation"
  bot_on_output="$(E2E_GROUP_MESSAGE_MODE=all_group_messages E2E_RESPOND_TO_BOTS=true simulate -group=true -mentioned=true -sender-type bot -text "/help")"
  require_contains "$bot_on_output" '**`/new`** `[--workdir path]' "bot sender enabled simulation"

  topics_store="${ROOT:?ROOT must be set}/.cache/verify-participated-topics-$RANDOM.json"
  E2E_GROUP_MESSAGE_MODE=participated_topics E2E_PARTICIPATED_TOPICS_STORE="$topics_store" simulate -group=true -thread=topic-verify -mentioned=true -text "/help" >/dev/null
  topic_followup_output="$(E2E_GROUP_MESSAGE_MODE=participated_topics E2E_PARTICIPATED_TOPICS_STORE="$topics_store" simulate -group=true -thread=topic-verify -mentioned=false -text "/help")"
  require_contains "$topic_followup_output" '**`/new`** `[--workdir path]' "participated topic restart simulation"
  rm -f "$topics_store"
}
