# Install, upgrade, back up

This guide takes a fresh Linux server to a working agent. Everything runs in Docker on one host.

## Requirements

- A Linux server (x86_64 or arm64) with at least 2 vCPUs, 4 GB RAM and 40 GB disk. Each agent needs its own
  resources on top, by default 2 CPUs and 4 GB RAM. Agents that sleep use only disk.
- A domain name pointing at the server, and ports 80 and 443 reachable from the internet. Caddy gets a TLS
  certificate automatically.
- A way to sign in: a GitHub OAuth app ([auth-github.md](auth-github.md)) or any OpenID Connect provider
  ([auth-keycloak.md](auth-keycloak.md)).

## 1. Install Docker

Docker 28 or newer is required.

```sh
curl -fsSL https://get.docker.com | sh
docker version --format '{{.Server.Version}}'
```

## 2. Get agenthold

```sh
git clone https://github.com/gempir/agenthold.git
cd agenthold/deploy/production
cp .env.example .env
```

Use published images (default) or build them from source:

```sh
# Option A: published release images. Nothing to do; `docker compose up` pulls them.

# Option B: build from source (takes a few minutes)
echo 'AGENTHOLD_IMAGE=agenthold:local' >> .env
echo 'AGENTHOLD_BASE_IMAGE=agenthold/base:local' >> .env
docker compose build
docker build -t agenthold/base:local ../../images/base
```

## 3. Configure

Edit `.env`:

- `AGENTHOLD_DOMAIN`: your domain, for example `agents.example.com`.
- `AGENTHOLD_MASTER_KEY`: run `openssl rand -base64 32` once and paste the result. **Back this value up.**
  Stored secrets cannot be decrypted without it.
- Sign-in: for GitHub, create an OAuth app with the callback URL
  `https://<domain>/api/v1/auth/callback/github` and fill in `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET`.

For OpenID Connect instead (Keycloak, Authentik, Okta, Google, ...), uncomment the `oidc` provider in
`agenthold.yaml`. Register the callback URL `https://<domain>/api/v1/auth/callback/oidc`, and add `OIDC_ISSUER`,
`OIDC_CLIENT_ID` and `OIDC_CLIENT_SECRET` to `.env`. Comment out the `github` provider if you do not use it:
the server refuses to start with a provider that has no client id. You can also keep both.

`agenthold.yaml` also sets the agent defaults and the address range for agent networks
(`AGENTHOLD_NETWORK_POOL`, default `10.213.0.0/16`). Pick a range that does not overlap your LAN or VPN.

## 4. Start

```sh
docker compose up -d
docker compose logs -f agenthold   # wait for "egress: firewall installed" and "listening"
```

If you use the published base image, pull it now, so creating the first agent does not wait for the download:
`docker pull ghcr.io/gempir/agenthold-base:latest`.

## 5. First agent

1. Open `https://<domain>` and sign in. The first login creates your user and a personal workspace.
2. **New agent**: give it a name and keep the defaults (base image, Claude Code). Wait until it shows
   *running*.
3. Sign the harness in. Either open **Overview → Sign in** and follow the login in the terminal (for example a
   Claude subscription), or add an API key under the workspace's **Secrets** (preset "Anthropic API key"), attach it
   to the agent with the variable `ANTHROPIC_API_KEY` on the **Network** tab, and rebuild the agent.
   To try it without an account, set the agent's harness to **OpenCode**, which has free models.
4. Open **Chat**, start a session and ask it to do something, for example "create hello.txt with a haiku".

## Upgrade

```sh
cd agenthold/deploy/production
docker compose exec agenthold agenthold backup        # see "Back up" below
git pull                                              # new compose file and docs
docker compose pull                                   # or: docker compose build
docker compose up -d
```

Running agents keep running. The new server reconnects to them, reattaches to their networks and reinstalls the
firewall. While the server is down, sandboxes have no network access (it fails closed) and pick up again on their
own. Database migrations run automatically at startup. If agents are marked *rebuild pending* after an upgrade,
rebuild them when convenient to get the new image.

## Back up

Two parts: the server's data, and the agents' home volumes.

```sh
# 1. Server data: a consistent snapshot of the database, the master key and the egress CA.
#    Safe while running. Writes /data/backups/agenthold-<time>.tar.gz inside the data volume.
docker compose exec agenthold agenthold backup
docker compose cp agenthold:/data/backups ./backups

# 2. Agent home volumes (files, harness logins, conversations kept by harnesses).
#    For a consistent copy, stop agents or the stack first.
docker compose stop agenthold
./backup-volumes.sh backup ./backups/volumes
docker compose start agenthold
```

Store backups somewhere other than this server, and treat them as secret: they contain the master key, and
harness logins can be inside agent volumes.

## Restore

On a new or reset server, after steps 1 to 3:

```sh
docker compose create agenthold                       # creates the volumes without starting
docker compose cp ./backups/agenthold-<time>.tar.gz agenthold:/data/restore.tar.gz
docker compose run --rm --no-deps agenthold restore /data/restore.tar.gz
./backup-volumes.sh restore ./backups/volumes
docker compose up -d
```

Agents come back *sleeping* or *error* ("sandbox no longer exists"). Rebuild each agent to recreate its
container around the restored home volume. Use the same `AGENTHOLD_MASTER_KEY` as before, or the master key from
the backup: the restore puts `master.key` back, and the environment variable takes precedence if set.

## Troubleshooting

- `docker compose logs agenthold` shows startup problems. "egress proxy unavailable" means the container lacks
  `CAP_NET_ADMIN`. Sandboxes then have no network at all.
- An agent stuck in *error* shows why on its page. *Start* or *Rebuild* retries.
- Sign-in errors are about the callback URL most of the time. It must be exactly
  `https://<domain>/api/v1/auth/callback/<provider id>`.
