#!/usr/bin/env bash
# E1 acceptance: an agent on an edge node behaves exactly like a local one.
#
#   scripts/e2e/edge.sh            (macOS: re-runs itself in the Lima VM "agenthold"; Linux: run directly)
#
# Inside the KVM-capable Linux VM it runs
#   - a controller with no local node (sandbox.driver: none), in the VM's network namespace, and
#   - an edge node with the Firecracker driver in its own network namespace "ahedge", whose only
#     route out is a veth pair to the controller's namespace plus NAT to the internet.
# (The node enters it with nsenter --net, keeping the host /sys and cgroup2 mount, plus a private
# mount namespace whose /etc/resolv.conf names a resolver reachable from there.)
# The node joins with a single-use token created through the admin API and dials the controller; the
# controller never connects to the node. Then scripts/e2e/native drives the public API: boot on the
# node, terminal, file upload, chat with the scripted harness, sleep/wake, node outage (unreachable,
# fail fast, reconnect), system reset, and a secret whose placeholder the node's proxy replaces on
# the wire (checked against an HTTPS echo service, so the VM needs internet access).
#
# Needs: make runtime guest-kernel guest-base (build/runtime, build/guest/<arch>). KEEP=1 keeps the
# work directory and logs. SECRET_HOST overrides the echo service (default postman-echo.com).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [[ "$(uname -s)" == "Darwin" ]]; then
  command -v limactl >/dev/null || { echo "limactl not found: brew install lima" >&2; exit 1; }
  make -C "$REPO_ROOT" dev-vm >/dev/null
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- env "KEEP=${KEEP:-}" "SECRET_HOST=${SECRET_HOST:-}" bash "$REPO_ROOT/scripts/e2e/edge.sh" "$@"
fi
if [[ $(id -u) -ne 0 ]]; then
  exec sudo env "PATH=$PATH" "KEEP=${KEEP:-}" "SECRET_HOST=${SECRET_HOST:-}" bash "$0" "$@"
fi
cd "$REPO_ROOT"
test -c /dev/kvm || { echo "/dev/kvm is required" >&2; exit 1; }
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; *) echo "unsupported architecture" >&2; exit 1 ;; esac
BASE=build/guest/$ARCH/base-$ARCH.ext4
KERNEL=build/guest/$ARCH/vmlinux-6.1.188-agenthold
for f in "$BASE" "$KERNEL" build/runtime/firecracker build/runtime/jailer; do
  test -f "$f" || { echo "missing $f: run make runtime guest-kernel guest-base" >&2; exit 1; }
done
SECRET_HOST=${SECRET_HOST:-postman-echo.com}

NS=ahedge
HOST_IF=e2eh0 NODE_IF=e2en0 # not "ah*": that prefix is reserved for guest taps
HOST_IP=10.250.13.1 NODE_IP=10.250.13.2
WORK=$(mktemp -d /var/tmp/ahe-XXXXXX) # short: jailer socket paths are limited
chmod 755 "$WORK"
CONTROLLER_PID= NODE_PID=

cleanup() {
  local code=$?
  # node.pid names the node restarted by node-up.sh, which is not a child of this shell.
  local restarted
  restarted=$(cat "$WORK/node.pid" 2>/dev/null || true)
  for pid in $NODE_PID $restarted $CONTROLLER_PID; do kill "$pid" 2>/dev/null || true; done
  for pid in $NODE_PID $CONTROLLER_PID; do wait "$pid" 2>/dev/null || true; done
  while [[ -n "$restarted" ]] && kill -0 "$restarted" 2>/dev/null; do sleep 0.2; done
  pkill -f -- "--chroot-base-dir $WORK/" 2>/dev/null || true
  pkill -f -- "$WORK/n/jailer" 2>/dev/null || true
  ip netns del "$NS" 2>/dev/null || true
  ip link del "$HOST_IF" 2>/dev/null || true
  nft delete table ip ahedge 2>/dev/null || true
  if ((code != 0)); then
    echo "--- controller log (tail)"; tail -n 40 "$WORK/controller.log" 2>/dev/null || true
    echo "--- node log (tail)"; tail -n 60 "$WORK/node.log" 2>/dev/null || true
  fi
  if [[ -n "${KEEP:-}" ]]; then
    echo "kept $WORK"
  else
    rm -rf --one-file-system -- "$WORK" 2>/dev/null || true
  fi
  exit "$code"
}
trap cleanup EXIT

