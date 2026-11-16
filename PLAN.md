# agenthold: project plan (v2, native + Firecracker)

Source of truth for what we are building and in which order. Written for coding agents that execute
one milestone at a time. The v1 plan (Docker, compose, SQLite only) is in git history; v1 is fully
built and is the starting point of this rework.

- Go module `github.com/gempir/agenthold`. Binaries: `agenthold` (server, also node) and `sandboxd`
  (in-guest daemon, embedded in `agenthold`).
- License: MIT.

## 1. What we are building

A self-hostable, multi-user platform for running many persistent AI agents. Each agent has its own
Firecracker microVM (its own "computer") with a persistent disk, tools and conversation history.

We do not build an agent harness. Claude Code, Codex and OpenCode run inside the VMs and are driven
through ACP. We build everything around them.

Deployment is **one static binary and one YAML file**. No Docker, no compose, no container runtime.
The database is SQLite by default or a Postgres the operator brings. Identity is an OIDC provider the
operator brings (or GitHub OAuth, or one local admin from the YAML). TLS is the operator's reverse
proxy or tunnel.

### Non-goals

- Our own agent loop, tools or model client.
- Container-based sandboxes of any kind. There is no fallback driver: a host without `/dev/kvm`
  cannot run agents (it can still be a controller once edge nodes exist).
- MySQL. Kubernetes. Agent migration between nodes.
- Built-in TLS/ACME. Password accounts beyond the single YAML admin.
- Per-agent OCI images. Network allowlists and request logs. Workspaces and role matrices.

## 2. Decisions

Decided with the project owner on 2026-11-15. Do not revisit while executing a milestone; if one turns
out unworkable, stop and report.

| Area | Decision |
|---|---|
| Isolation | Firecracker microVMs only. Docker driver, Sysbox/gVisor options and docs are deleted. The `fake` driver stays for tests and UI work. |
| Why | No Docker dependency, real isolation, a full machine per agent (own kernel, Docker-in-agent, enforced disk size), and later true suspend/resume. |
| Host requirement | Linux with `/dev/kvm`, x86_64 or aarch64, running as root (tap devices, nftables, jailer). |
| Binary | `agenthold` embeds `firecracker`, `jailer`, the guest kernel and `sandboxd` for its architecture and extracts them to `<data_dir>/runtime/<version>/` on start. |
| Base rootfs | One blessed base disk image per release, built from `images/base`. Too large to embed (several GB): fetched on first start from a pinned URL and verified by sha256, or pre-placed in `data_dir` for offline installs. |
| Agent disks | Shared read-only base + per-agent writable overlay disk (system changes survive sleep) + per-agent persistent `/home/agent` disk. Upgrading the base swaps it and resets the overlay; home always survives. No per-agent image; "Rebuild" becomes "Reset system". |
| Sleep | Cold shutdown and boot (same semantics as before: disk and conversation survive, processes do not). Memory snapshots are a later milestone. |
| Secrets | Unchanged: never enter the VM. A host-side transparent TLS-intercepting proxy injects them. Account-level harness logins stay. |
| Network | Every agent has open internet through the proxy. Network policy (allowlists) and the per-agent request log are removed. |
| Topology | One binary, two roles. `agenthold` alone is controller + node. Later `agenthold node --join <token>` adds a remote node that dials the controller. New agents are placed on the node with most free capacity and stay there. Node down = its agents offline. |
| Edge trust | Nodes are trusted infrastructure. The proxy runs on the node; the controller pushes the decrypted secrets of that node's agents over the node connection, held in memory only. |
| Database | SQLite (default, zero setup) or Postgres. sqlc stays, with one query set per engine. No MySQL. |
| Auth | OIDC and GitHub OAuth as before, plus `auth.local` = exactly one admin (username + password hash) in the YAML. No provider and no `auth.local` means nobody can log in; the server says so at startup. No generated credentials, no signup. |
| Ownership and sharing | Workspaces, memberships, invites and viewer/operator/admin roles are removed. A user owns agents. The owner can share an agent with other existing users (by email); a shared user can use it (chat, terminal, files), only the owner can configure, share or delete it. MCP servers, skills and secrets belong to a user. |
| Agent-to-agent | Kept. An agent may address agents its owner owns or has been shared. |
| TLS | Plain HTTP only. Docs show Caddy, nginx and cloudflared in front. |
| Development | macOS + Lima VM (`vmType: vz`, nested virtualization; OrbStack has no `/dev/kvm`). The server runs natively inside the Lima VM. |
| Demo | Mac mini `scarif.local` (M4, 24 GB): Lima VM, all-in-one, published through cloudflared. The OCI host (no KVM) is retired. No data migration. |
| Process | Straight on `main`. The old deploy workflow is disabled until the new one exists. Early development: no compatibility with v1 data, config or API is required. |

