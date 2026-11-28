-- +goose Up
-- Phase 1A Thread UX: pinning, snoozing, per-thread permission mode,
-- parent thread kind (subthread / side_chat / a2a), and persistent
-- follow-up message queue.
-- Timestamps are unix seconds (agent_sessions) or unix milliseconds
-- (session_queued_messages.created_at). Booleans are INTEGER 0/1.

ALTER TABLE agent_sessions ADD COLUMN pinned_at INTEGER;
-- snoozed_until: NULL = not snoozed, 0 = snoozed indefinitely, >0 = unix seconds.
ALTER TABLE agent_sessions ADD COLUMN snoozed_until INTEGER;
ALTER TABLE agent_sessions ADD COLUMN permission_mode TEXT NOT NULL DEFAULT 'full-access';
ALTER TABLE agent_sessions ADD COLUMN parent_kind TEXT NOT NULL DEFAULT '';

CREATE TABLE session_queued_messages (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_name   TEXT NOT NULL DEFAULT '',
    text        TEXT NOT NULL,
    images_json TEXT NOT NULL DEFAULT '',
    steer       INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);
CREATE INDEX session_queued_messages_session ON session_queued_messages(session_id, created_at);
CREATE INDEX agent_session_events_session_seq ON agent_session_events(session_id, seq);

-- +goose Down
DROP INDEX agent_session_events_session_seq;
DROP INDEX session_queued_messages_session;
DROP TABLE session_queued_messages;
ALTER TABLE agent_sessions DROP COLUMN parent_kind;
ALTER TABLE agent_sessions DROP COLUMN permission_mode;
ALTER TABLE agent_sessions DROP COLUMN snoozed_until;
ALTER TABLE agent_sessions DROP COLUMN pinned_at;
