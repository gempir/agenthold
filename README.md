# agenthold

Self-hostable, multi-user platform for running many persistent AI agents, each in its own sandbox.
See [PLAN.md](PLAN.md) for the design and milestones.

## Run it

Follow [the install guide](https://agenthold.dev/install/): a Linux server with Docker, a domain, and a GitHub OAuth app or any
OpenID Connect provider. `deploy/production` has the compose file.

## Documentation

The documentation is at [agenthold.dev](https://agenthold.dev), built from [`docs/`](docs) with [Nimbus](https://github.com/cloudflare/nimbus). A live instance runs at [demo.agenthold.dev](https://demo.agenthold.dev).

- [Install, upgrade, back up](https://agenthold.dev/install/), [automatic deploys from `main`](https://agenthold.dev/deploy/)
- [Authentication](https://agenthold.dev/auth/): [Keycloak and OIDC](https://agenthold.dev/auth-keycloak/), [GitHub](https://agenthold.dev/auth-github/)
- [Sandboxes](https://agenthold.dev/sandboxes/), [egress and secrets](https://agenthold.dev/egress/), [hardened runtimes](https://agenthold.dev/hardening/)
- [Sessions and harnesses](https://agenthold.dev/sessions/), [MCP servers and skills](https://agenthold.dev/tools/), [agent to agent](https://agenthold.dev/agents/)
- [Sharing and audit](https://agenthold.dev/sharing/), [API, tokens, schedules, webhooks](https://agenthold.dev/automation/)
- [Security](https://agenthold.dev/security/)

## Development

Requirements: Go 1.26, Node 26 with pnpm 11, Docker (OrbStack on macOS), golangci-lint v2.

```sh
make dev     # server (live reload via air) + Vite in containers -> http://localhost:8080
make test    # Go and web tests
make lint    # golangci-lint, eslint, prettier
make build   # web app + bin/agenthold + bin/sandboxd
make image   # production server image
scripts/e2e/run-all.sh   # end-to-end checks against make dev (needs make base-image)
cd web && pnpm e2e       # Playwright UI tests against make dev
```

In development the server proxies everything outside `/api` and `/healthz` to the Vite dev server,
so the UI hot-reloads through the server's own port. Release builds embed `web/dist` into the binary.