Known consequences, accepted:

- Most cheap cloud VPSes have no KVM and cannot host agents.
- The dev and demo setups run Firecracker under nested virtualization (slower than bare metal).
- Only arm64 can be tested locally; x86_64 is verified in CI (GitHub's x86 Linux runners have KVM).

## 3. Architecture

```
 Browser (React SPA)        API clients / webhooks
        | REST + WebSocket        |
        v                         v
+-----------------------------------------------------------+
| agenthold (one process, root, systemd)                    |
|                                                           |
|  controller                    node                       |
|  -----------                   ----                       |
|  HTTP API + embedded SPA       Firecracker driver         |
|  auth, sharing, tokens         (jailer, disks, tap)       |
|  session hub (ACP client)      egress proxy (TLS, inject) |
|  scheduler, triggers           DNS forwarder, nftables    |
|  secrets store                 tunnel endpoint (sandboxd) |
|  store: SQLite | Postgres                                 |
|        ^------ node interface (in-process now,            |
|                remote connection later) ------^           |
+----------------------------+------------------------------+
                             | tap device per VM
              +--------------+---------------+
              | microVM A        microVM B   |
              |  sandboxd (PID 1) ...        |
              |   |- harness (ACP on stdio)  |
              |   |- PTYs, files, MCP        |
              |  vda base (ro)  vdb overlay  |
              |  vdc /home/agent             |
              +------------------------------+
```

### 3.1 Node boundary

Everything bound to the machine that runs VMs lives behind one Go interface in `internal/node`:
sandbox lifecycle, opening a tunnel session to an agent's sandboxd, pushing the agent's secret rules
to the proxy, capacity reporting. The controller never touches the driver, the proxy or the tunnel hub
directly. Until the edge milestone the only implementation is in-process. Design every method so it
can cross a network: no shared pointers, context-cancellable, streams as `io.ReadWriteCloser`.

### 3.2 Firecracker driver

- One `firecracker` process per running agent, started through `jailer` (chroot, cgroup, uid drop).
- Configured over its API socket. Small hand-written client; no SDK dependency unless justified.
- Disks are sparse ext4 files under `<data_dir>/agents/<id>/`: `overlay.ext4`, `home.ext4`. Base is
  `<data_dir>/base/<sha>.ext4`, attached read-only to every VM.
- Network: one tap per VM with a /30 from `sandbox.network_pool`. Guest address via kernel `ip=`
  argument. Host nftables (table `agenthold`) redirects all guest TCP to the proxy and DNS to the
  forwarder, and drops everything else from guests to the host except the tunnel port. Guests cannot
  reach each other.
- Boot configuration (agent id, tunnel token, CA cert, harness files) reaches sandboxd through MMDS or
  a small config disk; no bind mounts exist any more.
- `sandboxd` is the guest's init: mounts the overlay over the base, mounts home, brings up the
  network, dials the node's tunnel endpoint at the gateway address, reaps children, powers off on
  request. Everything else it does today is unchanged.

### 3.3 Domain model

```
User (OIDC | GitHub | local admin)
 |- Agent (harness, model, resources, node)
 |   |- Share -> User
 |   |- Sandbox (state: creating | running | sleeping | error)
 |   |- Session -> Event (append-only)
 |   |- Schedule, Webhook
 |   '- attached MCP servers, skills, secrets
 |- MCP servers, Skills, Secrets, provider logins
 '- API tokens
Node (id, name, capacity, last seen)        # one row, "local", until the edge milestone
```

Authorization stays in `internal/authz`, reduced to: owner, shared user, nobody.

## 4. Technology

Unchanged from v1 unless listed: Go, chi, OpenAPI-first (`api/openapi.yaml`, oapi-codegen,
openapi-typescript), WebSocket streams, yamux tunnel, React/Vite/Tailwind SPA embedded in the binary,
XChaCha20-Poly1305 secrets, slog.

| Concern | Choice |
|---|---|
| VMM | Firecracker + jailer, pinned release, upstream static binaries |
| Guest kernel | Pinned upstream Firecracker-compatible kernel build per arch (needs virtio-blk, virtio-net, ext4, overlayfs, and what Docker-in-guest needs) |
| Base rootfs | Built in CI from `images/base` (build-time tooling is free to use containers), published as a release asset with checksum |
| Host networking | `vishvananda/netlink` for taps, `google/nftables` for rules (both already dependencies) |
| Store | `sqlc` with two engines (sqlite via `modernc.org/sqlite`, postgres via `pgx`), `goose` migrations per engine, one Go interface above both |
| Local auth | argon2id hash in YAML, `agenthold hash-password` prints one |
| Service | `agenthold install` writes a systemd unit; `agenthold` runs in the foreground otherwise |
| Dev | `deploy/dev/lima.yaml`; `make dev` starts the VM and runs the server in it with live reload |

### Conventions for executing agents

- Work lands on `main` in small commits; each milestone ends with its acceptance checks passing and
  tests covering them. Do not push unless told to.
- Tests that need KVM carry the build tag `kvm` and run inside the Lima VM (`make test-kvm`) and in
  CI on x86. Everything else uses the fake driver and must pass on macOS.
- The demo database is now live: never edit an applied migration. Add `00002_...` etc. (both engines). Editing `00001_init.sql` in place broke the first auto-deploy (`no such table: nodes`) and needed a manual DB reset.
- Change `api/openapi.yaml` first, regenerate, then implement.
- Delete what a decision removes: code, config fields, UI, docs, tests. No compatibility shims.
- No new dependency without a one-line justification in the commit message.
- Report anything that contradicts section 2 instead of working around it.

## 5. Milestones

### Step 1: native binary on Firecracker (all-in-one)

**N0. Dev environment and spike.** Lima template with nested virtualization, `make dev-vm`. Prove
inside it: Firecracker boots a pinned kernel with a throwaway rootfs, a tap device works, the guest
reaches a listener on the host. Write down exact versions and commands in `docs/` dev notes.
*Accept:* one script, run on a fresh Mac with Lima, ends with output produced inside a microVM.

**N1. Simplify the product.** Remove workspaces, memberships, invites and roles in favour of owner +
shares. Move MCP servers, skills and secrets to the user. Remove network policy and the request log
(API, store, UI, docs). Add `auth.local` and `agenthold hash-password`. Fresh squashed SQLite
migration. *Accept:* all tests green with the fake driver; a local admin logs in, creates an agent,
shares it with a second (OIDC test) user who can chat but not configure.

**N2. Guest artifacts.** Reproducible build of the base rootfs from `images/base` to ext4, kernel
fetch and pinning, sandboxd as PID 1 (overlay, home, network, tunnel dial, reaping, poweroff), boot
configuration channel. *Accept:* a hand-started Firecracker VM boots the base image and its sandboxd
connects to a test tunnel listener on the host.

**N3. Firecracker driver and node boundary.** `internal/node` interface with the in-process
implementation; `internal/sandbox/firecracker` implementing `sandbox.Driver` (jailer, disks, tap,
resources); egress proxy and nftables moved to host scope with per-tap attribution; "Reset system".
Delete the Docker driver. *Accept (in Lima, tag `kvm`):* the old e2e flows pass: create agent, chat
with the test harness, terminal, files, sleep and wake with files kept, secret placeholder replaced on
the wire and not readable in the guest, a guest cannot reach another guest or host services.

**N4. Packaging and demo.** Embed and extract runtime artifacts, base image download with checksum,
`agenthold install`, startup preflight (KVM, root, nftables) with clear errors, release workflow
building linux/amd64 and linux/arm64 binaries plus rootfs assets, CI with KVM tests on x86. Remove
Dockerfiles, compose files, old deploy scripts. Rewrite install, sandboxes, security docs. Bring the
demo up on `scarif.local` with a new deploy workflow. *Accept:* on a fresh KVM host, download one
binary, write one YAML, run it, and reach a working agent.

### Step 2: Postgres

**P1.** Store interface above sqlc; Postgres query set and migrations; `database.url` in config
(`sqlite:` default, `postgres://`); the whole Go test suite runs against both engines in CI; backup
command covers SQLite only and docs point Postgres users to their own tooling. *Accept:* e2e passes
against Postgres.

### Step 3: edge nodes

**E1.** Remote implementation of the node interface: `agenthold node --join <token>` dials the
controller (mutual TLS pinned at join, one multiplexed connection), carries sandbox lifecycle, tunnel
sessions and secret pushes. Node table, capacity-based placement, node status in the admin UI, agents
of an offline node shown as unreachable. *Accept:* controller on a host without KVM, node in the Lima
VM; an agent created in the UI runs on the node and behaves exactly like a local one.

### Step 4: machines, apps and exposure

Decided with the project owner on 2026-11-16. Work lands through pull requests that are merged when
CI is green; the demo deploys itself from `main`.

**Model.** The `agents` table is the table of *machines*. A machine is one Firecracker microVM with
an owner, shares, resources, disks and a sandbox row. `kind` says what runs in it:

- `agent`: a harness (Claude Code, Codex or OpenCode, nothing else) driven over ACP. Unchanged.
- `app`: no harness. sandboxd supervises one service command (restart with backoff, captured logs).
  An app is created by a user or by an agent (`created_by_agent_id`); it belongs to that agent's
  owner. Apps do not idle-sleep unless configured to.

No rename of tables, packages or routes: "agent" stays the word for kind `agent`, the REST surface
gains `/apps`. Anything that is machine-level (lifecycle, terminal, files, shares, secrets,
exposure, clone, snapshot) is written once and works for both kinds.

**Exposure (preview URLs).** Any machine can expose guest TCP ports over HTTP. An exposure is
`(machine, port, name, access)` and is served at `<name><preview.host_suffix>`, for example suffix
`.apps.example.com` or `-demo.agenthold.dev` (a suffix, not a subdomain, so one-level wildcard
certificates work). The operator points a wildcard DNS record and their TLS proxy at agenthold; we
still terminate no TLS. The controller routes by `Host`, dials the guest port through the tunnel
(new `dial` stream op in sandboxd; works across edge nodes) and proxies HTTP and WebSocket.
Without `preview.host_suffix` the feature is off.

Access per exposure:

- `private` (default): owner and shared users. The controller is the auth gate: no cookie means a
  redirect to the main host, which hands a one-time code back to the exposure host, which sets a
  host-only cookie. The app receives `X-Agenthold-User`, `X-Agenthold-Email` and a short-lived
  signed `X-Agenthold-Identity` JWT (keys at `/.well-known/agenthold/jwks.json` on the main host).
- `authenticated`: any signed-in user of this agenthold, same headers.
- `public`: no gate, identity headers stripped.

This is what "the app needs OIDC" and "the app needs an HTTPS domain" mean for now: the platform
gives an app a certificate-backed hostname and an authenticated user without app code. Acting as a
full OIDC provider for apps is a later option on top of the same keys.

**Agent tools.** The platform MCP server gains machine tools, scoped like agent-to-agent calls
(machines of the caller's owner): `expose_port`, `list_exposures`, `unexpose`, `deploy_app` (create
or update an app from a directory of the calling agent: tar through the tunnels, set command, port,
env, access; returns the URL), `list_apps`, `app_logs`, `app_exec`, `stop_app`, `delete_app`.

**Harness layer.** Exactly three adapters plus the test fake. The adapter interface, the sandboxd
harness process and the session manager's ACP connection are the foundation: every failure
(missing binary, crash, hang during initialize, auth required, protocol garbage, server restart
mid-turn, node disconnect) must end in a defined session state with a readable error and a clean
retry, covered by tests with the fake harness.

Milestones (migration numbers are reserved so parallel work does not collide):

- **H1. Harness hardening.** Audit and fix as above. No migration.
- **X1. Exposure.** `00002_exposures`. Tunnel `dial` op, host router, auth gate, API, UI, the three
  exposure tools. *Accept:* an agent runs `python3 -m http.server`, calls `expose_port`, and the URL
  works for the owner in a browser, is refused for a stranger, and carries WebSockets.
- **C1. Clone and templates.** `00003_clone_templates`. Driver `Clone` (reflink or sparse copy of
  overlay and home while the source is stopped or paused), node RPC, API, UI. A template is a
  stopped machine flagged `is_template`; "new from template" is a clone.
- **A1. Apps.** `00004_apps`. `kind`, service supervision in sandboxd, `/apps` API, app tools, UI.
  Builds on X1. *Accept:* an agent writes a small web app, calls `deploy_app`, and the returned URL
  serves it behind the auth gate; the app survives a server restart and a redeploy.
- **S1. Memory snapshots.** `00005_snapshots` if needed. Sleep = pause, snapshot to disk, exit;
  wake = restore, fix the guest clock, sandboxd re-dials the tunnel. Balloon to shrink the
  snapshot. Any restore failure (version change, missing file, error) falls back to cold boot.
  Exposures wake a sleeping machine on the first request. Builds on C1 (same driver code).
- **D1. Self-hosted artifacts.** No GitHub release assets. A controller serves the base image and
  release bundle it runs to its edge nodes and to other installs
  (`/dl/...`, checksummed, zstd, range requests); the demo box builds and tests releases itself
  (it has KVM on arm64) and is the project's download origin. GitHub Actions keeps only what needs
  no KVM and no hosting (unit tests, lint, build).

### Later

- Live browser view of an agent's Chromium.
- Agenthold as an OIDC provider for apps.
- Custom domains per exposure.

## 6. Open questions

- None open. Former ones: rootfs hosting is D1 (self-hosted); KVM tests run on our own hardware,
  not on GitHub runners.
