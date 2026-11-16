-- +goose Up
-- An exposure serves a guest TCP port over HTTP at <name><preview.host_suffix>.
CREATE TABLE exposures (
    id              TEXT PRIMARY KEY,
    agent_id        TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    port            INTEGER NOT NULL,
    -- A DNS label: lowercase letters, digits and hyphens, 1 to 40 characters.
    name            TEXT NOT NULL UNIQUE,
    -- private (owner and shared users), authenticated (any user) or public.
    access          TEXT NOT NULL DEFAULT 'private',
    -- Whether visitors who are neither owner nor shared user may wake the
    -- machine (and keep it awake). The owner and shared users always do.
    wake_on_request INTEGER NOT NULL DEFAULT 0,
    -- A user id, or agent:<id> when the agent exposed its own port.
    created_by      TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    UNIQUE (agent_id, port)
);

-- Names freed by deleting or renaming an exposure. Only the same owner may
-- reuse one before the cooldown ends, so a name never moves to a stranger's
-- app while browsers still trust it.
CREATE TABLE exposure_released_names (
    name        TEXT PRIMARY KEY,
    owner_id    TEXT NOT NULL,
    released_at INTEGER NOT NULL
);

-- Single-use codes the main host hands to an exposure host (60 s). They are
-- bound to the exposure and to the browser that started the sign-in
-- (state_hash: the hash of its state cookie).
CREATE TABLE preview_codes (
    code_hash   TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    exposure_id TEXT NOT NULL REFERENCES exposures(id) ON DELETE CASCADE,
    host        TEXT NOT NULL,
    state_hash  TEXT NOT NULL,
    expires_at  INTEGER NOT NULL
);
CREATE INDEX preview_codes_expires ON preview_codes(expires_at);

-- Sessions behind the host-only preview cookie of one exposure.
CREATE TABLE preview_sessions (
    token_hash  TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    exposure_id TEXT NOT NULL REFERENCES exposures(id) ON DELETE CASCADE,
    host        TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
);
CREATE INDEX preview_sessions_expires ON preview_sessions(expires_at);
CREATE INDEX preview_sessions_user ON preview_sessions(user_id, exposure_id, created_at);

-- Users who agreed to show their identity to an authenticated exposure of a
-- machine they have no access to.
CREATE TABLE preview_consents (
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    exposure_id TEXT NOT NULL REFERENCES exposures(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (user_id, exposure_id)
);

-- +goose Down
DROP TABLE preview_consents;
DROP TABLE preview_sessions;
DROP TABLE preview_codes;
DROP TABLE exposure_released_names;
DROP TABLE exposures;
