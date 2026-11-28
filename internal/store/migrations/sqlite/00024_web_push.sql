-- +goose Up
CREATE TABLE push_subscriptions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, endpoint TEXT NOT NULL UNIQUE, p256dh TEXT NOT NULL, auth TEXT NOT NULL, user_agent TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
CREATE INDEX push_subscriptions_user ON push_subscriptions(user_id);
CREATE TABLE push_vapid_keys (id TEXT PRIMARY KEY CHECK (id = 'default'), public_key TEXT NOT NULL, encrypted_private_key BLOB NOT NULL, created_at INTEGER NOT NULL);
-- +goose Down
DROP TABLE push_vapid_keys;
DROP TABLE push_subscriptions;
