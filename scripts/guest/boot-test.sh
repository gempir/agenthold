#!/usr/bin/env bash
# N2 proof: boots the real base image and guest kernel in Firecracker by hand, with sandboxd as
# PID 1, against a host-side test tunnel endpoint (scripts/guest/testhost, the real tunnel Hub).
#
#   scripts/guest/boot-test.sh [--kernel agenthold|ci] [--docker] [--verbose-kernel]
#
# Run it from macOS (it re-runs itself inside the Lima VM "agenthold") or inside the VM.
# It builds or reuses the artifacts (build/guest/<arch>/), creates blank overlay and home disks and
# checks, over four boots:
#   1. sandboxd connects and authenticates; commands run as "agent" in /home/agent; boot files and
#      the environment from the config drive arrive; a file in /home/agent, a file under / and a
#      dpkg-installed package are written; shutdown through the tunnel ends Firecracker quickly.
#   2. same disks: all three survived.
#   3. overlay disk replaced by a blank one: / is reset (file and package gone), /home kept.
#   4. SIGTERM-free path: Ctrl+Alt+Del is x86 only, so boot 4 checks a shutdown requested from
#      inside the guest (sudo kill -TERM 1) also powers off cleanly.
# With --docker it then boots once more with outbound NAT and DNS, installs Docker in the guest and
# runs `docker run --rm hello-world` and a container with outbound networking.
# Exits non-zero on any failure.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if [[ "$(uname -s)" == "Darwin" ]]; then
  command -v limactl >/dev/null || { echo "limactl not found: brew install lima" >&2; exit 1; }
  make -C "$REPO_ROOT" dev-vm >/dev/null
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- bash "$REPO_ROOT/scripts/guest/boot-test.sh" "$@"
fi

KERNEL_KIND=agenthold
DOCKER=0
QUIET=1
while (($#)); do
  case "$1" in
    --kernel) KERNEL_KIND="$2"; shift 2 ;;
    --docker) DOCKER=1; shift ;;
    --verbose-kernel) QUIET=0; shift ;; # kernel messages on the serial console (about 2 s slower)
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "$(uname -m)" in
  aarch64) ARCH=arm64; FC_ARCH=aarch64 ;;
  x86_64) ARCH=amd64; FC_ARCH=x86_64 ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac
ART="$REPO_ROOT/build/guest/$ARCH"
SCRIPTS="$REPO_ROOT/scripts/guest"

log() { printf '\n==> %s\n' "$*"; }
STATUS=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*" >&2; STATUS=1; }

# --- artifacts (as the invoking user; the builds sudo themselves) -------------------------------------
if [[ $EUID -ne 0 ]]; then
  if [[ "$KERNEL_KIND" == agenthold ]] && ! compgen -G "$ART/vmlinux-*-agenthold" >/dev/null; then
    log "Building the guest kernel"
    bash "$SCRIPTS/build-kernel.sh"
  fi
  log "Building sandboxd and testhost"
  mkdir -p "$ART/bin"
  version="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
  (cd "$REPO_ROOT" && CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -trimpath \
    -ldflags "-s -w -X github.com/gempir/agenthold/internal/version.Version=$version" -o "$ART/bin/sandboxd" ./cmd/sandboxd &&
    CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o "$ART/bin/testhost" ./scripts/guest/testhost)
  want="$(sha256sum "$ART/bin/sandboxd" | cut -d' ' -f1)"
  have="$(jq -r '.inputs.sandboxdSha256 // empty' "$ART/base-$ARCH.manifest.json" 2>/dev/null || true)"
  if [[ ! -f "$ART/base-$ARCH.ext4" || "$want" != "$have" ]]; then
    log "Building the base image (sandboxd changed or image missing)"
    bash "$SCRIPTS/build-rootfs.sh" --sandboxd "$ART/bin/sandboxd" --no-zstd
  fi
  exec sudo -E env "PATH=$PATH" bash "${BASH_SOURCE[0]}" --kernel "$KERNEL_KIND" $( ((DOCKER)) && echo --docker) $( ((QUIET)) || echo --verbose-kernel)
fi

