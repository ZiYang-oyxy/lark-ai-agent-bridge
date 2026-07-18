#!/usr/bin/env bash

E2E_CAP_NAMES=()
E2E_CAP_STATUSES=()
E2E_CAP_REASONS=()
E2E_CAP_SUMMARIES=()
E2E_CAP_REMEDIATIONS=()
E2E_CAP_EVIDENCE=()
E2E_PROFILE_LOCK_PATH=""
E2E_PROFILE_LOCK_TOKEN=""
E2E_PROFILE_LOCK_PID=""

e2e_cap_reset() {
  E2E_CAP_NAMES=()
  E2E_CAP_STATUSES=()
  E2E_CAP_REASONS=()
  E2E_CAP_SUMMARIES=()
  E2E_CAP_REMEDIATIONS=()
  E2E_CAP_EVIDENCE=()
}

e2e_cap_validate_identifier() {
  [[ "$1" =~ ^[A-Za-z0-9._-]+$ ]]
}

e2e_cap_validate_status() {
  case "$1" in
    PASS|FAIL|BLOCKED|SKIPPED) return 0 ;;
    *) return 1 ;;
  esac
}

e2e_cap_index() {
  local name="$1" i
  for ((i = 0; i < ${#E2E_CAP_NAMES[@]}; i++)); do
    if [[ "${E2E_CAP_NAMES[$i]}" == "$name" ]]; then
      printf '%s\n' "$i"
      return 0
    fi
  done
  return 1
}

e2e_cap_record() {
  local name="$1" status="$2" reason="${3:-}" summary="${4:-}" remediation="${5:-}" evidence="${6:-}" index
  e2e_cap_validate_identifier "$name" || {
    echo "invalid capability name" >&2
    return 2
  }
  e2e_cap_validate_status "$status" || {
    echo "invalid capability status" >&2
    return 2
  }
  if [[ -n "$reason" ]]; then
    e2e_cap_validate_identifier "$reason" || {
      echo "invalid capability reason code" >&2
      return 2
    }
  fi
  if index="$(e2e_cap_index "$name")"; then
    E2E_CAP_STATUSES[index]="$status"
    E2E_CAP_REASONS[index]="$reason"
    E2E_CAP_SUMMARIES[index]="$summary"
    E2E_CAP_REMEDIATIONS[index]="$remediation"
    E2E_CAP_EVIDENCE[index]="$evidence"
    return
  fi
  E2E_CAP_NAMES+=("$name")
  E2E_CAP_STATUSES+=("$status")
  E2E_CAP_REASONS+=("$reason")
  E2E_CAP_SUMMARIES+=("$summary")
  E2E_CAP_REMEDIATIONS+=("$remediation")
  E2E_CAP_EVIDENCE+=("$evidence")
}

e2e_cap_status() {
  local index
  if index="$(e2e_cap_index "$1")"; then
    printf '%s\n' "${E2E_CAP_STATUSES[$index]}"
    return
  fi
  printf 'SKIPPED\n'
}

e2e_cap_evaluate() {
  local case_name="$1" capability status saw_blocked=0 saw_skipped=0
  shift
  : "$case_name"
  for capability in "$@"; do
    status="$(e2e_cap_status "$capability")"
    case "$status" in
      FAIL)
        printf 'FAIL\n'
        return
        ;;
      BLOCKED) saw_blocked=1 ;;
      SKIPPED) saw_skipped=1 ;;
    esac
  done
  if [[ "$saw_blocked" -eq 1 ]]; then
    printf 'BLOCKED\n'
  elif [[ "$saw_skipped" -eq 1 ]]; then
    printf 'SKIPPED\n'
  else
    printf 'READY\n'
  fi
}

e2e_cap_exit_code() {
  local strict="${1:-0}" status saw_blocked=0
  for status in "${E2E_CAP_STATUSES[@]}"; do
    if [[ "$status" == "FAIL" ]]; then
      printf '1\n'
      return
    fi
    [[ "$status" == "BLOCKED" ]] && saw_blocked=1
  done
  if [[ "$strict" -eq 1 && "$saw_blocked" -eq 1 ]]; then
    printf '3\n'
  else
    printf '0\n'
  fi
}

