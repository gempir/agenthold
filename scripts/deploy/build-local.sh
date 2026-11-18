#!/usr/bin/env bash
# Build, test and publish the main head on this host (the demo box), so releases never depend on
# GitHub release assets or GitHub-hosted KVM runners.
#
#   build-local.sh                 build main's head if it is new (what the timer runs)
#   build-local.sh --commit SHA    build that commit (it must be reachable from main)
#   build-local.sh --force         rebuild even if the commit was built, deployed or failed before
#
# Every phase runs as its own transient systemd unit in agenthold-build.slice (memory and CPU
# limits, nice, idle I/O), as the unprivileged build user except the two that need root (the
# rootfs build inside scripts/release/build.sh and the KVM tests). systemd reports each phase's
# runtime and memory peak; they end up in the log and in $STATE/last-build.json.
#
# A commit is published only when everything passed: atomically into $STORE/sha-<commit>/ and then
# $STORE/channel.json (written last). A failed commit is recorded in $FAILED/<commit>.log and not
# retried until --force; a new commit on main is built normally. pull.sh deploys from $STORE.
set -euo pipefail
umask 022

REPO_URL="${AGENTHOLD_BUILD_REPO:-https://github.com/gempir/agenthold.git}"
BRANCH="${AGENTHOLD_BUILD_BRANCH:-main}"
STORE="${AGENTHOLD_BUILD_STORE:-/srv/agenthold/releases}"
FAILED="${AGENTHOLD_BUILD_FAILED:-/srv/agenthold/failed}"
STATE="${AGENTHOLD_BUILD_STATE:-/var/lib/agenthold-build}"
CACHE="${AGENTHOLD_BUILD_CACHE:-/var/cache/agenthold-build}"
LOGS="${AGENTHOLD_BUILD_LOGS:-/var/log/agenthold-build}"
BUILD_USER="${AGENTHOLD_BUILD_USER:-agenthold-build}"
DEPLOY_ROOT="${AGENTHOLD_DEPLOY_ROOT:-/opt/agenthold}"
SLICE="${AGENTHOLD_BUILD_SLICE:-agenthold-build.slice}"
MIN_FREE_GB="${AGENTHOLD_BUILD_MIN_FREE_GB:-12}"
KEEP_LOGS=20
GOMAXPROCS_BUILD="${AGENTHOLD_BUILD_GOMAXPROCS:-4}"

commit=""
force=0
while (($#)); do
  case "$1" in
    --commit) commit="$2"; shift 2 ;;
    --force) force=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ $EUID -eq 0 ]] || { echo 'build-local.sh runs as root (KVM tests and the rootfs build need it)' >&2; exit 1; }