# --- as root from here -----------------------------------------------------------------------------------
FC_VERSION="v1.18.0"
FC_SHA256_aarch64="2a6130ff0338901ff9d7fbd73ce046c6b1c6123889dc84a34d678aa53bd0c0c2"
FC_SHA256_x86_64="1ac8eec866dd5eea14efecf6323e22b23eea24054545338a5b34bc5bca6623cd"
CI_KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20261111-778a26c0594c-0/$FC_ARCH/vmlinux-6.1.188"
CI_KERNEL_SHA256_aarch64="7e7f3c7073c36e334e33e8a74ae744cea06c914f0ab9303b9e557e4106840da4"
CACHE=/var/cache/agenthold-guest
RUN=/var/lib/agenthold-boot-test
TAP=fcboot0
HOST_IP=172.31.254.1
GUEST_IP=172.31.254.2
GUEST_MAC=06:00:AC:1F:FE:02
TUNNEL_PORT=7443
mkdir -p "$CACHE" "$RUN"

fetch() { # url dest sha256
  local url="$1" dest="$2" sha="$3"
  if [[ -f "$dest" ]] && echo "$sha  $dest" | sha256sum -c --quiet - 2>/dev/null; then return; fi
  curl -fsSL --retry 3 -o "$dest.part" "$url"
  echo "$sha  $dest.part" | sha256sum -c --quiet - || { echo "sha256 mismatch for $url" >&2; exit 1; }
  mv "$dest.part" "$dest"
}

fc_sha_var="FC_SHA256_$FC_ARCH"
[[ -n "${!fc_sha_var}" ]] || { echo "no pinned Firecracker sha256 for $FC_ARCH" >&2; exit 1; }
FC_TGZ="$CACHE/firecracker-$FC_VERSION-$FC_ARCH.tgz"
fetch "https://github.com/firecracker-microvm/firecracker/releases/download/$FC_VERSION/firecracker-$FC_VERSION-$FC_ARCH.tgz" "$FC_TGZ" "${!fc_sha_var}"
FC_BIN="$CACHE/firecracker-$FC_VERSION-$FC_ARCH"
[[ -x "$FC_BIN" ]] || tar -xzf "$FC_TGZ" -O "release-$FC_VERSION-$FC_ARCH/firecracker-$FC_VERSION-$FC_ARCH" >"$FC_BIN" && chmod 0755 "$FC_BIN"

if [[ "$KERNEL_KIND" == ci ]]; then
  ci_sha_var="CI_KERNEL_SHA256_$FC_ARCH"
  KERNEL="$CACHE/vmlinux-6.1.188-ci"
  fetch "$CI_KERNEL_URL" "$KERNEL" "${!ci_sha_var}"
else
  KERNEL="$(compgen -G "$ART/vmlinux-*-agenthold" | head -1)"
fi
BASE_SRC="$ART/base-$ARCH.ext4"
BASE_SHA="$(cut -d' ' -f1 "$BASE_SRC.sha256")"
# Firecracker reads the base from local disk, not through the virtiofs mount of the repo.
BASE="$CACHE/base-$ARCH-${BASE_SHA:0:16}.ext4"
if [[ ! -f "$BASE" ]]; then
  rm -f "$CACHE"/base-"$ARCH"-*.ext4
  cp --sparse=always "$BASE_SRC" "$BASE.part"
  echo "$BASE_SHA  $BASE.part" | sha256sum -c --quiet -
  mv "$BASE.part" "$BASE"
fi
TESTHOST="$ART/bin/testhost"
"$FC_BIN" --version | head -1
echo "kernel: $KERNEL ($(sha256sum "$KERNEL" | cut -c1-16))"
echo "base:   $BASE ($(stat -c %s "$BASE") bytes)"

# --- host network -----------------------------------------------------------------------------------------
FC_PID=""
TH_PID=""
cleanup() {
  [[ -n "$FC_PID" ]] && kill -9 "$FC_PID" 2>/dev/null || true
  [[ -n "$TH_PID" ]] && kill "$TH_PID" 2>/dev/null || true
  nft delete table ip agenthold_boottest 2>/dev/null || true
  ip link del "$TAP" 2>/dev/null || true
  [[ -z "${FORWARDING:-}" ]] || sysctl -qw "net.ipv4.ip_forward=$FORWARDING"
}
trap cleanup EXIT
ip link del "$TAP" 2>/dev/null || true
ip tuntap add dev "$TAP" mode tap
ip addr add "$HOST_IP/30" dev "$TAP"
ip link set "$TAP" up

