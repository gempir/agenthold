-- +goose Up
-- Sub-threads can run on a different harness than the agent's default (e.g. a
-- Claude Code thread starting a Codex sub-thread in the same worktree). Empty
-- means the agent's own harness.
ALTER TABLE agent_sessions ADD COLUMN harness TEXT NOT NULL DEFAULT '';
CREATE INDEX agent_sessions_origin_session ON agent_sessions(origin_session_id);

-- +goose Down
DROP INDEX agent_sessions_origin_session;
ALTER TABLE agent_sessions DROP COLUMN harness;
