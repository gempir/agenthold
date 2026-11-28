-- +goose Up
-- Phase 4C: Per-turn token/cost usage analytics, custom model pricing,
-- and always-on durable multi-stage workflows with human approval gates.
-- Timestamps are unix seconds. Booleans are BIGINT 0/1.

CREATE TABLE usage_records (
    id                 TEXT PRIMARY KEY,
    user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id           TEXT NOT NULL,
    session_id         TEXT NOT NULL,
    harness            TEXT NOT NULL,
    model              TEXT NOT NULL DEFAULT '',
    input_tokens       BIGINT NOT NULL DEFAULT 0,
    output_tokens      BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens  BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd           DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at         BIGINT NOT NULL
);

CREATE INDEX usage_records_user_created ON usage_records(user_id, created_at);
CREATE INDEX usage_records_created ON usage_records(created_at);

CREATE TABLE model_prices (
    model            TEXT PRIMARY KEY,
    input_per_m      DOUBLE PRECISION NOT NULL,
    output_per_m     DOUBLE PRECISION NOT NULL,
    cache_read_per_m DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at       BIGINT NOT NULL
);

CREATE TABLE workflows (
    id              TEXT PRIMARY KEY,
    owner_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    definition_json TEXT NOT NULL,
    enabled         BIGINT NOT NULL DEFAULT 1,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL
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
    current_step BIGINT NOT NULL DEFAULT 0,
    started_at   BIGINT,
    finished_at  BIGINT,
    created_at   BIGINT NOT NULL,
    updated_at   BIGINT NOT NULL
);

CREATE INDEX workflow_runs_workflow_created ON workflow_runs(workflow_id, created_at);
CREATE INDEX workflow_runs_owner_status ON workflow_runs(owner_id, status, created_at);

CREATE TABLE workflow_steps (
    id               TEXT PRIMARY KEY,
    run_id           TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_index       BIGINT NOT NULL,
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
    started_at       BIGINT,
    finished_at      BIGINT,
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
