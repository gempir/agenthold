# agenthold

Self-hostable, multi-user platform for running many persistent AI agents, each in its own sandbox.
See [PLAN.md](PLAN.md) for the design and milestones.

## Development

Requirements: Go 1.26, Node 26 with pnpm 11, Docker (OrbStack on macOS), golangci-lint v2.

```sh
make dev     # server (live reload via air) + Vite in containers -> http://localhost:8080
make test    # Go and web tests
make lint    # golangci-lint, eslint, prettier
make build   # web app + bin/agenthold + bin/sandboxd
make image   # production server image
```

In development the server proxies everything outside `/api` and `/healthz` to the Vite dev server,
so the UI hot-reloads through the server's own port. Release builds embed `web/dist` into the binary.
