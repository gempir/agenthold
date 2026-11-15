#!/usr/bin/env bash
# Builds the pinned guest kernel for the microVMs.
#
#   scripts/guest/build-kernel.sh [--out DIR] [--jobs N]
#
# Runs on Linux (arm64 or amd64, natively) as any user; from macOS it re-runs itself inside the Lima
# VM "agenthold". Missing build tools are installed with apt (needs sudo).
#
# Inputs, all pinned by version and sha256:
#   - Linux $KERNEL_VERSION from cdn.kernel.org,
#   - Firecracker $FC_VERSION's resources/guest_configs/microvm-kernel-ci-<arch>-6.1.config and
#     ci.config (the configuration of Firecracker's own CI kernels),
#   - scripts/guest/kernel/agenthold.config and agenthold-<arch>.config (our additions).
# The configs are concatenated and resolved with `make olddefconfig`; the build fails if any line of
# our fragments did not survive. Build metadata (timestamp, user, host) is fixed.
#
# Output (default build/guest/<arch>/ in the repo, git-ignored):
#   vmlinux-<release>            arm64: the uncompressed Image; amd64: the ELF vmlinux
#   vmlinux-<release>.config     the resolved configuration
#   vmlinux-<release>.sha256     sha256sum format
#   kernel.manifest.json         release, arch, size, sha256, inputs
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if [[ "$(uname -s)" == "Darwin" ]]; then
  command -v limactl >/dev/null || { echo "limactl not found: brew install lima" >&2; exit 1; }
  make -C "$REPO_ROOT" dev-vm >/dev/null
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- bash "$REPO_ROOT/scripts/guest/build-kernel.sh" "$@"
fi

KERNEL_VERSION=6.1.188
KERNEL_SHA256=ed4d0acb1307c235230c89efc094e210e6290593f94a7e617f28b1001101a33a
FC_VERSION=v1.18.0
# Firecracker publishes no checksums for these; pinned here (an empty pin prints the hash and fails).
FC_CONFIG_SHA256_ARM64=5c73202d00b5591ad987d9f808208a318e317fd32dbc17de613c5a93fabe2939
FC_CONFIG_SHA256_AMD64=153ca1b40f3312bfb40b7587b471ea548a915f98f480f0d0892a1537957a2147
FC_CI_CONFIG_SHA256=6ce47107d9fb695a873ffc4733a1d8e1b4941c4f272e20dac4bf163dfb77f47e
# Fixed build metadata (shows up in uname -v), so rebuilds with the same toolchain are identical.
BUILD_TIMESTAMP="2026-11-10 00:00:00 UTC"

OUT=""
JOBS="$(nproc)"
while (($#)); do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --jobs) JOBS="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "$(uname -m)" in
  aarch64) ARCH=arm64; FC_ARCH=aarch64; TARGET=Image; BINARY=arch/arm64/boot/Image; FC_CONFIG_SHA256="$FC_CONFIG_SHA256_ARM64" ;;
  x86_64) ARCH=amd64; FC_ARCH=x86_64; TARGET=vmlinux; BINARY=vmlinux; FC_CONFIG_SHA256="$FC_CONFIG_SHA256_AMD64" ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
OUT="${OUT:-$REPO_ROOT/build/guest/$ARCH}"
FRAG_DIR="$REPO_ROOT/scripts/guest/kernel"

log() { printf '\n==> %s\n' "$*"; }

need=()
for pair in gcc:build-essential make:make flex:flex bison:bison bc:bc xz:xz-utils curl:curl jq:jq perl:perl; do
  command -v "${pair%%:*}" >/dev/null || need+=("${pair#*:}")
