#!/usr/bin/env bash
# Fetch the pinned Executor standalone runtime for this host (or --os/--arch).
#
# Usage:
#   scripts/guest/fetch-executor.sh [--out DIR] [--os linux|darwin] [--arch arm64|x64]
#   scripts/guest/fetch-executor.sh --zst FILE [--os ...] [--arch ...]
#   scripts/guest/fetch-executor.sh --print-version
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="2.0.0-beta.79"

OUT=""
ZST_OUT=""
OS_OVERRIDE=""
ARCH_OVERRIDE=""
while (($#)); do
  case "$1" in
    --print-version) echo "$VERSION"; exit 0 ;;
    --out) OUT="$2"; shift 2 ;;
    --zst) ZST_OUT="$2"; shift 2 ;;
    --os) OS_OVERRIDE="$2"; shift 2 ;;
    --arch) ARCH_OVERRIDE="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "${OS_OVERRIDE:-$(uname -s)}" in
  Linux|linux) PLATFORM=linux ;;
  Darwin|darwin) PLATFORM=darwin ;;
  *) echo "unsupported OS: ${OS_OVERRIDE:-$(uname -s)}" >&2; exit 1 ;;
esac
case "${ARCH_OVERRIDE:-$(uname -m)}" in
  aarch64|arm64) ARCH=arm64 ;;
  x86_64|amd64|x64) ARCH=x64 ;;
  *) echo "unsupported architecture: ${ARCH_OVERRIDE:-$(uname -m)}" >&2; exit 1 ;;
esac

if [[ -z "$OUT" ]]; then
  OUT="$REPO_ROOT/build/runtime/executor-$PLATFORM-$ARCH"
fi

case "$PLATFORM-$ARCH" in
  linux-arm64)  SHA="929029641efc49d8d09e436054bdcdc0eed2282ecbf48274be50cb7e0ef38bae" ;;
  linux-x64)    SHA="5c0fba1e77db7866b4dcb5addf80f9227c8f917f21c74141d2f9f61176ca9efe" ;;
  darwin-arm64) SHA="67e5044549ae3abfd989ab2bcd4e562685da6a8fe6f15374550bff337dab1d48" ;;
  darwin-x64)   SHA="9379fc5461b1395854e8f7d63bb9456478c148755d006d05e4877d82e1fcc7e0" ;;
  *) echo "no pinned Executor build for $PLATFORM-$ARCH" >&2; exit 1 ;;
esac

sha256_of() {
  local file="$1"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    sha256sum "$file" | awk '{print $1}'
  fi
}

sha256_check() {
  local expected="$1" file="$2"
  [[ -f "$file" ]] || return 1
  [[ "$(sha256_of "$file")" == "$expected" ]]
}

CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/agenthold-runtime"
mkdir -p "$CACHE"
FILE="executor-$VERSION-$PLATFORM-$ARCH.tgz"
ARCHIVE="$CACHE/$FILE"
PART="$(mktemp "$CACHE/executor-download.XXXXXX")"
STAGE="$(mktemp -d "$CACHE/executor-stage.XXXXXX")"
trap 'rm -f "$PART"; rm -rf "$STAGE"' EXIT

if ! sha256_check "$SHA" "$ARCHIVE"; then
  SHARED="/var/cache/agenthold-guest/$FILE"
  if sha256_check "$SHA" "$SHARED"; then
    cp "$SHARED" "$PART"
  else
    BASE="${EXECUTOR_DOWNLOAD_URL:-https://github.com/UsefulSoftwareCo/executor/releases/download}"
    curl -fL --retry 3 -o "$PART" "${BASE%/}/executor%40$VERSION/$FILE"
  fi
  sha256_check "$SHA" "$PART" || { echo "checksum mismatch for $FILE" >&2; exit 1; }
  mv "$PART" "$ARCHIVE"
fi

if [[ -n "$ZST_OUT" ]]; then
  command -v zstd >/dev/null 2>&1 || { echo "zstd is required for --zst" >&2; exit 1; }
  mkdir -p "$(dirname "$ZST_OUT")"
  ZST_TMP="$(mktemp "$(dirname "$ZST_OUT")/.executor-zst.XXXXXX")"
  trap 'rm -f "$PART" "$ZST_TMP"; rm -rf "$STAGE"' EXIT
  gzip -dc "$ARCHIVE" | zstd -f -q -6 -T0 -o "$ZST_TMP"
  mv "$ZST_TMP" "$ZST_OUT"
  echo "Executor $VERSION ($PLATFORM/$ARCH) -> $ZST_OUT"
  exit 0
fi

tar -xzf "$ARCHIVE" -C "$STAGE"
[[ -x "$STAGE/package/executor" ]] || { echo "extracted archive missing package/executor" >&2; exit 1; }
mkdir -p "$(dirname "$OUT")"
DEST_TMP="$(mktemp -d "$(dirname "$OUT")/.executor-install.XXXXXX")"
rm -rf "$DEST_TMP"
mv "$STAGE/package" "$DEST_TMP"
printf '%s\n' "$SHA" > "$DEST_TMP/.executor.sha256"
rm -rf "$OUT"
mv "$DEST_TMP" "$OUT"
if [[ "$PLATFORM" == "$(uname -s | tr '[:upper:]' '[:lower:]')" ]]; then
  "$OUT/executor" --version
else
  echo "Executor $VERSION ($PLATFORM/$ARCH) -> $OUT/executor"
fi
