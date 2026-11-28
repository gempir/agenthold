-- +goose Up
-- Phase 4C: Per-turn token/cost usage analytics, custom model pricing,
-- and always-on durable multi-stage workflows with human approval gates.
-- Timestamps are unix seconds. Booleans are INTEGER 0/1.

CREATE TABLE usage_records (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id           TEXT NOT NULL,
    session_id         TEXT NOT NULL,
    harness            TEXT NOT NULL,
    model              TEXT NOT NULL DEFAULT '',
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL
);

CREATE INDEX usage_records_user_created ON usage_records(user_id, created_at);
CREATE INDEX usage_records_created ON usage_records(created_at);

CREATE TABLE model_prices (
    model            TEXT PRIMARY KEY,
    input_per_m      REAL NOT NULL,
    output_per_m     REAL NOT NULL,
    cache_read_per_m REAL NOT NULL DEFAULT 0,
    updated_at       INTEGER NOT NULL
);

CREATE TABLE workflows (
    id              TEXT PRIMARY KEY,
    owner_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    definition_json TEXT NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX workflows_owner ON workflows(owner_id);

CREATE TABLE workflow_runs (
    id           TEXT PRIMARY KEY,
    workflow_id  TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    owner_id     TEXT NOT NULL,
    -- 'queued' | 'running' | 'needs_input' | 'completed' | 'failed' | 'cancelled'
    status       TEXT NOT NULL,
    -- 'manual' | 'schedule' | 'webhook'
    trigger_kind TEXT NOT NULL DEFAULT 'manual',
    input_json   TEXT NOT NULL DEFAULT '{}',
    output_json  TEXT NOT NULL DEFAULT '{}',
    error        TEXT NOT NULL DEFAULT '',
    current_step INTEGER NOT NULL DEFAULT 0,
    started_at   INTEGER,
    finished_at  INTEGER,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX workflow_runs_workflow_created ON workflow_runs(workflow_id, created_at);
CREATE INDEX workflow_runs_owner_status ON workflow_runs(owner_id, status, created_at);

CREATE TABLE workflow_steps (
    id               TEXT PRIMARY KEY,
    run_id           TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_index       INTEGER NOT NULL,
    name             TEXT NOT NULL,
    -- 'agent' | 'shell' | 'ask'
    kind             TEXT NOT NULL,
    -- 'pending' | 'running' | 'waiting' | 'completed' | 'failed' | 'skipped'
    status           TEXT NOT NULL,
    session_id       TEXT NOT NULL DEFAULT '',
    prompt_or_cmd    TEXT NOT NULL DEFAULT '',
    ask_title        TEXT NOT NULL DEFAULT '',
    ask_choices_json TEXT NOT NULL DEFAULT '[]',
    answer_json      TEXT NOT NULL DEFAULT '',
    answered_by      TEXT NOT NULL DEFAULT '',
    output           TEXT NOT NULL DEFAULT '',
    error            TEXT NOT NULL DEFAULT '',
    started_at       INTEGER,
    finished_at      INTEGER,
    UNIQUE(run_id, step_index)
);

CREATE INDEX workflow_steps_run ON workflow_steps(run_id, step_index);

-- +goose Down
DROP INDEX workflow_steps_run;
DROP TABLE workflow_steps;
DROP INDEX workflow_runs_owner_status;
DROP INDEX workflow_runs_workflow_created;
DROP TABLE workflow_runs;
DROP INDEX workflows_owner;
DROP TABLE workflows;
DROP TABLE model_prices;
DROP INDEX usage_records_created;
DROP INDEX usage_records_user_created;
DROP TABLE usage_records;
