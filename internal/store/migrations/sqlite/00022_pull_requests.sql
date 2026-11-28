-- +goose Up
-- Phase 3C: pull requests linked to threads, and the 24/7 PR watcher's state.
-- Timestamps are unix seconds; booleans are INTEGER 0/1.
-- checks_state is the last CI verdict read (pending, success, failure or '' for
-- none); comment_streak counts the watcher's consecutive comment-only wakes.
CREATE TABLE session_pull_requests (
    id               TEXT PRIMARY KEY,
    session_id       TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    host             TEXT NOT NULL,
    repo             TEXT NOT NULL,
    number           INTEGER NOT NULL,
    url              TEXT NOT NULL,
    title            TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT 'open',
    head_sha         TEXT NOT NULL DEFAULT '',
    checks_state     TEXT NOT NULL DEFAULT '',
    watching         INTEGER NOT NULL DEFAULT 0,
    wake_count       INTEGER NOT NULL DEFAULT 0,
    comment_streak   INTEGER NOT NULL DEFAULT 0,
    fail_count       INTEGER NOT NULL DEFAULT 0,
    last_checked_at  INTEGER,
    last_fingerprint TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    UNIQUE (session_id, host, repo, number)
);
CREATE INDEX session_pull_requests_watching ON session_pull_requests(watching);

-- +goose Down
DROP INDEX session_pull_requests_watching;
DROP TABLE session_pull_requests;
