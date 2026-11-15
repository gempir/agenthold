# agenthold documentation

The site at <https://agenthold.dev>, built with [Nimbus](https://github.com/cloudflare/nimbus) on Astro.
Pages are in `src/content/docs/` (MDX), the API reference is generated from `../api/openapi.yaml`, and the
sidebar is set in `astro.config.ts`. [AGENT.md](AGENT.md) has Nimbus's guidance for editing it.

```sh
cd docs
pnpm install
pnpm dev          # http://localhost:4321
pnpm run check    # structure, content and pending upgrades
pnpm run lint:docs
pnpm run typecheck
pnpm build        # static site in dist/
```

Link pages with site paths, `[Install](/install/)`; the lint fails the CI build on broken internal links.
The slug `api` belongs to the generated API reference, so the page about tokens, schedules and webhooks is
`automation`.

## Publishing

Cloudflare Workers Builds builds and deploys the site from `main`: a Worker with static assets (`wrangler.jsonc`,
name `agenthold-docs`). There is no GitHub Actions workflow for the docs; the checks run inside that build.

The Worker project is connected to this repository with these settings:

| Setting | Value |
| --- | --- |
| Project name | `agenthold-docs` (must match `name` in `wrangler.jsonc`) |
| Root directory | `docs` |
| Build command | `pnpm run build && pnpm run lint:docs && pnpm run typecheck` |
| Deploy command | `npx wrangler deploy` |
| Variable `NODE_VERSION` | `26` (Nimbus needs Node 22.23.4 or later) |

Attach the `agenthold.dev` custom domain in the Workers dashboard. The native demo deployment is being
rebuilt separately; no application deployment is triggered by the documentation workflow.
