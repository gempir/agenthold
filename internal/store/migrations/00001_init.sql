-- +goose Up
-- Timestamps are unix seconds unless noted. Booleans are INTEGER 0/1.

CREATE TABLE users (
    id             TEXT PRIMARY KEY,
    email          TEXT NOT NULL DEFAULT '',
    email_verified INTEGER NOT NULL DEFAULT 0,
    name           TEXT NOT NULL DEFAULT '',
    avatar_url     TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE INDEX users_email ON users(email);

-- One row per (provider, subject). A user can have several identities.
-- The local admin from auth.local is provider 'local', subject = username.
CREATE TABLE identities (
    provider      TEXT NOT NULL,
    subject       TEXT NOT NULL,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email         TEXT NOT NULL DEFAULT '',
    username      TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER NOT NULL,
    PRIMARY KEY (provider, subject)
);
CREATE INDEX identities_user ON identities(user_id);

-- Browser sessions. Only the SHA-256 of the cookie value is stored.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);

-- Pending OAuth/OIDC authorization requests (state, PKCE verifier, nonce).
CREATE TABLE login_states (
    state       TEXT PRIMARY KEY,
    provider    TEXT NOT NULL,
    verifier    TEXT NOT NULL,
    nonce       TEXT NOT NULL,
    redirect_to TEXT NOT NULL,
    expires_at  INTEGER NOT NULL
);

CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL, -- first characters, to recognize a token
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    expires_at   INTEGER
);
CREATE INDEX api_tokens_user ON api_tokens(user_id);

-- Provider logins (Claude Code, Codex) that live on a user's account instead
-- of inside each sandbox.
CREATE TABLE provider_logins (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The harness whose login this is ('claude-code', 'codex').
    provider       TEXT NOT NULL,
    -- How it was connected ('oauth-token', 'api-key', 'chatgpt', ...).
    kind           TEXT NOT NULL,
    -- XChaCha20-Poly1305(master key, credential, aad = id)
    ciphertext     BLOB NOT NULL,
    -- Random; placeholders in sandboxes derive from it. Stays the same when
    -- the login is reconnected or refreshed, so running harnesses keep working.
    seed           TEXT NOT NULL UNIQUE,
    -- Non-secret description (key hint, e-mail, plan).
    label          TEXT NOT NULL DEFAULT '',
    expires_at     INTEGER NOT NULL DEFAULT 0,
    refreshed_at   INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'error')),
    status_message TEXT NOT NULL DEFAULT '',
    last_used_at   INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    UNIQUE (user_id, provider)
);

CREATE TABLE agents (
    id                     TEXT PRIMARY KEY,
    owner_id               TEXT NOT NULL REFERENCES users(id),
    name                   TEXT NOT NULL,
    image                  TEXT NOT NULL,
    harness                TEXT NOT NULL DEFAULT 'claude-code',
    model                  TEXT NOT NULL DEFAULT '',
    cpus                   REAL NOT NULL,
    memory_mb              INTEGER NOT NULL,
    idle_minutes           INTEGER NOT NULL DEFAULT 15,
    permission_policy      TEXT NOT NULL DEFAULT 'auto-approve',
    -- Set once a harness login completed in the agent's terminal: the agent
    -- can read its own credentials.
    credentials_in_sandbox INTEGER NOT NULL DEFAULT 0,
    -- Whose account logins the harness uses ('' = none: the harness uses what
    -- is inside its sandbox, or an attached API key secret). Only the owner
    -- sets it, to themselves.
    login_user_id          TEXT NOT NULL DEFAULT '',
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL
);
CREATE INDEX agents_owner ON agents(owner_id);
CREATE INDEX agents_login_user ON agents(login_user_id);

-- Users besides the owner who may use an agent.
CREATE TABLE agent_shares (
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (agent_id, user_id)
);
CREATE INDEX agent_shares_user ON agent_shares(user_id);

