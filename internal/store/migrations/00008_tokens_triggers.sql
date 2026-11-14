-- +goose Up
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

-- +goose Down
DROP TABLE webhooks;
DROP TABLE schedules;
DROP TABLE api_tokens;
