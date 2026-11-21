#!/usr/bin/env bash
# Install the first release on a host so that pull.sh can take over later.
#
#   sudo scripts/deploy/bootstrap.sh SOURCE
#
# SOURCE is what pull.sh deploys from: a local release store (/srv/agenthold/releases, what
# build-local.sh publishes) or the /dl/ root of an agenthold that deployed one
# (https://demo.agenthold.dev/dl/). bootstrap.sh installs pull.sh, build-local.sh and the systemd
# units, then runs pull.sh --install, which verifies the channel's release for this architecture,
# places its binary under /opt/agenthold/releases/<commit>/ and its kernel and base image in the data
# directory, and points /opt/agenthold/current at it. It does not write /etc/agenthold, start or
# enable anything.
set -euo pipefail
umask 022

ROOT="${AGENTHOLD_DEPLOY_ROOT:-/opt/agenthold}"
DATA_DIR="${AGENTHOLD_DEPLOY_DATA_DIR:-/var/lib/agenthold}"
LIB_DIR="${AGENTHOLD_DEPLOY_LIB_DIR:-/usr/local/lib/agenthold}"
UNIT_DIR="${AGENTHOLD_DEPLOY_UNIT_DIR:-/etc/systemd/system}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNITS="agenthold.service agenthold-node.service agenthold-pull.service agenthold-pull.timer agenthold-cloudflared.service agenthold-build.service agenthold-build.timer agenthold-build.slice"
[[ $# -eq 1 ]] || { echo 'usage: bootstrap.sh SOURCE (a release store directory or a /dl/ root URL ending in /)' >&2; exit 2; }
SOURCE="$1"
[[ "$SOURCE" == https://*/ || -d "$SOURCE" ]] || { echo "$SOURCE is neither a directory nor an https /dl/ root ending in /" >&2; exit 2; }
[[ "$SOURCE" == https://* ]] || SOURCE="$(cd "$SOURCE" && pwd)"
[[ "$ROOT" == /* && "$DATA_DIR" == /* && "$LIB_DIR" == /* && "$UNIT_DIR" == /* ]] || { echo 'paths must be absolute' >&2; exit 2; }
for tool in jq zstd sha256sum curl flock; do command -v "$tool" >/dev/null || { echo "$tool is required (pull.sh needs it too)" >&2; exit 1; }; done
if [[ -e "$ROOT/current" || -L "$ROOT/current" ]]; then echo "$ROOT/current already exists; use pull.sh to update" >&2; exit 1; fi

install -d -m 0755 "$ROOT" "$ROOT/releases"
install -d -m 0700 "$DATA_DIR"
AGENTHOLD_DEPLOY_SOURCE="$SOURCE" AGENTHOLD_DEPLOY_ROOT="$ROOT" AGENTHOLD_DEPLOY_DATA_DIR="$DATA_DIR" bash "$HERE/pull.sh" --install

install -d -m 0755 "$LIB_DIR" "$UNIT_DIR"
install -m 0755 "$HERE/pull.sh" "$LIB_DIR/pull.sh"
install -m 0755 "$HERE/build-local.sh" "$LIB_DIR/build-local.sh"
for unit in $UNITS; do
  install -m 0644 "$HERE/$unit" "$UNIT_DIR/$unit"
done
echo 'Next: write /etc/agenthold/agenthold.yaml and /etc/agenthold/pull.conf, then: systemctl daemon-reload && systemctl enable --now agenthold'
echo '(an edge node: /etc/agenthold/node.yaml, agenthold node --join <token> once, then enable agenthold-node; see the deploy docs)'
