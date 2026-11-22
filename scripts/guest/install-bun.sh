#!/usr/bin/env bash
# Install the pinned Linux Bun binary into a staging directory, with a shared download cache.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../../images/base/versions.env
. "$REPO_ROOT/images/base/versions.env"

dest="${1:?usage: install-bun.sh DEST CACHE}"
cache="${2:?usage: install-bun.sh DEST CACHE}"
case "$(uname -m)" in
  aarch64) platform=bun-linux-aarch64; sha="$BUN_SHA256_ARM64" ;;
  x86_64) platform=bun-linux-x64-baseline; sha="$BUN_SHA256_X64" ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "$dest" "$cache"
archive="$cache/$platform-$BUN_VERSION.zip"
work="$(mktemp -d "$cache/.bun.XXXXXX")"
trap 'rm -rf "$work"' EXIT
if ! printf '%s  %s\n' "$sha" "$archive" | sha256sum -c --quiet - 2>/dev/null; then
  curl -fsSL --retry 3 -o "$work/bun.zip" "https://github.com/oven-sh/bun/releases/download/bun-v$BUN_VERSION/$platform.zip"
  printf '%s  %s\n' "$sha" "$work/bun.zip" | sha256sum -c --quiet -
  mv -f "$work/bun.zip" "$archive"
fi
unzip -p "$archive" "$platform/bun" >"$work/bun"
install -m 0755 "$work/bun" "$dest/bun"
