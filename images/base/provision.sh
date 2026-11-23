#!/bin/bash
# Provisions the base image. Runs as root inside the image's root filesystem after the Debian
# packages from packages.txt are installed by scripts/guest/build-rootfs.sh in a chroot.
#
# Expects the files staged by the rootfs builder in the same directory as the script.
set -euxo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=versions.env
. "$here/versions.env"
export DEBIAN_FRONTEND=noninteractive

install -m 0755 "$here/bun" /usr/local/bin/bun
# Keep npm package entry points with an env node shebang runnable with Bun.
ln -sf bun /usr/local/bin/node
ln -sf bun /usr/local/bin/bunx
test "$(bun --version)" = "$BUN_VERSION"

# Harnesses are delivered as their own read-only layer (images/harnesses/) mounted over the base.
mkdir -p /opt/agenthold/tools

ln -sf /usr/bin/fdfind /usr/local/bin/fd

# Go, for agents that build Go projects (agenthold develops itself in agents). GOTOOLCHAIN stays
# auto, so a go.mod that asks for a newer toolchain downloads it through the egress proxy.
rm -rf /usr/local/go
tar -C /usr/local -xzf "$here/go.tar.gz"
ln -sf /usr/local/go/bin/go /usr/local/bin/go
ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
test "$(go env GOVERSION)" = "go$GO_VERSION"

# GitHub CLI (Debian package): the egress proxy authenticates its API calls and git over HTTPS.
gh --version >/dev/null

# Git defaults: system-level so ~/.gitconfig and repo .git/config still win.
git lfs install --system --skip-repo
git config --system init.defaultBranch main
git config --system push.autoSetupRemote true
git config --system advice.detachedHead false
git config --system core.autocrlf input
git config --system safe.directory '*'
git config --system include.path /etc/agenthold/gitconfig
git config --system --add include.path /home/agent/.config/agenthold/gitconfig

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
