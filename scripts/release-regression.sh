#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}"
mkdir -p "$GOCACHE"

PROFILE=""
RELEASE="dev"
STRICT_L3=0

usage() {
  cat <<'USAGE'
Usage: scripts/release-regression.sh --profile <name> [--release <version>] [--strict-l3]

Full L0->L3 release regression:
  L0/L1  REQUIRE_LARK=1 verify.sh + go test -race (schedule/session/bridge/card)
  L2     e2e-real.sh --mode full --strict-capabilities  (real Feishu + fake Agent)
  L3     real Claude/Codex wrapper canary (new_basic, no fake)   -- warn-only by default

Note: real card.action.trigger platform delivery is NOT automatable (Feishu platform
limit); its handling is covered by L1 deterministic tests + L2 render injection.

Options:
  --profile <name>   E2E profile (required for L2/L3).
  --release <ver>    version label for the verdict line (default: dev).
  --strict-l3        treat L3 failure as a hard gate (default: warn-only).
  -h, --help         show this help.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --profile) PROFILE="${2:-}"; shift 2 ;;
    --release) RELEASE="${2:-}"; shift 2 ;;
    --strict-l3) STRICT_L3=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ -z "$PROFILE" ]]; then
  echo "release-regression: --profile is required for L2/L3" >&2
  exit 2
fi

echo "NOTE: real card.action.trigger platform delivery is not automated (Feishu platform limit);"
echo "      handling is covered by L1 deterministic tests + L2 render injection."

echo "== L0/L1: verify.sh (strict) =="
REQUIRE_LARK=1 ./scripts/verify.sh

echo "== L0/L1: race =="
go test -race ./internal/schedule ./internal/session ./internal/bridge ./internal/card

echo "== L2: e2e-real full (real Feishu + fake Agent) =="
./scripts/e2e-real.sh --profile "$PROFILE" --mode full --strict-capabilities

echo "== L3: real Claude canary (new_basic) =="
l3="passed"
if ! env -u E2E_REAL_E2E_FAKE_CLAUDE ./scripts/e2e-real.sh --profile "$PROFILE" --case new_basic; then
  l3="warn"
  echo "release-regression: L3 canary failed" >&2
  if [[ "$STRICT_L3" -eq 1 ]]; then
    echo "release-regression: --strict-l3 set, L3 failure is a hard gate" >&2
    exit 1
  fi
fi

echo "RELEASE_REGRESSION_OK release=$RELEASE l0l1=passed l2=passed l3=$l3"