echo "== build ($ARCH)"
export CGO_ENABLED=0 GOFLAGS=-buildvcs=false
go build -o "$WORK/bin/agenthold" ./cmd/agenthold
go build -o "$WORK/bin/fakeagent" ./internal/harness/fakeagent
go build -o "$WORK/bin/smoke" ./scripts/e2e/native
mkdir -p "$WORK/fixtures"
cp --sparse=always "$BASE" "$WORK/fixtures/base.ext4"
cp "$KERNEL" "$WORK/fixtures/kernel"
cp build/runtime/firecracker build/runtime/jailer "$WORK/fixtures/"
chmod 644 "$WORK/fixtures/base.ext4" "$WORK/fixtures/kernel"
chmod 755 "$WORK/fixtures/firecracker" "$WORK/fixtures/jailer"

echo "== network namespace for the node"
ip netns add "$NS"
ip link add "$HOST_IF" type veth peer name "$NODE_IF"
ip link set "$NODE_IF" netns "$NS"
ip addr add "$HOST_IP/30" dev "$HOST_IF"
ip link set "$HOST_IF" up
ip -n "$NS" addr add "$NODE_IP/30" dev "$NODE_IF"
ip -n "$NS" link set lo up
ip -n "$NS" link set "$NODE_IF" up
ip -n "$NS" route add default via "$HOST_IP"
sysctl -qw net.ipv4.ip_forward=1
UPLINK=$(ip -4 route show default | awk 'NR == 1 {print $5}')
nft -f - <<EOF
table ip ahedge {
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr $NODE_IP oifname "$UPLINK" masquerade
  }
}
EOF
DNS=$(resolvectl status 2>/dev/null | awk '/Current DNS Server/ && !done {print $4; done = 1}' || true)
DNS=${DNS:-1.1.1.1}
echo "nameserver $DNS" >"$WORK/resolv.conf"
# innode.sh runs a command as the node sees the world: its own network and resolver.
cat >"$WORK/innode.sh" <<EOF
#!/bin/sh
exec nsenter --net=/run/netns/$NS unshare --mount --propagation private \
  sh -c 'mount --bind "$WORK/resolv.conf" /etc/resolv.conf && exec "\$@"' innode "\$@"
EOF
chmod +x "$WORK/innode.sh"

PASSWORD=$(head -c 18 /dev/urandom | base64 | tr -d '/+=')
HASH=$(printf '%s\n' "$PASSWORD" | "$WORK/bin/agenthold" hash-password)
mkdir -p "$WORK/c" "$WORK/n"
chmod 700 "$WORK/c" "$WORK/n"
cat >"$WORK/controller.yaml" <<EOF
listen: 127.0.0.1:18080
public_url: http://127.0.0.1:18080
data_dir: $WORK/c
test_harness: true
auth:
  local:
    username: admin
    password_hash: '$HASH'
sandbox:
  driver: none
nodes:
  listen: $HOST_IP:18091
  advertise: $HOST_IP:18091
EOF
cat >"$WORK/node.yaml" <<EOF
data_dir: $WORK/n
node:
  name: edge-e2e
sandbox:
  driver: firecracker
  network_pool: 10.215.0.0/16
  firecracker:
    binary: $WORK/fixtures/firecracker
    jailer: $WORK/fixtures/jailer
    kernel: $WORK/fixtures/kernel
    base_image: $WORK/fixtures/base.ext4
egress:
  dns_upstream: $DNS:53
EOF

echo "== controller (no local node)"
AGENTHOLD_CONFIG="$WORK/controller.yaml" "$WORK/bin/agenthold" >"$WORK/controller.log" 2>&1 &
CONTROLLER_PID=$!
for _ in $(seq 100); do curl -fsS -o /dev/null http://127.0.0.1:18080/healthz 2>/dev/null && break; sleep 0.1; done
grep -q '"no local node' "$WORK/controller.log" || { echo "controller started a local node" >&2; exit 1; }
JAR="$WORK/cookies"
curl -fsS -c "$JAR" -o /dev/null -H content-type:application/json -X POST http://127.0.0.1:18080/api/v1/auth/local \
  -d "{\"username\":\"admin\",\"password\":\"$PASSWORD\"}"
