-- +goose Up
CREATE TABLE users (
    id          TEXT PRIMARY KEY,
    email       TEXT NOT NULL DEFAULT '',
    email_verified INTEGER NOT NULL DEFAULT 0,
    name        TEXT NOT NULL DEFAULT '',
    avatar_url  TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- One row per (provider, subject). A user can have several identities.
CREATE TABLE identities (
    provider    TEXT NOT NULL,
    subject     TEXT NOT NULL,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email       TEXT NOT NULL DEFAULT '',
    username    TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    last_login_at INTEGER NOT NULL,
    PRIMARY KEY (provider, subject)
);
CREATE INDEX identities_user ON identities(user_id);

-- Browser sessions. Only the SHA-256 of the cookie value is stored.
CREATE TABLE sessions (
    token_hash  TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    user_agent  TEXT NOT NULL DEFAULT ''
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

CREATE TABLE workspaces (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    personal    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE memberships (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX memberships_user ON memberships(user_id);

-- At most one personal workspace per user.
CREATE TABLE personal_workspaces (
    user_id      TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE
);

CREATE TABLE invites (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    role         TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    -- Optional: only a user with this verified email may accept.
    email        TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    accepted_by  TEXT REFERENCES users(id) ON DELETE SET NULL,
    accepted_at  INTEGER
);
CREATE INDEX invites_workspace ON invites(workspace_id);

-- +goose Down
DROP TABLE invites;
DROP TABLE personal_workspaces;
DROP TABLE memberships;
DROP TABLE workspaces;
DROP TABLE login_states;
DROP TABLE sessions;
DROP TABLE identities;
DROP TABLE users;
