-- +goose Up
-- Per-agent Executor MCP integration settings. Default disabled so existing
-- agents (including shared agents) do not silently expose owner accounts.
ALTER TABLE agents ADD COLUMN executor_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN executor_approval_mode TEXT NOT NULL DEFAULT 'model';

-- +goose Down
ALTER TABLE agents DROP COLUMN executor_approval_mode;
ALTER TABLE agents DROP COLUMN executor_enabled;
