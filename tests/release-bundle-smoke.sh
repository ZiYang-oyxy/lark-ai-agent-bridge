#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <dist-dir> <vMAJOR.MINOR.PATCH[-rc.N]>" >&2
  exit 2
fi

dist_dir="$(cd "$1" && pwd -P)"
tag="$2"
version_dir="$dist_dir/$tag"
stable_manifest="$dist_dir/stable/manifest.json"
version_manifest="$version_dir/manifest.json"

for path in \
  "$version_dir/lark-agent-bridge-linux-amd64" \
  "$version_dir/lark-agent-bridge-darwin-arm64" \
  "$version_dir/AI_INSTALL.md" \
  "$version_dir/release-notes.md" \
  "$version_dir/SHA256SUMS" \
  "$version_manifest" \
  "$dist_dir/stable/AI_INSTALL.md" \
  "$stable_manifest"; do
  [[ -f "$path" ]] || { echo "missing release artifact: $path" >&2; exit 1; }
done

cmp -s "$version_manifest" "$stable_manifest" || {
  echo "stable manifest differs from versioned manifest" >&2
  exit 1
}

canonical="${tag#v}"
jq -e --arg version "$canonical" '
  .schema_version == 1 and
  .version == $version and
  (.assets["linux/amd64"].sha256 | length == 64) and
  (.assets["darwin/arm64"].sha256 | length == 64)
' "$version_manifest" >/dev/null

linux_asset_url="$(jq -r '.assets["linux/amd64"].url' "$version_manifest")"
release_base="${linux_asset_url%/$tag/lark-agent-bridge-linux-amd64}"
version_manifest_url="$release_base/$tag/manifest.json"
stable_manifest_url="$release_base/stable/manifest.json"

grep -Fq "$version_manifest_url" "$version_dir/AI_INSTALL.md" || {
  echo "versioned AI install guide has wrong manifest URL" >&2
  exit 1
}
grep -Fq "$stable_manifest_url" "$dist_dir/stable/AI_INSTALL.md" || {
  echo "stable AI install guide has wrong manifest URL" >&2
  exit 1
}
if grep -Fq '{{MANIFEST_URL}}' "$version_dir/AI_INSTALL.md" "$dist_dir/stable/AI_INSTALL.md"; then
  echo "AI install guide still contains manifest marker" >&2
  exit 1
fi

(cd "$version_dir" && shasum -a 256 -c SHA256SUMS)
file "$version_dir/lark-agent-bridge-linux-amd64" | grep -q 'ELF 64-bit'
file "$version_dir/lark-agent-bridge-darwin-arm64" | grep -Eq 'Mach-O 64-bit .*arm64'

echo "release bundle ok: $tag"
