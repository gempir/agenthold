#!/usr/bin/env bash
# Download the pinned Linux Go release archive into a staging directory (as go.tar.gz), with a shared
# download cache. provision.sh unpacks it into /usr/local/go.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../../images/base/versions.env
. "$REPO_ROOT/images/base/versions.env"

dest="${1:?usage: install-go.sh DEST CACHE}"
cache="${2:?usage: install-go.sh DEST CACHE}"
case "$(uname -m)" in
  aarch64) goarch=arm64; sha="$GO_SHA256_ARM64" ;;
  x86_64) goarch=amd64; sha="$GO_SHA256_X64" ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "$dest" "$cache"
name="go$GO_VERSION.linux-$goarch.tar.gz"
archive="$cache/$name"
if ! printf '%s  %s\n' "$sha" "$archive" | sha256sum -c --quiet - 2>/dev/null; then
  curl -fsSL --retry 3 -o "$archive.part" "https://go.dev/dl/$name"
  printf '%s  %s\n' "$sha" "$archive.part" | sha256sum -c --quiet -
  mv -f "$archive.part" "$archive"
fi
install -m 0644 "$archive" "$dest/go.tar.gz"
