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

`.github/workflows/docs.yml` builds the site for every change under `docs/` (and `api/openapi.yaml`), and
deploys it with `wrangler deploy` when `main` changes. It serves the site from a Cloudflare Worker with static
assets, on the custom domain `agenthold.dev` (see `wrangler.jsonc`). One-time setup:

1. Create a Cloudflare API token with the "Edit Cloudflare Workers" template, plus Zone > DNS > Edit for the
   `agenthold.dev` zone (the custom domain creates its DNS record).
2. Add it to the repository as the secret `CLOUDFLARE_API_TOKEN`, and the account id as `CLOUDFLARE_ACCOUNT_ID`.
   Until both exist the deploy step is skipped and the workflow stays green.
3. The hostname `agenthold.dev` must not already have a DNS record or tunnel route. The live app used to
   be served there and now lives at `demo.agenthold.dev`; move it first (see below).

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
5. Delete the old `agenthold.dev` public hostname and its DNS record, then run the docs workflow (or merge a
   docs change): the Worker's custom domain claims `agenthold.dev`.
