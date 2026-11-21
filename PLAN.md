# agenthold: project plan (v2, native + Firecracker)

Source of truth for what we are building and in which order. Written for coding agents that execute
one milestone at a time. The v1 plan (Docker, compose, SQLite only) is in git history; v1 is fully
built and is the starting point of this rework.

- Go module `github.com/gempir/agenthold`. One binary, `agenthold`: server, edge node and the in-guest
  daemon `sandboxd` (G2: the role is decided at startup).
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
| Binary | `agenthold` embeds `firecracker`, `jailer` and the guest kernel for its architecture and extracts them to `<data_dir>/runtime/<version>/` on start. It is also `sandboxd` (G2). The node delivers its `sandboxd` to every guest at boot as an initramfs (G1), so a machine always runs the booting release's daemon. |
| Base rootfs | One blessed base disk image, built from `images/base`, OS only (no sandboxd): it changes only with its own inputs, so consecutive releases share it. Too large to embed (several GB): fetched on first start from a pinned URL and verified by sha256, or pre-placed in `data_dir` for offline installs. |
| Agent disks | Shared read-only base + per-agent writable overlay disk (system changes survive sleep) + per-agent persistent `/home/agent` disk. Upgrading the base swaps it and resets the overlay; home always survives. No per-agent image; "Rebuild" becomes "Reset system". |
| Sleep | Cold shutdown and boot (same semantics as before: disk and conversation survive, processes do not). Memory snapshots are a later milestone. |
| Secrets | Unchanged: never enter the VM. A host-side transparent TLS-intercepting proxy injects them. Account-level harness logins stay. |
| Network | Every agent has open internet through the proxy. Network policy (allowlists) and the per-agent request log are removed. |
| Topology | One binary, two roles. `agenthold` alone is controller + node. Later `agenthold node --join <token>` adds a remote node that dials the controller. New agents are placed on the node with most free capacity (or the node an admin picks) and stay there. Node down = its agents offline. |
| Edge trust | Nodes are trusted infrastructure. The proxy runs on the node; the controller pushes the decrypted secrets of that node's agents over the node connection, held in memory only. |
| Database | SQLite (default, zero setup) or Postgres. sqlc stays, with one query set per engine. No MySQL. |
| Auth | OIDC and GitHub OAuth as before, plus `auth.local` = exactly one admin (username + password hash) in the YAML. No provider and no `auth.local` means nobody can log in; the server says so at startup. No generated credentials, no signup. |
| Roles | Added 2026-11-21 at the owner's request: three installation roles, Admin, User (agent quota `auth.user_max_agents`, default 2) and Disabled (signs in, sees "no access"). Mapped per provider (GitHub `github_roles` by numeric id, OIDC `role_attribute_path` JMESPath like Grafana), `auth.admins` and `auth.local` give Admin, `auth.default_role` defaults to Disabled. Evaluated at sign-in and server start, stored on the user, read every request; admins can override per user in the Overview. Not a role matrix: per-agent rights stay owner / shared. |
| Ownership and sharing | Workspaces, memberships, invites and viewer/operator/admin roles are removed. A user owns agents. The owner can share an agent with other existing users (by email); a shared user can use it (chat, terminal, files), only the owner can configure, share or delete it. MCP servers, skills and secrets belong to a user. |
| Agent-to-agent | Kept. An agent may address agents its owner owns or has been shared. |
| TLS | Plain HTTP only. Docs show Caddy, nginx and cloudflared in front. |
| Development | macOS + Lima VM (`vmType: vz`, nested virtualization; OrbStack has no `/dev/kvm`). The server runs natively inside the Lima VM. |
| Demo | Mac mini `scarif.local` (M4, 24 GB): Lima VM, all-in-one, published through cloudflared. The OCI host (no KVM) is retired. No data migration. |
| Process | Straight on `main`. The old deploy workflow is disabled until the new one exists. Early development: no compatibility with v1 data, config or API is required. |

Known consequences, accepted:

