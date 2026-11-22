-- +goose Up
-- Threads (sessions) can be settled: done, out of the active list.
-- settled_at: unix seconds, NULL while active. settled_by: who settled it
-- (a user id, 'auto' for the auto-settle job, 'agent:<id>' for a platform
-- tool). unsettled_at: when it was last unsettled by hand, so the
-- auto-settle job counts that as activity and does not settle it again at once.
ALTER TABLE agent_sessions ADD COLUMN settled_at BIGINT;
ALTER TABLE agent_sessions ADD COLUMN settled_by TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN unsettled_at BIGINT;
-- Days without activity after which the threads of the user's agents settle
-- themselves; 0 = never.
ALTER TABLE users ADD COLUMN thread_auto_settle_days BIGINT NOT NULL DEFAULT 3;

-- +goose Down
ALTER TABLE users DROP COLUMN thread_auto_settle_days;
ALTER TABLE agent_sessions DROP COLUMN unsettled_at;
ALTER TABLE agent_sessions DROP COLUMN settled_by;
ALTER TABLE agent_sessions DROP COLUMN settled_at;
