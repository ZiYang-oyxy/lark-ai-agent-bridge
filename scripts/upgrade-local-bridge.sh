#!/usr/bin/env bash
# Upgrade one local systemd user-service from the latest trusted source commit.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
service="lark-ai-agent-bridge-dada.service"
workdir="/root/ws/dada-workspace"
branch="main"
go_bin=""
env_file=""
install_root="${HOME}/.local/lib/lark-ai-agent-bridge"
dry_run=0

usage() {
  cat <<'EOF'
Usage: scripts/upgrade-local-bridge.sh [options]

Build and activate the latest fast-forward commit from a trusted branch.

Options:
  --service NAME       systemd user unit (default: lark-ai-agent-bridge-dada.service)
  --workdir PATH       Bridge default workspace (default: /root/ws/dada-workspace)
  --branch NAME        Remote branch to fast-forward from (default: main)
  --env-file PATH      EnvironmentFile used by the service (default: inferred from service)
  --install-root PATH  Versioned binary parent directory (default: ~/.local/lib/lark-ai-agent-bridge)
  --go PATH            Go executable (default: /opt/go1.26.3/bin/go, then PATH)
  --dry-run            Print the resolved target without changing source or service state
  -h, --help           Show this help
EOF
}

die() {
  printf 'upgrade-local-bridge: %s\n' "$*" >&2
  exit 1
}

require_value() {
  [[ $# -eq 2 && -n $2 ]] || die "$1 requires a value"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --service) require_value "$1" "${2:-}"; service="$2"; shift 2 ;;
    --workdir) require_value "$1" "${2:-}"; workdir="$2"; shift 2 ;;
    --branch) require_value "$1" "${2:-}"; branch="$2"; shift 2 ;;
    --env-file) require_value "$1" "${2:-}"; env_file="$2"; shift 2 ;;
    --install-root) require_value "$1" "${2:-}"; install_root="$2"; shift 2 ;;
    --go) require_value "$1" "${2:-}"; go_bin="$2"; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

[[ $service == *.service ]] || die "--service must end in .service"
[[ $workdir != *$'\n'* && $workdir != *' '* ]] || die "--workdir must not contain spaces or newlines"
[[ $branch =~ ^[A-Za-z0-9._/-]+$ ]] || die "invalid --branch: $branch"

config_home="${XDG_CONFIG_HOME:-${HOME}/.config}"
unit_file="${config_home}/systemd/user/${service}"
instance="${service#lark-ai-agent-bridge-}"
instance="${instance%.service}"
if [[ -z $env_file ]]; then
  env_file="${config_home}/lark-ai-agent-bridge/${instance}.env"
fi
if [[ -z $go_bin ]]; then
  if [[ -x /opt/go1.26.3/bin/go ]]; then
    go_bin=/opt/go1.26.3/bin/go
  else
    go_bin="$(command -v go || true)"
  fi
fi

[[ -x $go_bin ]] || die "Go executable is unavailable: $go_bin"
[[ -d $workdir ]] || die "workspace does not exist: $workdir"
[[ -f $unit_file ]] || die "systemd unit does not exist: $unit_file"
[[ -f $env_file ]] || die "environment file does not exist: $env_file"

if ((dry_run)); then
  printf 'repo=%s\nservice=%s\nworkdir=%s\nbranch=origin/%s\nenv_file=%s\ninstall_root=%s\ngo=%s\n' \
    "$repo_root" "$service" "$workdir" "$branch" "$env_file" "$install_root" "$go_bin"
  exit 0
fi

command -v systemctl >/dev/null || die "systemctl is unavailable"
cd "$repo_root"
[[ -z $(git status --porcelain) ]] || die "refusing to update a dirty worktree"
git fetch origin "$branch"
git switch "$branch"
git pull --ff-only origin "$branch"
[[ $(git rev-parse HEAD) == "$(git rev-parse "origin/${branch}")" ]] || die "local HEAD does not match origin/${branch}"

# Tests must not inherit the live service's stores or higher-priority credentials.
(
  umask 022
  env \
    -u E2E_PREFERENCE_STORE -u E2E_REPLY_STORE -u E2E_MEDIA_CACHE_DIR -u E2E_SESSION_STORE \
    -u E2E_REPLY_MODE \
    -u LAB_LARK_APP_ID -u LAB_LARK_APP_SECRET -u LAB_LARK_APP_ID_FILE -u LAB_LARK_APP_SECRET_FILE \
    -u LARK_APP_ID -u LARK_APP_SECRET -u LARK_APP_ID_FILE -u LARK_APP_SECRET_FILE \
    GOCACHE="$repo_root/.cache/go-build" \
    "$go_bin" test ./...
)

commit="$(git rev-parse HEAD)"
short_commit="$(git rev-parse --short=12 HEAD)"
version="dev-${short_commit}"
install_dir="${install_root}/${version}"
candidate="${install_dir}/lark-agent-bridge"
mkdir -p "$install_dir"
temp_binary="$(mktemp "${install_dir}/.lark-agent-bridge.XXXXXX")"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
"$go_bin" build -trimpath -buildvcs=false \
  -ldflags "-X lark-agent-bridge/internal/buildinfo.Version=${version} -X lark-agent-bridge/internal/buildinfo.Commit=${commit} -X lark-agent-bridge/internal/buildinfo.BuildTime=${build_time}" \
  -o "$temp_binary" ./cmd/lark-agent-bridge
chmod 0755 "$temp_binary"
mv "$temp_binary" "$candidate"

# The env file is intentionally sourced only for the subprocess that needs it.
set -a
# shellcheck disable=SC1090
. "$env_file"
set +a
"$candidate" doctor --default-workdir "$workdir" --strict --online

expected_exec="ExecStart=${candidate} serve --default-workdir ${workdir}"
unit_temp="$(mktemp "${unit_file}.XXXXXX")"
unit_backup="${unit_file}.before-${short_commit}"
awk -v expected="$expected_exec" '
  /^ExecStart=/ { count++; print expected; next }
  { print }
  END { if (count != 1) exit 42 }
' "$unit_file" > "$unit_temp" || die "unit must contain exactly one ExecStart line: $unit_file"
chmod --reference="$unit_file" "$unit_temp"
cp -p "$unit_file" "$unit_backup"
mv "$unit_temp" "$unit_file"

rollback_unit() {
  cp -p "$unit_backup" "$unit_file"
  systemctl --user daemon-reload
  systemctl --user restart "$service"
}

if ! systemctl --user daemon-reload || ! systemctl --user restart "$service" || ! systemctl --user is-active --quiet "$service"; then
  rollback_unit || true
  die "activation failed; restored previous systemd unit"
fi

active_exec="$(systemctl --user show "$service" -p ExecStart --value)"
[[ $active_exec == *"${candidate} serve --default-workdir ${workdir}"* ]] || {
  rollback_unit || true
  die "running service does not use the newly built binary; restored previous unit"
}
"$candidate" doctor --default-workdir "$workdir" --strict --online
printf 'UPGRADE_LOCAL_BRIDGE_OK version=%s commit=%s service=%s\n' "$version" "$commit" "$service"