done
dpkg -s libelf-dev >/dev/null 2>&1 || need+=(libelf-dev)
dpkg -s libssl-dev >/dev/null 2>&1 || need+=(libssl-dev)
if ((${#need[@]})); then
  log "Installing build dependencies: ${need[*]}"
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends "${need[@]}"
fi

CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/agenthold-kernel"
WORK="/var/tmp/agenthold-kernel-$ARCH-$(id -u)"
mkdir -p "$CACHE" "$OUT" "$WORK"

fetch() { # url dest sha256
  local url="$1" dest="$2" sha="$3"
  if [[ -n "$sha" && -f "$dest" ]] && echo "$sha  $dest" | sha256sum -c --quiet - 2>/dev/null; then return; fi
  echo "downloading $url"
  curl -fsSL --retry 3 -o "$dest.part" "$url"
  if [[ -z "$sha" ]]; then
    echo "no pin for $url; its sha256 is $(sha256sum "$dest.part" | cut -d' ' -f1)" >&2
    exit 1
  fi
  echo "$sha  $dest.part" | sha256sum -c --quiet - || { echo "sha256 mismatch for $url" >&2; rm -f "$dest.part"; exit 1; }
  mv "$dest.part" "$dest"
}

t0=$(date +%s)
log "Fetching pinned inputs"
TARBALL="$CACHE/linux-$KERNEL_VERSION.tar.xz"
fetch "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-$KERNEL_VERSION.tar.xz" "$TARBALL" "$KERNEL_SHA256"
FC_RAW="https://raw.githubusercontent.com/firecracker-microvm/firecracker/$FC_VERSION/resources/guest_configs"
FC_CONFIG="$CACHE/microvm-kernel-ci-$FC_ARCH-6.1-$FC_VERSION.config"
FC_CI_CONFIG="$CACHE/ci-$FC_VERSION.config"
fetch "$FC_RAW/microvm-kernel-ci-$FC_ARCH-6.1.config" "$FC_CONFIG" "$FC_CONFIG_SHA256"
fetch "$FC_RAW/ci.config" "$FC_CI_CONFIG" "$FC_CI_CONFIG_SHA256"

SRC="$WORK/linux-$KERNEL_VERSION"
if [[ ! -f "$SRC/.agenthold-extracted" ]]; then
  log "Extracting linux-$KERNEL_VERSION"
  rm -rf "$SRC"
  tar -xJf "$TARBALL" -C "$WORK"
  touch "$SRC/.agenthold-extracted"
fi

log "Configuring ($FC_VERSION microvm config + ci.config + agenthold fragments)"
FRAGS=("$FRAG_DIR/agenthold.config" "$FRAG_DIR/agenthold-$ARCH.config")
awk 1 "$FC_CONFIG" "$FC_CI_CONFIG" "${FRAGS[@]}" >"$SRC/.config"
make -C "$SRC" -s olddefconfig
# Every option we ask for must be in the result exactly (olddefconfig silently drops options whose
# dependencies are missing).
bad=0
while IFS= read -r line; do
  [[ "$line" =~ ^CONFIG_([A-Za-z0-9_]+)=(.*)$ ]] || continue
  opt="${BASH_REMATCH[1]}" want="${BASH_REMATCH[2]}"
  if [[ "$want" == n ]]; then
    grep -qE "^CONFIG_$opt=" "$SRC/.config" && { echo "config: CONFIG_$opt should be off" >&2; bad=1; }
  else
    grep -qxF "CONFIG_$opt=$want" "$SRC/.config" || { echo "config: CONFIG_$opt=$want did not survive olddefconfig (got: $(grep -E "^(# )?CONFIG_$opt[= ]" "$SRC/.config" || echo nothing))" >&2; bad=1; }
  fi
done < <(cat "${FRAGS[@]}")
((bad == 0)) || exit 1

log "Building $TARGET with $JOBS jobs"
export KBUILD_BUILD_TIMESTAMP="$BUILD_TIMESTAMP"
export KBUILD_BUILD_USER=agenthold KBUILD_BUILD_HOST=agenthold KBUILD_BUILD_VERSION=1
make -C "$SRC" -s -j "$JOBS" "$TARGET"

RELEASE="$(cat "$SRC/include/config/kernel.release")"
NAME="vmlinux-$RELEASE"
cp "$SRC/$BINARY" "$OUT/$NAME"
cp "$SRC/.config" "$OUT/$NAME.config"
sha=$(sha256sum "$OUT/$NAME" | cut -d' ' -f1)
echo "$sha  $NAME" >"$OUT/$NAME.sha256"
jq -n --arg release "$RELEASE" --arg arch "$ARCH" --arg file "$NAME" --argjson size "$(stat -c %s "$OUT/$NAME")" \
  --arg sha256 "$sha" --arg configSha256 "$(sha256sum "$OUT/$NAME.config" | cut -d' ' -f1)" \
  --arg linux "$KERNEL_VERSION" --arg firecracker "$FC_VERSION" --arg gcc "$(gcc -dumpfullversion)" \
  '{release: $release, arch: $arch, file: $file, size: $size, sha256: $sha256, configSha256: $configSha256,
    inputs: {linux: $linux, firecrackerConfig: $firecracker, gcc: $gcc}}' >"$OUT/kernel.manifest.json"

log "Done in $(( $(date +%s) - t0 ))s"
cat "$OUT/kernel.manifest.json"
