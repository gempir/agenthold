#!/usr/bin/env bash
# Builds the read-only base disk image of the microVMs from images/base.
#
#   scripts/guest/build-rootfs.sh [--out DIR] [--sandboxd PATH] [--squashfs] [--no-zstd]
#
# Runs on Linux (arm64 or amd64, natively: no cross builds) as root, for example inside the Lima dev
# VM or on a GitHub runner; from macOS it re-runs itself inside the Lima VM "agenthold".
#
# Build path: mmdebstrap (no Docker or other container runtime) creates a Debian minbase tree from
# snapshot.debian.org at the pinned timestamp, installs images/base/packages.txt and runs
# images/base/provision.sh in a chroot. sandboxd is built from this checkout and installed at
# /opt/agenthold/sandboxd. The tree becomes a journal-less ext4 image with fixed UUID, hash seed and
# timestamps, shrunk to its minimum size.
#
# Output (default build/guest/<arch>/ in the repo, git-ignored):
#   base-<arch>.ext4             the image (attach read-only)
#   base-<arch>.ext4.sha256      sha256sum format
#   base-<arch>.ext4.zst         zstd -6 --long, for release assets (unless --no-zstd)
#   base-<arch>.manifest.json    version, arch, size, sha256, compressed size and sha256, inputs
#   base-<arch>.squashfs         only with --squashfs (for comparisons; not used by the driver)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if [[ "$(uname -s)" == "Darwin" ]]; then
  command -v limactl >/dev/null || { echo "limactl not found: brew install lima" >&2; exit 1; }
  make -C "$REPO_ROOT" dev-vm >/dev/null
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- bash "$REPO_ROOT/scripts/guest/build-rootfs.sh" "$@"
fi
if [[ $EUID -ne 0 ]]; then
  exec sudo -E env "PATH=$PATH" bash "${BASH_SOURCE[0]}" "$@"
fi

OUT=""
SANDBOXD=""
SQUASHFS=0
ZSTD=1
while (($#)); do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --sandboxd) SANDBOXD="$2"; shift 2 ;;
    --squashfs) SQUASHFS=1; shift ;;
    --no-zstd) ZSTD=0; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "$(uname -m)" in
  aarch64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
OUT="${OUT:-$REPO_ROOT/build/guest/$ARCH}"
IMG_DIR="$REPO_ROOT/images/base"
# shellcheck source=../../images/base/versions.env
. "$IMG_DIR/versions.env"

log() { printf '\n==> %s\n' "$*"; }

# --- build dependencies (Debian/Ubuntu hosts) --------------------------------------------------------
need=()
for pair in mmdebstrap:mmdebstrap mkfs.ext4:e2fsprogs zstd:zstd mksquashfs:squashfs-tools jq:jq curl:curl; do
  command -v "${pair%%:*}" >/dev/null || need+=("${pair#*:}")
