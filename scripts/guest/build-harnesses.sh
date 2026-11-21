#!/usr/bin/env bash
# Builds the read-only coding harnesses layer ext4 image from images/harnesses.
#
#   scripts/guest/build-harnesses.sh [--out DIR] [--no-zstd]
#
# Runs on Linux (arm64 or amd64, natively: no cross builds) as root, or from macOS
# re-runs itself inside the Lima VM "agenthold". Because the harnesses layer sits
# above the Debian base image in overlayfs, bumping Claude Code, Codex, or OpenCode
# rebuilds only this small ext4 layer in seconds instead of rebuilding the full rootfs.
#
# Output (default build/guest/<arch>/ in the repo, git-ignored):
#   harnesses-<arch>.ext4             the read-only layer image
#   harnesses-<arch>.ext4.sha256      sha256sum format
#   harnesses-<arch>.ext4.zst         zstd -6 --long, for release assets (unless --no-zstd)
#   harnesses-<arch>.manifest.json    size, sha256, compressed size/sha256, inputs
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if [[ "$(uname -s)" == "Darwin" ]]; then
  command -v limactl >/dev/null || { echo "limactl not found: brew install lima" >&2; exit 1; }
  make -C "$REPO_ROOT" dev-vm >/dev/null
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- bash "$REPO_ROOT/scripts/guest/build-harnesses.sh" "$@"
fi
if [[ $EUID -ne 0 ]]; then
  exec sudo -E env "PATH=$PATH" bash "${BASH_SOURCE[0]}" "$@"
fi

