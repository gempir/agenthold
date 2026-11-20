-- +goose Up
-- The Slack app this installation is connected to (at most one). Tokens are
-- sealed with the master key (aad 'slack:bot' and 'slack:app').
CREATE TABLE slack_installation (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    bot_token   BLOB NOT NULL,
    app_token   BLOB NOT NULL,
    team_id     TEXT NOT NULL,
    team_name   TEXT NOT NULL DEFAULT '',
    bot_user_id TEXT NOT NULL,
    bot_id      TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    updated_by  TEXT NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL
);

-- Slack users linked to agenthold users. Everything a Slack user does runs
-- as the linked user.
CREATE TABLE slack_users (
    team_id       TEXT NOT NULL,
    slack_user_id TEXT NOT NULL,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slack_name    TEXT NOT NULL DEFAULT '',
    team_name     TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    PRIMARY KEY (team_id, slack_user_id)
);
CREATE INDEX slack_users_user ON slack_users(user_id);

-- One-time codes that link a Slack user (SHA-256 of the code, hex).
CREATE TABLE slack_link_codes (
    code_hash     TEXT PRIMARY KEY,
    team_id       TEXT NOT NULL,
    slack_user_id TEXT NOT NULL,
    slack_name    TEXT NOT NULL DEFAULT '',
    team_name     TEXT NOT NULL DEFAULT '',
    expires_at    INTEGER NOT NULL
);
CREATE INDEX slack_link_codes_user ON slack_link_codes(team_id, slack_user_id);

-- Slack threads bound to sessions. header_ts is the bot's header message.
-- last_seq is the last session event the thread rendered; turn_seq is the
-- first event of the turn it is rendering (0 for none) and turn_ts that
-- turn's current message, so a restart can finish what it left open.
CREATE TABLE slack_threads (
    team_id    TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    thread_ts  TEXT NOT NULL,
    session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    header_ts  TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    last_seq   INTEGER NOT NULL DEFAULT 0,
    turn_seq   INTEGER NOT NULL DEFAULT 0,
    turn_ts    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (team_id, channel_id, thread_ts)
);
CREATE INDEX slack_threads_session ON slack_threads(session_id);

-- The default agent of a channel or DM.
CREATE TABLE slack_channel_agents (
    team_id    TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    set_by     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (team_id, channel_id)
);

-- +goose Down
DROP TABLE slack_channel_agents;
DROP TABLE slack_threads;
DROP TABLE slack_link_codes;
DROP TABLE slack_users;
DROP TABLE slack_installation;
