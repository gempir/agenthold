# agenthold

agenthold is a self-hosted, multi-user platform for persistent AI agents. Each agent runs in its own
Firecracker microVM with persistent disks and conversations. See [PLAN.md](PLAN.md) for the design.

## Run

A Linux x86_64 or arm64 host needs `/dev/kvm`, root privileges, nftables and a reverse proxy or tunnel
for HTTPS. Configure one local administrator or an external OIDC/GitHub provider in YAML. Follow the
[install guide](https://agenthold.dev/install/) for setup, backup and operational details.

Release packaging and the new demo deployment are in progress; there is no migration from the former
container deployment. Automatic deployment remains disabled.

## Development

On macOS use an Apple M3 or newer, macOS 15 or newer, and Lima with nested virtualization. Install Go,
Node and pnpm to run the host-side tests and build the web UI. The VM installs its own Go toolchain.

```sh
make dev-vm                         # create and start the Lima VM
make runtime guest-kernel guest-base # prepare the node and guest artifacts
make dev                            # native server with Go live reload, http://localhost:8080
make test                           # Go and web tests, no KVM required
make test-kvm                       # Linux KVM tests in the dev VM
make test-guest                     # boot the guest and check its disks and tunnel
make lint
make build
```

The development server uses the embedded web build. Rebuild it with `make web` after UI changes.
See [development](https://agenthold.dev/development/) and [guest artifacts](https://agenthold.dev/guest-artifacts/)
for artifact preparation, testing and the development-only local administrator.

## Documentation

Documentation lives in [`docs/`](docs), including [authentication](https://agenthold.dev/auth/),
[sandbox isolation](https://agenthold.dev/sandboxes/), [secrets](https://agenthold.dev/egress/),
[sharing](https://agenthold.dev/sharing/) and [security](https://agenthold.dev/security/).
