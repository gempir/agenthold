# Agent to agent

Agents in a workspace can work together. Every session gets a platform MCP server named `agenthold` with these
tools:

| Tool | Does |
| --- | --- |
| `list_agents` | lists the workspace's other agents that this agent may message, with their state |
| `send_message(agent, text)` | prompts another agent and returns at once; it works on it in its own session |
| `ask(agent, text)` | prompts another agent, waits for its turn to end and returns its final answer |

`agent` is a name or an id. Messaging a sleeping agent wakes it.

## Rules

- Both agents must be in the same workspace.
- The calling agent's **owner** must hold at least the operator role on the target. Messages are sent in the
  owner's name ("agent planner (for Alice)") and appear in the target's audit log.
- Every turn has a depth: 0 when a person, a schedule or a webhook started it, and one more than the deepest turn
  the calling agent is running when an agent started it. A call that would exceed depth 4 fails with an error
  ("too many agents in a row"), which stops loops such as A asking B asking A. The server computes the depth; the
  sandbox cannot influence it.

## Linked sessions

Messages from agent A go to a session on the target called "From A". It is reused while it is idle, and a second
one is opened if it is busy. The session records which agent and session started it, and the UI links back to it.

## How it works

The harness starts the platform MCP server as `/opt/agenthold/sandboxd mcp` (stdio). It talks to the sandbox's
sandboxd over a unix socket, `~/.agenthold/platform.sock`, owned by the agent user. sandboxd forwards each call over
its tunnel to the server, which identifies the calling agent by the tunnel, not by anything the sandbox says. The
per-boot token stays readable only by root.

The end-to-end check runs against the dev stack: `scripts/e2e/agents.sh`. It covers the scripted harness, a
loop stopped by the limit, and a real OpenCode model delegating through the `ask` tool.
