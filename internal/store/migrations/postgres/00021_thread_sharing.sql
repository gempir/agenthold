-- +goose Up
-- Phase 3A: Multiplayer thread-level sharing, single-thread guest links &
-- owner-approved prompt queue, and persistent collaborative diff review comments.
-- Timestamps are unix seconds. All integer columns are BIGINT for SQLite parity.

CREATE TABLE session_shares (
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- 'viewer' | 'collaborator'
    role       TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (session_id, user_id)
);

CREATE INDEX session_shares_user ON session_shares(user_id);

CREATE TABLE session_guest_links (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_by TEXT NOT NULL,
    expires_at BIGINT NOT NULL,
    revoked_at BIGINT,
    created_at BIGINT NOT NULL
);

CREATE INDEX session_guest_links_session ON session_guest_links(session_id);

CREATE TABLE session_guest_requests (
    id            TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    guest_link_id TEXT NOT NULL REFERENCES session_guest_links(id) ON DELETE CASCADE,
    guest_name    TEXT NOT NULL DEFAULT 'Guest',
    text          TEXT NOT NULL,
    -- 'pending' | 'approved' | 'dismissed'
    status        TEXT NOT NULL DEFAULT 'pending',
    resolved_by   TEXT,
    created_at    BIGINT NOT NULL,
    resolved_at   BIGINT
);

CREATE INDEX session_guest_requests_session ON session_guest_requests(session_id, created_at);

CREATE TABLE session_review_comments (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_name   TEXT NOT NULL DEFAULT '',
    file_path   TEXT NOT NULL,
    start_line  BIGINT NOT NULL,
    end_line    BIGINT NOT NULL,
    -- 'new' | 'old'
    side        TEXT NOT NULL DEFAULT 'new',
    body        TEXT NOT NULL,
    resolved_at BIGINT,
    created_at  BIGINT NOT NULL
);

CREATE INDEX session_review_comments_session ON session_review_comments(session_id, created_at);

-- +goose Down
DROP INDEX session_review_comments_session;
DROP TABLE session_review_comments;
DROP INDEX session_guest_requests_session;
DROP TABLE session_guest_requests;
DROP INDEX session_guest_links_session;
DROP TABLE session_guest_links;
DROP INDEX session_shares_user;
DROP TABLE session_shares;
