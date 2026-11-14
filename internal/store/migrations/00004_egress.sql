-- +goose Up
ALTER TABLE sandboxes ADD COLUMN subnet TEXT NOT NULL DEFAULT '';
CREATE INDEX sandboxes_subnet ON sandboxes(subnet);

-- JSON egress.Policy; empty means the default (open).
ALTER TABLE agents ADD COLUMN network_policy TEXT NOT NULL DEFAULT '';

CREATE TABLE secrets (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    owner_id     TEXT NOT NULL REFERENCES users(id),
    name         TEXT NOT NULL,
    -- XChaCha20-Poly1305(master key, value, aad = id)
    ciphertext   BLOB NOT NULL,
    -- JSON secrets.Rule
    rule         TEXT NOT NULL,
    -- What the sandbox sees instead of the value.
    placeholder  TEXT NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE (workspace_id, name)
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

CREATE TABLE egress_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    at         INTEGER NOT NULL, -- unix milliseconds
    kind       TEXT NOT NULL,
    host       TEXT NOT NULL,
    ip         TEXT NOT NULL,
    port       INTEGER NOT NULL,
    method     TEXT NOT NULL,
    path       TEXT NOT NULL,
    status     INTEGER NOT NULL,
    allowed    INTEGER NOT NULL,
    reason     TEXT NOT NULL,
    injected   INTEGER NOT NULL,
    bytes_up   INTEGER NOT NULL,
    bytes_down INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL
);
CREATE INDEX egress_events_agent ON egress_events(agent_id, id);

-- +goose Down
DROP TABLE egress_events;
DROP TABLE agent_secrets;
DROP TABLE secrets;
ALTER TABLE agents DROP COLUMN network_policy;
DROP INDEX sandboxes_subnet;
ALTER TABLE sandboxes DROP COLUMN subnet;