[[ "$(uname -m)" == aarch64 ]] || { echo 'the demo builder runs on arm64 Linux' >&2; exit 1; }
[[ "$STORE" == /* && "$FAILED" == /* && "$STATE" == /* && "$CACHE" == /* && "$LOGS" == /* ]] || { echo 'paths must be absolute' >&2; exit 2; }
[[ "$MIN_FREE_GB" =~ ^[0-9]+$ && "$GOMAXPROCS_BUILD" =~ ^[1-9][0-9]*$ ]] || { echo 'invalid limits' >&2; exit 2; }
id -u "$BUILD_USER" >/dev/null 2>&1 || { echo "build user $BUILD_USER does not exist" >&2; exit 1; }
for tool in git go node pnpm jq zstd sha256sum flock systemd-run make; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done

install -d -m 0755 "$STORE" "$FAILED" "$LOGS"
install -d -m 0755 -o "$BUILD_USER" -g "$BUILD_USER" "$STATE" "$STATE/work" "$CACHE" "$CACHE/user" "$CACHE/kernel"
install -d -m 0700 "$CACHE/root"
exec 9>"/run/agenthold-build.lock"
flock -n 9 || { echo 'another build is running'; exit 0; }

as_user() { setpriv --reuid="$BUILD_USER" --regid="$BUILD_USER" --init-groups env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin HOME="$STATE" "$@"; }
mirror="$STATE/agenthold.git"
if [[ ! -d "$mirror" ]]; then
  as_user git clone --quiet --bare --single-branch --branch "$BRANCH" "$REPO_URL" "$mirror"
fi
as_user git -C "$mirror" fetch --quiet --prune "$REPO_URL" "+refs/heads/$BRANCH:refs/heads/$BRANCH"
head="$(as_user git -C "$mirror" rev-parse --verify "refs/heads/$BRANCH^{commit}")"
if [[ -z "$commit" ]]; then
  commit="$head"
else
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || { echo '--commit takes a full 40-hex commit' >&2; exit 2; }
  as_user git -C "$mirror" merge-base --is-ancestor "$commit" "$head" || { echo "$commit is not on $BRANCH" >&2; exit 1; }
fi
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || { echo 'unexpected commit id' >&2; exit 1; }
version="sha-$commit"

deployed="$(basename "$(readlink -f "$DEPLOY_ROOT/current" 2>/dev/null || true)")"
if ((!force)); then
  if [[ -f "$STORE/$version/SHA256SUMS" ]]; then echo "$commit is already built"; exit 0; fi
  if [[ "$deployed" == "$commit" ]]; then echo "$commit is already deployed"; exit 0; fi
  if [[ -f "$FAILED/$commit.log" ]]; then echo "$commit failed before (see $FAILED/$commit.log); waiting for a new commit"; exit 0; fi
fi
free_kb="$(df -Pk / | awk 'END {print $4}')"
if ! [[ "$free_kb" =~ ^[0-9]+$ ]] || ((free_kb < MIN_FREE_GB * 1024 * 1024)); then
  echo "less than $MIN_FREE_GB GB free on /; not building $commit" >&2
  exit 1
fi

log="$LOGS/$commit.log"
: > "$log"
exec > >(tee -a "$log") 2>&1
wt="$STATE/work/$commit"
phases="$(mktemp)"
current_unit=""
started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
finish() {
  local status=$?
  trap - EXIT INT TERM
  set +e
  [[ -z "$current_unit" ]] || systemctl stop "$current_unit" 2>/dev/null
  [[ -z "${stage:-}" ]] || rm -rf -- "$stage"
  if [[ -d "$wt" ]]; then rm -rf -- "$wt"; fi
  as_user git -C "$mirror" worktree prune
  if ((status != 0)); then
    echo "build of $commit FAILED (status $status)"
    cp -- "$log" "$FAILED/.$commit.log.tmp" && mv -f -- "$FAILED/.$commit.log.tmp" "$FAILED/$commit.log"
  fi
  jq -n --arg commit "$commit" --arg started "$started" --arg finished "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson status "$status" \
    --slurpfile phases <(jq -s . "$phases" 2>/dev/null || echo '[]') \
    '{commit: $commit, started: $started, finished: $finished, status: $status, phases: $phases[0]}' > "$STATE/last-build.json.tmp" &&
    mv -f "$STATE/last-build.json.tmp" "$STATE/last-build.json"
  rm -f -- "$phases"
  # Keep the newest logs only; failure records stay (they stop rebuild loops).
  ls -1t "$LOGS"/*.log 2>/dev/null | tail -n +$((KEEP_LOGS + 1)) | xargs -r rm -f --
  find "$FAILED" -maxdepth 1 -name '*.log' -mtime +60 -delete 2>/dev/null
  exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "==> building $commit (main head $head) at $started"
rm -rf -- "$wt"
as_user git -C "$mirror" worktree prune
as_user git -C "$mirror" worktree add --quiet --detach "$wt" "$commit"

# phase <name> <user|root> <command...>: one transient unit per phase. Root phases get their own
# caches so nothing root-owned lands in the build user's caches.
phase() {
  local name="$1" who="$2" unit out rc runtime peak t0
  shift 2
  unit="agenthold-build-$name-${commit:0:12}.service"
  local -a props=(
    --wait --pipe --collect --unit="$unit" --slice="$SLICE" --service-type=exec
    -p Nice=10 -p IOSchedulingClass=idle -p CPUWeight=20 -p IOWeight=20 -p MemoryAccounting=yes
    -p WorkingDirectory="$wt" -p TimeoutStopSec=60s
    -E PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin -E GOMAXPROCS="$GOMAXPROCS_BUILD" -E GOTOOLCHAIN=local
    -E CI=1 -E VERSION="$version" -E BASE_URL= -E GOFLAGS=-buildvcs=false
  )
  if [[ "$who" == user ]]; then
    # The unit tests derive cgroup paths under /sys/fs/cgroup/agenthold, where the live service
    # keeps its microVMs: give unprivileged phases an empty private view of that directory.
    [[ ! -d /sys/fs/cgroup/agenthold ]] || props+=(-p TemporaryFileSystem=/sys/fs/cgroup/agenthold:ro)
    props+=(-p User="$BUILD_USER" -p Group="$BUILD_USER" -E HOME="$STATE"
      -E GOCACHE="$CACHE/user/go-build" -E GOMODCACHE="$CACHE/user/go-mod"
      -E XDG_CACHE_HOME="$CACHE/user/xdg-cache" -E XDG_DATA_HOME="$CACHE/user/xdg-data")
  else
    props+=(-E HOME="$CACHE/root" -E GOCACHE="$CACHE/root/go-build" -E GOMODCACHE="$CACHE/root/go-mod"
      -E XDG_CACHE_HOME="$CACHE/root/xdg-cache"
      -E GIT_CONFIG_COUNT=1 -E GIT_CONFIG_KEY_0=safe.directory -E GIT_CONFIG_VALUE_0="*")
  fi
  echo "==> phase $name ($who)"
  t0=$SECONDS
  current_unit="$unit"
  out="$(mktemp)"
  # The phase's own output goes to stdout as it happens; systemd-run's stderr carries only its
  # summary (runtime, CPU time, memory peak), read once the unit is gone.
  # systemd-run's own "Memory peak" line is not reliable here; sample the phase cgroup's
  # memory.peak (monotonic for the cgroup's lifetime) until the unit is gone.
  local peakfile="$out.peak"
  # memory.peak includes page cache (writing the image fills it); the anonymous memory peak is what
  # actually competes with the live service, so both are recorded.
  (cg=""; anon_max=0; while sleep 1; do
    [[ -n "$cg" ]] || cg="$(systemctl show -P ControlGroup "$unit" 2>/dev/null)"
    [[ -n "$cg" && -r "/sys/fs/cgroup$cg/memory.peak" ]] || continue
    anon="$(awk '$1 == "anon" {print $2}' "/sys/fs/cgroup$cg/memory.stat" 2>/dev/null)"
    [[ "$anon" =~ ^[0-9]+$ ]] && ((anon > anon_max)) && anon_max=$anon
    echo "$(cat "/sys/fs/cgroup$cg/memory.peak" 2>/dev/null) $anon_max" > "$peakfile.tmp" && mv -f "$peakfile.tmp" "$peakfile"
  done) &
  local sampler=$!
  set +e
  systemd-run "${props[@]}" -- bash -c 'exec "$@" 2>&1' phase "$@" 2>"$out"
  rc=$?
  set -e
  kill "$sampler" 2>/dev/null; wait "$sampler" 2>/dev/null || true
  cat -- "$out"
  current_unit=""
  runtime=$((SECONDS - t0))
  peak=""
  anon=""
  if [[ -s "$peakfile" ]]; then
    read -r peak anon < "$peakfile"
    peak="$((peak / 1048576))M"
    anon="$((anon / 1048576))M"
  fi
  rm -f -- "$out" "$peakfile" "$peakfile.tmp"
  jq -nc --arg name "$name" --arg who "$who" --argjson seconds "$runtime" --arg peak "${peak:-unknown}" --arg anon "${anon:-unknown}" --argjson rc "$rc" \
    '{name: $name, as: $who, seconds: $seconds, memory_peak: $peak, anon_peak: $anon, status: $rc}' >> "$phases"
  echo "==> phase $name: status $rc, ${runtime}s, memory peak ${peak:-unknown} (anonymous ${anon:-unknown})"
  return "$rc"
}

# Cheap checks first, then the expensive ones.
phase deploy-tests user bash -c 'bash scripts/deploy/pull_test.sh && bash scripts/deploy/bootstrap_test.sh'
phase test-go user make test-go
phase web user make web

# The guest kernel only changes with its inputs: cache it by their hash (like CI did).
kernel_key="$(cd "$wt" && { sha256sum scripts/guest/build-kernel.sh; find scripts/guest/kernel -type f -print0 | sort -z | xargs -0 sha256sum; } | sha256sum | cut -c1-32)"
kernel_cache="$CACHE/kernel/$kernel_key"
if [[ ! -f "$kernel_cache/kernel.manifest.json" ]]; then
  phase kernel user bash scripts/guest/build-kernel.sh
  tmpk="$(mktemp -d "$CACHE/kernel/.tmp.XXXXXX")"
  cp -- "$wt/build/guest/arm64/kernel.manifest.json" "$wt/build/guest/arm64/$(jq -er .file "$wt/build/guest/arm64/kernel.manifest.json")" "$tmpk/"
  chmod 0755 "$tmpk"
  rm -rf -- "$kernel_cache"
  mv -- "$tmpk" "$kernel_cache"
  find "$CACHE/kernel" -mindepth 1 -maxdepth 1 -type d ! -name "$kernel_key" -mtime +30 -exec rm -rf -- {} +
else
  echo "==> kernel: cached ($kernel_key)"
  install -d -o "$BUILD_USER" -g "$BUILD_USER" "$wt/build" "$wt/build/guest" "$wt/build/guest/arm64"
  cp -- "$kernel_cache"/* "$wt/build/guest/arm64/"
fi

# The base image only changes with its own inputs (scripts/guest/base-key.sh; sandboxd is not in it):
# cache its compressed image and manifest by that key. The cached .zst is hard-linked into the store,
# and pull.sh hard-links the store's copy into the service's base cache, so consecutive releases with
# the same base share one copy on disk.
base_key="$(cd "$wt" && bash scripts/guest/base-key.sh)"
[[ "$base_key" =~ ^[0-9a-f]{32}$ ]] || { echo 'unexpected base key' >&2; exit 1; }
base_cache="$CACHE/base/$base_key"
guest="$wt/build/guest/arm64"
if [[ -f "$base_cache/base-arm64.manifest.json" && -f "$base_cache/base-arm64.ext4.zst" ]]; then
  echo "==> base: cached ($base_key)"
  jq -e --arg key "$base_key" '.inputs.baseKey == $key' "$base_cache/base-arm64.manifest.json" >/dev/null || { echo 'cached base manifest has another key' >&2; exit 1; }
  install -d -o "$BUILD_USER" -g "$BUILD_USER" "$wt/build" "$wt/build/guest" "$guest"
  cp -- "$base_cache/base-arm64.manifest.json" "$guest/"
  ln -f -- "$base_cache/base-arm64.ext4.zst" "$guest/base-arm64.ext4.zst" 2>/dev/null || cp -- "$base_cache/base-arm64.ext4.zst" "$guest/"
  zstd -d -q -f --long=27 --sparse "$guest/base-arm64.ext4.zst" -o "$guest/base-arm64.ext4"
fi

phase release root bash scripts/release/build.sh

if [[ ! -f "$base_cache/base-arm64.ext4.zst" ]]; then
  install -d -m 0755 "$CACHE/base"
  tmpb="$(mktemp -d "$CACHE/base/.tmp.XXXXXX")"
  cp -- "$guest/base-arm64.manifest.json" "$tmpb/"
  ln -f -- "$guest/base-arm64.ext4.zst" "$tmpb/base-arm64.ext4.zst" 2>/dev/null || cp -- "$guest/base-arm64.ext4.zst" "$tmpb/"
  chown -R root:root "$tmpb"
  chmod 0755 "$tmpb"
  chmod 0644 "$tmpb"/*
  rm -rf -- "$base_cache"
  mv -T -- "$tmpb" "$base_cache"
fi
# One cached base: older keys are only reachable through the store, which pull.sh prunes.
find "$CACHE/base" -mindepth 1 -maxdepth 1 -type d ! -name "$base_key" -exec rm -rf -- {} +
phase kvm root make test-kvm

# --- publish ----------------------------------------------------------------------------------
out="$wt/build/release/arm64"
(cd "$out" && sha256sum --check --quiet SHA256SUMS)
for f in agenthold-linux-arm64 runtime-linux-arm64.manifest.json base-arm64.ext4.zst base-arm64.manifest.json; do
  grep -q "  $f\$" "$out/SHA256SUMS" || { echo "$f is not covered by SHA256SUMS" >&2; exit 1; }
done
jq -e --arg v "$version" '.format == 1 and .arch == "arm64" and .version == $v and .base.url == ""' "$out/runtime-linux-arm64.manifest.json" >/dev/null ||
  { echo 'runtime manifest does not describe this build' >&2; exit 1; }
[[ "$("$out/agenthold-linux-arm64" --version)" == "agenthold $version ("* ]] || { echo 'binary version does not match' >&2; exit 1; }
stage="$(mktemp -d "$STORE/.stage.XXXXXX")"
for f in agenthold-linux-arm64 runtime-linux-arm64.manifest.json base-arm64.manifest.json SHA256SUMS; do
  cp -- "$out/$f" "$stage/$f"
done
# The compressed base is the cached one (verified against SHA256SUMS below): link, do not copy.
if cmp -s -- "$out/base-arm64.ext4.zst" "$base_cache/base-arm64.ext4.zst" &&
   ln -- "$base_cache/base-arm64.ext4.zst" "$stage/base-arm64.ext4.zst" 2>/dev/null; then
  echo "==> base: linked from the cache"
else
  cp -- "$out/base-arm64.ext4.zst" "$stage/base-arm64.ext4.zst"
fi
chown -R root:root "$stage"
chmod 0644 "$stage"/*
chmod 0755 "$stage" "$stage/agenthold-linux-arm64"
(cd "$stage" && sha256sum --check --quiet SHA256SUMS)
if [[ -e "$STORE/$version" ]]; then
  # Only reached with --force: the old directory goes, the new one takes its name atomically enough
  # (the channel still points at it, and the bytes of a rebuilt commit are verified by pull.sh).
  rm -rf -- "$STORE/$version"
fi
mv -T -- "$stage" "$STORE/$version"
stage=""
sync -f "$STORE/$version/SHA256SUMS" 2>/dev/null || sync
sha() { sha256sum "$1" | cut -d' ' -f1; }
jq -n --arg commit "$commit" --arg version "$version" --arg built "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg binary "$(sha "$STORE/$version/agenthold-linux-arm64")" --arg manifest "$(sha "$STORE/$version/runtime-linux-arm64.manifest.json")" \
  --arg sums "$(sha "$STORE/$version/SHA256SUMS")" \
  '{format: 2, arch: "arm64", commit: $commit, version: $version, built_at: $built,
    binary: {path: ($version + "/agenthold-linux-arm64"), sha256: $binary},
    runtime_manifest: {path: ($version + "/runtime-linux-arm64.manifest.json"), sha256: $manifest},
    sums: {path: ($version + "/SHA256SUMS"), sha256: $sums}}' > "$STORE/.channel.json.tmp"
chmod 0644 "$STORE/.channel.json.tmp"
mv -f -- "$STORE/.channel.json.tmp" "$STORE/channel.json"
rm -f -- "$FAILED/$commit.log"
echo "==> published $version to $STORE"
cat "$phases"
# Deploy right away instead of waiting for the next pull timer run.
if systemctl is-enabled --quiet agenthold-pull.timer 2>/dev/null; then
  systemctl start --no-block agenthold-pull.service || true
fi
