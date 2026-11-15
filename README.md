# agenthold

Self-hostable, multi-user platform for running many persistent AI agents, each in its own sandbox.
See [PLAN.md](PLAN.md) for the design and milestones.

## Run it

Follow [docs/install.md](docs/install.md): a Linux server with Docker, a domain, and a GitHub OAuth app or any
OpenID Connect provider. `deploy/production` has the compose file.

## Documentation

- [Install, upgrade, back up](docs/install.md), [automatic deploys from `main`](docs/deploy.md)
- [Authentication](docs/auth.md): [Keycloak and OIDC](docs/auth-keycloak.md), [GitHub](docs/auth-github.md)
- [Sandboxes](docs/sandboxes.md), [egress and secrets](docs/egress.md), [hardened runtimes](docs/hardening.md)
- [Sessions and harnesses](docs/sessions.md), [MCP servers and skills](docs/tools.md), [agent to agent](docs/agents.md)
- [Sharing and audit](docs/sharing.md), [API, tokens, schedules, webhooks](docs/api.md)
- [Security](docs/security.md)

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
