# Egress and secrets

Sandboxes have no route to the internet of their own. All of their traffic goes through the server,
which applies the agent's network policy, injects secrets and logs every connection.

## How traffic is intercepted

Each agent network is an internal Docker network in isolated gateway mode (Docker 28 or newer), so it has
no gateway to the host. After every start the server runs `sandboxd net-setup` as a privileged exec in the
sandbox. It points the sandbox's default route and DNS at the server's address on that network (`.2`). The
sandbox itself never gets `NET_ADMIN`.

Inside its own network namespace, the server (which needs `CAP_NET_ADMIN`) installs nftables rules:

- DNS (UDP and TCP port 53) from agent networks goes to the server's resolver.
- All other TCP that is not addressed to the server goes to the transparent proxy. The proxy reads the
  original destination with `SO_ORIGINAL_DST`.
- Sandboxes can reach only the tunnel port and the explicit proxy port on the server itself. Other traffic
  to the server, and anything the server would have to forward (UDP other than DNS, ICMP), is dropped.

This catches programs that ignore `HTTPS_PROXY`: SSH, raw sockets, package managers. `HTTPS_PROXY` is still
set in every sandbox as a hint, pointing at the explicit proxy, which goes through the same checks.

The proxy decides by name:

| Traffic | Name used for the policy |
| --- | --- |
| TLS | the SNI of the ClientHello |
| Plain HTTP | the `Host` header, per request |
| Other TCP (SSH, databases, ...) | the name the agent resolved to that address through the server's DNS |

Upstream connections are made to what the **name** resolves to on the server, not to the address the
client used, so a forged SNI cannot reach a different host. Private, loopback, link-local (including
cloud metadata at 169.254.169.254), CGNAT, multicast and reserved addresses are never reachable, whatever
the policy.

## Network policy

Per agent, set by its admins. Changes take effect within seconds.

- `open` (default): any public address and port.
- `allowlist`: only the listed entries. Entries look like `github.com`, `*.npmjs.org` (subdomains only),
  `github.com:22`, `registry.example.com:*` or `203.0.113.0/24:443`. Without a port, 80 and 443 are allowed.
  DNS queries for names that are not on the list get NXDOMAIN, so DNS cannot be used to smuggle data out.

## Secrets

A secret has a value, encrypted at rest with XChaCha20-Poly1305 under the master key (`secrets.master_key_file`,
default `<data_dir>/master.key`, created on first start, or `AGENTHOLD_MASTER_KEY`; back it up). It also has
an injection rule:

- **hosts**: where the secret may be sent (`api.anthropic.com`, `*.example.com`),
- **header**: which request header to set,
- **template**: the header value; `{{.Value}}` is the secret, and `base64` is available, for example
  `Basic {{base64 (print "x-access-token:" .Value)}}` for git over HTTPS.

Presets exist for Anthropic, OpenAI, GitHub, GitLab and OpenRouter.

Attaching a secret to an agent can also set an environment variable. Inside the sandbox that variable holds
a **placeholder** (`agenthold-placeholder-...`), never the value. For requests to the rule's hosts, the proxy
terminates TLS with the installation's CA, sets the header, and replaces the placeholder wherever it appears
in the request headers or query string. TLS to hosts without an injection rule is passed through untouched.

The CA is generated on first start (`<data_dir>/egress/ca.crt`). It is written into every sandbox on boot,
added to the system trust store where the image supports it, and exported through `SSL_CERT_FILE`,
`NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO` and `AWS_CA_BUNDLE`.

Account logins (Claude Code, Codex, see [sessions.md](sessions.md#account-logins-the-normal-way)) use the same
mechanism with narrower rules: the placeholder is replaced only inside the one header the harness sends it in, the
header is never added, and some rules are limited to URL paths. For a Codex ChatGPT login the proxy also answers the
harness's token refresh requests itself, so the account's rotating refresh token never leaves the server.

Only list hosts you trust in a rule. A service that echoes request headers back (for example an HTTP echo
API) would show the injected value to the agent.

## Request log

Every connection and HTTP request is logged per agent: time, kind, host, port, method, path, status,
allowed or denied and why, whether a secret was injected, and bytes transferred. Spliced connections are
logged when they close. The newest `egress.log_keep` entries (default 5000) are kept.

## Ports and configuration

```yaml
egress:
  disabled: false       # true: sandboxes have no network access at all
  proxy_port: 15001     # transparent proxy
  explicit_port: 15002  # HTTPS_PROXY target
  dns_port: 15053
  dns_upstream: ""      # default: the server's /etc/resolv.conf
  log_keep: 5000
secrets:
  master_key_file: ""   # default <data_dir>/master.key
```

The end-to-end check for this milestone runs against the dev stack: `make dev`, then `scripts/e2e/egress.sh`.
