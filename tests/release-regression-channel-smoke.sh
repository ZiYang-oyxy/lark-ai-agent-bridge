#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/release-regression.sh"

grep -Fq -- '--arg channel "$channel"' "$ROOT/scripts/release-regression.sh"
grep -Fq -- 'release:$release,channel:$channel,strict_l3:true' "$ROOT/scripts/release-regression.sh"
if grep -Fq -- 'channel:"stable"' "$ROOT/scripts/release-regression.sh"; then
  echo "regression evidence must not fix channel to stable" >&2
  exit 1
fi
"$ROOT/scripts/release-regression.sh" --help | grep -Fq 'strict evidence requires vMAJOR.MINOR.PATCH[-rc.N]'

assert_channel() {
  local release="$1" want="$2" got
  got="$(derive_release_channel "$release")"
  [[ "$got" == "$want" ]] || {
    echo "channel mismatch for $release: got $got, want $want" >&2
    exit 1
  }
}

assert_invalid() {
  local release="$1"
  if derive_release_channel "$release" >/dev/null 2>&1; then
    echo "expected invalid release to be rejected: $release" >&2
    exit 1
  fi
}

assert_channel v0.0.0 stable
assert_channel v1.2.3 stable
assert_channel v1.2.3-rc.0 rc
assert_channel v1.2.3-rc.7 rc

assert_invalid dev
assert_invalid 1.2.3
assert_invalid v01.2.3
assert_invalid v1.2.3-rc.01
assert_invalid v1.2.3-rc
assert_invalid v1.2.3-beta.1

echo "release regression channel smoke ok"
