# Sessions and harnesses

agenthold does not implement an agent loop. A **harness** (Claude Code, ...) runs inside each sandbox and speaks
the [Agent Client Protocol](https://agentclientprotocol.com) (ACP) on stdio. The server is the ACP client.

## How a turn works

1. A user prompts a session (operator role). If the agent sleeps, the prompt wakes it.
2. The server makes sure the agent's harness is running. sandboxd starts it as the `agent` user, in
   `/home/agent`, with the sandbox's environment plus what the adapter adds. Its stdio goes over the tunnel.
   One harness process serves all of an agent's sessions.
3. If the harness doesn't know the session yet (new process after a sleep or restart), the server uses
   `session/load` when the harness supports it, and `session/resume` otherwise. History the harness replays
   while loading is not stored again. Without either capability, a new ACP session starts and the log records a
   notice that the context was reset.
4. `session/prompt` runs. Every `session/update` the harness sends is appended to the session's event log and
   streamed to everyone watching.

The client file system and terminal capabilities are not advertised. The harness uses its own tools inside
its sandbox.

## Event log and streaming

Each session has an append-only log of events, each with a sequence number. Event types are `prompt`, `update`
(an ACP `SessionUpdate`, stored as received), `permission`, `permission_resolved`, `turn_end`, `error` and
`notice`. The stream (`GET /api/v1/sessions/{id}/stream?after=<seq>`, WebSocket) sends every stored event after
the cursor and then live events, with no gap or duplicate between the two. A browser that reloads mid-turn
reconnects with the last sequence number it saw and continues. `GET /sessions/{id}/events` returns the same log
as JSON.

## Permissions

Each agent has a permission policy:

- `auto-approve` (default): the sandbox is the safety boundary. Requests are approved with the harness's
  one-time "allow" option and logged as auto-approved.
- `ask`: connected operators see the request with the harness's options. The turn waits until someone answers
  or cancels.

## Signing in a harness

API keys go through the egress proxy: create a secret (for example the Anthropic preset), attach it to the agent
with an environment variable (`ANTHROPIC_API_KEY`), and rebuild. The sandbox only ever sees a placeholder.

Subscription logins use the harness's own login. Agent admins open **Overview → Sign in**. The server starts the
harness, reads the login methods it advertises in `initialize`, and runs the chosen method in the agent's web
terminal. For Claude Code that is `claude /login`: the terminal shows the login URL, and the code is pasted back
into it. The harness stores the resulting token on the agent's home volume, so it survives sleep and rebuilds.
Such agents are marked "credentials inside sandbox", because the agent can read its own login.

## Harness adapters

An adapter (`internal/harness/<name>`) says how to start the harness in ACP mode, which environment it needs
(never secret values), which secret presets fit it, where it keeps logins, and how to install it in a custom
image.

| Harness | ACP entry point | In the base image |
| --- | --- | --- |
| `claude-code` | `claude-agent-acp` ([@agentclientprotocol/claude-agent-acp](https://www.npmjs.com/package/@agentclientprotocol/claude-agent-acp) 0.107.0, which bundles the Claude Agent SDK and Claude Code) | yes |
| `fake` | `/opt/agenthold/fakeagent`, a scripted agent for tests and UI work (development only) | shipped with sandboxd |

For a custom image, install the harness so that its command is on `PATH` for the `agent` user. For Claude Code:
`npm install -g @agentclientprotocol/claude-agent-acp@0.107.0` (Node.js 20 or newer).

## Base image

`images/base` (`make base-image`) is Debian trixie with Node.js 24, the Claude Code ACP adapter, git, Python 3,
build tools, ripgrep, jq and headless Chromium (`CHROME_BIN`, Playwright and Puppeteer variables set). The
`agent` user has passwordless sudo inside its own sandbox.

## End-to-end checks

Against the dev stack (`make dev`, `make base-image`):

- `scripts/e2e/sessions.sh`: the scripted harness edits files in a real sandbox, a reload mid-turn continues
  from the cursor, and a terminal login survives sleep.
- `scripts/e2e/claude-harness.sh`: starts the real Claude Code harness and shows what it advertises.
