#!/usr/bin/env bash
# N0 spike: prove Firecracker runs inside the Lima dev VM.
#
# Run from macOS:   scripts/spike/firecracker.sh
#
# It starts the Lima VM "agenthold" (make dev-vm), re-runs itself inside it as root, downloads a
# pinned Firecracker release, a pinned guest kernel and a throwaway Ubuntu rootfs, boots a microVM
# through the Firecracker API socket with a tap device (/30), and checks that
#   (a) the guest prints `uname -a` on the serial console, and
#   (b) the guest opens a TCP connection to a listener on the Lima host over the tap and gets a
#       random per-run token back.
# Then it shuts the VM down and removes the tap. Exits non-zero if either check fails.
# Safe to re-run; downloads and the rootfs are cached in /var/cache/agenthold-spike inside the VM.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# --- on macOS: make sure the VM is up, then run this same script inside it ------------------------
if [[ "$(uname -s)" == "Darwin" ]]; then
  command -v limactl >/dev/null || { echo "limactl not found: brew install lima" >&2; exit 1; }
  make -C "$REPO_ROOT" dev-vm
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- bash "$REPO_ROOT/scripts/spike/firecracker.sh" "$@"
fi

# --- inside the VM ---------------------------------------------------------------------------------
[[ "$(uname -m)" == "aarch64" ]] || { echo "this spike pins aarch64 artifacts" >&2; exit 1; }
if [[ $EUID -ne 0 ]]; then
  exec sudo -E bash "${BASH_SOURCE[0]}" "$@"
fi

# Pinned artifacts. The Firecracker release publishes its own sha256 (checked against the pin below);
# the CI kernel/rootfs publish none, so their hashes are pinned here.
FC_VERSION="v1.18.0"
FC_URL="https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-aarch64.tgz"
FC_SHA256="2a6130ff0338901ff9d7fbd73ce046c6b1c6123889dc84a34d678aa53bd0c0c2"

CI_PREFIX="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20261111-778a26c0594c-0/aarch64"
KERNEL_NAME="vmlinux-6.1.188"
KERNEL_SHA256="7e7f3c7073c36e334e33e8a74ae744cea06c914f0ab9303b9e557e4106840da4"
KERNEL_CONFIG_SHA256="d52cc45ca86f338ffbe456140b61fe79cd391ad8b4a7f38bc8ec24ceefb787d9"
ROOTFS_NAME="ubuntu-24.04.squashfs"
ROOTFS_SHA256="fc6e9a413344b9ab7e3415598988a9a4a7fd8bd62fc49cf8ce5225ca3b7039b9"

TAP="fcspike0"
HOST_IP="172.31.255.1"
GUEST_IP="172.31.255.2"
GUEST_MAC="06:00:AC:1F:FF:02"

CACHE="/var/cache/agenthold-spike"
RUN="$(mktemp -d /run/agenthold-spike.XXXXXX)"
API="$RUN/fc.sock"
SERIAL="$RUN/serial.log"
FC_PID=""
LISTENER_PID=""

log() { printf '\n==> %s\n' "$*"; }
now() { date +%s.%N; }
fail() { echo "FAIL: $*" >&2; STATUS=1; }
STATUS=0

cleanup() {
  [[ -n "$FC_PID" ]] && kill "$FC_PID" 2>/dev/null || true
  [[ -n "$LISTENER_PID" ]] && kill "$LISTENER_PID" 2>/dev/null || true
  ip link del "$TAP" 2>/dev/null || true
  # Keep the serial log of the last run for debugging, drop everything else.
  [[ -f "$SERIAL" ]] && cp "$SERIAL" "$CACHE/last-serial.log" || true
  rm -rf "$RUN"
}
trap cleanup EXIT

fetch() { # url dest sha256
  local url="$1" dest="$2" sha="$3"
  if [[ -f "$dest" ]] && echo "$sha  $dest" | sha256sum -c --quiet - 2>/dev/null; then return; fi
  echo "downloading $url"
  curl -fsSL --retry 3 -o "$dest.part" "$url"
  echo "$sha  $dest.part" | sha256sum -c --quiet - || { echo "sha256 mismatch for $url" >&2; rm -f "$dest.part"; exit 1; }
  mv "$dest.part" "$dest"
}

api() { # method path json
  local code data=()
  [[ -n "${3:-}" ]] && data=(--data "$3")
  code="$(curl -s -o "$RUN/api.out" -w '%{http_code}' --unix-socket "$API" -X "$1" \
    -H 'Content-Type: application/json' "${data[@]}" "http://localhost$2")"
  if [[ "$code" != 2* ]]; then
    echo "firecracker API $1 $2 -> HTTP $code: $(cat "$RUN/api.out")" >&2
    exit 1
  fi
}

mkdir -p "$CACHE"

# --- stale state from an interrupted run ----------------------------------------------------------
ip link del "$TAP" 2>/dev/null || true