- Most cheap cloud VPSes have no KVM and cannot host agents.
- The dev and demo setups run Firecracker under nested virtualization (slower than bare metal).
- Release binaries are built for amd64 and arm64 by GitHub Actions; guest kernels and base images are built
  and KVM-tested natively per architecture: arm64 on the demo VM, amd64 on korriban (G3).

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
- `sandboxd` reaches the guest as the kernel's initramfs (`initrd_path`: an uncompressed newc cpio with
  only `/init`, written in Go by the driver from the node's own sandboxd) and is the guest's init: it
  mounts the base read-only and switches into it, mounts the overlay over the base, mounts home,
  bind-mounts itself over `/opt/agenthold/sandboxd` (the stable in-guest path; old bases' own copy is
  covered), brings up the network, dials the node's tunnel endpoint at the gateway address, reaps
  children, powers off on request. A restart updates a machine's sandboxd; the tunnel hello reports
  its version.

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
| Base rootfs | Built on the demo host from `images/base` (`scripts/deploy/build-local.sh`, build-time tooling is free to use containers) and served with checksums under its `/dl/`; no GitHub release assets |
| Host networking | `vishvananda/netlink` for taps, `google/nftables` for rules (both already dependencies) |
| Store | `sqlc` with two engines (sqlite via `modernc.org/sqlite`, postgres via `pgx`), `goose` migrations per engine, one Go interface above both |
| Local auth | argon2id hash in YAML, `agenthold hash-password` prints one |
| Service | `agenthold install` writes a systemd unit; `agenthold` runs in the foreground otherwise |
| Dev | `deploy/dev/lima.yaml`; `make dev` starts the VM and runs the server in it with live reload |

### Conventions for executing agents

- Work lands on `main` in small commits; each milestone ends with its acceptance checks passing and
  tests covering them. Do not push unless told to.
- Tests that need KVM carry the build tag `kvm` and run inside the Lima VM (`make test-kvm`) and on the
  demo host before every release (`scripts/deploy/build-local.sh`); GitHub CI does not run them. Tests
  run there next to the live service: they must isolate themselves (network/mount namespace, temporary
  data and jail directories, own address pool) and never touch host-wide state such as
  `/sys/fs/cgroup/agenthold`. Everything else uses the fake driver and must pass on macOS.
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
building linux/amd64 and linux/arm64 binaries plus rootfs assets, CI with KVM tests on x86 (both
replaced by D1: the demo host builds and KVM-tests arm64 releases itself). Remove
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
  *As built:* services are machine-level (`services` table, any kind): sandboxd runs each as the agent
  user in its own cgroup leaf (`agenthold-svc-<name>`), restarts with backoff (1 s doubling to 30 s,
  reset after 60 s), logs to `~/.agenthold/services/<name>/log` (10 MiB x 2), and gets the desired set in
  the boot files (`/etc/agenthold/services.json`, so old guests ignore it instead of failing to boot) and
  through the `service` tunnel op. Service CPU does not count as idle activity. Commands are shell strings
  run with `sh -lc`. API rule: `/agents/{id}` (and its lifecycle) is agents only, `/apps/{id}` apps only
  (404 for the other kind); harness sub-resources answer 409 for apps; machine sub-resources stay under
  `/agents/{id}/...` for both and an App lists them in `links`. `idle_minutes = 0` means never sleep
  (apps default). Deploy: `fs.tar`/`fs.untar` through the controller into `~/app/releases/<n>`,
  `fs.symlink` swap of `~/app/current`, keep 3, service `app`, exposure, wait for the port; a release
  that does not come up (or crash-loops) is rolled back to the previous one. Deviation: tar/untar run
  in sandboxd itself (root) with ownership set to the agent user, not re-executed as the agent user: the
  agent has passwordless sudo in its machine, so the separation would buy nothing; sandboxd's own state
  (`/etc/agenthold`, `/run/agenthold`) is never archived; every root write into the home (untar,
  symlink swap, service logs) goes through an `os.Root`. An agent manages only apps it created (every
  app tool but `list_apps`); the owner can take an app over for good, after which no agent manages it.
  App names are DNS labels unique per owner; `apps.max_per_owner` (10) and `apps.max_per_agent` (3, 0 =
  agents create none) are checked with the insert. Deploy touches the exposure only once a release
  serves (a port change moves it in place) and sets access only at creation. Deploys have a 15 min
  deadline, a 60 s relay idle timeout and byte/entry caps; guest output the controller buffers is capped.
  Clone is machine-level: an app's clone is an unmanaged app with its services but no exposures, and apps
  can be templates. Apps cannot call platform tools.