OUT=""
ZSTD=1
while (($#)); do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
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
HARNESS_DIR="$REPO_ROOT/images/harnesses"
# shellcheck source=../../images/base/versions.env
. "$REPO_ROOT/images/base/versions.env"

log() { printf '\n==> %s\n' "$*"; }

need=()
for pair in mkfs.ext4:e2fsprogs zstd:zstd jq:jq curl:curl; do
  command -v "${pair%%:*}" >/dev/null || need+=("${pair#*:}")
done
if ((${#need[@]})); then
  log "Installing build dependencies: ${need[*]}"
  DEBIAN_FRONTEND=noninteractive apt-get install -y -q --no-install-recommends "${need[@]}"
fi

HARNESSES_KEY="$(bash "$REPO_ROOT/scripts/guest/harnesses-key.sh")"
SOURCE_DATE_EPOCH="$(date -u -d "$(sed -E 's/^(....)(..)(..)T(..)(..)(..)Z$/\1-\2-\3 \4:\5:\6/' <<<"$DEBIAN_SNAPSHOT")" +%s)"
export SOURCE_DATE_EPOCH
FS_UUID="$(printf 'agenthold-harnesses-%s-%s' "$ARCH" "$HARNESSES_KEY" | sha256sum | sed -E 's/^(.{8})(.{4}).(.{3}).(.{3})(.{12}).*/\1-\2-4\3-8\4-\5/')"

WORK="/var/tmp/agenthold-harnesses-$ARCH"
CACHE="/var/cache/agenthold-guest"
mkdir -p "$OUT" "$CACHE"
rm -rf "$WORK"
mkdir -p "$WORK"
trap 'rm -rf "$WORK"' EXIT

t0=$(date +%s)

# --- pinned Node.js toolchain (shared cache with build-rootfs.sh) -----------------------------------
if [[ "$ARCH" == arm64 ]]; then node_arch=arm64; node_sha="$NODE_SHA256_ARM64"; else node_arch=x64; node_sha="$NODE_SHA256_X64"; fi
NODE_TARBALL="$CACHE/node-v$NODE_VERSION-linux-$node_arch.tar.xz"
if ! echo "$node_sha  $NODE_TARBALL" | sha256sum -c --quiet - 2>/dev/null; then
  log "Downloading Node.js $NODE_VERSION"
  curl -fsSL --retry 3 -o "$NODE_TARBALL.part" "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-$node_arch.tar.xz"
  echo "$node_sha  $NODE_TARBALL.part" | sha256sum -c --quiet -
  mv "$NODE_TARBALL.part" "$NODE_TARBALL"
fi
mkdir -p "$WORK/node"
tar -xJf "$NODE_TARBALL" -C "$WORK/node" --strip-components=1 --no-same-owner

# --- install harnesses into layer tree --------------------------------------------------------------
TREE="$WORK/tree"
mkdir -p "$TREE/opt/agenthold/tools" "$TREE/usr/local/bin" "$TREE/etc/agenthold"
cp "$HARNESS_DIR/package.json" "$HARNESS_DIR/package-lock.json" "$TREE/opt/agenthold/tools/"
log "Installing harnesses ($ARCH) with Node.js $NODE_VERSION"
PATH="$WORK/node/bin:$PATH" HOME="$WORK/home" npm_config_cache="$WORK/npm-cache" \
  npm ci --prefix "$TREE/opt/agenthold/tools" --omit=dev --no-fund --no-audit --no-update-notifier
for bin in "$TREE"/opt/agenthold/tools/node_modules/.bin/*; do
  [[ -e "$bin" || -L "$bin" ]] || continue
  name="$(basename "$bin")"
  ln -sf "/opt/agenthold/tools/node_modules/.bin/$name" "$TREE/usr/local/bin/$name"
done
printf '%s\n' "$HARNESSES_KEY" >"$TREE/etc/agenthold/harnesses-version"
chown -R 0:0 "$TREE"
find "$TREE" -xdev -newermt "@$SOURCE_DATE_EPOCH" -exec touch --no-dereference --date="@$SOURCE_DATE_EPOCH" {} + 2>/dev/null || true

# --- ext4 -------------------------------------------------------------------------------------------
NAME="harnesses-$ARCH"
IMG="$OUT/$NAME.ext4"
tree_mb=$(du -sm --apparent-size "$TREE" | cut -f1)
inodes=$(( $(find "$TREE" -xdev | wc -l) * 5 / 4 + 1024 ))
size_mb=$(( tree_mb * 6 / 5 + 64 ))
log "ext4 image (tree ${tree_mb} MiB, ${inodes} inodes)"
rm -f "$IMG" "$IMG.part"
truncate -s "${size_mb}M" "$IMG.part"
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" mkfs.ext4 -q -F -L ah-harnesses -U "$FS_UUID" \
  -E "hash_seed=$FS_UUID,lazy_itable_init=0,nodiscard" -O ^has_journal -m 0 -N "$inodes" \
  -T default -d "$TREE" "$IMG.part"
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" debugfs -w -R "ssv kbytes_written 0" "$IMG.part" >/dev/null 2>&1
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" E2FSCK_TIME="$SOURCE_DATE_EPOCH" e2fsck -fy "$IMG.part" >/dev/null 2>&1 || [[ $? -le 1 ]]
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" resize2fs -M "$IMG.part" >/dev/null 2>&1
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" E2FSCK_TIME="$SOURCE_DATE_EPOCH" e2fsck -fy "$IMG.part" >/dev/null 2>&1 || [[ $? -le 1 ]]
dumpe2fs "$IMG.part" 2>/dev/null | awk -v sde="$SOURCE_DATE_EPOCH" '
  /^Inode count:/ {n = $3}
  /^  Free inodes: / {
    sub(/^  Free inodes: /, ""); k = split($0, r, ", ")
    for (i = 1; i <= k; i++) if (r[i] != "") { split(r[i], ab, "-"); hi = (ab[2] == "" ? ab[1] : ab[2]); for (j = ab[1]; j <= hi; j++) free[j] = 1 }
  }
  END { for (i = 1; i <= n; i++) if (!(i in free)) printf "sif <%d> atime @%d\nsif <%d> ctime @%d\n", i, sde, i, sde }' >"$WORK/pin-times"
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" debugfs -w -f "$WORK/pin-times" "$IMG.part" >/dev/null 2>&1
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" E2FSCK_TIME="$SOURCE_DATE_EPOCH" e2fsck -fy "$IMG.part" >/dev/null 2>&1 || [[ $? -le 1 ]]
blocks=$(dumpe2fs -h "$IMG.part" 2>/dev/null | awk -F: '/^Block count/ {gsub(/ /,"",$2); print $2}')
bsize=$(dumpe2fs -h "$IMG.part" 2>/dev/null | awk -F: '/^Block size/ {gsub(/ /,"",$2); print $2}')
truncate -s $(( blocks * bsize )) "$IMG.part"
printf 'ssv mtime @%s\nssv lastcheck @%s\nssv wtime @%s\nssv kbytes_written 0\n' \
  "$SOURCE_DATE_EPOCH" "$SOURCE_DATE_EPOCH" "$SOURCE_DATE_EPOCH" >"$WORK/pin-super"
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" debugfs -w -f "$WORK/pin-super" "$IMG.part" >/dev/null 2>&1
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

jq -n --arg arch "$ARCH" --arg file "$NAME.ext4" --argjson size "$size" \
  --arg sha256 "$sha" --argjson zstdSize "$zsize" --argjson zstdSha256 "$zsha" \
  --arg fsUuid "$FS_UUID" --arg node "$NODE_VERSION" \
  --arg npmLockSha256 "$(sha256sum "$HARNESS_DIR/package-lock.json" | cut -d' ' -f1)" \
  --argjson harnesses "$(jq '.dependencies' "$HARNESS_DIR/package.json")" \
  --arg harnessesKey "$HARNESSES_KEY" --argjson treeMiB "$tree_mb" --argjson sde "$SOURCE_DATE_EPOCH" \
  '{arch: $arch, format: "ext4", file: $file, size: $size, sha256: $sha256,
    zstd: (if $zstdSize == null then null else {file: ($file + ".zst"), size: $zstdSize, sha256: $zstdSha256} end),
    fsUuid: $fsUuid, treeMiB: $treeMiB, sourceDateEpoch: $sde,
    inputs: {node: $node, harnesses: $harnesses, npmLockSha256: $npmLockSha256, harnessesKey: $harnessesKey}}' >"$OUT/$NAME.manifest.json"

if [[ -n "${SUDO_UID:-}" ]]; then chown -R "$SUDO_UID:${SUDO_GID:-$SUDO_UID}" "$OUT"; fi

log "Done in $(( $(date +%s) - t0 ))s"
cat "$OUT/$NAME.manifest.json"
