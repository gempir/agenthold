-- +goose Up
-- Sessions started by another agent (send_message / ask) record where from.
ALTER TABLE agent_sessions ADD COLUMN origin_agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN origin_session_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_sessions DROP COLUMN origin_session_id;
ALTER TABLE agent_sessions DROP COLUMN origin_agent_id;
