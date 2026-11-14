# Security

## Threat model

agenthold runs code that AI agents decide to run, on behalf of many users. It assumes:

- **Agents are untrusted.** Whatever runs inside a sandbox (the harness, the model's tool calls, a prompt-injected
  web page) may try to escape, read other agents' data, steal secrets or misuse the network.
- **Users are partly trusted.** They are authenticated, and their power is bounded by their workspace and agent
  roles.
- **The host and its operator are trusted.** The server controls Docker, so it is effectively root on the host.

## Boundaries

| Boundary | How it is enforced |
| --- | --- |
| Sandbox ↔ host | Docker containers with default capabilities, no privileged mode, CPU, memory and PID limits, no Docker socket. Optional gVisor or Sysbox ([hardening.md](hardening.md)). The only privileged step, `sandboxd net-setup`, runs a read-only, statically linked binary as a short exec. |
| Sandbox ↔ network | Internal Docker network in isolated gateway mode: no route to the host or the internet. All TCP and DNS are redirected to the egress proxy by nftables in the server's namespace. Private, loopback, link-local (metadata), CGNAT and multicast destinations are never reachable. The proxy resolves names itself, so a forged SNI or Host header cannot reach a different address. Optional allowlist per agent; disallowed names get NXDOMAIN. |
| Sandbox ↔ server | Sandboxes can reach only the tunnel and the explicit proxy port on the server. The tunnel authenticates each boot with a fresh 256-bit token (only its hash is stored, accepted only while the sandbox runs). The server identifies the agent behind every call by its tunnel, never by what the sandbox says. |
| Sandbox ↔ secrets | Secret values never enter a sandbox: it sees placeholders, and the proxy injects values into HTTPS requests to the hosts in the secret's rule only. TLS is intercepted only for those hosts. Values are encrypted at rest (XChaCha20-Poly1305, AAD bound to the secret id) and never returned by the API. |
| Agent ↔ agent | Separate networks, volumes and tunnels. Agent-to-agent messages need the same workspace and operator rights of the calling agent's owner, are audited, and stop at depth 4. |
| User ↔ user | Every handler asks `internal/authz` (workspace roles, agent grants). Non-members get 404. A table-driven test checks all agent endpoints for every role. |
| Browser ↔ server | OIDC or GitHub OAuth with PKCE, state and nonce. Session cookies are `HttpOnly`, `SameSite=Lax` and `Secure` on HTTPS, and stored hashed. Cross-origin state-changing requests are rejected (`http.CrossOriginProtection`). WebSockets check the Origin. The UI gets a strict CSP, `X-Frame-Options: DENY`, `nosniff` and a referrer policy. Login redirects only go to local paths. |
| API clients | Personal tokens (256 bits, hashed, optional expiry, revocable) act with their user's permissions. A token cannot create further tokens. |
| Webhooks | Unguessable URLs (hashed), optional HMAC-SHA256 signatures (constant-time check), 256 KiB bodies. Payloads reach the agent between "untrusted data" markers it cannot forge. |

## Known limits and accepted risks

- **Kernel sharing.** Without gVisor or Sysbox, a kernel exploit in a sandbox reaches the host.
- **Harness logins inside the sandbox.** Subscription logins (for example Claude) are stored on the agent's disk,
  so the agent and its operators can read them. Such agents are marked "credentials inside sandbox". Viewers
  cannot download these files or sandboxd's boot file through the file API: the check resolves symlinks.
- **Services that echo requests.** If a secret's hosts include a service that reflects request headers back,
  the agent can read the injected value. Only list APIs you trust in a rule.
- **Open network policy.** By default an agent can reach any public address, which allows exfiltrating anything
  it can read. Use an allowlist for agents that handle sensitive data.
- **Prompt injection.** Agents act on what they read (web pages, webhook payloads, files). The platform limits
  the damage (sandbox, network policy, no secrets in the sandbox, depth limit); it cannot prevent the agent from
  being misled.
- **The server is root on the host.** It needs the Docker socket and `CAP_NET_ADMIN`. Protect the host
  accordingly.
- **No rate limiting.** There are no per-user rate limits yet. Put the server behind a proxy with limits if
  untrusted people can reach it.

## Review for the first release

**Checked:**
- **Dependencies:** `govulncheck` for Go and `pnpm audit --prod` for the web app.
- **Authorization:** all endpoints, covered by the role matrix.
- **Identity:** the login flow (state, PKCE, nonce, open redirects), session and token handling.
- **Browser protections:** CSRF and WebSocket origins.
- **Sandbox network:** egress (private destinations, SNI and Host spoofing, DNS rebinding, DNS exfiltration), the sandbox firewall, and access from the sandbox to the server.
- **Secrets:** injection scope.
- **File access:** file API access by role.
- **Triggers:** webhook signatures and body limits.
- **Agent-to-agent:** identity and depth.

**Found and fixed in this release:**
- **Dependencies:** the Go toolchain moved to 1.26.11 (standard library advisories in `net/http`, `crypto/tls`, `crypto/x509`, `net/url`), `kin-openapi` was upgraded, and the `diff` npm package moved to 8.0.3.
- **Plain HTTP:** secrets could be injected into plain-HTTP requests to a rule's host, sending the value unencrypted. Injection now happens over HTTPS only.
- **Token persistence:** a personal access token could create more tokens and so survive its own revocation. Tokens can now only be created from a browser session.
- **Body sizes:** request bodies (other than file uploads) are capped at 8 MiB, and prompts at 256 KiB.
- **Viewer file access:** viewers could download harness credential files or sandboxd's boot file through the file API, even through a symlink the agent planted. These paths are now refused after resolving symlinks.
- **Security headers:** the web UI now sends a Content Security Policy and the related headers.
- **Server recreate:** after the server container was recreated (an upgrade), running sandboxes lost their tunnel and egress until restarted. Reconcile now reattaches the server to their networks.

**Reporting:** please report vulnerabilities privately through GitHub security advisories of the repository.