# --- disks ------------------------------------------------------------------------------------------------
rm -rf "$RUN"/*
OVERLAY="$RUN/overlay.ext4"
HOME_DISK="$RUN/home.ext4"
truncate -s 4G "$OVERLAY"
truncate -s 2G "$HOME_DISK"
TOKEN="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
NONCE="$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
AGENT=ag_boottest
printf 'boot file %s\n' "$NONCE" >"$RUN/bootfile.txt"
"$TESTHOST" -write-config "$RUN/config.img" -agent "$AGENT" -token "$TOKEN" -port "$TUNNEL_PORT" \
  -hostname boottest -file "/etc/agenthold/boottest.txt=$RUN/bootfile.txt" -env 'BOOTTEST_SERVER={{server}}'
echo "config drive: $(stat -c %s "$RUN/config.img") bytes"

BOOT_ARGS="console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda ro rootfstype=ext4 init=/opt/agenthold/sandboxd"
BOOT_ARGS+=" ip=$GUEST_IP::$HOST_IP:255.255.255.252::eth0:off"
((QUIET)) && BOOT_ARGS+=" quiet"

ms() { date +%s%3N; }

# boot NAME MEM_MIB TESTHOST_ARGS...: boots once, runs the testhost with the given args, waits for
# Firecracker to exit. Leaves $RUN/NAME.json (report), $RUN/NAME.serial and timing variables.
boot() {
  local name="$1" mem="$2"; shift 2
  local api="$RUN/$name.sock" serial="$RUN/$name.serial"
  rm -f "$api" "$RUN/$name.ready"
  "$TESTHOST" -listen "$HOST_IP:$TUNNEL_PORT" -agent "$AGENT" -token "$TOKEN" -out "$RUN/$name.json" \
    -ready "$RUN/$name.ready" "$@" 2>"$RUN/$name.testhost.log" &
  TH_PID=$!
  for _ in $(seq 1 100); do [[ -f "$RUN/$name.ready" ]] && break; sleep 0.05; done
  "$FC_BIN" --api-sock "$api" </dev/null >"$serial" 2>&1 &
  FC_PID=$!
  for _ in $(seq 1 100); do [[ -S "$api" ]] && break; sleep 0.05; done
  fcapi() {
    local code
    code="$(curl -s -o "$RUN/api.out" -w '%{http_code}' --unix-socket "$api" -X PUT -H 'Content-Type: application/json' --data "$2" "http://localhost$1")"
    [[ "$code" == 2* ]] || { echo "firecracker PUT $1 -> $code $(cat "$RUN/api.out")" >&2; return 1; }
  }
  fcapi /machine-config "{\"vcpu_count\": 2, \"mem_size_mib\": $mem}"
  fcapi /boot-source "{\"kernel_image_path\": \"$KERNEL\", \"boot_args\": \"$BOOT_ARGS\"}"
  fcapi /drives/base "{\"drive_id\": \"base\", \"path_on_host\": \"$BASE\", \"is_root_device\": true, \"is_read_only\": true}"
  fcapi /drives/overlay "{\"drive_id\": \"overlay\", \"path_on_host\": \"$OVERLAY\", \"is_root_device\": false, \"is_read_only\": false}"
  fcapi /drives/home "{\"drive_id\": \"home\", \"path_on_host\": \"$HOME_DISK\", \"is_root_device\": false, \"is_read_only\": false}"
  fcapi /drives/config "{\"drive_id\": \"config\", \"path_on_host\": \"$RUN/config.img\", \"is_root_device\": false, \"is_read_only\": true}"
  fcapi /network-interfaces/eth0 "{\"iface_id\": \"eth0\", \"guest_mac\": \"$GUEST_MAC\", \"host_dev_name\": \"$TAP\"}"
  T_START=$(ms)
  fcapi /actions '{"action_type": "InstanceStart"}'
  wait "$TH_PID" || true
  TH_PID=""
  # Firecracker should exit by itself shortly after a shutdown; give it 20s, then kill.
  local deadline=$(( $(date +%s) + 20 ))
  while kill -0 "$FC_PID" 2>/dev/null && (( $(date +%s) < deadline )); do sleep 0.02; done
  T_EXIT=$(ms)
  FC_EXITED=1
  if kill -0 "$FC_PID" 2>/dev/null; then FC_EXITED=0; kill -9 "$FC_PID"; fi
  wait "$FC_PID" 2>/dev/null || true
  FC_PID=""
  local rep="$RUN/$name.json"
  T_CONN=$(jq -r '.connectedAtMs // 0' "$rep" 2>/dev/null || echo 0)
  T_SHUT=$(jq -r '.shutdownRequestedAtMs // 0' "$rep" 2>/dev/null || echo 0)
  printf 'timing %s: InstanceStart -> tunnel connected %s ms' "$name" "$(( T_CONN > 0 ? T_CONN - T_START : -1 ))"
  ((T_SHUT > 0)) && printf ', shutdown request -> Firecracker exit %s ms' "$(( T_EXIT - T_SHUT ))"
  printf '\n'
  grep -E 'uptime=' "$serial" | sed -E 's/.*msg="?([^"]*)"? .*uptime=([0-9.]+s).*/  guest: \1 at uptime \2/' || true
  # A shell that sources this file can inspect $RUN/$name.serial for kernel and init messages.
}

