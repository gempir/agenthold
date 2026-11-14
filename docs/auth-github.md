# GitHub

GitHub sign-in uses plain OAuth2, not OpenID Connect: the server exchanges the code for an access token and
then reads the user and their emails from the GitHub API.

## Create an OAuth App

1. GitHub: Settings, Developer settings, OAuth Apps, New OAuth App (or the same under an organization).
2. Homepage URL: your `public_url`.
3. Authorization callback URL: `<public_url>/api/v1/auth/callback/<provider id>`, for example
   `https://agenthold.example.com/api/v1/auth/callback/github`. For local dev:
   `http://localhost:8080/api/v1/auth/callback/github`.
4. Register, then generate a client secret.

Requested scopes are `read:user` and `user:email` (the defaults; the email scope is needed to read the
primary verified email even if it is private).

## Configure

```yaml
auth:
  providers:
    - id: github
      type: github
      name: GitHub
      client_id: ${GITHUB_CLIENT_ID}
      client_secret: ${GITHUB_CLIENT_SECRET}
      # Optional: immutable GitHub user IDs allowed to sign in.
      github_allowed_ids: ["9765622"]
```

GitHub OAuth Apps do not provide a private-user setting. To restrict access, configure
`github_allowed_ids`; the server rejects other accounts after GitHub identifies them and before
creating a user or session. An empty or omitted list allows any GitHub account. Find a user's
immutable numeric ID from their GitHub API profile (`https://api.github.com/users/<login>`).

For `make dev`, export `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET` in your shell, uncomment the GitHub block
in `deploy/dev/agenthold.yaml`, and restart the stack.

## GitHub Enterprise Server

```yaml
    - id: ghe
      type: github
      name: Company GitHub
      github_url: https://github.example.com
      github_api_url: https://github.example.com/api/v3
      client_id: ${GHE_CLIENT_ID}
      client_secret: ${GHE_CLIENT_SECRET}
```

`github_url` defaults to `https://github.com` and `github_api_url` to `https://api.github.com`.
