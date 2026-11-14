# agenthold: project plan

This document is the source of truth for what we are building and in which order. It is written for
coding agents that execute one milestone at a time.

- Name: **agenthold**. Go module `github.com/gempir/agenthold`. Binaries: `agenthold` (the server) and
  `sandboxd` (the in-sandbox daemon).
- License: MIT (see `LICENSE`).

## 1. What we are building

A self-hostable, multi-user platform for running many persistent AI agents. Each user has workspaces.
A workspace contains agents. Each agent has its own isolated sandbox (its own "computer") with a
persistent disk, tools and conversation history, and can be shared with other workspace members.

We do **not** build an agent harness. Existing harnesses (Claude Code, Codex, OpenCode, ...) run inside
the sandboxes and are driven through the Agent Client Protocol (ACP). We build everything around them:
sandbox orchestration, identity and sharing, secrets, networking, session persistence, triggers and UI.

Comparable projects for orientation: OpenHands, TencentCloud/Octop.

### Non-goals for v1

- Writing our own agent loop, tool implementations or model client.
- Multi-node scheduling or Kubernetes. One Linux host only.
- Firecracker. It is a planned second sandbox driver, not part of v1.
- Password accounts. Sign-in is OIDC (plus GitHub, see section 2).
- Billing, quotas per plan, a marketplace.

## 2. Decisions

These were decided with the project owner. Do not revisit them while executing a milestone. If one
turns out to be unworkable, stop and report instead of silently choosing something else.

