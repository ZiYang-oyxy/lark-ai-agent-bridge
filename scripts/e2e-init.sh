#!/usr/bin/env bash
set -euo pipefail

ROOT="${E2E_REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
# shellcheck disable=SC1091
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-profile.sh"

PROFILE=""
NON_INTERACTIVE=0
P2P_CHAT_ID="${E2E_REAL_E2E_P2P_CHAT_ID:-}"

usage() {
  cat <<'EOF'
Usage: scripts/e2e-init.sh --profile <name> [--non-interactive] [--p2p-chat-id <oc_...>]

Creates or updates a developer-local E2E profile under the gitignored
.lark-agent-bridge/e2e/profiles directory. Secrets are never printed.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile)
      PROFILE="${2:-}"
      shift 2
      ;;
    --non-interactive)
      NON_INTERACTIVE=1
      shift
      ;;
    --p2p-chat-id)
      P2P_CHAT_ID="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

e2e_profile_validate_name "$PROFILE" || {
  e2e_profile_error "--profile must match [A-Za-z0-9._-]+"
  exit 2
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

prompt_value() {
  local variable="$1" label="$2" secret="${3:-0}" value
  value="${!variable-}"
  if [[ -n "$value" ]]; then
    return
  fi
  if [[ "$NON_INTERACTIVE" -eq 1 ]]; then
    echo "missing required value: $variable" >&2
    exit 2
  fi
  if [[ "$secret" -eq 1 ]]; then
    read -r -s -p "$label: " value
    printf '\n' >&2
  else
    read -r -p "$label: " value
  fi
  [[ -n "$value" ]] || {
    echo "missing required value: $variable" >&2
    exit 2
  }
  printf -v "$variable" '%s' "$value"
  export "${variable?}"
}

fetch_bot_open_id() {
  local token_response token info_response
  [[ -n "${LARK_BOT_OPEN_ID:-}" ]] && return
  token_response="$(curl -fsS -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg app_id "$LARK_APP_ID" --arg app_secret "$LARK_APP_SECRET" '{app_id:$app_id,app_secret:$app_secret}')")" || {
      echo "BLOCKED credentials_rejected: bridge app credentials were rejected" >&2
      exit 3
    }
  token="$(printf '%s' "$token_response" | jq -r '.tenant_access_token // empty')"
  [[ -n "$token" ]] || {
    echo "BLOCKED credentials_rejected: tenant token was not returned" >&2
    exit 3
  }
  info_response="$(curl -fsS 'https://open.feishu.cn/open-apis/bot/v3/info' -H "Authorization: Bearer $token")" || {
    echo "BLOCKED bot_not_found: bot info is unavailable" >&2
    exit 3
  }
  LARK_BOT_OPEN_ID="$(printf '%s' "$info_response" | jq -r '.bot.open_id // .data.open_id // .open_id // empty')"
  [[ -n "$LARK_BOT_OPEN_ID" ]] || {
    echo "BLOCKED bot_not_found: app has no available bot identity" >&2
    exit 3
  }
  export LARK_BOT_OPEN_ID
}

verify_user_auth() {
  local response auth_app
  response="$(lark-cli auth status --json --verify 2>/dev/null)" || {
    echo "BLOCKED lark_cli_auth_missing: run lark-cli auth login with the bridge app" >&2
    exit 3
  }
  auth_app="$(printf '%s' "$response" | jq -r '.app_id // .data.app_id // .auth.app_id // empty')"
  if [[ -n "$auth_app" && "$auth_app" != "$LARK_APP_ID" ]]; then
    echo "BLOCKED oauth_app_mismatch: lark-cli user OAuth belongs to another app" >&2
    exit 3
  fi
}

verify_group() {
  lark-cli im chats get --as user --chat-id "$E2E_E2E_CHAT_ID" --json >/dev/null 2>&1 || {
    echo "BLOCKED group_unavailable: current lark-cli user cannot read the configured test group" >&2
    exit 3
  }
}

