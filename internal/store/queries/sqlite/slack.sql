-- name: GetSlackInstallation :one
SELECT * FROM slack_installation WHERE id = 1;

-- name: PutSlackInstallation :exec
INSERT INTO slack_installation (id, bot_token, app_token, team_id, team_name, bot_user_id, bot_id, enabled, updated_by, updated_at)
VALUES (1, ?, ?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT (id) DO UPDATE SET bot_token = excluded.bot_token, app_token = excluded.app_token,
    team_id = excluded.team_id, team_name = excluded.team_name, bot_user_id = excluded.bot_user_id,
    bot_id = excluded.bot_id, enabled = 1, updated_by = excluded.updated_by, updated_at = excluded.updated_at;

-- name: DeleteSlackInstallation :exec
DELETE FROM slack_installation WHERE id = 1;

-- name: GetSlackUser :one
SELECT * FROM slack_users WHERE team_id = ? AND slack_user_id = ?;

-- name: PutSlackUser :exec
INSERT INTO slack_users (team_id, slack_user_id, user_id, slack_name, team_name, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (team_id, slack_user_id) DO UPDATE SET user_id = excluded.user_id, slack_name = excluded.slack_name,
    team_name = excluded.team_name, created_at = excluded.created_at;

-- name: ListUserSlackUsers :many
SELECT * FROM slack_users WHERE user_id = ? ORDER BY team_name, slack_name;

-- name: DeleteSlackUser :execrows
DELETE FROM slack_users WHERE team_id = ? AND slack_user_id = ? AND user_id = ?;

-- name: CreateSlackLinkCode :exec
INSERT INTO slack_link_codes (code_hash, team_id, slack_user_id, slack_name, team_name, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetSlackLinkCode :one
SELECT * FROM slack_link_codes WHERE code_hash = ?;

-- name: DeleteSlackLinkCode :execrows
DELETE FROM slack_link_codes WHERE code_hash = ?;

-- name: DeleteSlackLinkCodesOf :exec
DELETE FROM slack_link_codes WHERE team_id = ? AND slack_user_id = ?;

-- name: DeleteExpiredSlackLinkCodes :exec
DELETE FROM slack_link_codes WHERE expires_at < ?;

-- name: CreateSlackThread :exec
INSERT INTO slack_threads (team_id, channel_id, thread_ts, session_id, agent_id, created_by, header_ts, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSlackThread :one
SELECT * FROM slack_threads WHERE team_id = ? AND channel_id = ? AND thread_ts = ?;

-- name: ListSlackThreadsOfSession :many
SELECT * FROM slack_threads WHERE session_id = ? ORDER BY created_at;

-- name: ListSlackThreadSessions :many
SELECT DISTINCT session_id FROM slack_threads;

-- name: SetSlackThreadHeader :exec
UPDATE slack_threads SET header_ts = ? WHERE team_id = ? AND channel_id = ? AND thread_ts = ?;

-- name: DeleteSlackThread :exec
DELETE FROM slack_threads WHERE team_id = ? AND channel_id = ? AND thread_ts = ?;

-- name: GetSlackChannelAgent :one
SELECT * FROM slack_channel_agents WHERE team_id = ? AND channel_id = ?;

-- name: PutSlackChannelAgent :exec
INSERT INTO slack_channel_agents (team_id, channel_id, agent_id, set_by, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (team_id, channel_id) DO UPDATE SET agent_id = excluded.agent_id, set_by = excluded.set_by,
    updated_at = excluded.updated_at;
