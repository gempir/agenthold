-- name: UpsertSessionPullRequest :exec
-- Links a pull request to a session, or refreshes what is known about a
-- linked one (its watch state is left alone).
INSERT INTO session_pull_requests (id, session_id, host, repo, number, url, title, state, head_sha, checks_state, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (session_id, host, repo, number) DO UPDATE SET
    url = excluded.url,
    title = excluded.title,
    state = excluded.state,
    head_sha = excluded.head_sha,
    checks_state = excluded.checks_state,
    updated_at = excluded.updated_at;

-- name: GetSessionPullRequest :one
SELECT * FROM session_pull_requests WHERE id = $1;

-- name: GetSessionPullRequestByKey :one
SELECT * FROM session_pull_requests WHERE session_id = $1 AND host = $2 AND repo = $3 AND number = $4;

-- name: ListSessionPullRequests :many
SELECT * FROM session_pull_requests WHERE session_id = $1 ORDER BY created_at, id;

-- name: ListPullRequestSessions :many
-- The links of one pull request, in every session.
SELECT * FROM session_pull_requests WHERE host = $1 AND repo = $2 AND number = $3 ORDER BY created_at, id;

-- name: ListWatchedPullRequests :many
-- Watched pull requests of active (unsettled) sessions, with whom they act for.
SELECT sqlc.embed(session_pull_requests), agent_sessions.agent_id, agents.owner_id, agent_sessions.title AS session_title
FROM session_pull_requests
JOIN agent_sessions ON agent_sessions.id = session_pull_requests.session_id
JOIN agents ON agents.id = agent_sessions.agent_id
WHERE session_pull_requests.watching = 1 AND agent_sessions.settled_at IS NULL
ORDER BY session_pull_requests.id;

-- name: UpdatePullRequestInfo :exec
UPDATE session_pull_requests SET title = $1, state = $2, head_sha = $3, checks_state = $4, updated_at = $5 WHERE id = $6;

-- name: SetPullRequestWatching :execrows
-- Starts (with the baseline fingerprint) or stops watching; counters restart.
UPDATE session_pull_requests SET watching = $1, comment_streak = 0, fail_count = 0, last_fingerprint = $2,
    last_checked_at = $3, updated_at = $4
WHERE id = $5;

-- name: RecordPullRequestPoll :execrows
-- The watcher's result; only while the pull request is still watched.
UPDATE session_pull_requests SET title = $1, state = $2, head_sha = $3, checks_state = $4, watching = $5, wake_count = $6,
    comment_streak = $7, fail_count = $8, last_checked_at = $9, last_fingerprint = $10, updated_at = $11
WHERE id = $12 AND watching = 1;

-- name: DeleteSessionPullRequest :execrows
DELETE FROM session_pull_requests WHERE id = $1 AND session_id = $2;
