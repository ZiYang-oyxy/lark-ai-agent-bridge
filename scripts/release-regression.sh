#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}"
mkdir -p "$GOCACHE"
export E2E_STATE_ROOT="${E2E_STATE_ROOT:-$HOME}"

PROFILE=""
ENVIRONMENT=""
RELEASE="dev"
STRICT_L3=0
EVIDENCE_DIR=""
EVIDENCE_FILE=""
RUNNING_FILE=""

usage() {
  cat <<'USAGE'
Usage: scripts/release-regression.sh (--environment <name> | --profile <name>) [--release <version>] [--strict-l3]

Full L0->L3 release regression:
  L0/L1  REQUIRE_LARK=1 verify.sh + go test -race (schedule/session/bridge/card)
  L2     e2e-real.sh --mode full --strict-capabilities  (real Feishu + fake Agent)
  L3     real Claude/Codex wrapper canary (new_basic, no fake)   -- warn-only by default

Note: real card.action.trigger platform delivery is NOT automatable (Feishu platform
limit); its handling is covered by L1 deterministic tests + L2 render injection.

Options:
  --environment <name> docs/environments/<name>.env; declares role bindings, TEST_BOT_AUDIT,
                       L3 sender, and the default profile. Preferred for L2/L3.
  --profile <name>   fallback for older callers; used verbatim as --profile to e2e-real.sh.
                     Ignored when --environment is set (env supplies its own profile).
  --release <ver>    version label for the verdict line (default: dev; strict evidence requires vMAJOR.MINOR.PATCH[-rc.N]).
  --strict-l3        treat L3 failure as a hard gate (default: warn-only).
  -h, --help         show this help.
USAGE
}

derive_release_channel() {
  local release="$1"
  if [[ "$release" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-rc\.(0|[1-9][0-9]*)$ ]]; then
    printf '%s\n' rc
  elif [[ "$release" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    printf '%s\n' stable
  else
    echo "release-regression: strict evidence requires canonical release tag vMAJOR.MINOR.PATCH[-rc.N], got $release" >&2
    return 1
  fi
}

main() {
  local channel=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --profile) PROFILE="${2:-}"; shift 2 ;;
      --environment) ENVIRONMENT="${2:-}"; shift 2 ;;
      --release) RELEASE="${2:-}"; shift 2 ;;
      --strict-l3) STRICT_L3=1; shift ;;
      -h|--help) usage; exit 0 ;;
      *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    esac
  done

  if [[ -z "$ENVIRONMENT" && -z "$PROFILE" ]]; then
    echo "release-regression: --environment (preferred) or --profile is required for L2/L3" >&2
    exit 2
  fi
  # --environment 会驱动 e2e-real.sh 读 docs/environments/<name>.env,那里声明的
  # E2E_PROFILE 是默认 profile。observe 模式 case 依赖 environment 提供 TEST_BOT_AUDIT
  # 等运行时地址,单靠 --profile 无法启动;因此 L2/L3 一律优先传 --environment。
  build_e2e_args() {
    if [[ -n "$ENVIRONMENT" ]]; then
      printf '%s\0%s\0' --environment "$ENVIRONMENT"
    else
      printf '%s\0%s\0' --profile "$PROFILE"
    fi
  }
  if [[ -n "$(git status --porcelain --untracked-files=all)" ]]; then
    echo "release-regression: worktree must be clean before regression" >&2
    exit 1
  fi
  commit="$(git rev-parse HEAD)"
  git_common="$(git rev-parse --git-common-dir)"
  [[ "$git_common" == /* ]] || git_common="$ROOT/$git_common"
  EVIDENCE_DIR="$git_common/release-state"
  mkdir -p "$EVIDENCE_DIR"
  chmod 700 "$EVIDENCE_DIR"
  EVIDENCE_FILE="$EVIDENCE_DIR/regression-evidence-$commit.json"
  RUNNING_FILE="$EVIDENCE_DIR/regression-running-$commit.pid"
  rm -f "$EVIDENCE_FILE"
  running_tmp="$RUNNING_FILE.tmp.$$"
  printf '%s\n' "$$" >"$running_tmp"
  chmod 600 "$running_tmp"
  mv "$running_tmp" "$RUNNING_FILE"
  cleanup_running() {
    rm -f "$RUNNING_FILE"
  }
  trap cleanup_running EXIT
  if [[ "$STRICT_L3" -eq 1 ]]; then
    channel="$(derive_release_channel "$RELEASE")" || exit 2
  fi

  echo "NOTE: real card.action.trigger platform delivery is not automated (Feishu platform limit);"
  echo "      handling is covered by L1 deterministic tests + L2 render injection."

  echo "== L0/L1: verify.sh (strict) =="
  REQUIRE_LARK=1 ./scripts/verify.sh

  echo "== L0/L1: race =="
  go test -race ./internal/schedule ./internal/session ./internal/bridge ./internal/card

  local -a e2e_target
  mapfile -d '' -t e2e_target < <(build_e2e_args)

  echo "== L2: e2e-real full (real Feishu + fake Agent) =="
  ./scripts/e2e-real.sh "${e2e_target[@]}" --mode full --strict-capabilities

  echo "== L3: real Claude canary (new_basic) =="
  l3="passed"
  if ! env -u E2E_REAL_E2E_FAKE_CLAUDE ./scripts/e2e-real.sh "${e2e_target[@]}" --case new_basic; then
    l3="warn"
    echo "release-regression: L3 canary failed" >&2
    if [[ "$STRICT_L3" -eq 1 ]]; then
      echo "release-regression: --strict-l3 set, L3 failure is a hard gate" >&2
      exit 1
    fi
  fi

  if [[ -n "$(git status --porcelain --untracked-files=all)" ]]; then
    echo "release-regression: worktree changed during regression; refusing evidence" >&2
    exit 1
  fi
  if [[ "$(git rev-parse HEAD)" != "$commit" ]]; then
    echo "release-regression: HEAD changed during regression; refusing evidence" >&2
    exit 1
  fi
  if [[ "$STRICT_L3" -eq 1 && "$l3" == "passed" ]]; then
    evidence_tmp="$EVIDENCE_FILE.tmp.$$"
    jq -n \
      --arg commit "$commit" \
      --arg timestamp "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
      --arg profile "$PROFILE" \
      --arg environment "$ENVIRONMENT" \
      --arg release "$RELEASE" \
      --arg channel "$channel" \
      '{schema_version:1,commit:$commit,timestamp:$timestamp,
        profile:$profile,environment:$environment,
        release:$release,channel:$channel,strict_l3:true,
        l0l1:"passed",l2:"passed",l3:"passed"}' >"$evidence_tmp"
    chmod 600 "$evidence_tmp"
    mv "$evidence_tmp" "$EVIDENCE_FILE"
  fi

  echo "RELEASE_REGRESSION_OK release=$RELEASE l0l1=passed l2=passed l3=$l3"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
