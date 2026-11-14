-- +goose Up
CREATE TABLE agents (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    owner_id     TEXT NOT NULL REFERENCES users(id),
    name         TEXT NOT NULL,
    image        TEXT NOT NULL,
    harness      TEXT NOT NULL DEFAULT 'claude-code',
    model        TEXT NOT NULL DEFAULT '',
    cpus         REAL NOT NULL,
    memory_mb    INTEGER NOT NULL,
    idle_minutes INTEGER NOT NULL DEFAULT 15,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX agents_workspace ON agents(workspace_id);
CREATE INDEX agents_owner ON agents(owner_id);

-- The persisted sandbox state machine, one row per agent.
CREATE TABLE sandboxes (
    agent_id     TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    driver       TEXT NOT NULL,
    driver_id    TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL CHECK (state IN
                   ('creating', 'starting', 'running', 'stopping', 'sleeping', 'rebuilding', 'destroying', 'error')),
    error        TEXT NOT NULL DEFAULT '',
    -- The spec the current sandbox was built from (JSON), to detect pending rebuilds.
    applied_spec TEXT NOT NULL DEFAULT '',
    state_changed_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE sandboxes;
DROP TABLE agents;