discover_p2p_chat() {
  local list chat members matches=()
  if [[ -n "$P2P_CHAT_ID" ]]; then
    members="$(lark-cli im +chat-members-list --as user --chat-id "$P2P_CHAT_ID" --member-types bots --json 2>/dev/null)" || {
      echo "BLOCKED p2p_unavailable: the selected direct chat is not readable" >&2
      exit 3
    }
    if ! printf '%s' "$members" | jq -e --arg bot "$LARK_BOT_OPEN_ID" \
      '[(.bots // .data.bots // .items // .data.items // [])[] | (.member_id // .open_id // .member.open_id // empty)] | index($bot) != null' >/dev/null 2>&1; then
      echo "BLOCKED p2p_bot_mismatch: the selected direct chat does not contain this bot" >&2
      exit 3
    fi
    E2E_REAL_E2E_P2P_CHAT_ID="$P2P_CHAT_ID"
    export E2E_REAL_E2E_P2P_CHAT_ID
    return
  fi
  list="$(lark-cli im +chat-list --as user --types p2p --json 2>/dev/null)" || {
    echo "BLOCKED p2p_list_unavailable: current user cannot list P2P chats" >&2
    exit 3
  }
  while IFS= read -r chat; do
    [[ -n "$chat" ]] || continue
    members="$(lark-cli im +chat-members-list --as user --chat-id "$chat" --member-types bots --json 2>/dev/null || true)"
    if printf '%s' "$members" | jq -e --arg bot "$LARK_BOT_OPEN_ID" \
      '[(.bots // .data.bots // .items // .data.items // [])[] | (.member_id // .open_id // .member.open_id // empty)] | index($bot) != null' >/dev/null 2>&1; then
      matches+=("$chat")
    fi
  done < <(printf '%s' "$list" | jq -r '(.items // .data.items // .data.chats // [])[] | .chat_id // empty')

  if (( ${#matches[@]} == 0 )); then
    echo "BLOCKED p2p_not_found: start a direct chat with this bot, then rerun bootstrap" >&2
    exit 3
  fi
  if (( ${#matches[@]} > 1 )); then
    echo "BLOCKED p2p_ambiguous: pass --p2p-chat-id to select the intended direct chat" >&2
    exit 3
  fi
  E2E_REAL_E2E_P2P_CHAT_ID="${matches[0]}"
  export E2E_REAL_E2E_P2P_CHAT_ID
}

load_existing_profile_defaults() {
  local env_path="$ROOT/.lark-agent-bridge/e2e/profiles/$PROFILE.env"
  local app_set="${LARK_APP_ID+x}" secret_set="${LARK_APP_SECRET+x}" bot_set="${LARK_BOT_OPEN_ID+x}"
  local group_set="${E2E_E2E_CHAT_ID+x}" p2p_set="${E2E_REAL_E2E_P2P_CHAT_ID+x}"
  local app_value="${LARK_APP_ID-}" secret_value="${LARK_APP_SECRET-}" bot_value="${LARK_BOT_OPEN_ID-}"
  local group_value="${E2E_E2E_CHAT_ID-}" p2p_value="${E2E_REAL_E2E_P2P_CHAT_ID-}"
  [[ -f "$env_path" ]] || return 0
  e2e_profile_load "$env_path"
  if [[ -n "$app_set" ]]; then LARK_APP_ID="$app_value"; export LARK_APP_ID; fi
  if [[ -n "$secret_set" ]]; then LARK_APP_SECRET="$secret_value"; export LARK_APP_SECRET; fi
  if [[ -n "$bot_set" ]]; then LARK_BOT_OPEN_ID="$bot_value"; export LARK_BOT_OPEN_ID; fi
  if [[ -n "$group_set" ]]; then E2E_E2E_CHAT_ID="$group_value"; export E2E_E2E_CHAT_ID; fi
  if [[ -n "$p2p_set" ]]; then E2E_REAL_E2E_P2P_CHAT_ID="$p2p_value"; export E2E_REAL_E2E_P2P_CHAT_ID; fi
  if [[ -z "$P2P_CHAT_ID" && -n "${E2E_REAL_E2E_P2P_CHAT_ID:-}" ]]; then
    P2P_CHAT_ID="$E2E_REAL_E2E_P2P_CHAT_ID"
  fi
}

require_cmd jq
require_cmd curl
require_cmd lark-cli
load_existing_profile_defaults
prompt_value LARK_APP_ID "Feishu app ID"
prompt_value LARK_APP_SECRET "Feishu app secret" 1
prompt_value E2E_E2E_CHAT_ID "Test group chat ID"
verify_user_auth
fetch_bot_open_id
verify_group
discover_p2p_chat
e2e_profile_write "$ROOT" "$PROFILE"

echo "E2E profile '$PROFILE' initialized in the local gitignored profile directory."
echo "Next: ./scripts/e2e-real.sh --profile '$PROFILE' --doctor"
