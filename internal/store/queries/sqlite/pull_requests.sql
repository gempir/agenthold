-- name: UpsertSessionPullRequest :exec
-- Links a pull request to a session, or refreshes what is known about a
-- linked one (its watch state is left alone).
INSERT INTO session_pull_requests (id, session_id, host, repo, number, url, title, state, head_sha, checks_state, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id, host, repo, number) DO UPDATE SET
    url = excluded.url,
    title = excluded.title,
    state = excluded.state,
    head_sha = excluded.head_sha,
    checks_state = excluded.checks_state,
    updated_at = excluded.updated_at;

-- name: GetSessionPullRequest :one
SELECT * FROM session_pull_requests WHERE id = ?;

-- name: GetSessionPullRequestByKey :one
SELECT * FROM session_pull_requests WHERE session_id = ? AND host = ? AND repo = ? AND number = ?;

-- name: ListSessionPullRequests :many
SELECT * FROM session_pull_requests WHERE session_id = ? ORDER BY created_at, id;

-- name: ListPullRequestSessions :many
-- The links of one pull request, in every session.
SELECT * FROM session_pull_requests WHERE host = ? AND repo = ? AND number = ? ORDER BY created_at, id;

-- name: ListWatchedPullRequests :many
-- Watched pull requests of active (unsettled) sessions, with whom they act for.
SELECT sqlc.embed(session_pull_requests), agent_sessions.agent_id, agents.owner_id, agent_sessions.title AS session_title
FROM session_pull_requests
JOIN agent_sessions ON agent_sessions.id = session_pull_requests.session_id
JOIN agents ON agents.id = agent_sessions.agent_id
WHERE session_pull_requests.watching = 1 AND agent_sessions.settled_at IS NULL
ORDER BY session_pull_requests.id;

-- name: UpdatePullRequestInfo :exec
UPDATE session_pull_requests SET title = ?, state = ?, head_sha = ?, checks_state = ?, updated_at = ? WHERE id = ?;

-- name: SetPullRequestWatching :execrows
-- Starts (with the baseline fingerprint) or stops watching; counters restart.
UPDATE session_pull_requests SET watching = ?, comment_streak = 0, fail_count = 0, last_fingerprint = ?,
    last_checked_at = ?, updated_at = ?
WHERE id = ?;

-- name: RecordPullRequestPoll :execrows
-- The watcher's result; only while the pull request is still watched.
UPDATE session_pull_requests SET title = ?, state = ?, head_sha = ?, checks_state = ?, watching = ?, wake_count = ?,
    comment_streak = ?, fail_count = ?, last_checked_at = ?, last_fingerprint = ?, updated_at = ?
WHERE id = ? AND watching = 1;

-- name: DeleteSessionPullRequest :execrows
DELETE FROM session_pull_requests WHERE id = ? AND session_id = ?;