# --- artifacts -------------------------------------------------------------------------------------
log "Fetching pinned artifacts"
fetch "$FC_URL" "$CACHE/firecracker-$FC_VERSION-aarch64.tgz" "$FC_SHA256"
# Also verify against the checksum the release itself publishes.
published="$(curl -fsSL "$FC_URL.sha256.txt" | awk '{print $1}')"
[[ "$published" == "$FC_SHA256" ]] || { echo "release sha256 ($published) differs from the pin ($FC_SHA256)" >&2; exit 1; }
FC_BIN="$CACHE/firecracker-$FC_VERSION"
if [[ ! -x "$FC_BIN" ]]; then
  tar -xzf "$CACHE/firecracker-$FC_VERSION-aarch64.tgz" -C "$CACHE" --no-same-owner \
    --strip-components=1 "release-$FC_VERSION-aarch64/firecracker-$FC_VERSION-aarch64"
  mv "$CACHE/firecracker-$FC_VERSION-aarch64" "$FC_BIN"
fi
fetch "$CI_PREFIX/$KERNEL_NAME" "$CACHE/$KERNEL_NAME" "$KERNEL_SHA256"
fetch "$CI_PREFIX/$KERNEL_NAME.config" "$CACHE/$KERNEL_NAME.config" "$KERNEL_CONFIG_SHA256"
fetch "$CI_PREFIX/$ROOTFS_NAME" "$CACHE/$ROOTFS_NAME" "$ROOTFS_SHA256"
"$FC_BIN" --version | head -1

# --- throwaway rootfs: squashfs -> ext4 with our own /spike-init as init --------------------------------
write_init() {
  cat >"$1" <<'INIT'
#!/bin/bash
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
mount -t proc proc /proc
mount -t sysfs sysfs /sys
cmd="$(cat /proc/cmdline)"
host="$(tr ' ' '\n' </proc/cmdline | sed -n 's/^spike.host=//p')"
port="$(tr ' ' '\n' </proc/cmdline | sed -n 's/^spike.port=//p')"
echo "SPIKE-INIT uptime=$(cut -d' ' -f1 /proc/uptime)"
echo "SPIKE-UNAME $(uname -a)"
echo "SPIKE-IP $(ip -4 -o addr show dev eth0 | awk '{print $4}')"
echo "SPIKE-FS $(grep -oE '(overlay|ext4|xfs|squashfs|fuse|tmpfs)$' /proc/filesystems | sort -u | tr '\n' ' ')"
ok=0
for _ in $(seq 1 20); do
  if exec 3<>"/dev/tcp/$host/$port"; then
    echo "guest-hello $(uname -n)" >&3
    IFS= read -r -t 10 line <&3
    echo "SPIKE-NET $line"
    exec 3>&-
    ok=1
    break
  fi
  sleep 0.25
done
[ "$ok" = 1 ] || echo "SPIKE-NET-FAIL could not reach $host:$port"
sync
echo "SPIKE-POWEROFF"
poweroff -f
echo o >/proc/sysrq-trigger
sleep 5
INIT
  chmod 0755 "$1"
}

# The cache key covers the squashfs and the init script, so editing the init rebuilds the image.
write_init "$RUN/spike-init"
ROOTFS_BASE="$CACHE/rootfs-${ROOTFS_SHA256:0:12}-$(sha256sum <"$RUN/spike-init" | cut -c1-12).ext4"
if [[ ! -f "$ROOTFS_BASE" ]]; then
  log "Building throwaway ext4 rootfs from $ROOTFS_NAME"
  tree="$(mktemp -d "$CACHE/tree.XXXXXX")"
  unsquashfs -no-progress -f -d "$tree" "$CACHE/$ROOTFS_NAME" >/dev/null
  cp -p "$RUN/spike-init" "$tree/spike-init"
  size_mb=$(( $(du -sm "$tree" | cut -f1) * 5 / 4 + 64 ))
  truncate -s "${size_mb}M" "$ROOTFS_BASE.part"
  mkfs.ext4 -q -F -d "$tree" "$ROOTFS_BASE.part"
  mv "$ROOTFS_BASE.part" "$ROOTFS_BASE"
  rm -rf "$tree"
fi
cp --sparse=always "$ROOTFS_BASE" "$RUN/rootfs.ext4"

# --- host side: tap with a /30 and a TCP listener on it --------------------------------------------------
log "Creating $TAP ($HOST_IP/30, guest $GUEST_IP)"
ip tuntap add dev "$TAP" mode tap
ip addr add "$HOST_IP/30" dev "$TAP"
ip link set "$TAP" up

TOKEN="$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
cat >"$RUN/listener.py" <<'PY'
import json, socket, sys
host, token, out = sys.argv[1], sys.argv[2], sys.argv[3]
srv = socket.socket()
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind((host, 0))
srv.listen(1)
open(out + ".port", "w").write(str(srv.getsockname()[1]))
srv.settimeout(120)
try:
    conn, peer = srv.accept()
