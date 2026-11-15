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

The custom domain is attached in the dashboard (Worker > Settings > Domains & Routes > Add > Custom domain >
`agenthold.dev`), once the hostname is free: the live app used to be served there and now lives at
`demo.agenthold.dev`, so move it first.

### Moving the app to demo.agenthold.dev

In this order, so the app is never without a working sign-in:

1. Cloudflare Zero Trust > Networks > Tunnels > your tunnel > Public hostnames: add `demo.agenthold.dev`
   pointing at `http://agenthold:8080`.
2. GitHub > Settings > Developer settings > OAuth Apps > agenthold: set the callback URL to
   `https://demo.agenthold.dev/api/v1/auth/callback/github` and the homepage URL to `https://agenthold.dev`.
3. On the host, set `AGENTHOLD_DOMAIN=demo.agenthold.dev` in `deploy/production/.env`, then restart with
   `docker compose --env-file .env --env-file .deploy.env up -d agenthold`.
4. In GitHub, set the repository variable `DEPLOY_URL` to `https://demo.agenthold.dev` so the deploy
   workflow checks the new address.
5. Delete the old `agenthold.dev` public hostname and its DNS record, then add `agenthold.dev` as the Worker's
   custom domain.
