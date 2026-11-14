# Sharing and audit

## Who can do what

Workspaces have members with a workspace role: `owner`, `admin` or `member`. Each agent has an effective
role for every member:

| Agent role | Can |
| --- | --- |
| `viewer` | read sessions (live), files, the request log, the network policy and who has access |
| `operator` | everything a viewer can, plus prompt, cancel, answer permission requests, open terminals, change files, start and stop |
| `admin` | everything an operator can, plus change settings, secrets, the network policy and grants, read the audit log, run harness logins, delete |

The effective role is the highest of:

- **admin**, if the user owns the agent or is an owner or admin of its workspace,
- the role of a **grant** to that user,
- the role of a **workspace grant**, which applies to every member of the agent's workspace.

Members without any of these do not see the agent at all (API requests about it return 404). All checks are made
on the server, in `internal/authz`. The UI only hides what a role cannot do.

Grants are managed on the agent's **Sharing** tab by its admins. Only workspace members can be granted access.
When someone leaves a workspace, their grants on its agents are removed.

## Watching together

Any number of people can watch a session live. The stream shows everyone who is watching, with their role.
Viewers see the conversation as it happens but cannot prompt. Every prompt in the log names the user who sent
it.

## Audit log

Agent admins see an agent's **Activity** tab: who did what, and when. It records prompts (with the prompt
text), cancels, permission answers, sessions created and deleted, terminals opened, harness logins, file
uploads, folders and deletions, starts, stops, rebuilds, setting changes, network policy changes, secrets
attached and detached, grants and deletions.
