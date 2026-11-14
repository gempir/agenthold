# Authentication

agenthold has no passwords of its own. Users sign in through an external identity provider, configured in the
server config file (path in `AGENTHOLD_CONFIG`, YAML, `${VAR}` references are expanded from the environment).

Supported provider types:

- `oidc`: any OpenID Connect provider (Keycloak, Authentik, Okta, Google, ...). See [auth-keycloak.md](auth-keycloak.md).
- `github`: GitHub or GitHub Enterprise Server via OAuth2. See [auth-github.md](auth-github.md).

## How sign-in works

1. The browser visits `/api/v1/auth/login/<provider id>`.
2. The server redirects to the provider (authorization code flow; OIDC uses PKCE).
3. The provider redirects back to `<public_url>/api/v1/auth/callback/<provider id>`.
4. The server exchanges the code, reads the user's identity and email, and starts a browser session.

An identity is stored as (provider `id`, subject). Changing a provider's `id` detaches existing users from it,
so pick it once. Accounts are never merged by email address: signing in with Keycloak and with GitHub
creates two separate users, because an email claim alone does not prove that both logins belong to the same person.

An invite restricted to an email address can only be accepted by a user whose provider reported that email
as verified.

On a user's first login a personal workspace is created for them. Further workspaces and invites are managed
inside the app.

## Sessions

Sessions are cookie based and expire after `session_ttl` without use (default `720h`, 30 days).

## Config reference

```yaml
listen: ":8080"                    # default :8080
public_url: https://agenthold.example.com   # default http://localhost:8080; used to build callback URLs
data_dir: /data                    # default ./data
auth:
  session_ttl: 720h                # Go duration, default 30 days
  providers:
    - id: keycloak                 # URL path segment, unique, no / ? #
      type: oidc                   # oidc | github
      name: Keycloak               # label on the login button, defaults to id
      client_id: agenthold         # required
      client_secret: ${KC_SECRET}
      scopes: [openid, profile, email]   # optional, see defaults below
      issuer: https://sso.example.com/realms/main   # oidc, required
      discovery_url: ""            # oidc, optional
      github_url: ""               # github, optional
      github_api_url: ""           # github, optional
```

| Field | Types | Notes |
| --- | --- | --- |
| `id` | all | Used in `/api/v1/auth/login/{id}` and the callback URL. |
| `type` | all | `oidc` or `github`. |
| `name` | all | Display name. |
| `client_id`, `client_secret` | all | From the provider. Use `${ENV}` for the secret. |
| `scopes` | all | Default `openid profile email` (oidc), `read:user user:email` (github). |
| `issuer` | oidc | Must match the `iss` claim in ID tokens exactly. |
| `discovery_url` | oidc | Base URL to fetch `/.well-known/openid-configuration` from, when the server cannot reach `issuer`. |
| `github_url` | github | Default `https://github.com`. |
| `github_api_url` | github | Default `https://api.github.com`. |

Environment overrides: `AGENTHOLD_LISTEN`, `AGENTHOLD_PUBLIC_URL`, `AGENTHOLD_DATA_DIR`, `AGENTHOLD_DEV_PROXY`.

Unknown YAML keys are rejected at startup. Set `public_url` to the exact URL users open in the browser,
otherwise the redirect URI will not match what you registered with the provider.
