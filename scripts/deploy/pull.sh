#!/usr/bin/env bash
# Deploy the tested release a source offers and switch the already-installed demo service.
#
#   pull.sh            deploy the source's channel if it is new (what agenthold-pull.timer runs)
#   pull.sh --check    fetch and verify everything into a temporary directory, change nothing
#   pull.sh --install  first installation (bootstrap.sh): place the release and its assets and point
#                      /opt/agenthold/current at it, without a running service to replace or start
#
# The source is configured in /etc/agenthold/pull.conf (AGENTHOLD_DEPLOY_CONFIG), one key=value per
# line, AGENTHOLD_DEPLOY_SOURCE overriding it:
#
#   source=/srv/agenthold/releases          a local release store (what build-local.sh publishes)
#   source=https://demo.agenthold.dev/dl/   the /dl/ root of another agenthold that deployed one
#   host_mount=/Users/gempir/dev/agenthold  also require free space on this mount (empty: skip)
#   mode=node                               an edge node (agenthold node): deploy agenthold-node.service
#                                           and judge its health by its journal (default: controller)
#
# A source is a directory tree shaped like /dl/: channel.json (format 3) names a sha-<commit>
# version, the sha256 of its binary, runtime manifest and SHA256SUMS, and the guest kernel and base
# image by key with the sha256 of their asset manifests (assets/<arch>/<kind>-<key>.json, which pin
# the compressed assets/<arch>/<kind>-<key>.zst and the uncompressed file). Every file is checked
# against that chain. Before the switch the assets are placed where the release binary looks for
# them: the manifests in <data_dir>/assets/<arch>/, the kernel at <data_dir>/kernel/<sha256> and the
# base at <data_dir>/base/<sha256>.ext4, each with its compressed file next to it (+.zst). After a
# healthy switch channel.json and SHA256SUMS go to <data_dir>/dl/, so the deployed controller's
# /dl/ (which serves the binary and the assets itself) can be the pull source of another host.
set -euo pipefail
umask 077

CONFIG="${AGENTHOLD_DEPLOY_CONFIG:-/etc/agenthold/pull.conf}"
conf() { # conf <key>: the value from the config file, empty if absent
  [[ -f "$CONFIG" ]] || return 0
  sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "$CONFIG" | tail -1 | sed 's/[[:space:]]*$//'
}
SOURCE="${AGENTHOLD_DEPLOY_SOURCE:-$(conf source)}"
SOURCE="${SOURCE:-/srv/agenthold/releases}"
HOST_MOUNT="${AGENTHOLD_DEPLOY_HOST_MOUNT-$(conf host_mount)}"
MODE="${AGENTHOLD_DEPLOY_MODE:-$(conf mode)}"
MODE="${MODE:-controller}"
[[ "$MODE" == controller || "$MODE" == node ]] || { echo "invalid mode $MODE: controller or node" >&2; exit 1; }
ROOT="${AGENTHOLD_DEPLOY_ROOT:-/opt/agenthold}"
HEALTH="${AGENTHOLD_DEPLOY_HEALTH:-http://127.0.0.1:8080}"
DATA_DIR="${AGENTHOLD_DEPLOY_DATA_DIR:-/var/lib/agenthold}"
TIMEOUT="${AGENTHOLD_DEPLOY_TIMEOUT:-1800}"
if [[ "$MODE" == node ]]; then SERVICE="${AGENTHOLD_DEPLOY_SERVICE:-agenthold-node.service}"
else SERVICE="${AGENTHOLD_DEPLOY_SERVICE:-agenthold.service}"; fi
PRUNE="${PRUNE:-1}"
KEEP_RELEASES=3
# The local store holds a compressed base per release; rollback needs only the previous one.
KEEP_STORE=2
case "$(uname -m)" in
  aarch64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) echo 'releases exist for arm64 and amd64 Linux only' >&2; exit 1 ;;
esac

check=0
install=0
case "${1:-}" in
  '') ;;
  --check) check=1 ;;
  --install) install=1 ;;
  *) echo "usage: pull.sh [--check|--install]" >&2; exit 2 ;;