except socket.timeout:
    json.dump({"error": "no connection"}, open(out, "w"))
    sys.exit(1)
conn.settimeout(15)
conn.sendall((token + "\n").encode())
hello = conn.makefile().readline().strip()
json.dump({"peer": peer[0], "hello": hello}, open(out, "w"))
PY
python3 -I "$RUN/listener.py" "$HOST_IP" "$TOKEN" "$RUN/listener.json" &
LISTENER_PID=$!
for _ in $(seq 1 50); do [[ -f "$RUN/listener.json.port" ]] && break; sleep 0.1; done
PORT="$(cat "$RUN/listener.json.port")"
echo "host listener on $HOST_IP:$PORT"

# --- boot the microVM -----------------------------------------------------------------------------------
log "Starting Firecracker"
: >"$SERIAL"
"$FC_BIN" --api-sock "$API" </dev/null >"$SERIAL" 2>&1 &
FC_PID=$!
for _ in $(seq 1 50); do [[ -S "$API" ]] && break; sleep 0.1; done
[[ -S "$API" ]] || { echo "firecracker API socket did not appear" >&2; cat "$SERIAL" >&2; exit 1; }

BOOT_ARGS="keep_bootcon console=ttyS0 reboot=k panic=1 root=/dev/vda rw init=/spike-init"
BOOT_ARGS+=" ip=$GUEST_IP::$HOST_IP:255.255.255.252::eth0:off"
BOOT_ARGS+=" spike.host=$HOST_IP spike.port=$PORT"

api PUT /machine-config '{"vcpu_count": 2, "mem_size_mib": 512}'
api PUT /boot-source "{\"kernel_image_path\": \"$CACHE/$KERNEL_NAME\", \"boot_args\": \"$BOOT_ARGS\"}"
api PUT /drives/rootfs "{\"drive_id\": \"rootfs\", \"path_on_host\": \"$RUN/rootfs.ext4\", \"is_root_device\": true, \"is_read_only\": false}"
api PUT /network-interfaces/net1 "{\"iface_id\": \"net1\", \"guest_mac\": \"$GUEST_MAC\", \"host_dev_name\": \"$TAP\"}"

t_start="$(now)"
api PUT /actions '{"action_type": "InstanceStart"}'

t_init=""
t_net=""
deadline=$(( $(date +%s) + 90 ))
while kill -0 "$FC_PID" 2>/dev/null && (( $(date +%s) < deadline )); do
  [[ -z "$t_init" ]] && grep -q '^SPIKE-INIT' "$SERIAL" && t_init="$(now)"
  [[ -z "$t_net" ]] && grep -q '^SPIKE-NET' "$SERIAL" && t_net="$(now)"
  sleep 0.02
done
if kill -0 "$FC_PID" 2>/dev/null; then
  fail "guest did not power off within 90s"
  kill "$FC_PID" 2>/dev/null || true
fi
wait "$FC_PID" 2>/dev/null || true
FC_PID=""
wait "$LISTENER_PID" 2>/dev/null || true
LISTENER_PID=""
t_end="$(now)"

# --- results ------------------------------------------------------------------------------------------
log "Guest serial output (SPIKE lines)"
grep '^SPIKE-' "$SERIAL" | tr -d '\r' || true

log "Checks"
if grep -q '^SPIKE-UNAME Linux .* aarch64' "$SERIAL"; then
  echo "PASS (a) output produced inside the guest"
else
  fail "(a) no 'uname -a' output from the guest"
fi

peer_json="$(cat "$RUN/listener.json" 2>/dev/null || echo '{}')"
peer="$(jq -r '.peer // empty' <<<"$peer_json")"
hello="$(jq -r '.hello // empty' <<<"$peer_json")"
if [[ "$peer" == "$GUEST_IP" && "$hello" == guest-hello* ]] && grep -q "^SPIKE-NET $TOKEN" "$SERIAL"; then
  echo "PASS (b) guest $peer connected to $HOST_IP:$PORT over $TAP and received the token ($hello)"
else
  fail "(b) guest->host TCP check: listener saw '$peer_json', serial has $(grep '^SPIKE-NET' "$SERIAL" || echo no SPIKE-NET line)"
fi

if [[ -n "$t_init" ]]; then
  uptime_at_init="$(sed -n 's/^SPIKE-INIT uptime=//p' "$SERIAL" | tr -d '\r')"
  awk -v s="$t_start" -v i="$t_init" -v n="${t_net:-$t_end}" -v e="$t_end" -v u="$uptime_at_init" 'BEGIN {
    printf "timing: InstanceStart -> guest init %.2fs (guest kernel uptime at init %ss), -> TCP done %.2fs, -> VM exit %.2fs\n", i - s, u, n - s, e - s }'
fi

log "Cleanup"
# the EXIT trap removes the tap and the run directory
if (( STATUS == 0 )); then echo "SPIKE OK"; else echo "SPIKE FAILED (serial log: $CACHE/last-serial.log)"; fi
exit "$STATUS"
