#!/usr/bin/env bash
# Test and publish a main commit on this host, for this host's architecture (arm64 or amd64): the
# demo VM for arm64, korriban for amd64. The agenthold binary comes from the commit's GitHub
# release (sha-<commit>, built by .github/workflows/release.yml); the guest kernel and base image are
# built here, natively, and cached by their keys. GitHub never hosts kernels or base images.
#
#   build-local.sh                 publish main's newest released commit if it is new (the timer),
#                                  or with AGENTHOLD_BUILD_FOLLOW=<controller URL> the commit that
#                                  controller runs (an edge node of another architecture)
#   build-local.sh --commit SHA    publish that commit (it must be reachable from main)
#   build-local.sh --force         rebuild even if the commit was built, deployed or failed before
#                                  (ignores the failure record and the wait; a failure still counts)
#
# Every phase runs as its own transient systemd unit in agenthold-build.slice (memory and CPU
# limits, nice, idle I/O), as the unprivileged build user except the two that need root (the
# asset build, for the base image's mmdebstrap, and the KVM tests). systemd reports each phase's
# runtime and memory peak; they end up in the log and in $STATE/last-build.json.
#
# A commit is published only when everything passed: its assets into $STORE/assets/<arch>/, then
# atomically $STORE/sha-<commit>/ and $STORE/channel.json (written last). A failed commit is retried
# automatically: up to MAX_ATTEMPTS attempts in total (default 3), at least RETRY_DELAY seconds apart
# (default 15 minutes). Each failure writes $FAILED/<commit>.attempt-N.log, copies it to
# $FAILED/<commit>.log (the latest) and records the count in $FAILED/<commit>.attempts. After the
# last failed attempt the commit is not built again until --force; a new commit on main is built
# normally. A commit without a release (yet) is not a failure: it is looked at again next time.
# pull.sh deploys from $STORE.
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
MAX_ATTEMPTS="${AGENTHOLD_BUILD_MAX_ATTEMPTS:-3}"
RETRY_DELAY="${AGENTHOLD_BUILD_RETRY_DELAY:-900}"
KEEP_LOGS=20
GOMAXPROCS_BUILD="${AGENTHOLD_BUILD_GOMAXPROCS:-4}"
# The base URL of a controller (https://demo.agenthold.dev): build the release it runs (GET
# /api/v1/version) instead of main's head, so a node built here runs exactly its controller's version.
FOLLOW="${AGENTHOLD_BUILD_FOLLOW:-}"
# Where release binaries are downloaded from: <RELEASES>/sha-<commit>/<file>. A file:// directory of
# the same shape works too (tests). "local" builds the binary here instead (scripts/release/build.sh),
# for hosts that do not use the GitHub releases.
RELEASES="${AGENTHOLD_BUILD_RELEASES:-https://github.com/gempir/agenthold/releases/download}"
# How far back from main's head to look for the newest commit that has a release (docs-only
# commits skip CI and have none).
LOOKBACK="${AGENTHOLD_BUILD_LOOKBACK:-20}"

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
case "$(uname -m)" in
  aarch64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) echo 'the builder runs on arm64 or amd64 Linux' >&2; exit 1 ;;