| Area | Decision |
|---|---|
| Harnesses | Any harness, through a thin adapter. First adapters: Claude Code, then one of Codex / OpenCode. |
| Harness protocol | ACP (JSON-RPC over the harness's stdio). No terminal-scraping. |
| Agent purpose | Coding and general-purpose assistant work equally. Sandboxes include dev tooling and a browser. |
| Deployment | Single Linux box, `docker compose up`. |
| Agent lifetime | Persistent. The sandbox sleeps when idle and wakes on demand. |
| Isolation | Docker containers in v1, behind a narrow sandbox driver interface. Optional hardened runtime (Sysbox or gVisor) through configuration. Firecracker driver later. |
| Backend | Go. One module, two binaries: the server and the in-sandbox daemon. |
| Database | SQLite only (cgo-free driver). No Postgres abstraction. |
| Frontend | React + Vite + TypeScript + Tailwind, built to static files and embedded in the server binary. |
| Auth | Generic OIDC, plus a built-in GitHub OAuth provider (GitHub user login is plain OAuth2, not OIDC). No passwords. Documented examples for GitHub and Keycloak. |
| Sharing | Workspace membership plus per-agent roles: viewer, operator, admin. |
| Secrets | Never enter the sandbox. A host-side egress proxy injects them into outgoing requests. |
| Harness logins | API keys go through the proxy. Subscription logins (Claude, ChatGPT plans) use the harness's own login, triggered through ACP authentication. Those tokens live on the agent's disk, an accepted exception to the secrets rule. |
| Network | Transparent interception in v1: all sandbox traffic goes through the proxy, including tools that ignore proxy settings. |
| License | MIT. |
| Sandbox images | Ordinary OCI images. With the Docker driver they are used as they are. |
| Tools | Per-agent and workspace-level MCP servers, plus a workspace skills library. |
| Triggers in v1 | Web UI, REST/WebSocket API with tokens, cron schedules, inbound webhooks, agent-to-agent messaging. |
| Development | macOS with OrbStack as the Docker host. A fake sandbox driver for tests without Docker. |

Known consequences of choosing Docker, accepted for v1:

- Sandboxes share the host kernel. The hardened runtime option and the egress proxy are the mitigations.
- "Sleep" stops the container. The disk and the conversation survive, running processes do not.
- Docker inside an agent only works with the Sysbox runtime.

## 3. Architecture

```
 Browser (React SPA)          API clients / webhooks
        |  REST + WebSocket          |
        v                            v
+--------------------------------------------------------------+
| server (one Go binary, runs as a container)                  |
|                                                              |
|  HTTP API + embedded SPA      OIDC + sessions + API tokens   |
|  Authorization (roles)        Scheduler (cron, idle sleeper) |
|  Session hub (ACP client,     Secrets store (encrypted)      |
|    event log, fan-out)        Egress proxy (TLS intercept,   |
|  Sandbox manager -> driver      allowlist, secret injection) |
|  Tunnel endpoint for sandboxd Platform MCP backend           |
|  SQLite                                                      |
+-----------+-------------------------------+------------------+
            | Docker API (socket)           | one small internal
            v                               | network per agent
   +------------------+   +------------------+
   | agent sandbox A  |   | agent sandbox B  |   ...
   |  sandboxd  ------+---+--> dials the server (tunnel)
   |   |- harness (ACP over stdio)           |
   |   |- PTYs, file access                  |
   |   '- local platform MCP endpoint        |
   |  /home/agent  (persistent volume)       |
   +------------------+   +------------------+
```

### 3.1 Components

**server** (binary `agenthold`). The only long-running host process. It always runs as a container, in development too,
because it has to be attached to every agent's network. It talks to the Docker socket.

**sandboxd.** A static Go binary bind-mounted read-only into every sandbox and used as its entrypoint.
This is what lets any OCI image work as a sandbox. On start it dials the server over a WebSocket,
authenticates with a per-boot token, and then serves multiplexed channels:

- start, stop and pipe the harness process (raw ACP frames in both directions),
- PTY terminals,
- file listing, read, write, upload and download,
- writing harness configuration at boot (MCP servers, skills, instructions),
- a local MCP endpoint that forwards platform tool calls to the server,
- activity reporting for the idle sleeper.

sandboxd dials out, the server never dials in. A future Firecracker driver can reuse the same tunnel.

**Sandbox driver.** The interface the rest of the server uses. Keep it small:

```go
type Driver interface {
    Create(ctx, SandboxSpec) (SandboxID, error)   // image, resources, volume, network, env
    Start(ctx, SandboxID) error
    Stop(ctx, SandboxID) error                    // sleep: keep disk
    Destroy(ctx, SandboxID, DestroyOpts) error    // optionally keep the home volume
    Status(ctx, SandboxID) (Status, error)
    Rebuild(ctx, SandboxID, SandboxSpec) error    // new image, same home volume
}
```

Implementations: `docker` and `fake` (in-process, runs sandboxd as a goroutine or child process, for
unit tests and for UI work without Docker). Nothing outside the driver package may import the Docker SDK.

**Session hub.** The server is the ACP client. One hub goroutine per live agent session owns the ACP
connection (through the tunnel), appends every update to the event log, and fans events out to all
subscribed viewers. We do not advertise the ACP client file system or terminal capabilities; the
harness uses its own tools inside the sandbox.

**Egress proxy.** Each agent sits on its own Docker network created with `internal: true`, so it has no
route to the internet. The server joins that network, and its proxy is the only way out. The proxy
identifies the agent by source network, applies the agent's network policy, terminates TLS with a
per-installation CA and injects secrets into matching requests.

Interception is transparent. Setting `HTTPS_PROXY` alone is not enough, because many programs ignore it
(SSH, some Node and Go clients, package managers in certain modes, raw TCP). The server's address is
the sandbox's default gateway, and outgoing TCP is redirected to the proxy on the server side
(iptables/nftables `REDIRECT` plus `SO_ORIGINAL_DST`). HTTPS is identified by SNI; other TCP is allowed
or denied by destination and port according to the policy, without injection. DNS is answered by the
server. This means `git` over SSH works when the policy allows it, and anything blocked fails with a
clear entry in the agent's request log instead of hanging. `HTTPS_PROXY` is still set as a hint.

### 3.2 Domain model

```
User (from OIDC)
 '- Membership (role: owner | admin | member) -> Workspace
Workspace
 |- Agent
 |   |- owner (User), image, harness, model, resources, network policy
 |   |- Grant (subject: user | whole workspace, role: viewer | operator | admin)
 |   |- Sandbox (driver id, state: creating | running | sleeping | error)
 |   |- Session -> Event (append-only log)
 |   |- Schedule, Webhook
 |   '- attached MCP servers, skills, secrets
 |- MCP server definitions, Skills, Secrets (workspace level)
 '- API tokens belong to a User and inherit that user's permissions
```

Every user gets a personal workspace on first login. Agent role meanings:

- **viewer**: read sessions and files.
- **operator**: viewer, plus prompt, cancel, answer permission requests, open a terminal.
- **admin**: operator, plus change configuration, secrets, grants, and delete.

Workspace owners and admins are implicit admins of every agent in the workspace. Authorization is
enforced in one package that every handler calls; never in the UI only.

### 3.3 Key flows

**Prompt.** UI sends a prompt -> authorization check (operator) -> if the sandbox is sleeping, start it
and wait for sandboxd -> hub sends `session/prompt` -> updates are stored and streamed -> turn ends.

**Sleep and wake.** An agent is idle when it has no running turn, no open terminal, and no activity
reported by sandboxd for N minutes (configurable per agent, default 15). The sleeper then calls
`Driver.Stop`. Any prompt, webhook, schedule, agent message or terminal request wakes it. After waking,
the hub resumes the conversation with ACP `session/load` if the harness advertises that capability,
and otherwise starts a new session and tells the user that context was reset.

**Permission requests.** Each agent has a policy: `auto-approve` (default, the sandbox is the safety
boundary) or `ask`. With `ask`, ACP `session/request_permission` is shown to connected operators and
the turn waits.

**Secrets.** A secret has a value and an injection rule: host pattern, header name, value template
(for example `api.anthropic.com`, `x-api-key`, `{{value}}`). It is stored encrypted with a master key
supplied to the server. Inside the sandbox the harness gets a placeholder value. The proxy replaces it.
The CA certificate is installed into the sandbox and exported through the usual variables
(`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, ...), together with `HTTPS_PROXY`.

**Subscription logins.** When the harness advertises auth methods in the ACP `initialize` response,
the UI offers them to agent admins and calls ACP `authenticate`. Any login URL or device code the
harness produces is shown to the user. The resulting tokens stay where the harness stores them, on the
agent's persistent home volume, so they survive sleep and image rebuilds. The proxy does not inject or
refresh them. The UI marks such agents as "credentials inside sandbox" so admins know the agent can
read its own login.

**Agent-to-agent.** The platform MCP endpoint in each sandbox offers tools such as `list_agents`,
`send_message(agent, text)` and `ask(agent, text)` (waits for the turn to finish and returns the final
answer). The server enforces that both agents are in the same workspace and that the calling agent's
owner holds at least operator on the target. Calls carry a depth counter with a hard limit to stop loops.

**Schedules and webhooks.** A schedule is a cron expression plus a prompt. A webhook is a secret URL
plus a prompt template that receives the request body. Both wake the agent and start a turn in a
designated session. Webhook payloads are untrusted input and are clearly delimited in the prompt.

### 3.4 Harness adapters

An adapter is a small Go type plus data, one package per harness:

- how to check for and install the harness in a sandbox,
- the command that starts it in ACP mode,
- where its MCP configuration, skills and instruction file live and how to write them,
- which environment variables carry model credentials and base URLs (placeholders only),
- which secret injection presets it needs.

Before implementing an adapter, check the current ACP specification (agentclientprotocol.com), the
state of the Go SDK (`github.com/coder/acp-go-sdk`) and the harness's current ACP entry point. These
move quickly and package names in older notes may be outdated.

## 4. Technology

| Concern | Choice |
|---|---|
| Go HTTP | standard library `net/http` with `chi` router |
| API contract | OpenAPI document in the repo is the source of truth; `oapi-codegen` for Go, `openapi-typescript` for the client |
| Live updates | WebSocket: per-session event stream with a resume cursor, terminals, tunnel |
| SQLite | `modernc.org/sqlite`, WAL mode, `sqlc` for queries, `goose` for migrations |
| OIDC | `github.com/coreos/go-oidc` with authorization code + PKCE, server-side sessions in SQLite |
| Docker | official Go SDK, only inside the driver package |
| Encryption | XChaCha20-Poly1305 with a master key from a file or environment variable |
| Logging | `log/slog`, structured |
| Frontend | React, Vite, TypeScript, Tailwind, shadcn/ui, TanStack Query and Router, xterm.js |
| Tests | Go `testing`, Vitest, Playwright for a small end-to-end suite |
| Build | `make` targets, multi-stage Dockerfile, GitHub Actions |

### Repository layout

```
cmd/agenthold/         main for the server
cmd/sandboxd/          main for the in-sandbox daemon
internal/api/          HTTP handlers, generated OpenAPI types
internal/auth/         OIDC, sessions, API tokens
internal/authz/        role checks, the only place that decides access
internal/store/        migrations, sqlc queries
internal/sandbox/      Driver interface, manager, lifecycle
internal/sandbox/docker/
internal/sandbox/fake/
internal/tunnel/       server <-> sandboxd protocol
internal/session/      ACP client, hub, event log
internal/harness/      adapter interface and one package per harness
internal/egress/       proxy, CA, policy, injection
internal/secrets/
internal/trigger/      schedules, webhooks
internal/mcp/          platform MCP tools
web/                   React app
images/base/           Dockerfile of the default agent image
deploy/                docker-compose.yml, Keycloak and GitHub auth examples, Caddy example
api/openapi.yaml
docs/
```

### Conventions for executing agents

- One milestone per branch and pull request. Do not start a milestone before the previous one is merged.
- Every milestone ends with its acceptance checks passing and automated tests covering them.
- Everything must be testable on macOS with OrbStack. Tests that need Docker carry a build tag and are
  skipped when no Docker socket is available. All other tests use the fake driver.
- Change `api/openapi.yaml` first, regenerate, then implement.
- No new dependency without a one-line justification in the pull request.
- Report anything that contradicts section 2 instead of working around it.

## 5. Milestones

**M0. Scaffold.** Go module, both binaries building, web app skeleton embedded and served, Makefile,
lint, CI, dev compose file with live reload. *Accept:* `make dev` serves a page from the server
container in OrbStack; `make test` and `make lint` pass in CI.

**M1. Identity and workspaces.** Migrations, generic OIDC login, GitHub OAuth login, sessions,
personal workspace on first login, workspace CRUD, memberships and invites, the `authz` package. The dev
compose file runs Keycloak with a pre-imported realm and two test users so login works locally.
Example configs and docs for Keycloak and for GitHub. *Accept:* two users can log in through Keycloak,
one invites the other into a workspace, GitHub login works against a real OAuth app, and role checks are
covered by table-driven tests.

**M2. Sandboxes.** Driver interface, fake driver, Docker driver (per-agent internal network from a
configurable address pool, home volume, resource limits, optional runtime name), sandbox manager with a
persisted state machine, agent CRUD in API and UI. *Accept:* creating an agent starts a container,
stop and start keep files in `/home/agent`, and state survives a server restart.

**M3. sandboxd and tunnel.** Daemon, bind-mount injection, authenticated tunnel, terminal and file
channels, web terminal and file browser in the UI. *Accept:* a shell in the browser works for an agent
built from the base image and from a stock `debian` image.

**M4. Egress and secrets.** Transparent proxy (gateway, redirect, DNS), CA generation and
installation, network policy (`open` or `allowlist` by host and port), encrypted secrets with injection
rules and presets for common providers, per-agent request log. *Accept:* a sandbox reaches the internet
only through the proxy, a program that ignores `HTTPS_PROXY` is still intercepted, `git` over SSH works
when allowed and is logged as denied when not, a placeholder key is replaced on the wire, and the real
value is not readable from inside the sandbox by any means tested.

**M5. Sessions over ACP.** Adapter interface, Claude Code adapter, session hub, event log, chat UI
with streaming, tool call rendering, diffs, cancel, permission prompts, multiple sessions per agent,
ACP authentication flow for subscription logins. Default base image containing the harness, git,
common runtimes and a headless browser. *Accept:* a user chats with an agent that edits files in its
sandbox, reloads the page mid-turn and sees the stream continue from the stored cursor; an agent logged
in with a subscription stays logged in across sleep.

**M6. Sleep and wake.** Idle detection, sleeper, wake on demand, session resume, clear UI states.
*Accept:* an idle agent stops, a new prompt wakes it and the conversation continues with its history.

**M7. Sharing.** Grants in API and UI, multi-viewer sessions with presence, audit log of who prompted
what. *Accept:* a viewer watches a live session and cannot prompt; an operator can; all checks are
enforced server-side and tested.

**M8. Second harness.** Codex or OpenCode adapter, chosen for the better ACP support at that time.
*Accept:* the same UI flows work unchanged. Anything harness-specific that leaked outside
`internal/harness` is moved back in.

**M9. MCP and skills.** Workspace and agent MCP server definitions, remote MCP authentication through
the proxy, skills library with attach and detach, written into the sandbox by the adapters.
*Accept:* attaching an MCP server and a skill makes both usable in the next session for both harnesses.

**M10. API tokens, schedules, webhooks.** Personal access tokens, published API reference, cron
schedules, webhook endpoints with optional signature verification. *Accept:* a script using a token
creates an agent and prompts it; a schedule and a webhook each wake a sleeping agent.

**M11. Agent-to-agent.** Platform MCP endpoint and tools, permission and depth rules, linked sessions
in the UI. *Accept:* one agent delegates a task to another and receives the result; a loop is stopped
by the depth limit.

**M12. Release.** Production compose file, install and upgrade guide, backup and restore of SQLite and
volumes, hardened runtime guide (Sysbox, gVisor), security review, end-to-end suite.
*Accept:* a fresh Linux VPS goes from nothing to a working agent by following the guide only.

### After v1

- Firecracker driver: OCI to ext4 conversion, guest kernel, tap networking, memory snapshots for true
  suspend and resume. Developed in a Lima VM with nested virtualization or on a KVM host, since OrbStack
  has no `/dev/kvm`.
- Preview URLs for ports inside a sandbox, live view of the agent's browser.
- Disk snapshots and cloning an agent, agent templates.
- Multi-node, usage metering and budgets, chat integrations (Slack and similar).

## 6. Open questions

None at the moment. Add new ones here instead of deciding them silently.
