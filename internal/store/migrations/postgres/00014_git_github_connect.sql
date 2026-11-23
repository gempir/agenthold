-- +goose Up
-- GitHub accounts connected through the device flow (Settings → Git), next to
-- pasted tokens. source is 'token' (pasted) or 'github' (connected): account is
-- the GitHub login, scopes what the token was granted. GitHub App user tokens
-- expire; their refresh token is sealed like the token (aad = id + ":refresh")
-- and the server refreshes them, so sandboxes never see either.
ALTER TABLE git_credentials ADD COLUMN source TEXT NOT NULL DEFAULT 'token';
ALTER TABLE git_credentials ADD COLUMN account TEXT NOT NULL DEFAULT '';
ALTER TABLE git_credentials ADD COLUMN scopes TEXT NOT NULL DEFAULT '';
ALTER TABLE git_credentials ADD COLUMN refresh_ciphertext BYTEA NOT NULL DEFAULT ''::bytea;
ALTER TABLE git_credentials ADD COLUMN expires_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE git_credentials ADD COLUMN refresh_expires_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE git_credentials DROP COLUMN refresh_expires_at;
ALTER TABLE git_credentials DROP COLUMN expires_at;
ALTER TABLE git_credentials DROP COLUMN refresh_ciphertext;
ALTER TABLE git_credentials DROP COLUMN scopes;
ALTER TABLE git_credentials DROP COLUMN account;
ALTER TABLE git_credentials DROP COLUMN source;
