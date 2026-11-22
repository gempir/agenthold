#!/usr/bin/env bash
# Guest assets for this host's architecture: scripts/release/build-assets.sh [--out DIR]
#
# Builds (or reuses, when its key matches) the guest kernel and the base image in build/guest/<arch>/
# and writes them as assets, named by key, in build/assets/<arch>/:
#   kernel-<key>.zst  kernel-<key>.json  base-<key>.zst  base-<key>.json
# An asset manifest is {"format":1,"kind","arch","key","sha256","size","compressed":{"sha256","size"}};
# release binaries embed the keys and fetch the files from an asset source's /dl/assets/<arch>/.
# Runs natively (no cross builds) as root for the base (mmdebstrap): the demo VM builds arm64, an
# amd64 host (korriban) builds amd64.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
[[ "$(uname -s)" == Linux ]] || { echo 'asset builds require Linux' >&2; exit 1; }
case "$(uname -m)" in
  aarch64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) echo 'unsupported architecture' >&2; exit 1 ;;
esac
OUT="$REPO_ROOT/build/assets/$ARCH"
while (($#)); do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
ART="$REPO_ROOT/build/guest/$ARCH"
mkdir -p "$ART"

KERNEL_KEY="$(bash "$REPO_ROOT/scripts/guest/kernel-key.sh")"
BASE_KEY="$(bash "$REPO_ROOT/scripts/guest/base-key.sh")"
HARNESSES_KEY="$(bash "$REPO_ROOT/scripts/guest/harnesses-key.sh")"

# The kernel is rebuilt only when its key changed (build/guest/<arch>/kernel.key records it).
if [[ ! -f "$ART/kernel.manifest.json" || "$(cat "$ART/kernel.key" 2>/dev/null)" != "$KERNEL_KEY" ]]; then
  bash "$REPO_ROOT/scripts/guest/build-kernel.sh"
  printf '%s\n' "$KERNEL_KEY" > "$ART/kernel.key"
fi
KERNEL="$ART/$(jq -er .file "$ART/kernel.manifest.json")"
echo "$(jq -er .sha256 "$ART/kernel.manifest.json")  $KERNEL" | sha256sum --check --status

BASE_MANIFEST="$ART/base-$ARCH.manifest.json"
BASE="$ART/base-$ARCH.ext4"
if [[ ! -f "$BASE" || "$(jq -r '.inputs.baseKey // empty' "$BASE_MANIFEST" 2>/dev/null || true)" != "$BASE_KEY" ]]; then
  bash "$REPO_ROOT/scripts/guest/build-rootfs.sh"
fi
[[ "$(jq -er '.inputs.baseKey' "$BASE_MANIFEST")" == "$BASE_KEY" ]] || { echo 'base image was not built from these inputs' >&2; exit 1; }
echo "$(jq -er .sha256 "$BASE_MANIFEST")  $BASE" | sha256sum --check --status
if [[ ! -f "$BASE.zst" || "$(jq -r '.zstd.sha256 // empty' "$BASE_MANIFEST")" == '' ]]; then
  zstd -q -f -T0 -6 --long=27 -o "$BASE.zst" "$BASE"
  jq --arg sha "$(sha256sum "$BASE.zst" | cut -d' ' -f1)" --argjson size "$(stat -c %s "$BASE.zst")" \
    '.zstd = {file: (.file + ".zst"), size: $size, sha256: $sha}' "$BASE_MANIFEST" > "$BASE_MANIFEST.tmp"
  mv "$BASE_MANIFEST.tmp" "$BASE_MANIFEST"
fi
echo "$(jq -er .zstd.sha256 "$BASE_MANIFEST")  $BASE.zst" | sha256sum --check --status
[[ "$(stat -c %s "$BASE.zst")" -le 2000000000 ]] || { echo 'compressed base exceeds the 2 GB download budget' >&2; exit 1; }

HARNESSES_MANIFEST="$ART/harnesses-$ARCH.manifest.json"
HARNESSES="$ART/harnesses-$ARCH.ext4"
if [[ ! -f "$HARNESSES" || "$(jq -r '.inputs.harnessesKey // empty' "$HARNESSES_MANIFEST" 2>/dev/null || true)" != "$HARNESSES_KEY" ]]; then
  bash "$REPO_ROOT/scripts/guest/build-harnesses.sh"
fi
[[ "$(jq -er '.inputs.harnessesKey' "$HARNESSES_MANIFEST")" == "$HARNESSES_KEY" ]] || { echo 'harnesses image was not built from these inputs' >&2; exit 1; }
echo "$(jq -er .sha256 "$HARNESSES_MANIFEST")  $HARNESSES" | sha256sum --check --status
if [[ ! -f "$HARNESSES.zst" || "$(jq -r '.zstd.sha256 // empty' "$HARNESSES_MANIFEST")" == '' ]]; then
  zstd -q -f -T0 -6 --long=27 -o "$HARNESSES.zst" "$HARNESSES"
  jq --arg sha "$(sha256sum "$HARNESSES.zst" | cut -d' ' -f1)" --argjson size "$(stat -c %s "$HARNESSES.zst")" \
    '.zstd = {file: (.file + ".zst"), size: $size, sha256: $sha}' "$HARNESSES_MANIFEST" > "$HARNESSES_MANIFEST.tmp"
  mv "$HARNESSES_MANIFEST.tmp" "$HARNESSES_MANIFEST"
fi
echo "$(jq -er .zstd.sha256 "$HARNESSES_MANIFEST")  $HARNESSES.zst" | sha256sum --check --status
[[ "$(stat -c %s "$HARNESSES.zst")" -le 2000000000 ]] || { echo 'compressed harnesses exceeds the 2 GB download budget' >&2; exit 1; }

# asset <kind> <key> <raw file> <compressed file>: write <kind>-<key>.zst (a hard link where
# possible) and its manifest into OUT.
asset() {
  local kind="$1" key="$2" raw="$3" zst="$4"
  ln -f "$zst" "$OUT/$kind-$key.zst" 2>/dev/null || cp "$zst" "$OUT/$kind-$key.zst"
  jq -n --arg kind "$kind" --arg arch "$ARCH" --arg key "$key" \
    --arg sha "$(sha256sum "$raw" | cut -d' ' -f1)" --argjson size "$(stat -c %s "$raw")" \
    --arg csha "$(sha256sum "$OUT/$kind-$key.zst" | cut -d' ' -f1)" --argjson csize "$(stat -c %s "$OUT/$kind-$key.zst")" \
    '{format: 1, kind: $kind, arch: $arch, key: $key, sha256: $sha, size: $size, compressed: {sha256: $csha, size: $csize}}' \
    > "$OUT/$kind-$key.json"
}
if [[ ! -f "$KERNEL.zst" || "$KERNEL.zst" -ot "$KERNEL" ]]; then
  zstd -q -f -T0 -19 -o "$KERNEL.zst" "$KERNEL"
fi
asset kernel "$KERNEL_KEY" "$KERNEL" "$KERNEL.zst"
asset base "$BASE_KEY" "$BASE" "$BASE.zst"
asset harnesses "$HARNESSES_KEY" "$HARNESSES" "$HARNESSES.zst"
printf 'Assets: %s\n' "$OUT"
ls -lh "$OUT"