done
if ((${#need[@]})); then
  log "Installing build dependencies: ${need[*]}"
  DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends "${need[@]}"
fi

# Reproducibility: one timestamp for everything, derived from the Debian snapshot.
SOURCE_DATE_EPOCH="$(date -u -d "$(sed -E 's/^(....)(..)(..)T(..)(..)(..)Z$/\1-\2-\3 \4:\5:\6/' <<<"$DEBIAN_SNAPSHOT")" +%s)"
export SOURCE_DATE_EPOCH
# Stable per architecture, so the same inputs give the same image.
FS_UUID="$(printf 'agenthold-base-%s-%s' "$ARCH" "$BASE_VERSION" | sha256sum | sed -E 's/^(.{8})(.{4}).(.{3}).(.{3})(.{12}).*/\1-\2-4\3-8\4-\5/')"

WORK="/var/tmp/agenthold-rootfs-$ARCH"
CACHE="/var/cache/agenthold-guest"
mkdir -p "$OUT" "$CACHE"
rm -rf "$WORK"
mkdir -p "$WORK"
trap 'rm -rf "$WORK"' EXIT

t0=$(date +%s)

# --- inputs ----------------------------------------------------------------------------------------
if [[ "$ARCH" == arm64 ]]; then node_arch=arm64; node_sha="$NODE_SHA256_ARM64"; else node_arch=x64; node_sha="$NODE_SHA256_X64"; fi
NODE_TARBALL="$CACHE/node-v$NODE_VERSION-linux-$node_arch.tar.xz"
if ! echo "$node_sha  $NODE_TARBALL" | sha256sum -c --quiet - 2>/dev/null; then
  log "Downloading Node.js $NODE_VERSION"
  curl -fsSL --retry 3 -o "$NODE_TARBALL.part" "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-$node_arch.tar.xz"
  echo "$node_sha  $NODE_TARBALL.part" | sha256sum -c --quiet -
  mv "$NODE_TARBALL.part" "$NODE_TARBALL"
fi

KEYRING_DEB="$CACHE/$DEBIAN_KEYRING_DEB"
if ! echo "$DEBIAN_KEYRING_SHA256  $KEYRING_DEB" | sha256sum -c --quiet - 2>/dev/null; then
  log "Downloading $DEBIAN_KEYRING_DEB"
  curl -fsSL --retry 3 -o "$KEYRING_DEB.part" "http://snapshot.debian.org/archive/debian/$DEBIAN_SNAPSHOT/pool/main/d/debian-archive-keyring/$DEBIAN_KEYRING_DEB"
  echo "$DEBIAN_KEYRING_SHA256  $KEYRING_DEB.part" | sha256sum -c --quiet -
  mv "$KEYRING_DEB.part" "$KEYRING_DEB"
fi
dpkg-deb -x "$KEYRING_DEB" "$WORK/keyring"
KEYRING="$WORK/keyring/usr/share/keyrings/debian-archive-keyring.gpg"

if [[ -z "$SANDBOXD" ]]; then
  log "Building sandboxd"
  command -v go >/dev/null || { echo "go not found (or pass --sandboxd PATH)" >&2; exit 1; }
  version="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
  commit="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  # Own caches: this runs as root and must not leave root-owned files in the user's Go cache.
  (cd "$REPO_ROOT" && GOCACHE="$CACHE/go-build" GOMODCACHE="$CACHE/go-mod" CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -trimpath \
    -ldflags "-s -w -X github.com/gempir/agenthold/internal/version.Version=$version -X github.com/gempir/agenthold/internal/version.Commit=$commit" \
    -o "$WORK/sandboxd" ./cmd/sandboxd)
  SANDBOXD="$WORK/sandboxd"
fi

# --- tree (cached: only rebuilt when images/base changes) ---------------------------------------------
# Bump TREE_FORMAT when the mmdebstrap invocation below changes.
TREE_FORMAT=2
tree_key="$( (echo "$TREE_FORMAT $ARCH"; cat "$IMG_DIR/versions.env" "$IMG_DIR/packages.txt" "$IMG_DIR/package.json" "$IMG_DIR/package-lock.json" "$IMG_DIR/provision.sh" "$IMG_DIR/image.env") | sha256sum | cut -c1-16)"
TREE="$CACHE/tree-$ARCH-$tree_key"
if [[ ! -f "$TREE.done" ]]; then
  rm -rf "$CACHE/tree-$ARCH-"*
  log "mmdebstrap $DEBIAN_SUITE ($ARCH) from snapshot $DEBIAN_SNAPSHOT"
  packages="$(grep -vE '^\s*(#|$)' "$IMG_DIR/packages.txt" | paste -sd, -)"
  snap="http://snapshot.debian.org/archive"
  mmdebstrap \
    --mode=root --variant=minbase --arch="$ARCH" --format=directory \
    --keyring="$KEYRING" \
    --include="$packages" \
    --aptopt='Acquire::Check-Valid-Until "false"' \
    --aptopt='Acquire::Retries "5"' \
    --aptopt='APT::Install-Recommends "false"' \
    --dpkgopt='path-exclude=/usr/share/man/*' \
    --dpkgopt='path-exclude=/usr/share/info/*' \
    --dpkgopt='path-exclude=/usr/share/doc/*' \
    --dpkgopt='path-include=/usr/share/doc/*/copyright' \
    --dpkgopt='path-exclude=/usr/share/lintian/*' \
    --dpkgopt='path-exclude=/usr/share/locale/*' \
    --dpkgopt='path-include=/usr/share/locale/locale.alias' \
    --customize-hook='mkdir -p "$1/tmp/agenthold-base"' \
    --customize-hook="copy-in $IMG_DIR/versions.env $IMG_DIR/image.env $IMG_DIR/package.json $IMG_DIR/package-lock.json $IMG_DIR/provision.sh /tmp/agenthold-base" \
    --customize-hook="upload $NODE_TARBALL /tmp/node.tar.xz" \
    --customize-hook='chroot "$1" /bin/bash /tmp/agenthold-base/provision.sh' \
    --customize-hook='rm -rf "$1/tmp/agenthold-base" "$1/etc/resolv.conf" && touch "$1/etc/resolv.conf"' \
    "$DEBIAN_SUITE" "$TREE" \
    "deb $snap/debian/$DEBIAN_SNAPSHOT $DEBIAN_SUITE main" \
    "deb $snap/debian/$DEBIAN_SNAPSHOT $DEBIAN_SUITE-updates main" \
    "deb $snap/debian-security/$DEBIAN_SNAPSHOT $DEBIAN_SUITE-security main"
  # mmdebstrap leaves its own apt config behind (the sources were replaced by provision.sh).
  rm -f "$TREE/etc/apt/apt.conf.d/99mmdebstrap" "$TREE/etc/apt/sources.list"
  touch "$TREE.done"
else
  log "Reusing cached tree $TREE"
fi
install -m 0755 "$SANDBOXD" "$TREE/opt/agenthold/sandboxd"
# Clamp every timestamp (npm, the hooks and sandboxd carry "now").
find "$TREE" -xdev -newermt "@$SOURCE_DATE_EPOCH" -exec touch --no-dereference --date="@$SOURCE_DATE_EPOCH" {} + 2>/dev/null || true

# --- ext4 -------------------------------------------------------------------------------------------
NAME="base-$ARCH"
IMG="$OUT/$NAME.ext4"
tree_mb=$(du -sm --apparent-size "$TREE" | cut -f1)
inodes=$(( $(find "$TREE" -xdev | wc -l) * 5 / 4 + 4096 ))
size_mb=$(( tree_mb * 6 / 5 + 256 ))
log "ext4 image (tree ${tree_mb} MiB, ${inodes} inodes)"
rm -f "$IMG" "$IMG.part"
truncate -s "${size_mb}M" "$IMG.part"
# No journal (the base is only ever mounted read-only), no reserved blocks, fixed identity.
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" mkfs.ext4 -q -F -L agenthold-base -U "$FS_UUID" \
  -E "hash_seed=$FS_UUID,lazy_itable_init=0,nodiscard" -O ^has_journal -m 0 -N "$inodes" \
  -T default -d "$TREE" "$IMG.part"
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" e2fsck -fy "$IMG.part" >/dev/null 2>&1 || [[ $? -le 1 ]]
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" resize2fs -M "$IMG.part" >/dev/null 2>&1
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" e2fsck -fy "$IMG.part" >/dev/null 2>&1 || [[ $? -le 1 ]]
blocks=$(dumpe2fs -h "$IMG.part" 2>/dev/null | awk -F: '/^Block count/ {gsub(/ /,"",$2); print $2}')
bsize=$(dumpe2fs -h "$IMG.part" 2>/dev/null | awk -F: '/^Block size/ {gsub(/ /,"",$2); print $2}')
truncate -s $(( blocks * bsize )) "$IMG.part"
# Last-mount/check times are part of the superblock; pin them as well.
debugfs -w -R "ssv mtime $SOURCE_DATE_EPOCH" "$IMG.part" >/dev/null 2>&1 || true
debugfs -w -R "ssv lastcheck $SOURCE_DATE_EPOCH" "$IMG.part" >/dev/null 2>&1 || true
debugfs -w -R "ssv wtime $SOURCE_DATE_EPOCH" "$IMG.part" >/dev/null 2>&1 || true
mv "$IMG.part" "$IMG"

size=$(stat -c %s "$IMG")
sha=$(sha256sum "$IMG" | cut -d' ' -f1)
echo "$sha  $NAME.ext4" >"$IMG.sha256"

zsize=null zsha=null
rm -f "$IMG.zst"
if ((ZSTD)); then
  log "zstd"
  zstd -q -T0 -6 --long=27 -o "$IMG.zst" "$IMG"
  zsize=$(stat -c %s "$IMG.zst")
  zsha="\"$(sha256sum "$IMG.zst" | cut -d' ' -f1)\""
fi

sqsize=null
if ((SQUASHFS)); then
  log "squashfs (comparison only)"
  rm -f "$OUT/$NAME.squashfs"
  mksquashfs "$TREE" "$OUT/$NAME.squashfs" -comp zstd -Xcompression-level 15 -b 128K -noappend -quiet
  sqsize=$(stat -c %s "$OUT/$NAME.squashfs")
fi

sandboxd_sha=$(sha256sum "$TREE/opt/agenthold/sandboxd" | cut -d' ' -f1)
jq -n --arg version "$BASE_VERSION" --arg arch "$ARCH" --arg file "$NAME.ext4" --argjson size "$size" \
  --arg sha256 "$sha" --argjson zstdSize "$zsize" --argjson zstdSha256 "$zsha" --argjson squashfsSize "$sqsize" \
  --arg fsUuid "$FS_UUID" --arg debian "$DEBIAN_SUITE@$DEBIAN_SNAPSHOT" --arg node "$NODE_VERSION" \
  --arg npmLockSha256 "$(sha256sum "$IMG_DIR/package-lock.json" | cut -d' ' -f1)" \
  --argjson harnesses "$(jq '.dependencies' "$IMG_DIR/package.json")" \
  --arg sandboxd "$sandboxd_sha" --argjson treeMiB "$tree_mb" --argjson sde "$SOURCE_DATE_EPOCH" \
  '{version: $version, arch: $arch, format: "ext4", file: $file, size: $size, sha256: $sha256,
    zstd: (if $zstdSize == null then null else {file: ($file + ".zst"), size: $zstdSize, sha256: $zstdSha256} end),
    squashfsSize: $squashfsSize, fsUuid: $fsUuid, treeMiB: $treeMiB, sourceDateEpoch: $sde,
    inputs: {debian: $debian, node: $node, harnesses: $harnesses, npmLockSha256: $npmLockSha256, sandboxdSha256: $sandboxd}}' >"$OUT/$NAME.manifest.json"

# Hand the outputs to the invoking user when run through sudo (the repo is theirs).
if [[ -n "${SUDO_UID:-}" ]]; then chown -R "$SUDO_UID:${SUDO_GID:-$SUDO_UID}" "$OUT"; fi

log "Done in $(( $(date +%s) - t0 ))s"
cat "$OUT/$NAME.manifest.json"