out() { jq -r ".results[$2].stdout" "$RUN/$1.json"; } # report index
code() { jq -r ".results[$2].code" "$RUN/$1.json"; }
check_conn() { # name
  if [[ "$(jq -r .connected "$RUN/$1.json")" == true && "$(jq -r .rejected "$RUN/$1.json")" == 0 ]]; then
    pass "$1: sandboxd connected and authenticated (hello: $(jq -c .hello "$RUN/$1.json"))"
  else
    fail "$1: no authenticated tunnel ($(jq -c '{connected, rejected, error}' "$RUN/$1.json" 2>/dev/null)); serial tail:"
    tail -40 "$RUN/$1.serial" >&2
  fi
}
check_exit() { # name
  if ((FC_EXITED)) && [[ "$(jq -r .shutdownAck "$RUN/$1.json")" == true ]] && (( T_EXIT - T_SHUT < 8000 )); then
    pass "$1: shutdown acknowledged, Firecracker exited $(( T_EXIT - T_SHUT )) ms after the request"
  elif ((FC_EXITED)) && (( T_SHUT == 0 )); then
    pass "$1: Firecracker exited by itself"
  else
    fail "$1: Firecracker did not exit after shutdown (exited=$FC_EXITED, $(( T_EXIT - T_SHUT )) ms)"
  fi
  grep -q 'power off' "$RUN/$1.serial" || fail "$1: no clean power off in the serial log"
}

# A tiny local .deb, to show a dpkg-installed package lives on the overlay.
MKDEB='set -e; d=$(mktemp -d); mkdir -p $d/DEBIAN $d/usr/local/share/boottest; echo pkg-NONCE > $d/usr/local/share/boottest/pkg.txt; printf "Package: agenthold-boottest\nVersion: 1.0\nArchitecture: all\nMaintainer: agenthold <noreply@example.com>\nDescription: boot test\n" > $d/DEBIAN/control; dpkg-deb --build $d /tmp/boottest.deb >/dev/null; sudo dpkg -i /tmp/boottest.deb >/dev/null; dpkg-query -W -f="\${Status}" agenthold-boottest'
MKDEB="${MKDEB//NONCE/$NONCE}"