api() { curl -fsS -b "$JAR" -H content-type:application/json "$@"; }
TOKEN=$(api -X POST http://127.0.0.1:18080/api/v1/nodes/join-tokens -d '{"ttlMinutes":10}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
echo "  ok   join token created through the admin API"

start_node() {
  "$WORK/innode.sh" env AGENTHOLD_CONFIG="$WORK/node.yaml" "$WORK/bin/agenthold" node "$@" >>"$WORK/node.log" 2>&1 &
  NODE_PID=$!
  echo "$NODE_PID" >"$WORK/node.pid"
}
wait_node() {
  for _ in $(seq 600); do
    if api http://127.0.0.1:18080/api/v1/nodes | python3 -c 'import json,sys; n=json.load(sys.stdin); sys.exit(0 if any(x["name"]=="edge-e2e" and x["online"] for x in n) else 1)'; then
      return 0
    fi
    kill -0 "$(cat "$WORK/node.pid")" 2>/dev/null || { echo "node exited" >&2; return 1; }
    sleep 0.5
  done
  echo "node did not come online" >&2
  return 1
}

echo "== edge node joins"
start_node --join "$TOKEN"
wait_node
echo "  ok   node joined over pinned mutual TLS and is online"
# A second node presenting the same (used) token must be refused. It uses the fake driver so it
# does not touch the real node's firewall, runtime or ports.
mkdir -p "$WORK/r" && chmod 700 "$WORK/r"
cat >"$WORK/replay.yaml" <<EOF
data_dir: $WORK/r
sandbox:
  driver: fake
  tunnel_listen: 127.0.0.1:18190
EOF
if AGENTHOLD_CONFIG="$WORK/replay.yaml" timeout 60 "$WORK/bin/agenthold" node --join "$TOKEN" --name replay >"$WORK/replay.log" 2>&1; then
  echo "a used join token was accepted twice" >&2
  exit 1
fi
grep -q "join refused" "$WORK/replay.log" || { cat "$WORK/replay.log" >&2; exit 1; }
echo "  ok   the used join token is refused for a second node"

cat >"$WORK/node-down.sh" <<EOF
#!/bin/sh
pid=\$(cat "$WORK/node.pid"); kill -TERM "\$pid"; while kill -0 "\$pid" 2>/dev/null; do sleep 0.2; done
EOF
cat >"$WORK/node-up.sh" <<EOF
#!/bin/sh
"$WORK/innode.sh" env AGENTHOLD_CONFIG="$WORK/node.yaml" "$WORK/bin/agenthold" node >>"$WORK/node.log" 2>&1 &
echo \$! >"$WORK/node.pid"
EOF
chmod +x "$WORK/node-down.sh" "$WORK/node-up.sh"

echo "== agent on the edge node (scripts/e2e/native)"
AGENTHOLD_SMOKE_PASSWORD="$PASSWORD" "$WORK/bin/smoke" -base http://127.0.0.1:18080 -fakeagent "$WORK/bin/fakeagent" \
  -node edge-e2e -secret-host "$SECRET_HOST" -node-down "$WORK/node-down.sh" -node-up "$WORK/node-up.sh"
NODE_PID=$(cat "$WORK/node.pid")
for i in $(seq 120); do
  api http://127.0.0.1:18080/api/v1/agents | python3 -c 'import json,sys; sys.exit(1 if json.load(sys.stdin) else 0)' && break
  ((i < 120)) || { echo "agent was not deleted from the node" >&2; exit 1; }
  sleep 0.5
done
test -z "$(ls -A "$WORK/n/agents" 2>/dev/null)" || { echo "agent disks left on the node" >&2; ls -la "$WORK/n/agents" >&2; exit 1; }
echo "PASS: agent deleted from the node"
echo "PASS: edge node e2e"