e2e_cap_write_json() {
  local path="$1" dir tmp records i evidence_json generated_at
  dir="$(dirname "$path")"
  mkdir -p "$dir"
  tmp="$(mktemp "$dir/.capabilities.XXXXXX")"
  records="$(mktemp "$dir/.capability-records.XXXXXX")"
  chmod 600 "$tmp" "$records"
  for ((i = 0; i < ${#E2E_CAP_NAMES[@]}; i++)); do
    if [[ -n "${E2E_CAP_EVIDENCE[$i]}" ]]; then
      evidence_json="$(jq -nc --arg value "${E2E_CAP_EVIDENCE[$i]}" '[$value]')"
    else
      evidence_json='[]'
    fi
    jq -nc \
      --arg name "${E2E_CAP_NAMES[$i]}" \
      --arg status "${E2E_CAP_STATUSES[$i]}" \
      --arg reason "${E2E_CAP_REASONS[$i]}" \
      --arg summary "${E2E_CAP_SUMMARIES[$i]}" \
      --arg remediation "${E2E_CAP_REMEDIATIONS[$i]}" \
      --argjson evidence "$evidence_json" \
      '{name:$name,status:$status,reason_code:$reason,summary:$summary,remediation:$remediation,evidence_refs:$evidence}' >>"$records"
  done
  generated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  jq -s --arg generated_at "$generated_at" '{schema_version:1,generated_at:$generated_at,capabilities:.}' "$records" >"$tmp"
  mv "$tmp" "$path"
  chmod 600 "$path"
  rm -f "$records"
}

e2e_cap_import_json() {
  local path="$1" name record status reason summary remediation evidence
  shift
  [[ -f "$path" ]] || return 0
  jq -e '.schema_version == 1 and (.capabilities | type == "array")' "$path" >/dev/null 2>&1 || return 1
  for name in "$@"; do
    record="$(jq -c --arg name "$name" '.capabilities[] | select(.name == $name)' "$path" | tail -n 1)"
    [[ -n "$record" ]] || continue
    status="$(printf '%s' "$record" | jq -r '.status')"
    reason="$(printf '%s' "$record" | jq -r '.reason_code // ""')"
    summary="$(printf '%s' "$record" | jq -r '.summary // ""')"
    remediation="$(printf '%s' "$record" | jq -r '.remediation // ""')"
    evidence="$(printf '%s' "$record" | jq -r '.evidence_refs[0] // ""')"
    e2e_cap_record "$name" "$status" "$reason" "$summary" "$remediation" "$evidence"
  done
}

e2e_cap_write_summary() {
  local path="$1" dir tmp i
  dir="$(dirname "$path")"
  mkdir -p "$dir"
  tmp="$(mktemp "$dir/.capability-summary.XXXXXX")"
  chmod 600 "$tmp"
  {
    echo "## E2E capabilities"
    echo
    echo "| Capability | Status | Reason | Summary |"
    echo "| --- | --- | --- | --- |"
    for ((i = 0; i < ${#E2E_CAP_NAMES[@]}; i++)); do
      printf '| %s | %s | %s | %s |\n' \
        "${E2E_CAP_NAMES[$i]}" "${E2E_CAP_STATUSES[$i]}" "${E2E_CAP_REASONS[$i]}" "${E2E_CAP_SUMMARIES[$i]}"
    done
    echo
  } >"$tmp"
  mv "$tmp" "$path"
  chmod 600 "$path"
}

e2e_profile_lock_acquire() {
  local profile="$1" path="$2" token="$3" owner_file owner_pid current_pid now
  : "$profile"
  current_pid="${BASHPID:-$$}"
  owner_file="$path/owner"
  if ! mkdir "$path" 2>/dev/null; then
    if [[ ! -f "$owner_file" ]]; then
      echo "BLOCKED profile_busy: lock owner cannot be verified" >&2
      return 3
    fi
    owner_pid="$(sed -n '1p' "$owner_file" 2>/dev/null || true)"
    if [[ ! "$owner_pid" =~ ^[0-9]+$ ]] || kill -0 "$owner_pid" >/dev/null 2>&1; then
      echo "BLOCKED profile_busy: another active run owns this profile" >&2
      return 3
    fi
    rm -f "$owner_file"
    rmdir "$path" 2>/dev/null || {
      echo "BLOCKED profile_busy: stale lock could not be reclaimed" >&2
      return 3
    }
    mkdir "$path" 2>/dev/null || {
      echo "BLOCKED profile_busy: another run acquired the profile" >&2
      return 3
    }
  fi
  chmod 700 "$path"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf '%s\n%s\n%s\n' "$current_pid" "$token" "$now" >"$owner_file"
  chmod 600 "$owner_file"
  E2E_PROFILE_LOCK_PATH="$path"
  E2E_PROFILE_LOCK_TOKEN="$token"
  E2E_PROFILE_LOCK_PID="$current_pid"
}

e2e_profile_lock_release() {
  local owner_pid owner_token
  [[ -n "$E2E_PROFILE_LOCK_PATH" ]] || return 0
  if [[ -f "$E2E_PROFILE_LOCK_PATH/owner" ]]; then
    owner_pid="$(sed -n '1p' "$E2E_PROFILE_LOCK_PATH/owner" 2>/dev/null || true)"
    owner_token="$(sed -n '2p' "$E2E_PROFILE_LOCK_PATH/owner" 2>/dev/null || true)"
    if [[ "$owner_pid" != "$E2E_PROFILE_LOCK_PID" || "$owner_token" != "$E2E_PROFILE_LOCK_TOKEN" ]]; then
      echo "refusing to release a profile lock owned by another run" >&2
      return 1
    fi
    rm -f "$E2E_PROFILE_LOCK_PATH/owner"
  fi
  rmdir "$E2E_PROFILE_LOCK_PATH"
  E2E_PROFILE_LOCK_PATH=""
  E2E_PROFILE_LOCK_TOKEN=""
  E2E_PROFILE_LOCK_PID=""
}
