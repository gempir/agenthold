-- +goose Up
-- An exposure serves a guest TCP port over HTTP at <name><preview.host_suffix>.
CREATE TABLE exposures (
    id         TEXT PRIMARY KEY,
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    port       INTEGER NOT NULL,
    -- A DNS label: lowercase letters, digits and hyphens, 1 to 40 characters.
    name       TEXT NOT NULL UNIQUE,
    -- private (owner and shared users), authenticated (any user) or public.
    access     TEXT NOT NULL DEFAULT 'private',
    -- A user id, or agent:<id> when the agent exposed its own port.
    created_by TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (agent_id, port)
);

-- Single-use codes the main host hands to an exposure host (60 s).
CREATE TABLE preview_codes (
    code_hash  TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    host       TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);

-- Sessions behind the host-only ah_preview cookie of one exposure host.
CREATE TABLE preview_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    host       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE preview_sessions;
DROP TABLE preview_codes;
DROP TABLE exposures;