# --- boot 1: first boot on blank disks ---------------------------------------------------------------------
log "Boot 1: blank overlay and home disks"
boot boot1 1024 \
  -run 'id -un; id -u; pwd; echo "$HOME"; stat -c "%U:%G %a" /home/agent' \
  -run "echo home-$NONCE > ~/persist.txt && sudo sh -c 'echo root-$NONCE > /etc/boottest-root.txt' && echo ok" \
  -run "$MKDEB" \
  -run 'cat /etc/agenthold/boottest.txt; echo "env=$BOOTTEST_SERVER"; hostname; cat /etc/resolv.conf | grep nameserver; ip route show default' \
  -run 'echo "--- cmdline"; cat /proc/cmdline; echo "--- mounts"; findmnt -rn -o TARGET,SOURCE,FSTYPE,OPTIONS | grep -vE "^/(proc|sys|dev)/"; echo "--- blk"; for b in /sys/block/vd*; do echo "$(basename $b) $(cat $b/size) ro=$(cat $b/ro) serial=$(cat $b/serial 2>/dev/null)"; done; echo "--- pid1"; ps -o pid,ppid,user,comm -e | head -20; echo "--- uname"; uname -a; echo "--- df"; df -h / /home/agent' \
  -shutdown
check_conn boot1
[[ "$(out boot1 0)" == $'agent\n1000\n/home/agent\n/home/agent\nagent:agent 755' ]] && pass "boot1: commands run as agent (uid 1000) in /home/agent, home owned by agent" || fail "boot1: identity/cwd: $(out boot1 0 | tr '\n' '|')"
[[ "$(out boot1 1)" == ok* ]] && pass "boot1: wrote ~/persist.txt and /etc/boottest-root.txt" || fail "boot1: writes: $(jq -c '.results[1]' "$RUN/boot1.json")"
[[ "$(out boot1 2)" == "install ok installed" ]] && pass "boot1: dpkg installed agenthold-boottest" || fail "boot1: dpkg: $(jq -c '.results[2]' "$RUN/boot1.json")"
o="$(out boot1 3)"
[[ "$o" == *"boot file $NONCE"* ]] && pass "boot1: boot file from the config drive is in place" || fail "boot1: boot file: $o"
[[ "$o" == *"env=$HOST_IP"* ]] && pass "boot1: boot env reached the agent with {{server}} resolved to $HOST_IP" || fail "boot1: env: $o"
[[ "$o" == *"boottest"* && "$o" == *"nameserver $HOST_IP"* && "$o" == *"default via $HOST_IP"* ]] && pass "boot1: hostname, resolver and default route point at the gateway" || fail "boot1: net: $o"
log "Guest view (boot 1)"
out boot1 4
check_exit boot1

# --- boot 2: same disks ------------------------------------------------------------------------------------------
log "Boot 2: same disks"
boot boot2 1024 \
  -run 'cat ~/persist.txt; cat /etc/boottest-root.txt; cat /usr/local/share/boottest/pkg.txt; dpkg-query -W -f="\${Status}" agenthold-boottest' \
  -shutdown
check_conn boot2
[[ "$(out boot2 0)" == "home-$NONCE"$'\n'"root-$NONCE"$'\n'"pkg-$NONCE"$'\n'"install ok installed" ]] && pass "boot2: home file, root file and package survived shutdown and boot" || fail "boot2: persistence: $(jq -c '.results[0]' "$RUN/boot2.json")"
check_exit boot2

# --- boot 3: blank overlay --------------------------------------------------------------------------------------
log "Boot 3: overlay disk replaced by a blank one (\"Reset system\")"
rm -f "$OVERLAY"
truncate -s 4G "$OVERLAY"
boot boot3 1024 \
  -run 'cat ~/persist.txt; test -e /etc/boottest-root.txt && echo root-file-present || echo root-file-gone; dpkg-query -W -f="\${Status}" agenthold-boottest 2>/dev/null || echo package-gone' \
  -shutdown
check_conn boot3
[[ "$(out boot3 0)" == "home-$NONCE"$'\n'"root-file-gone"$'\n'"package-gone" ]] && pass "boot3: / was reset, /home/agent kept" || fail "boot3: reset: $(jq -c '.results[0]' "$RUN/boot3.json")"
check_exit boot3

# --- boot 4: shutdown from inside the guest ----------------------------------------------------------------------
log "Boot 4: power off from inside the guest (sudo kill -TERM 1)"
boot boot4 1024 -run 'sudo kill -TERM 1; echo sent'
check_conn boot4
if ((FC_EXITED)); then pass "boot4: Firecracker exited after the in-guest power off ($(( T_EXIT - T_CONN )) ms after connect)"; else fail "boot4: Firecracker still running"; fi
grep -q 'power off' "$RUN/boot4.serial" || fail "boot4: no clean power off in the serial log"

