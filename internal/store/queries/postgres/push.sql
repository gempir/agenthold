-- name: GetPushVAPIDKey :one
SELECT * FROM push_vapid_keys WHERE id = 'default';
-- name: CreatePushVAPIDKey :exec
INSERT INTO push_vapid_keys (id, public_key, encrypted_private_key, created_at) VALUES ('default', $1, $2, $3) ON CONFLICT (id) DO NOTHING;
-- name: ListPushSubscriptions :many
SELECT * FROM push_subscriptions WHERE user_id = $1 ORDER BY created_at;
-- name: UpsertPushSubscription :exec
INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth, user_agent, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, user_agent = excluded.user_agent WHERE push_subscriptions.user_id = excluded.user_id;
-- name: DeletePushSubscription :exec
DELETE FROM push_subscriptions WHERE id = $1 AND user_id = $2;