- **S1. Memory snapshots.** `00005_snapshots` if needed. Sleep = pause, snapshot to disk, exit;
  wake = restore, fix the guest clock, sandboxd re-dials the tunnel. Balloon to shrink the
  snapshot. Any restore failure (version change, missing file, error) falls back to cold boot.
  Exposures wake a sleeping machine on the first request. Builds on C1 (same driver code).
  *As built* (no migration, no API or node protocol change: everything is in the driver; the controller sees
  sleep and wake as before). Sleep: tunnel op `suspend` (sandboxd syncs; an older sandboxd refuses it, so
  its machine shuts down as before), balloon inflated to the guest's free memory, `suspend` again,
  `PATCH /vm Paused`, full snapshot into `<dir>/snapout` (the VMM user's; bind-mounted as the jail's `/snap`),
  VMM killed, then the host takes each file by descriptor (`O_NOFOLLOW|O_NONBLOCK`, regular file of the VMM
  user with one link), punches its zero pages (Firecracker v1.18 writes it fully allocated), fsyncs it, gives
  it to root and `renameat`s it into the root-owned store `<dir>/snapshot` (0700, bound read-only on resume),
  fsyncs the disks, and writes `snapshot.json` last. Nothing the host later opens or binds as root lives in a
  directory the VMM user can write; sockets in the jail are dialed through `O_PATH|O_NOFOLLOW`, jail
  unmounts use `UMOUNT_NOFOLLOW`. The slow part runs without the driver lock (the machine is marked
  suspending: its other operations wait, Status reports it running). It records
  what a resume needs unchanged: Firecracker, kernel and initramfs (sandboxd) hashes, resume protocol,
  host CPU model and host kernel, VM shape, base image, disk sizes and mtimes. Wake: the manifest is removed
  first (a snapshot is resumed at most once and never outlives a disk change), new jailed VMM,
  `/snapshot/load` with `resume_vm`, balloon deflated, then a `sandboxd.ResumeMessage` over vsock (port 1100;
  every VM now has a vsock device) carries the new boot token, boot files and host clock: sandboxd steps
  the clock, rewrites its boot file and redials the tunnel at once. The driver waits up to 15 s (lock
  released) for the tunnel, else kills and cold boots. Any failure before is a cold boot after discarding the
  snapshot; cold boot, Reset system and Destroy always discard it, Clone only reads disks. The tap gets a
  fixed MAC (derived from the gateway address): with a random one, a resumed guest's neighbour entry
  black-holed its traffic for about 3 s. On wake sandboxd also applies the services file it is handed. A VM an
  interrupted sleep left paused is resumed by Status/Start and gets a resume message (clock; Start's new
  token). Config `sandbox.firecracker.snapshots` (true), `balloon` (true),
  `snapshot_max_gb` (0 = no cap), `snapshot_reserve_gb` (2). Measured on the demo box (1 GiB guest, nested):
  sleep 1.0-1.5 s (up to 3.8 s with 400 MiB of freed dirty memory to balloon), wake to tunnel 0.4-0.5 s
  (cold boot 2.3-2.7 s), memory file 88-110 MiB allocated with the balloon, 491 MiB without after freeing
  400 MiB. *Revised 2026-11-21:* on a nested host (the demo, Lima) a resumed guest's memory is
  the snapshot file's private mapping, which gets no huge pages: every touched page is a 4 KiB nested fault
  (1 GiB allocation 17-150 s after a resume, 0.4 s after a cold boot; a resumed Claude Code took 111 s to start).
  Snapshots therefore default to off when the host is itself a VM; cold boot there is 0.3-0.5 s to the tunnel
  because the node sets transparent huge pages to `always` (Firecracker does not madvise guest memory; with
  `madvise` a 4 GiB guest needed 25 s to reach init). A start of a running but unconnected VM hands it the new
  tunnel token over vsock (a slow boot that outlived the controller's wait was refused forever before). Capacity reporting is unchanged: snapshots are on the data filesystem, whose free space is reported.
- **D1. Self-hosted artifacts.** No GitHub release assets. A controller serves the base image and
  release bundle it runs to its edge nodes and to other installs
  (`/dl/...`, checksummed, zstd, range requests); the demo box builds and tests releases itself
  (it has KVM on arm64) and is the project's download origin. GitHub Actions keeps only what needs
  no KVM and no hosting (unit tests, lint, build).
  - D1a (done): `/dl/` on every release controller, resumable base download, edge nodes fetch from
    their controller.
  - D1b (done): `build-local.sh` + `agenthold-build.timer` build, race-test, KVM-test and publish main
    into `/srv/agenthold/releases`; `pull.sh` deploys from a configured source (local store or a `/dl/`
    root) and publishes the deployed release under the controller's `/dl/`. `deploy.yml`,
    `release.yml` and the CI `kvm` job are gone. Open: tagged `v*` releases (build-local only builds
    `sha-<commit>` from main), amd64 builds (from source only).

- **G1. Runtime-delivered sandboxd** (done). The base image no longer contains sandboxd: the driver
  boots every VM with an initramfs carrying the node's sandboxd, which mounts the base and switches into
  it, and bind-mounts itself over `/opt/agenthold/sandboxd`. The base is keyed by
  `scripts/guest/base-key.sh` (images/base and the rootfs script), built reproducibly (all inode and
  superblock times pinned) and cached by the demo builder, so Go-only releases share one base on disk
  and edge nodes download nothing new. Existing machines on old bases get the new sandboxd at their next
  boot without a reset. `Status.BaseStale` and the clone refusal for machines on an older base are gone:
  a clone stays on its source's base, which pruning keeps while any machine names it. Release store
  retention is two versions. No migration.

- **G2. One binary** (done). `cmd/sandboxd` is gone: `agenthold` is also the in-guest daemon. The role is
  decided once at startup from PID and argv[0] (`cmd/agenthold/sandboxd.go`): PID 1 as `/init` or `sandboxd`
  is guest init, argv[0] `sandboxd` (the stable in-guest path `/opt/agenthold/sandboxd`) the daemon,
  `agenthold sandboxd` the daemon explicitly; everything else, PID 1 of a container included, is the server.
  The node delivers its own executable as the initramfs. Releases ship one file per architecture; the
  runtime manifest lists `firecracker`, `jailer` and `kernel` only. The initramfs grows from the 7 MB sandboxd
  to the whole binary (see guest-artifacts for the measurement). No migration.

- **G3. Binaries from GitHub, assets per architecture** (done). Release binaries (`release.yml`: pre-release
  `sha-<commit>` for every green main commit, `v*` tags as releases) embed Firecracker and the jailer only; the
  guest kernel and the base image are assets named by their input keys (`kernel-key.sh`, `base-key.sh`), built
  natively per architecture by `scripts/release/build-assets.sh`, described by asset manifests and served under
  `/dl/assets/<arch>/`. The runtime manifest is format 2 (keys instead of sums), the store channel format 3.
  Nodes fetch missing assets from `sandbox.firecracker.assets_url`, then their controller; assets are matched
  by key, so node and controller versions no longer have to match for them. Trust change: the asset manifest
  comes from the source, so sources must be trusted (they are the operator's own hosts). korriban (amd64)
  builds its own assets in a build container and follows the demo's version. No migration; pull.sh places
  the assets before the first switch.

### Later

- Live browser view of an agent's Chromium.
- Agenthold as an OIDC provider for apps.
- Custom domains per exposure.

## 6. Open questions

- None open. Former ones: rootfs hosting is D1 (self-hosted); KVM tests run on our own hardware,
  not on GitHub runners.
