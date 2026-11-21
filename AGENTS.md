# Ownership

The codebase is yours.
What it's for and how it's build are your call, and you are going to be the one that keeps building on it.

# Demo

The demo (https://demo.agenthold.dev) runs on the Mac mini: `ssh scarif.local`, then the Lima VM `agenthold`
(`/opt/homebrew/bin/limactl shell agenthold -- <cmd>`, has /dev/kvm, so you can experiment with Firecracker there).
GitHub Actions cannot reach it. The VM's `agenthold-build.timer` tests each new main commit, builds the arm64
guest kernel and base image (assets) natively and publishes the release into `/srv/agenthold/releases`;
`agenthold-pull.timer` switches to it (see the docs deploy page). Release binaries for amd64 and arm64 come from
GitHub releases (`release.yml`; until that workflow is in the repo, both builders run with
`AGENTHOLD_BUILD_RELEASES=local` and build the binary themselves); kernels and base images are never on GitHub. The controller advertises its node
port as `192.168.178.100:8091` (Lima forwards 8091 to the LAN).

# korriban (amd64 edge node)

`korriban` is an edge node of the demo: `ssh gempir@192.168.178.153`, bare-metal x86_64 Arch Linux (Ryzen 7
9800X3D, 16 CPUs, 60 GB RAM, /dev/kvm, passwordless sudo). It runs `agenthold node` on the host
(`agenthold-node.service`, config `/etc/agenthold/node.yaml`, data `/var/lib/agenthold`, joined state in
`/var/lib/agenthold/node/state.json`) and builds its own amd64 assets, because the arm64 controller cannot:

- Build container `agenthold-build` (Ubuntu 24.04, `systemd-nspawn@agenthold-build`, config
  `/etc/systemd/nspawn/agenthold-build.nspawn`; `sudo machinectl shell agenthold-build` or
  `sudo systemd-run -M agenthold-build --pipe --wait -q <cmd>`). Inside, `agenthold-build.timer` runs
  `build-local.sh` with `AGENTHOLD_BUILD_FOLLOW=https://demo.agenthold.dev` (it builds the commit the demo runs)
  and publishes into the host's `/srv/agenthold/releases` (bind-mounted). Logs: `/var/log/agenthold-build/` in
  the container.
- On the host, `agenthold-pull.timer` (`/etc/agenthold/pull.conf`: `mode=node`) deploys what the container
  published and checks that the node reconnected.
- So korriban updates itself after the demo did. The deploy scripts and units are not updated by a deploy:
  after changing `scripts/deploy`, install them on the host (`pull.sh`, units) and in the container
  (`build-local.sh`, build units) by hand.
- KVM experiments on korriban: in the container, under `flock /tmp/agenthold-kvm.lock` (the builder's KVM lock).