esac
[[ -z "$FOLLOW" || "$FOLLOW" =~ ^https?://[A-Za-z0-9.-]+(:[0-9]+)?/?$ ]] || { echo "invalid AGENTHOLD_BUILD_FOLLOW $FOLLOW: a controller base URL" >&2; exit 2; }
[[ "$STORE" == /* && "$FAILED" == /* && "$STATE" == /* && "$CACHE" == /* && "$LOGS" == /* ]] || { echo 'paths must be absolute' >&2; exit 2; }
[[ "$MIN_FREE_GB" =~ ^[0-9]+$ && "$GOMAXPROCS_BUILD" =~ ^[1-9][0-9]*$ && "$LOOKBACK" =~ ^[1-9][0-9]*$ ]] || { echo 'invalid limits' >&2; exit 2; }
[[ "$RELEASES" == local || "$RELEASES" =~ ^(https://[A-Za-z0-9.-]+(:[0-9]+)?|file://)(/[A-Za-z0-9._~-]+)*$ ]] || { echo "invalid AGENTHOLD_BUILD_RELEASES $RELEASES" >&2; exit 2; }
proto='=https'; [[ "$RELEASES" == file://* ]] && proto='=file'
[[ "$MAX_ATTEMPTS" =~ ^[1-9][0-9]*$ && "$RETRY_DELAY" =~ ^[0-9]+$ ]] || { echo 'invalid retry limits' >&2; exit 2; }
id -u "$BUILD_USER" >/dev/null 2>&1 || { echo "build user $BUILD_USER does not exist" >&2; exit 1; }
for tool in git go node pnpm jq zstd sha256sum flock systemd-run make curl; do
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
if [[ -z "$commit" && -n "$FOLLOW" ]]; then
  # Not a failure: the controller may be restarting. The next timer run asks again.
  commit="$(curl -fsS --proto =https,http --max-time 20 "${FOLLOW%/}/api/v1/version" |
    jq -er '.version | select(type == "string" and test("^sha-[0-9a-f]{40}$")) | ltrimstr("sha-")')" ||
    { echo "cannot read the release ${FOLLOW%/} runs; nothing to build"; exit 0; }
  echo "${FOLLOW%/} runs $commit"
fi
# released <commit>: whether the commit's release has this architecture's binary.
released() { [[ "$RELEASES" == local ]] && return 0; curl -fsS --proto "$proto" --head --location --max-time 20 -o /dev/null "$RELEASES/sha-$1/agenthold-linux-$ARCH"; }
if [[ -z "$commit" ]]; then
  while read -r c; do
    if released "$c"; then commit="$c"; break; fi
  done < <(as_user git -C "$mirror" rev-list --max-count="$LOOKBACK" "$head")
  [[ -n "$commit" ]] || { echo "none of the newest $LOOKBACK commits on $BRANCH has a release yet; nothing to build"; exit 0; }
  [[ "$commit" == "$head" ]] || echo "newest released commit is $commit (main head $head)"
else
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || { echo '--commit takes a full 40-hex commit' >&2; exit 2; }
  as_user git -C "$mirror" merge-base --is-ancestor "$commit" "$head" || { echo "$commit is not on $BRANCH" >&2; exit 1; }
fi
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || { echo 'unexpected commit id' >&2; exit 1; }
version="sha-$commit"
if [[ -n "$FOLLOW" ]] && ! released "$commit"; then echo "$commit has no release for $ARCH yet; nothing to build"; exit 0; fi

# Failed attempts of a commit. A record from before attempts were counted holds one attempt.
failed_attempts() {
  local n=""
  [[ -f "$FAILED/$1.attempts" ]] && n="$(cat -- "$FAILED/$1.attempts")"
  if [[ "$n" =~ ^[0-9]+$ ]]; then echo "$n"
  elif [[ -f "$FAILED/$1.log" ]]; then echo 1
  else echo 0
  fi
}

deployed="$(basename "$(readlink -f "$DEPLOY_ROOT/current" 2>/dev/null || true)")"
if ((!force)); then
  if [[ -f "$STORE/$version/SHA256SUMS" ]]; then echo "$commit is already built"; exit 0; fi
  if [[ "$deployed" == "$commit" ]]; then echo "$commit is already deployed"; exit 0; fi
  if [[ -f "$FAILED/$commit.log" ]]; then
    attempts="$(failed_attempts "$commit")"
    if ((attempts >= MAX_ATTEMPTS)); then echo "$commit failed before (see $FAILED/$commit.log); waiting for a new commit"; exit 0; fi
    wait_s=$(( $(stat -c %Y -- "$FAILED/$commit.log" 2>/dev/null || echo 0) + RETRY_DELAY - $(date +%s) ))
    if ((wait_s > 0)); then echo "$commit failed $attempts of $MAX_ATTEMPTS time(s); next attempt in ${wait_s}s"; exit 0; fi
    echo "$commit failed $attempts of $MAX_ATTEMPTS time(s); retrying"
  fi
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
    # Count before writing anything: a record from before counting is read through its latest log.
    failed_n=$(( $(failed_attempts "$commit") + 1 ))
    cp -- "$log" "$FAILED/$commit.attempt-$failed_n.log"
    printf '%s\n' "$failed_n" > "$FAILED/.$commit.attempts.tmp" && mv -f -- "$FAILED/.$commit.attempts.tmp" "$FAILED/$commit.attempts"
    # The latest log goes last: its existence and mtime are what the next run waits on.
    cp -- "$log" "$FAILED/.$commit.log.tmp" && mv -f -- "$FAILED/.$commit.log.tmp" "$FAILED/$commit.log"
    echo "failed attempt $failed_n of $MAX_ATTEMPTS for $commit"
  fi
  jq -n --arg commit "$commit" --arg started "$started" --arg finished "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson status "$status" \
    --slurpfile phases <(jq -s . "$phases" 2>/dev/null || echo '[]') \
    '{commit: $commit, started: $started, finished: $finished, status: $status, phases: $phases[0]}' > "$STATE/last-build.json.tmp" &&
    mv -f "$STATE/last-build.json.tmp" "$STATE/last-build.json"
  rm -f -- "$phases"
  # Keep the newest logs only; failure records stay (they stop rebuild loops).
  ls -1t "$LOGS"/*.log 2>/dev/null | tail -n +$((KEEP_LOGS + 1)) | xargs -r rm -f --
  # Failure records age out together (latest log, attempt logs, counter); a commit still at main's head
  # is then built again, as before. Attempt logs of a commit that later passed age out the same way.
  find "$FAILED" -maxdepth 1 \( -name '*.log' -o -name '*.attempts' \) -mtime +60 -delete 2>/dev/null
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

# --- the release binary -------------------------------------------------------------------------
# Downloaded before anything is built: its embedded manifest names the kernel and base this commit
# needs. It must be the binary of this commit (version) for this architecture.
rel="$STATE/work/$commit.release"
rm -rf -- "$rel"
install -d -m 0755 "$rel"
if [[ "$RELEASES" == local ]]; then
  # Built as the build user right after the web phase; checked there.
  chown "$BUILD_USER:$BUILD_USER" "$rel"
fi
[[ "$RELEASES" == local ]] || for f in SHA256SUMS "agenthold-linux-$ARCH" "runtime-linux-$ARCH.manifest.json"; do
  curl -fsSL --proto "$proto" --proto-redir "$proto" --retry 3 --max-time 600 -o "$rel/$f" "$RELEASES/$version/$f"
done
check_release() {
(cd "$rel" && grep -E "  (agenthold-linux-$ARCH|runtime-linux-$ARCH\.manifest\.json)\$" SHA256SUMS | sha256sum --check --strict --quiet) ||
  { echo 'release files do not match their SHA256SUMS' >&2; exit 1; }
[[ "$(grep -cE "  (agenthold-linux-$ARCH|runtime-linux-$ARCH\.manifest\.json)\$" "$rel/SHA256SUMS")" == 2 ]] || { echo 'SHA256SUMS does not cover the release files' >&2; exit 1; }
chmod 0755 "$rel/agenthold-linux-$ARCH"
[[ "$("$rel/agenthold-linux-$ARCH" --version)" == "agenthold $version ("* ]] || { echo 'release binary version does not match' >&2; exit 1; }
want_kernel="$(jq -er --arg v "$version" --arg arch "$ARCH" 'select(.format == 2 and .version == $v and .arch == $arch) | .kernel.key' "$rel/runtime-linux-$ARCH.manifest.json")" &&
want_base="$(jq -er '.base.key' "$rel/runtime-linux-$ARCH.manifest.json")" ||
  { echo 'release runtime manifest does not describe this commit' >&2; exit 1; }
kernel_key="$(cd "$wt" && bash scripts/guest/kernel-key.sh)"
base_key="$(cd "$wt" && bash scripts/guest/base-key.sh)"
[[ "$kernel_key" == "$want_kernel" && "$base_key" == "$want_base" ]] ||
  { echo "release names kernel $want_kernel and base $want_base, the source $kernel_key and $base_key" >&2; exit 1; }
echo "==> release $version: kernel $kernel_key, base $base_key"
}
[[ "$RELEASES" == local ]] || check_release

# Cheap checks first, then the expensive ones.
phase deploy-tests user bash -c 'bash scripts/deploy/pull_test.sh && bash scripts/deploy/bootstrap_test.sh'
phase test-go user make test-go
phase web user make web
if [[ "$RELEASES" == local ]]; then
  phase binary user env VERSION="$version" bash scripts/release/build.sh --out "$rel"
  check_release
fi

# The guest kernel only changes with its inputs: cache it by its key.
kernel_cache="$CACHE/kernel/$kernel_key"
guest="$wt/build/guest/$ARCH"
install -d -o "$BUILD_USER" -g "$BUILD_USER" "$wt/build" "$wt/build/guest" "$guest"
if [[ ! -f "$kernel_cache/kernel.manifest.json" ]]; then
  phase kernel user bash scripts/guest/build-kernel.sh
  tmpk="$(mktemp -d "$CACHE/kernel/.tmp.XXXXXX")"
  cp -- "$guest/kernel.manifest.json" "$guest/$(jq -er .file "$guest/kernel.manifest.json")" "$tmpk/"
  chmod 0755 "$tmpk"
  rm -rf -- "$kernel_cache"
  mv -- "$tmpk" "$kernel_cache"
  find "$CACHE/kernel" -mindepth 1 -maxdepth 1 -type d ! -name "$kernel_key" -mtime +30 -exec rm -rf -- {} +
else
  echo "==> kernel: cached ($kernel_key)"
  cp -- "$kernel_cache"/* "$guest/"
fi
printf '%s\n' "$kernel_key" > "$guest/kernel.key"

# The base image only changes with its own inputs: cache its compressed image and manifest by key.
# The cached .zst is hard-linked into the store, and pull.sh hard-links the store's copy into the
# service's base cache, so releases with the same base share one copy on disk.
# Root's own cache (0700): the build user must not be able to swap the base a release ships.
base_cache="$CACHE/root/base/$base_key"
if [[ -f "$base_cache/base-$ARCH.manifest.json" && -f "$base_cache/base-$ARCH.ext4.zst" ]]; then
  echo "==> base: cached ($base_key)"
  jq -e --arg key "$base_key" '.inputs.baseKey == $key' "$base_cache/base-$ARCH.manifest.json" >/dev/null || { echo 'cached base manifest has another key' >&2; exit 1; }
  cp -- "$base_cache/base-$ARCH.manifest.json" "$guest/"
  ln -f -- "$base_cache/base-$ARCH.ext4.zst" "$guest/base-$ARCH.ext4.zst" 2>/dev/null || cp -- "$base_cache/base-$ARCH.ext4.zst" "$guest/"
  zstd -d -q -f --long=27 --sparse "$guest/base-$ARCH.ext4.zst" -o "$guest/base-$ARCH.ext4"
fi

phase assets root bash scripts/release/build-assets.sh

if [[ ! -f "$base_cache/base-$ARCH.ext4.zst" ]]; then
  install -d -m 0700 "$CACHE/root/base"
  tmpb="$(mktemp -d "$CACHE/root/base/.tmp.XXXXXX")"
  cp -- "$guest/base-$ARCH.manifest.json" "$tmpb/"
  ln -f -- "$guest/base-$ARCH.ext4.zst" "$tmpb/base-$ARCH.ext4.zst" 2>/dev/null || cp -- "$guest/base-$ARCH.ext4.zst" "$tmpb/"
  chown -R root:root "$tmpb"
  chmod 0755 "$tmpb"
  chmod 0644 "$tmpb"/*
  rm -rf -- "$base_cache"
  mv -T -- "$tmpb" "$base_cache"
fi
# Under the lock every KVM experiment on this host takes, so they never run beside these tests.
# The machines boot the release binary itself as their sandboxd.
phase kvm root env AGENTHOLD_KVM_SANDBOXD="$rel/agenthold-linux-$ARCH" flock /tmp/agenthold-kvm.lock make test-kvm

# --- publish ----------------------------------------------------------------------------------
assets="$wt/build/assets/$ARCH"
for kind in kernel base; do
  key="$kernel_key"; [[ "$kind" == base ]] && key="$base_key"
  jq -e --arg kind "$kind" --arg arch "$ARCH" --arg key "$key" '.format == 1 and .kind == $kind and .arch == $arch and .key == $key' \
    "$assets/$kind-$key.json" >/dev/null || { echo "$kind asset manifest does not describe this build" >&2; exit 1; }
  echo "$(jq -er .compressed.sha256 "$assets/$kind-$key.json")  $assets/$kind-$key.zst" | sha256sum --check --status ||
    { echo "$kind asset does not match its manifest" >&2; exit 1; }
done
# Assets are named by key and shared by every release that names them: an existing one stays (a
# rebuild of the same inputs must not change bytes that deployed nodes already verified).
store_assets="$STORE/assets/$ARCH"
install -d -m 0755 "$STORE/assets" "$store_assets"
for kind in kernel base; do
  key="$kernel_key"; [[ "$kind" == base ]] && key="$base_key"
  [[ -f "$store_assets/$kind-$key.json" ]] && { echo "==> $kind $key: already in the store"; continue; }
  ln -f -- "$assets/$kind-$key.zst" "$store_assets/.$kind-$key.zst.tmp" 2>/dev/null || cp -- "$assets/$kind-$key.zst" "$store_assets/.$kind-$key.zst.tmp"
  cp -- "$assets/$kind-$key.json" "$store_assets/.$kind-$key.json.tmp"
  chown root:root "$store_assets/.$kind-$key.zst.tmp" "$store_assets/.$kind-$key.json.tmp"
  chmod 0644 "$store_assets/.$kind-$key.zst.tmp" "$store_assets/.$kind-$key.json.tmp"
  mv -f -- "$store_assets/.$kind-$key.zst.tmp" "$store_assets/$kind-$key.zst"
  mv -f -- "$store_assets/.$kind-$key.json.tmp" "$store_assets/$kind-$key.json"
done
stage="$(mktemp -d "$STORE/.stage.XXXXXX")"
cp -- "$rel/agenthold-linux-$ARCH" "$rel/runtime-linux-$ARCH.manifest.json" "$stage/"
(cd "$stage" && sha256sum "agenthold-linux-$ARCH" "runtime-linux-$ARCH.manifest.json" > SHA256SUMS)
chown -R root:root "$stage"
chmod 0644 "$stage"/*
chmod 0755 "$stage" "$stage/agenthold-linux-$ARCH"
if [[ -e "$STORE/$version" ]]; then
  # Only reached with --force: the old directory goes, the new one takes its name.
  rm -rf -- "$STORE/$version"
fi
mv -T -- "$stage" "$STORE/$version"
stage=""
sync -f "$STORE/$version/SHA256SUMS" 2>/dev/null || sync
sha() { sha256sum "$1" | cut -d' ' -f1; }
jq -n --arg commit "$commit" --arg version "$version" --arg built "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg arch "$ARCH" \
  --arg binary "$(sha "$STORE/$version/agenthold-linux-$ARCH")" --arg manifest "$(sha "$STORE/$version/runtime-linux-$ARCH.manifest.json")" \
  --arg sums "$(sha "$STORE/$version/SHA256SUMS")" \
  --arg kkey "$kernel_key" --arg ksha "$(sha "$store_assets/kernel-$kernel_key.json")" \
  --arg bkey "$base_key" --arg bsha "$(sha "$store_assets/base-$base_key.json")" \
  '{format: 3, arch: $arch, commit: $commit, version: $version, built_at: $built,
    binary: {path: ($version + "/agenthold-linux-" + $arch), sha256: $binary},
    runtime_manifest: {path: ($version + "/runtime-linux-" + $arch + ".manifest.json"), sha256: $manifest},
    sums: {path: ($version + "/SHA256SUMS"), sha256: $sums},
    kernel: {key: $kkey, manifest: {path: ("assets/" + $arch + "/kernel-" + $kkey + ".json"), sha256: $ksha}},
    base: {key: $bkey, manifest: {path: ("assets/" + $arch + "/base-" + $bkey + ".json"), sha256: $bsha}}}' > "$STORE/.channel.json.tmp"
chmod 0644 "$STORE/.channel.json.tmp"
mv -f -- "$STORE/.channel.json.tmp" "$STORE/channel.json"
# A commit that passed on a retry keeps its attempt logs (flaky tests show up there) until they age out.
rm -f -- "$FAILED/$commit.log" "$FAILED/$commit.attempts"
rm -rf -- "$rel"
echo "==> published $version to $STORE"
# One cached base, cleaned only after a successful publish: older keys stay reachable through the
# store (hard links), which pull.sh prunes.
find "$CACHE/root/base" -mindepth 1 -maxdepth 1 -type d ! -name "$base_key" -exec rm -rf -- {} +
cat "$phases"
# Deploy right away instead of waiting for the next pull timer run.
if systemctl is-enabled --quiet agenthold-pull.timer 2>/dev/null; then
  systemctl start --no-block agenthold-pull.service || true
fi