-- The persisted sandbox state machine, one row per agent.
CREATE TABLE sandboxes (
    agent_id         TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    driver           TEXT NOT NULL,
    driver_id        TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL CHECK (state IN
                       ('creating', 'starting', 'running', 'stopping', 'sleeping', 'rebuilding', 'destroying', 'error')),
    error            TEXT NOT NULL DEFAULT '',
    -- The spec the current sandbox was built from (JSON), to detect pending rebuilds.
    applied_spec     TEXT NOT NULL DEFAULT '',
    state_changed_at INTEGER NOT NULL,
    -- SHA-256 of the token the current boot of the sandbox uses to dial the tunnel.
    boot_token_hash  TEXT NOT NULL DEFAULT '',
    subnet           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sandboxes_subnet ON sandboxes(subnet);

CREATE TABLE secrets (
    id          TEXT PRIMARY KEY,
    owner_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    -- XChaCha20-Poly1305(master key, value, aad = id)
    ciphertext  BLOB NOT NULL,
    -- JSON secrets.Rule
    rule        TEXT NOT NULL,
    -- What the sandbox sees instead of the value.
    placeholder TEXT NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (owner_id, name)
);

CREATE TABLE agent_secrets (
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    secret_id  TEXT NOT NULL REFERENCES secrets(id) ON DELETE CASCADE,
    -- Environment variable that holds the placeholder inside the sandbox ('' for none).
    env_var    TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (agent_id, secret_id)
);
CREATE INDEX agent_secrets_secret ON agent_secrets(secret_id);

-- MCP servers. agent_id = '' means the owner's library (attached per agent);
-- otherwise the server belongs to that agent alone.
CREATE TABLE mcp_servers (
    id         TEXT PRIMARY KEY,
    owner_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id   TEXT NOT NULL DEFAULT '',
    name       TEXT NOT NULL,
    transport  TEXT NOT NULL CHECK (transport IN ('stdio', 'http')),
    command    TEXT NOT NULL DEFAULT '',
    args       TEXT NOT NULL DEFAULT '[]', -- JSON array
    env        TEXT NOT NULL DEFAULT '{}', -- JSON object
    url        TEXT NOT NULL DEFAULT '',
    headers    TEXT NOT NULL DEFAULT '{}', -- JSON object
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (owner_id, agent_id, name)
);
CREATE INDEX mcp_servers_agent ON mcp_servers(agent_id);

CREATE TABLE agent_mcp_servers (
    agent_id  TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    server_id TEXT NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    PRIMARY KEY (agent_id, server_id)
);

-- A user's skills library (Agent Skills: a SKILL.md per skill).
CREATE TABLE skills (
    id          TEXT PRIMARY KEY,
    owner_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL, -- directory name: lowercase letters, digits, dashes
    description TEXT NOT NULL,
    content     TEXT NOT NULL, -- SKILL.md body (without front matter)
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (owner_id, name)
);

CREATE TABLE agent_skills (
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    PRIMARY KEY (agent_id, skill_id)
);

CREATE TABLE agent_sessions (
    id                TEXT PRIMARY KEY,
    agent_id          TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    -- The harness's session id ('' until first used).
    acp_session_id    TEXT NOT NULL DEFAULT '',
    title             TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'idle' CHECK (status IN ('idle', 'running')),
    created_by        TEXT NOT NULL REFERENCES users(id),
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    -- Sessions started by another agent (send_message / ask) record where from.
    origin_agent_id   TEXT NOT NULL DEFAULT '',
    origin_session_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX agent_sessions_agent ON agent_sessions(agent_id, updated_at);

-- Append-only event log of a session.
CREATE TABLE agent_session_events (
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    seq        INTEGER NOT NULL,
    at         INTEGER NOT NULL, -- unix milliseconds
    type       TEXT NOT NULL,
    user_id    TEXT NOT NULL DEFAULT '',
    data       TEXT NOT NULL,
    PRIMARY KEY (session_id, seq)
);

-- A schedule prompts its session on a cron schedule.
CREATE TABLE schedules (
    id          TEXT PRIMARY KEY,
    agent_id    TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    session_id  TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    cron        TEXT NOT NULL,
    timezone    TEXT NOT NULL DEFAULT 'UTC',
    prompt      TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_by  TEXT NOT NULL REFERENCES users(id),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    next_run_at INTEGER NOT NULL,
    last_run_at INTEGER,
    last_error  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX schedules_due ON schedules(enabled, next_run_at);

-- A webhook prompts its session when its secret URL receives a request.
CREATE TABLE webhooks (
    id               TEXT PRIMARY KEY,
    agent_id         TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    session_id       TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    token_hash       TEXT NOT NULL UNIQUE,
    prompt_template  TEXT NOT NULL,
    -- Optional HMAC-SHA256 key, encrypted with the master key.
    signing_secret   BLOB,
    signature_header TEXT NOT NULL DEFAULT '',
    enabled          INTEGER NOT NULL DEFAULT 1,
    created_by       TEXT NOT NULL REFERENCES users(id),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    last_delivery_at INTEGER,
    last_error       TEXT NOT NULL DEFAULT ''
);

-- Who did what. agent_id = '' for account-level actions (provider logins).
CREATE TABLE audit_log (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    at       INTEGER NOT NULL, -- unix milliseconds
    agent_id TEXT NOT NULL DEFAULT '',
    user_id  TEXT NOT NULL,
    action   TEXT NOT NULL,
    target   TEXT NOT NULL DEFAULT '',
    details  TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_agent ON audit_log(agent_id, id);
CREATE INDEX audit_log_user ON audit_log(user_id, id);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE webhooks;
DROP TABLE schedules;
DROP TABLE agent_session_events;
DROP TABLE agent_sessions;
DROP TABLE agent_skills;
DROP TABLE skills;
DROP TABLE agent_mcp_servers;
DROP TABLE mcp_servers;
DROP TABLE agent_secrets;
DROP TABLE secrets;
DROP TABLE sandboxes;
DROP TABLE agent_shares;
DROP TABLE agents;
DROP TABLE provider_logins;
DROP TABLE api_tokens;
DROP TABLE login_states;
DROP TABLE sessions;
DROP TABLE identities;
DROP TABLE users;