# Clean-unmount check: the overlay and home filesystems must not need recovery.
for d in "$OVERLAY" "$HOME_DISK"; do
  if e2fsck -fn "$d" >/dev/null 2>&1; then pass "$(basename "$d"): e2fsck clean after power off"; else fail "$(basename "$d"): e2fsck reports problems"; fi
done

# --- docker -------------------------------------------------------------------------------------------------------
if ((DOCKER)); then
  log "Docker in the guest (kernel: $KERNEL_KIND)"
  # Outbound NAT for this test only (agenthold itself sends guest traffic through its proxy).
  FORWARDING="$(sysctl -n net.ipv4.ip_forward)"
  sysctl -qw net.ipv4.ip_forward=1
  nft -f - <<NFT
table ip agenthold_boottest {
  chain boottest_post { type nat hook postrouting priority 100; ip saddr $GUEST_IP oifname != "$TAP" masquerade; }
  chain boottest_forward { type filter hook forward priority 0; iifname "$TAP" accept; oifname "$TAP" ct state established,related accept; }
}
NFT
  boot docker 2048 -dns "$HOST_IP:53" -cmd-timeout 15m \
    -run 'sudo apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io >/tmp/apt.log 2>&1; echo apt=$?; tail -3 /tmp/apt.log; docker --version' \
    -run 'sudo sh -c "echo \$\$ >/sys/fs/cgroup/cgroup.procs; setsid dockerd --debug >/var/log/dockerd.log 2>&1 </dev/null &"; for i in $(seq 1 120); do sudo docker version --format "{{.Server.Version}}" >/dev/null 2>&1 && break; sleep 0.5; done; sudo docker version --format "{{.Server.Version}}" || { sudo tail -80 /var/log/dockerd.log; exit 1; }; sudo docker info --format "server={{.ServerVersion}} storage={{.Driver}} cgroup={{.CgroupDriver}}/v{{.CgroupVersion}}"' \
    -run 'sudo docker run --rm hello-world 2>&1 | grep -m1 "Hello from Docker"' \
    -run 'sudo docker run --rm busybox sh -c "nslookup deb.debian.org >/dev/null && wget -qO /tmp/Release http://deb.debian.org/debian/dists/trixie/Release && head -3 /tmp/Release"' \
    -run 'sudo grep -iE "level=(warn|error)" /var/log/dockerd.log | cut -c1-300 | head -20; true' \
    -shutdown
  check_conn docker
  echo "--- docker results"
  jq -r '.results[] | "$ \(.cmd | .[0:80])...\n  exit \(.code)\n\(.stdout)\(.stderr)"' "$RUN/docker.json"
  [[ "$(out docker 1)" == *"storage=overlay2"* && "$(out docker 1)" == *"/v2"* ]] && pass "docker: overlay2 on native ext4 and cgroup v2" || fail "docker: storage/cgroup setup failed"
  [[ "$(code docker 2)" == 0 && "$(out docker 2)" == *"Hello from Docker"* ]] && pass "docker: hello-world ran" || fail "docker: hello-world failed"
  [[ "$(code docker 3)" == 0 && "$(out docker 3)" == *"Origin: Debian"* ]] && pass "docker: container reached the internet" || fail "docker: container networking failed"
  check_exit docker
fi

# Docker was running when shutdown began; check its filesystems too, plus the immutable base.
for d in "$OVERLAY" "$HOME_DISK" "$BASE"; do
  if e2fsck -fn "$d" >/dev/null 2>&1; then pass "$(basename "$d"): e2fsck clean"; else fail "$(basename "$d"): e2fsck reports problems"; fi
done
echo "$BASE_SHA  $BASE" | sha256sum -c --quiet - && pass "read-only base checksum unchanged" || fail "base image changed"

log "Serial logs and reports: $RUN"
if ((STATUS == 0)); then echo "BOOT TEST OK"; else echo "BOOT TEST FAILED"; fi
exit "$STATUS"
