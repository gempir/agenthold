#!/bin/bash
# Provisions the base image. Runs as root inside the image's root filesystem after the Debian
# packages from packages.txt are installed by scripts/guest/build-rootfs.sh in a chroot.
#
# Expects this directory's files in the same directory as the script. If /tmp/node.tar.xz and
# /tmp/agy.tar.gz exist they are used (the rootfs build downloads them on the host); otherwise they
# are downloaded here. Either way they are checked against versions.env.
set -euxo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=versions.env
. "$here/versions.env"
export DEBIAN_FRONTEND=noninteractive

case "$(dpkg --print-architecture)" in
  arm64) node_arch=arm64; node_sha="$NODE_SHA256_ARM64"; agy_path=linux-arm/cli_linux_arm64.tar.gz; agy_sha="$AGY_SHA256_ARM64" ;;
  amd64) node_arch=x64; node_sha="$NODE_SHA256_X64"; agy_path=linux-x64/cli_linux_x64.tar.gz; agy_sha="$AGY_SHA256_X64" ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac

# Node.js from the official binaries (Debian's is too old for current harnesses).
if [[ ! -f /tmp/node.tar.xz ]]; then
  curl -fsSL -o /tmp/node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${node_arch}.tar.xz"
fi
echo "${node_sha}  /tmp/node.tar.xz" | sha256sum -c -
tar -xJf /tmp/node.tar.xz -C /usr/local --strip-components=1 --no-same-owner \
  --exclude='*/CHANGELOG.md' --exclude='*/README.md' --exclude='*/LICENSE' --exclude='*/share/doc'
rm -f /tmp/node.tar.xz
node --version

# Freeze the entire dependency graph, including architecture-specific optional packages.
mkdir -p /opt/agenthold/tools
cp "$here/package.json" "$here/package-lock.json" /opt/agenthold/tools/
npm ci --prefix /opt/agenthold/tools --omit=dev --no-fund --no-audit --no-update-notifier
for bin in /opt/agenthold/tools/node_modules/.bin/*; do
  ln -s "$bin" "/usr/local/bin/$(basename "$bin")"
done
npm cache clean --force

# Antigravity CLI: one native binary, pinned like Node.js (install.sh would fetch the latest build
# and set up shell aliases). Root-owned, so its self-updater cannot replace it; image.env also
# turns the updater off.
if [[ ! -f /tmp/agy.tar.gz ]]; then
  curl -fsSL -o /tmp/agy.tar.gz "https://storage.googleapis.com/antigravity-public/antigravity-cli/${AGY_BUILD}/${agy_path}"
fi
echo "${agy_sha}  /tmp/agy.tar.gz" | sha256sum -c -
mkdir -p "/opt/agenthold/antigravity-cli/${AGY_VERSION}"
tar -xzf /tmp/agy.tar.gz -C "/opt/agenthold/antigravity-cli/${AGY_VERSION}" --no-same-owner antigravity
chmod 0755 "/opt/agenthold/antigravity-cli/${AGY_VERSION}/antigravity"
ln -sf "/opt/agenthold/antigravity-cli/${AGY_VERSION}/antigravity" /usr/local/bin/agy
rm -f /tmp/agy.tar.gz
# (A throwaway home: agy writes its state under ~/.gemini on any run.)
HOME="$(mktemp -d)" agy --version

ln -sf /usr/bin/fdfind /usr/local/bin/fd

# The agent account. Passwordless sudo: the VM is the isolation boundary.
id agent >/dev/null 2>&1 || useradd --create-home --home-dir /home/agent --shell /bin/bash --uid 1000 --user-group agent
echo 'agent ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/agent
chmod 0440 /etc/sudoers.d/agent
mkdir -p /home/agent/.local/bin
chown -R agent:agent /home/agent

# agenthold paths: sandboxd lives in /opt/agenthold, per-boot files go to /etc/agenthold.
mkdir -p /opt/agenthold /etc/agenthold
install -m 0644 "$here/image.env" /etc/agenthold/image.env
printf '%s\n' "$BASE_VERSION" >/etc/agenthold/base-version

# Mount points sandboxd uses in the microVM (harmless in a container). /var/lib/docker and
# /var/lib/containerd are bind-mounted from the overlay disk: overlayfs cannot be the upper layer
# of Docker's own overlay mounts.
mkdir -p /var/lib/docker /var/lib/containerd

# Runtime apt sources: the live archive, not the build snapshot.
rm -f /etc/apt/sources.list
cat >/etc/apt/sources.list.d/debian.sources <<SOURCES
Types: deb
URIs: http://deb.debian.org/debian
Suites: ${DEBIAN_SUITE} ${DEBIAN_SUITE}-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: http://deb.debian.org/debian-security
Suites: ${DEBIAN_SUITE}-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
SOURCES

# Written per boot by sandboxd in the microVM.
printf 'agent\n' >/etc/hostname
printf '127.0.0.1\tlocalhost\n127.0.1.1\tagent\n::1\tlocalhost ip6-localhost ip6-loopback\n' >/etc/hosts
: >/etc/machine-id

apt-get clean
rm -rf /var/lib/apt/lists/* /var/cache/apt/*.bin /var/cache/debconf/*-old /var/lib/dpkg/*-old \
  /root/.npm /root/.cache /tmp/* /var/tmp/* /var/log/*.log /var/log/apt/*
