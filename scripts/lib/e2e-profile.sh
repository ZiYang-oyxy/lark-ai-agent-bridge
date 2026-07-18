#!/usr/bin/env bash

e2e_profile_error() {
  printf 'e2e profile error: %s\n' "$1" >&2
}

e2e_profile_validate_name() {
  local name="${1:-}"
  [[ -n "$name" && "$name" != "." && "$name" != ".." && "$name" =~ ^[A-Za-z0-9._-]+$ ]]
}

e2e_profile_dir() {
  printf '%s/.lark-agent-bridge/e2e/profiles\n' "$1"
}

e2e_profile_mode() {
  local path="$1"
  stat -f '%Lp' "$path" 2>/dev/null || stat -c '%a' "$path" 2>/dev/null
}

e2e_profile_allowed_key() {
  case "$1" in
    LARK_APP_ID|LARK_APP_SECRET|LARK_BOT_OPEN_ID|E2E_E2E_CHAT_ID|E2E_REAL_E2E_P2P_CHAT_ID) return 0 ;;
    *) return 1 ;;
  esac
}

e2e_profile_safe_value() {
  [[ "$1" =~ ^[A-Za-z0-9._:/+=,@%~-]*$ ]]
}

e2e_profile_paths() {
  local root="$1" name="$2" dir
  e2e_profile_validate_name "$name" || {
    e2e_profile_error "invalid profile name"
    return 2
  }
  dir="$(e2e_profile_dir "$root")"
  printf '%s\t%s/%s.env\t%s/%s.json\n' "$name" "$dir" "$name" "$dir" "$name"
}

e2e_profile_select() {
  local root="$1" explicit="${2:-}" requested="" dir legacy
  local files=()
  dir="$(e2e_profile_dir "$root")"
  legacy="$root/.lark-agent-bridge/e2e.env"

  if [[ -n "$explicit" ]]; then
    requested="$explicit"
  elif [[ -n "${E2E_E2E_PROFILE:-}" ]]; then
    requested="$E2E_E2E_PROFILE"
  fi

  if [[ -n "$requested" ]]; then
    e2e_profile_validate_name "$requested" || {
      e2e_profile_error "invalid profile name"
      return 2
    }
    if [[ "$requested" == "legacy" && -f "$legacy" ]]; then
      printf 'legacy\t%s\t-\n' "$legacy"
      return 0
    fi
    if [[ ! -f "$dir/$requested.env" ]]; then
      e2e_profile_error "profile not found: $requested"
      return 2
    fi
    e2e_profile_paths "$root" "$requested"
    return
  fi

  if [[ -d "$dir" ]]; then
    while IFS= read -r file; do
      [[ -n "$file" ]] && files+=("$file")
    done < <(find "$dir" -maxdepth 1 -type f -name '*.env' -print | LC_ALL=C sort)
  fi

  if (( ${#files[@]} == 1 )); then
    requested="$(basename "${files[0]}" .env)"
    e2e_profile_paths "$root" "$requested"
    return
  fi
  if (( ${#files[@]} > 1 )); then
    e2e_profile_error "multiple profiles found; pass --profile or set E2E_E2E_PROFILE"
    printf 'available profiles:' >&2
    for file in "${files[@]}"; do
      printf ' %s' "$(basename "$file" .env)" >&2
    done
    printf '\n' >&2
    return 2
  fi
  if [[ -f "$legacy" ]]; then
    printf 'legacy\t%s\t-\n' "$legacy"
    return 0
  fi
  e2e_profile_error "no profile found; run scripts/e2e-init.sh --profile <name>"
  return 2
}

e2e_profile_load() {
  local path="$1" mode line key value
  [[ -f "$path" ]] || {
    e2e_profile_error "profile file not found"
    return 2
  }
  mode="$(e2e_profile_mode "$path")" || {
    e2e_profile_error "cannot inspect profile permissions"
    return 2
  }
  [[ "$mode" == "600" ]] || {
    e2e_profile_error "profile file permissions must be 0600"
    return 2
  }

  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    [[ "$line" == *=* ]] || {
      e2e_profile_error "profile contains a malformed assignment"
      return 2
    }
    key="${line%%=*}"
    value="${line#*=}"
    e2e_profile_allowed_key "$key" || {
      e2e_profile_error "profile contains an unsupported field"
      return 2
    }
    e2e_profile_safe_value "$value" || {
      e2e_profile_error "profile contains an unsupported value encoding"
      return 2
    }
    printf -v "$key" '%s' "$value"
    export "${key?}"
  done <"$path"
}

e2e_profile_redacted() {
  local value="${1:-}" length=${#1}
  if (( length <= 8 )); then
    printf '***'
    return
  fi
  printf '%s...%s' "${value:0:3}" "${value: -4}"
}

e2e_profile_write() {
  local root="$1" name="$2" dir env_path json_path env_tmp json_tmp key value now
  local keys=(LARK_APP_ID LARK_APP_SECRET LARK_BOT_OPEN_ID E2E_E2E_CHAT_ID E2E_REAL_E2E_P2P_CHAT_ID)
  e2e_profile_validate_name "$name" || {
    e2e_profile_error "invalid profile name"
    return 2
  }
  command -v jq >/dev/null 2>&1 || {
    e2e_profile_error "jq is required"
    return 1
  }
  dir="$(e2e_profile_dir "$root")"
  mkdir -p "$dir" "$root/.lark-agent-bridge/e2e/locks"
  chmod 700 "$root/.lark-agent-bridge" "$root/.lark-agent-bridge/e2e" "$dir" "$root/.lark-agent-bridge/e2e/locks"
  env_path="$dir/$name.env"
  json_path="$dir/$name.json"
  env_tmp="$(mktemp "$dir/.$name.env.XXXXXX")"
  json_tmp="$(mktemp "$dir/.$name.json.XXXXXX")"
  chmod 600 "$env_tmp" "$json_tmp"
  trap 'rm -f "$env_tmp" "$json_tmp"' RETURN

  for key in "${keys[@]}"; do
    value="${!key:-}"
    e2e_profile_safe_value "$value" || {
      e2e_profile_error "profile field $key contains unsupported characters"
      return 2
    }
    printf '%s=%s\n' "$key" "$value" >>"$env_tmp"
  done

  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  jq -n \
    --arg profile "$name" \
    --arg updated_at "$now" \
    --arg app "$(e2e_profile_redacted "${LARK_APP_ID:-}")" \
    --arg bot "$(e2e_profile_redacted "${LARK_BOT_OPEN_ID:-}")" \
    --arg group "$(e2e_profile_redacted "${E2E_E2E_CHAT_ID:-}")" \
    --arg p2p "$(e2e_profile_redacted "${E2E_REAL_E2E_P2P_CHAT_ID:-}")" \
    '{schema_version:1,profile:$profile,updated_at:$updated_at,identity:{app:$app,bot:$bot,group:$group,p2p:$p2p}}' >"$json_tmp"

  mv "$env_tmp" "$env_path"
  mv "$json_tmp" "$json_path"
  chmod 600 "$env_path" "$json_path"
  trap - RETURN
}
