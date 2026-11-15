-- +goose Up
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

-- Whose account logins the agent's harness uses ('' = none: the harness uses
-- what is inside its sandbox, or an attached API key secret). Only that user
-- can choose it, for an agent they administer.
ALTER TABLE agents ADD COLUMN login_user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX agents_login_user ON agents(login_user_id);

-- Existing agents use their owner's logins, except those already signed in
-- inside their sandbox: they keep that login.
UPDATE agents SET login_user_id = owner_id WHERE credentials_in_sandbox = 0;

-- +goose Down
DROP INDEX agents_login_user;
ALTER TABLE agents DROP COLUMN login_user_id;
DROP TABLE provider_logins;
