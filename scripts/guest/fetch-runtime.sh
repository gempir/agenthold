#!/usr/bin/env bash
# Fetch the pinned Firecracker+jailer pair for native Linux builds.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ "$(uname -s)" == Darwin ]]; then
  make -C "$REPO_ROOT" dev-vm >/dev/null
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- bash "$REPO_ROOT/scripts/guest/fetch-runtime.sh" "$@"
fi
OUT="$REPO_ROOT/build/runtime"
while (($#)); do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
VERSION=v1.18.0
case "$(uname -m)" in
  aarch64) ARCH=aarch64; SHA=2a6130ff0338901ff9d7fbd73ce046c6b1c6123889dc84a34d678aa53bd0c0c2 ;;
  x86_64) ARCH=x86_64; SHA=1ac8eec866dd5eea14efecf6323e22b23eea24054545338a5b34bc5bca6623cd ;;
  *) echo 'unsupported architecture' >&2; exit 1 ;;
esac
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/agenthold-runtime"
mkdir -p "$CACHE" "$OUT"
ARCHIVE="$CACHE/firecracker-$VERSION-$ARCH.tgz"
PART="$(mktemp "$CACHE/download.XXXXXX")"
STAGE="$(mktemp -d "$OUT/.runtime.XXXXXX")"
trap 'rm -f "$PART"; rm -rf "$STAGE"' EXIT
if ! echo "$SHA  $ARCHIVE" | sha256sum --check --status 2>/dev/null; then
  SHARED="/var/cache/agenthold-guest/firecracker-$VERSION-$ARCH.tgz"
  if echo "$SHA  $SHARED" | sha256sum --check --status 2>/dev/null; then
    cp "$SHARED" "$PART"
  else
    curl -fL --retry 3 -o "$PART" "https://github.com/firecracker-microvm/firecracker/releases/download/$VERSION/firecracker-$VERSION-$ARCH.tgz"
  fi
  echo "$SHA  $PART" | sha256sum --check --status
  mv "$PART" "$ARCHIVE"
fi
for binary in firecracker jailer; do
  tar -xzf "$ARCHIVE" -O "release-$VERSION-$ARCH/$binary-$VERSION-$ARCH" > "$STAGE/$binary"
  chmod 0755 "$STAGE/$binary"
  mv "$STAGE/$binary" "$OUT/$binary"
done
"$OUT/firecracker" --version
"$OUT/jailer" --version
