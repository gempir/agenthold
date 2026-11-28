# Ownership

The codebase is yours.
What it's for and how it's build are your call, and you are going to be the one that keeps building on it.

You work autonomously, committing, pushing yourself. Multiple agents might be working on the same thing, feel free to use worktrees if it helps.

# Demo

The demo (https://demo.agenthold.dev) runs on the Mac mini: `ssh scarif.local`, then the Lima VM `agenthold`
(`/opt/homebrew/bin/limactl shell agenthold -- <cmd>`, has /dev/kvm, so you can experiment with Firecracker there).
GitHub Actions cannot reach it. The VM's `agenthold-build.timer` tests each new non-docs `main` commit, builds the
arm64 release binary (`AGENTHOLD_BUILD_RELEASES=local`, the default for `main` commits) and arm64 guest kernel, base
image and harnesses layer natively and publishes the release into `/srv/agenthold/releases`; `agenthold-pull.timer`
switches to it (see the docs deploy page). Official GitHub releases (`release.yml`) are semver tags only (`v0.1.0`
and later, publishing amd64 and arm64 binaries); `sha-*` GitHub releases are no longer used. GitHub CI (`ci.yml`)
still builds and smoke-tests both amd64 and arm64 release binaries as workflow artifacts (`release-amd64`,
`release-arm64`) on `main` and PRs, and `build-local.sh --tag v0.1.0` (or following a controller running a `v*` tag)
downloads binaries from GitHub Releases by default; kernels, base images and harness layers are never on GitHub. The
controller advertises its node port as `192.168.178.100:8091` (Lima forwards 8091 to the LAN).

The VM's 500 GiB sparse disk lives on the external HDD at
`/Volumes/Terramaster/agenthold/disk`, symlinked from `~/.lima/agenthold/disk` on scarif.
Keep Terramaster mounted before starting the VM. The read-only guest mount
`/Volumes/Terramaster/agenthold/host-storage` lets the puller check the backing HDD's free space.

# korriban (amd64 edge node)

`korriban` is an edge node of the demo: `ssh gempir@192.168.178.153`, bare-metal x86_64 Arch Linux (Ryzen 7
9800X3D, 16 CPUs, 60 GB RAM, /dev/kvm, passwordless sudo). It runs `agenthold node` on the host
(`agenthold-node.service`, config `/etc/agenthold/node.yaml`, data `/var/lib/agenthold`, joined state in
`/var/lib/agenthold/node/state.json`) and builds its own amd64 binary and assets, because the arm64 controller
cannot:

- Build container `agenthold-build` (Ubuntu 24.04, `systemd-nspawn@agenthold-build`, config
  `/etc/systemd/nspawn/agenthold-build.nspawn`; `sudo machinectl shell agenthold-build` or
  `sudo systemd-run -M agenthold-build --pipe --wait -q <cmd>`). Inside, `agenthold-build.timer` runs
  `build-local.sh` with `AGENTHOLD_BUILD_FOLLOW=https://demo.agenthold.dev` (it builds the release the demo runs:
  locally when the demo runs a `sha-<commit>` `main` build, or downloading from GitHub Releases when it runs a
  semver `v*` tag) and publishes into the host's `/srv/agenthold/releases` (bind-mounted). Logs:
  `/var/log/agenthold-build/` in the container.
- On the host, `agenthold-pull.timer` (`/etc/agenthold/pull.conf`: `mode=node`) deploys what the container
  published and checks that the node reconnected.
- So korriban updates itself after the demo did. The deploy scripts and units are not updated by a deploy:
  after changing `scripts/deploy`, install them on the host (`pull.sh`, units) and in the container
  (`build-local.sh`, build units) by hand.
- KVM experiments on korriban: in the container, under `flock /tmp/agenthold-kvm.lock` (the builder's KVM lock).