esac
case "$SOURCE" in
  https://*/) kind=https ;;
  /*) kind=local; SOURCE="${SOURCE%/}" ;;
  *) echo "invalid source $SOURCE: an absolute directory or an https URL ending in /" >&2; exit 1 ;;
esac
[[ "$SOURCE" != *[[:space:]\'\"\\]* ]] || { echo 'invalid source' >&2; exit 1; }
[[ "$ROOT" == /* && "$DATA_DIR" == /* && ( -z "$HOST_MOUNT" || "$HOST_MOUNT" == /* ) ]] || { echo 'invalid deployment paths' >&2; exit 1; }
[[ "$HEALTH" == 'http://127.0.0.1:'* && "$TIMEOUT" =~ ^[0-9]+$ && "$TIMEOUT" -ge 1 && "$TIMEOUT" -le 3600 ]] || { echo 'invalid health settings' >&2; exit 1; }
if ((install)); then
  [[ -d "$ROOT/releases" && ! -e "$ROOT/current" && ! -L "$ROOT/current" ]] || { echo "$ROOT/current exists (or $ROOT/releases is missing); use pull.sh to update" >&2; exit 1; }
elif ((!check)); then
  [[ -d "$ROOT/releases" && -L "$ROOT/current" ]] || { echo 'deployment has not been bootstrapped; refusing to start a service' >&2; exit 1; }
  exec 9>"$ROOT/.pull.lock"
  flock -n 9 || { echo 'another pull is running'; exit 0; }
fi

current=""
current_commit=""
if [[ -L "$ROOT/current" ]]; then
  current="$(readlink -f "$ROOT/current")"
  current_commit="$(basename -- "$current")"
fi
if ((check)); then
  work="$(mktemp -d "${TMPDIR:-/var/tmp}/agenthold-pull-check.XXXXXXXX")"
  base_dir="$work/base"
  kernel_dir="$work/kernel"
  assets_dir="$work/assets"
else
  ((install)) || [[ "$current" == "$ROOT/releases/"* && -x "$current/agenthold" ]] || { echo 'invalid current deployment' >&2; exit 1; }
  work="$(mktemp -d "$ROOT/.pull.XXXXXXXX")"
  base_dir="$DATA_DIR/base"
  kernel_dir="$DATA_DIR/kernel"
  assets_dir="$DATA_DIR/assets"
fi
tmp_files=()
switched=0
old_version=""
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if ((switched)); then
    echo 'deployment interrupted or failed; rolling back' >&2
    if ln -s "$current" "$work/rollback" && mv -Tf "$work/rollback" "$ROOT/current"; then
      if systemctl restart "$SERVICE"; then
        if healthy "$old_version" 180; then echo 'rollback restored service' >&2
        else echo 'rollback did not become healthy' >&2; fi
      else echo 'rollback restart failed' >&2; fi
    else echo 'rollback could not restore the previous release link' >&2; fi
  fi
  local f
  for f in "${tmp_files[@]}"; do rm -f -- "$f"; done
  rm -rf -- "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

verify() { printf '%s  %s\n' "$1" "$2" | sha256sum --check --status; }
# get <relative path> <max bytes> <destination> [timeout]: copy one file of the source.
get() {
  local rel="$1" max="$2" dest="$3" size
  [[ "$rel" =~ ^(channel\.json|sha-[0-9a-f]{40}/[A-Za-z0-9][A-Za-z0-9._-]*|assets/(amd64|arm64)/(kernel|base)-[0-9a-f]{32}\.(json|zst))$ ]] || { echo "refusing source path $rel" >&2; return 1; }
  if [[ "$kind" == local ]]; then
    local src="$SOURCE/$rel"
    [[ -f "$src" && ! -L "$src" && ! -L "$(dirname -- "$src")" ]] || { echo "$src is missing or not a regular file" >&2; return 1; }
    size="$(stat -c %s -- "$src")"
    ((size <= max)) || { echo "$src is larger than $max bytes" >&2; return 1; }
    cp -- "$src" "$dest"
  else
    curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time "${4:-300}" --max-filesize "$max" "$SOURCE$rel" -o "$dest"
    size="$(stat -c %s -- "$dest")"
    ((size <= max)) || { echo "$rel is larger than $max bytes" >&2; return 1; }
  fi
}
# place_zst <rel> <sha> <size> <dest>: put a compressed asset at dest. A local store on the same
# filesystem shares the bytes through a hard link; anything else is copied or downloaded.
place_zst() {
  local rel="$1" sha="$2" size="$3" dest="$4" tmp
  tmp="$(mktemp "$(dirname -- "$dest")/.download.XXXXXXXX")"
  tmp_files+=("$tmp")
  if [[ "$kind" == local && -f "$SOURCE/$rel" && ! -L "$SOURCE/$rel" ]] && ln -f -- "$SOURCE/$rel" "$tmp" 2>/dev/null; then
    :
  else
    get "$rel" "$size" "$tmp" 1800
  fi
  [[ "$(stat -c %s -- "$tmp")" == "$size" ]] && verify "$sha" "$tmp" || { echo "compressed $rel verification failed" >&2; return 1; }
  mv -f -- "$tmp" "$dest"
}
sum_of() { # sum_of <file name>: its sha256 according to SHA256SUMS
  awk -v f="$1" '$2 == f || $2 == "*" f {print $1; n++} END {exit n == 1 ? 0 : 1}' "$work/SHA256SUMS"
}

get channel.json 65536 "$work/channel.json"
commit="$(jq -er --arg arch "$ARCH" 'select(.format == 3 and .arch == $arch) | .commit | select(type == "string" and test("^[0-9a-f]{40}$"))' "$work/channel.json")" ||
  { echo "channel.json is not a format 3 $ARCH channel" >&2; exit 1; }
version="sha-$commit"
jq -e --arg v "$version" --arg arch "$ARCH" '
  def sha: type == "string" and test("^[0-9a-f]{64}$");
  def key: type == "string" and test("^[0-9a-f]{32}$");
  .version == $v and
  .binary.path == ($v + "/agenthold-linux-" + $arch) and .runtime_manifest.path == ($v + "/runtime-linux-" + $arch + ".manifest.json") and
  .sums.path == ($v + "/SHA256SUMS") and
  ([.binary.sha256, .runtime_manifest.sha256, .sums.sha256, .kernel.manifest.sha256, .base.manifest.sha256] | all(sha)) and
  (.kernel.key | key) and (.base.key | key) and
  .kernel.manifest.path == ("assets/" + $arch + "/kernel-" + .kernel.key + ".json") and
  .base.manifest.path == ("assets/" + $arch + "/base-" + .base.key + ".json")
' "$work/channel.json" >/dev/null || { echo 'channel does not point to the files of its version' >&2; exit 1; }
binary_sha="$(jq -er .binary.sha256 "$work/channel.json")"
manifest_sha="$(jq -er .runtime_manifest.sha256 "$work/channel.json")"
sums_sha="$(jq -er .sums.sha256 "$work/channel.json")"
running=0
[[ "$current_commit" == "$commit" ]] && running=1
dl="$DATA_DIR/dl"
if ((running && !check)) && cmp -s "$work/channel.json" "$dl/channel.json" && [[ -f "$dl/$version/SHA256SUMS" ]]; then
  echo "already running $commit"
  exit 0
fi

get "$version/SHA256SUMS" 65536 "$work/SHA256SUMS"
verify "$sums_sha" "$work/SHA256SUMS" || { echo 'SHA256SUMS checksum mismatch' >&2; exit 1; }
[[ "$(sum_of agenthold-linux-$ARCH)" == "$binary_sha" && "$(sum_of runtime-linux-$ARCH.manifest.json)" == "$manifest_sha" ]] ||
  { echo 'SHA256SUMS disagrees with the channel' >&2; exit 1; }
get "$version/runtime-linux-$ARCH.manifest.json" 65536 "$work/runtime.json"
verify "$manifest_sha" "$work/runtime.json" || { echo 'runtime manifest checksum mismatch' >&2; exit 1; }
jq -e --arg version "$version" --arg arch "$ARCH" --slurpfile c "$work/channel.json" '
  .format == 2 and .arch == $arch and .version == $version and .kernel.key == $c[0].kernel.key and .base.key == $c[0].base.key
' "$work/runtime.json" >/dev/null || { echo 'runtime manifest does not match the release' >&2; exit 1; }

# The asset manifests, pinned by the channel; each pins its files.
for k in kernel base; do
  rel="$(jq -er --arg k "$k" '.[$k].manifest.path' "$work/channel.json")"
  get "$rel" 65536 "$work/$k.json"
  verify "$(jq -er --arg k "$k" '.[$k].manifest.sha256' "$work/channel.json")" "$work/$k.json" || { echo "$k asset manifest checksum mismatch" >&2; exit 1; }
  max=34359738368; [[ "$k" == kernel ]] && max=536870912
  jq -e --arg k "$k" --arg arch "$ARCH" --argjson max "$max" --slurpfile c "$work/channel.json" '
    .format == 1 and .kind == $k and .arch == $arch and .key == $c[0][$k].key and
    (.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and (.compressed.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
    (.size | type == "number" and . == floor and . > 0 and . <= $max) and
    (.compressed.size | type == "number" and . == floor and . > 0 and . <= 2000000000)
  ' "$work/$k.json" >/dev/null || { echo "$k asset manifest does not match the release" >&2; exit 1; }
done
kernel_key="$(jq -er .kernel.key "$work/channel.json")"
base_key="$(jq -er .base.key "$work/channel.json")"

if ((running && !check)); then
  verify "$binary_sha" "$current/agenthold" || { echo 'the running release differs from the channel' >&2; exit 1; }
else
  get "$version/agenthold-linux-$ARCH" 500000000 "$work/agenthold"
  verify "$binary_sha" "$work/agenthold" || { echo 'binary checksum mismatch' >&2; exit 1; }
  chmod 0755 "$work/agenthold"
  [[ "$("$work/agenthold" --version)" == "agenthold $version ("* ]] || { echo 'binary version does not match the release' >&2; exit 1; }
fi

if ((install)); then
  : # nothing runs yet
elif ((!check)) && [[ "$MODE" == node ]]; then
  # A node serves no HTTP: its baseline is a running service and the version of its binary.
  systemctl is-active --quiet "$SERVICE" || { echo 'baseline service is not running; refusing deployment' >&2; exit 1; }
  old_version="$("$current/agenthold" --version | awk '{print $2}')"
  [[ -n "$old_version" ]] || { echo 'baseline version is unavailable' >&2; exit 1; }
elif ((!check)); then
  systemctl is-active --quiet "$SERVICE" && curl -fsS --max-time 3 "$HEALTH/healthz" >/dev/null || { echo 'baseline service is not healthy; refusing deployment' >&2; exit 1; }
  old_version="$(curl -fsS --max-time 3 "$HEALTH/api/v1/version" | jq -er '.version | select(type == "string" and length > 0)')" || { echo 'baseline version is unavailable' >&2; exit 1; }
fi

# Each asset's compressed file stays next to the decompressed one (/dl/assets/ serves it from there).
# Everything is fetched and verified while the old service is still running.
install -d -m 0700 "$base_dir" "$kernel_dir"
install -d -m 0755 "$assets_dir" "$assets_dir/$ARCH"
# Disk space for what is missing, plus headroom for a rollback.
required=536870912
for k in kernel base; do
  sha="$(jq -er .sha256 "$work/$k.json")"; csha="$(jq -er .compressed.sha256 "$work/$k.json")"
  size="$(jq -er .size "$work/$k.json")"; csize="$(jq -er .compressed.size "$work/$k.json")"
  if [[ "$k" == kernel ]]; then f="$kernel_dir/$sha"; else f="$base_dir/$sha.ext4"; fi
  [[ -f "$f.zst" && ! -L "$f.zst" && "$(stat -c %s "$f.zst")" == "$csize" ]] && verify "$csha" "$f.zst" || required=$((required + csize))
  [[ -f "$f" && ! -L "$f" && "$(stat -c %s "$f")" == "$size" ]] && verify "$sha" "$f" || required=$((required + size))
done
if ((required > 536870912)); then
  available="$(df -PB1 "$base_dir" | awk 'END {print $4}')"
  [[ "$available" =~ ^[0-9]+$ ]] && ((available >= required)) || { echo 'insufficient disk space for the assets and rollback' >&2; exit 1; }
  if [[ -n "$HOST_MOUNT" ]]; then
    [[ -d "$HOST_MOUNT" ]] || { echo 'host storage mount is unavailable' >&2; exit 1; }
    host_available="$(df -PB1 "$HOST_MOUNT" | awk 'END {print $4}')"
    [[ "$host_available" =~ ^[0-9]+$ ]] && ((host_available >= required)) || { echo 'insufficient host disk space for the assets and rollback' >&2; exit 1; }
  fi
fi
# place_asset <kernel|base>: the verified compressed and uncompressed file, then the manifest.
place_asset() {
  local k="$1" key sha csha size csize f tmp
  key="$(jq -er .key "$work/$k.json")"
  sha="$(jq -er .sha256 "$work/$k.json")"; csha="$(jq -er .compressed.sha256 "$work/$k.json")"
  size="$(jq -er .size "$work/$k.json")"; csize="$(jq -er .compressed.size "$work/$k.json")"
  if [[ "$k" == kernel ]]; then f="$kernel_dir/$sha"; else f="$base_dir/$sha.ext4"; fi
  if [[ -f "$f.zst" && ! -L "$f.zst" && "$(stat -c %s "$f.zst")" == "$csize" ]] && verify "$csha" "$f.zst"; then
    echo "verified compressed $k is already cached"
  else
    place_zst "assets/$ARCH/$k-$key.zst" "$csha" "$csize" "$f.zst"
  fi
  chmod 0644 "$f.zst"
  if [[ -f "$f" && ! -L "$f" && "$(stat -c %s "$f")" == "$size" ]] && verify "$sha" "$f"; then
    echo "verified $k is already cached"
  else
    tmp="$(mktemp "$(dirname -- "$f")/.unpack.XXXXXXXX")"
    tmp_files+=("$tmp")
    (ulimit -f $(((size + 1023) / 1024)); zstd -d -q -f --long=27 --sparse "$f.zst" -o "$tmp")
    [[ "$(stat -c %s "$tmp")" == "$size" ]] && verify "$sha" "$tmp" || { echo "$k verification failed" >&2; return 1; }
    # zstd copies the owner of its input; the cache belongs to the service user.
    if [[ $EUID -eq 0 ]]; then chown root:root "$tmp"; fi
    chmod 0644 "$tmp"
    mv -f -- "$tmp" "$f"
  fi
  install -m 0644 "$work/$k.json" "$assets_dir/$ARCH/.$k-$key.json.tmp"
  mv -f -- "$assets_dir/$ARCH/.$k-$key.json.tmp" "$assets_dir/$ARCH/$k-$key.json"
}
place_asset kernel
place_asset base

if ((check)); then
  echo "check passed: $version from $SOURCE is complete and verified (running: ${current_commit:-nothing})"
  exit 0
fi

# publish_dl: serve what is deployed under the controller's /dl/ (operator files of D1a).
publish_dl() {
  local tmp
  install -d -m 0755 "$dl"
  tmp="$(mktemp -d "$dl/.publish.XXXXXXXX")"
  install -m 0644 "$work/SHA256SUMS" "$tmp/SHA256SUMS"
  chmod 0755 "$tmp"
  rm -rf -- "${dl:?}/$version"
  mv -T -- "$tmp" "$dl/$version"
  install -m 0644 "$work/channel.json" "$dl/.channel.json.tmp"
  mv -f -- "$dl/.channel.json.tmp" "$dl/channel.json"
  echo "published $version under $dl"
}

if ((running)); then
  publish_dl
  echo "already running $commit"
  exit 0
fi

release_dir="$ROOT/releases/$commit"
if [[ -e "$release_dir" ]]; then
  [[ -d "$release_dir" && ! -L "$release_dir" && -f "$release_dir/agenthold" ]] && verify "$binary_sha" "$release_dir/agenthold" || { echo 'existing release differs from channel' >&2; exit 1; }
else
  mkdir "$work/release"
  mv "$work/agenthold" "$work/release/agenthold"
  cp "$work/runtime.json" "$work/release/runtime.manifest.json"
  # The assets this release runs with, for pruning.
  cp "$work/kernel.json" "$work/release/kernel.json"
  cp "$work/base.json" "$work/release/base.json"
  chmod 0755 "$work/release"
  mv "$work/release" "$release_dir"
fi

healthy() {
  local version="$1" deadline=$((SECONDS + $2)) reply
  [[ "$MODE" != node ]] || { node_healthy "$@"; return; }
  while ((SECONDS < deadline)); do
    if reply="$(curl -fsS --max-time 3 "$HEALTH/api/v1/version" 2>/dev/null)" &&
       jq -e --arg expected "$version" '.version == $expected' >/dev/null 2>&1 <<<"$reply" &&
       curl -fsS --max-time 3 "$HEALTH/healthz" >/dev/null 2>&1; then return 0; fi
    ((SECONDS + 5 < deadline)) || break
    sleep 5
  done
  return 1
}

# node_healthy <version> <seconds>: the node service runs that version (its start line) and reached
# its controller, both logged by the current invocation of the unit. A node whose controller is
# unreachable is not healthy: the switch is rolled back and the next timer run tries again.
node_healthy() {
  local version="$1" deadline=$((SECONDS + $2)) inv log
  while ((SECONDS < deadline)); do
    if systemctl is-active --quiet "$SERVICE" && inv="$(systemctl show -P InvocationID "$SERVICE" 2>/dev/null)" && [[ -n "$inv" ]] &&
       log="$(journalctl --no-pager -o cat "_SYSTEMD_INVOCATION_ID=$inv" 2>/dev/null)" &&
       grep -F '"msg":"edge node starting"' <<<"$log" | grep -qF "\"version\":\"$version\"" &&
       grep -qF '"msg":"connected to controller"' <<<"$log"; then return 0; fi
    ((SECONDS + 5 < deadline)) || break
    sleep 5
  done
  return 1
}

# Reclaim disk after a verified switch. Only regular files named <sha256>.ext4 or <sha256>.ext4.zst
# directly inside the base cache, <sha256> or <sha256>.zst inside the kernel cache, asset manifests
# whose file is gone, assets in the local store no kept version names, 40-hex directories directly
# inside $ROOT/releases and sha-<40-hex> directories directly inside <data_dir>/dl and the local
# store are ever removed. Any doubt
# (unreadable agent state, symlinked directories, missing manifests) skips that part of the prune.
prune_bytes=0
prune_remove() { # prune_remove <parent-dir> <path> <file|dir|vdir>
  local parent="$1" path="$2" kind="$3" size
  [[ "$(dirname -- "$path")" == "$parent" && ! -L "$path" ]] || { echo "prune: refusing $path" >&2; return 0; }
  case "$kind" in
    file) [[ -f "$path" && "$(basename -- "$path")" =~ ^[0-9a-f]{64}\.ext4(\.zst)?$ ]] || return 0;;
    kfile) [[ -f "$path" && "$(basename -- "$path")" =~ ^[0-9a-f]{64}(\.zst)?$ ]] || return 0;;
    asset) [[ -f "$path" && "$(basename -- "$path")" =~ ^(kernel|base)-[0-9a-f]{32}\.(json|zst)$ ]] || return 0;;
    dir) [[ -d "$path" && "$(basename -- "$path")" =~ ^[0-9a-f]{40}$ ]] || return 0;;
    vdir) [[ -d "$path" && "$(basename -- "$path")" =~ ^sha-[0-9a-f]{40}$ ]] || return 0;;
    *) return 0;;
  esac
  size="$(du -sk -- "$path" 2>/dev/null | cut -f1)" || size=0
  [[ "$size" =~ ^[0-9]+$ ]] || size=0
  if [[ "$kind" == dir || "$kind" == vdir ]]; then rm -rf -- "$path"; else rm -f -- "$path"; fi
  if [[ -e "$path" ]]; then echo "prune: could not remove $path" >&2; return 0; fi
  prune_bytes=$((prune_bytes + size * 1024))
  echo "prune: removed $path ($((size * 1024)) bytes)"
}
prune_bases() { # prune_bases <new-release-dir> <previous-release-dir>
  local real state ref f sha dir name
  [[ -d "$base_dir" && ! -L "$base_dir" ]] || { echo 'prune: base cache is not a plain directory; skipping bases' >&2; return 0; }
  real="$(cd "$base_dir" && pwd -P)"
  [[ "$real" == "$(cd "$DATA_DIR" && pwd -P)/base" ]] || { echo 'prune: base cache resolves outside the data directory; skipping bases' >&2; return 0; }
  declare -A keep=()
  for dir in "$1" "$2"; do
    # base.json: releases with base assets; runtime.manifest.json: releases that embedded the base sums.
    sha="$(jq -er '.sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$dir/base.json" 2>/dev/null ||
      jq -er '.base.sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$dir/runtime.manifest.json" 2>/dev/null)" || { echo "prune: no base recorded for $dir; skipping bases" >&2; return 0; }
    keep[$sha]=1
  done
  # An agent's overlay depends on the exact base in its state.json until it is reset, so every
  # referenced base stays. Unreadable state means we cannot know, so nothing is pruned.
  while IFS= read -r -d '' state; do
    ref="$(jq -er '.baseImagePath | select(type == "string")' "$state" 2>/dev/null)" || { echo "prune: cannot read $state; skipping bases" >&2; return 0; }
    ref="$(basename -- "$ref")"
    [[ "$ref" =~ ^([0-9a-f]{64})\.ext4$ ]] && keep[${BASH_REMATCH[1]}]=1
  done < <(find "$DATA_DIR" -maxdepth 5 -path '*/agents/*/state.json' -type f -not -path "$base_dir/*" -print0 2>/dev/null)
  for f in "$base_dir"/*.ext4 "$base_dir"/*.ext4.zst; do
    [[ -e "$f" || -L "$f" ]] || continue
    name="$(basename -- "$f")"
    sha="${name%%.*}"
    [[ "$sha" =~ ^[0-9a-f]{64}$ && -z "${keep[$sha]:-}" ]] || continue
    prune_remove "$base_dir" "$f" file
  done
}
prune_kernels() { # prune_kernels <new-release-dir> <previous-release-dir>: keep the kernels of both
  local f sha dir name
  [[ -d "$kernel_dir" && ! -L "$kernel_dir" ]] || return 0
  declare -A keep=()
  sha="$(jq -er '.sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$1/kernel.json" 2>/dev/null)" || { echo "prune: no kernel recorded for $1; skipping kernels" >&2; return 0; }
  keep[$sha]=1
  # A previous release from before kernel assets embedded its kernel: nothing to keep for it.
  if [[ -f "$2/kernel.json" ]]; then
    sha="$(jq -er '.sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$2/kernel.json" 2>/dev/null)" || { echo "prune: unreadable $2/kernel.json; skipping kernels" >&2; return 0; }
    keep[$sha]=1
  fi
  for f in "$kernel_dir"/*; do
    [[ -e "$f" || -L "$f" ]] || continue
    name="$(basename -- "$f")"
    sha="${name%%.*}"
    [[ "$sha" =~ ^[0-9a-f]{64}$ && -z "${keep[$sha]:-}" ]] || continue
    prune_remove "$kernel_dir" "$f" kfile
  done
}
prune_manifests() { # asset manifests whose file the prune removed
  local f k sha dir="$assets_dir/$ARCH"
  [[ -d "$dir" && ! -L "$dir" ]] || return 0
  for f in "$dir"/*.json; do
    [[ -f "$f" ]] || continue
    k="$(jq -er .kind "$f" 2>/dev/null)" && sha="$(jq -er '.sha256 | select(test("^[0-9a-f]{64}$"))' "$f" 2>/dev/null)" || continue
    case "$k" in
      kernel) [[ -e "$kernel_dir/$sha" ]] || prune_remove "$dir" "$f" asset;;
      base) [[ -e "$base_dir/$sha.ext4" ]] || prune_remove "$dir" "$f" asset;;
    esac
  done
}
prune_store_assets() { # store assets that no version left in the local store names
  local m f name dir="$SOURCE/assets/$ARCH"
  [[ -d "$dir" && ! -L "$dir" ]] || return 0
  declare -A keep=()
  for m in "$SOURCE"/sha-*/runtime-linux-"$ARCH".manifest.json; do
    [[ -f "$m" ]] || continue
    keep["kernel-$(jq -er .kernel.key "$m" 2>/dev/null)"]=1 && keep["base-$(jq -er .base.key "$m" 2>/dev/null)"]=1 ||
      { echo "prune: cannot read $m; skipping store assets" >&2; return 0; }
    [[ -n "${keep[kernel-]:-}${keep[base-]:-}" ]] && { echo "prune: no keys in $m; skipping store assets" >&2; return 0; }
  done
  for f in "$dir"/*.json "$dir"/*.zst; do
    [[ -f "$f" ]] || continue
    name="$(basename -- "$f")"
    [[ -z "${keep[${name%.*}]:-}" ]] && prune_remove "$dir" "$f" asset
  done
}
prune_releases() { # prune_releases <new-release-dir> <previous-release-dir>
  local dir name n=0 rel_dir="$ROOT/releases"
  [[ -d "$rel_dir" && ! -L "$rel_dir" ]] || { echo 'prune: releases is not a plain directory; skipping releases' >&2; return 0; }
  while IFS= read -r name; do
    dir="$rel_dir/$name"
    [[ "$name" =~ ^[0-9a-f]{40}$ && -d "$dir" && ! -L "$dir" ]] || continue
    n=$((n + 1))
    ((n > KEEP_RELEASES)) || continue
    [[ "$dir" != "$1" && "$dir" != "$2" && "$dir" != "$(readlink -f "$ROOT/current")" ]] || continue
    prune_remove "$rel_dir" "$dir" dir
  done < <(ls -t -- "$rel_dir")
}
prune_versions() { # prune_versions <dir> <keep> <versions to keep...>: sha-<commit> trees, newest first
  local parent="$1" max="$2" name n=0 v skip
  shift 2
  [[ -d "$parent" && ! -L "$parent" ]] || return 0
  while IFS= read -r name; do
    [[ "$name" =~ ^sha-[0-9a-f]{40}$ && -d "$parent/$name" && ! -L "$parent/$name" ]] || continue
    n=$((n + 1))
    ((n > max)) || continue
    skip=0
    for v in "$@"; do [[ "$name" == "$v" ]] && skip=1; done
    ((skip)) || prune_remove "$parent" "$parent/$name" vdir
  done < <(ls -t -- "$parent")
}
prune() {
  if [[ "$PRUNE" == 0 ]]; then echo 'prune: disabled by PRUNE=0'; return 0; fi
  [[ "$(readlink -f "$ROOT/current")" == "$1" ]] || { echo 'prune: current is not the new release; skipping' >&2; return 0; }
  local previous="sha-$(basename -- "$2")"
  prune_bytes=0
  prune_bases "$1" "$2"
  prune_kernels "$1" "$2"
  prune_manifests
  prune_releases "$1" "$2"
  prune_versions "$dl" 1 "$version"
  if [[ "$kind" == local ]]; then
    prune_versions "$SOURCE" "$KEEP_STORE" "$version" "$previous"
    prune_store_assets
  fi
  echo "prune: freed $prune_bytes bytes"
}

if ((install)); then
  ln -s "$release_dir" "$work/next"
  mv -Tf "$work/next" "$ROOT/current"
  publish_dl
  echo "installed $version under $ROOT (not started)"
  exit 0
fi

ln -s "$release_dir" "$work/next"
switched=1
mv -Tf "$work/next" "$ROOT/current"
if systemctl restart "$SERVICE" && healthy "$version" "$TIMEOUT"; then
  switched=0
  echo "deployed $commit"
  publish_dl || echo 'could not publish the release under /dl/' >&2
  # Pruning is best effort and runs only after the new release proved healthy.
  prune "$release_dir" "$current" || echo 'prune failed; leaving disk as is' >&2
  exit 0
fi

echo 'new release failed health checks' >&2
exit 1
