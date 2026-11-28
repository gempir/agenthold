package api_test

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
)

func TestPushSubscriptionsCRUD(t *testing.T) {
	e := newEnv(t)
	alice := e.login("push-a", "Alice", "push-a@example.com")
	bob := e.login("push-b", "Bob", "push-b@example.com")
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	input := map[string]any{"endpoint": "https://push.example.com/device", "p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(auth), "userAgent": "Browser"}
	expect(t, "test notification", alice.do(http.MethodPost, "/me/push/test", nil, nil), http.StatusOK)
	var vapid gen.PushVapidKey
	expect(t, "vapid", alice.do(http.MethodGet, "/me/push/vapid-key", nil, &vapid), 200)
	if !vapid.Enabled || vapid.PublicKey == "" {
		t.Fatal("push disabled")
	}
	var devices []gen.PushSubscription
	expect(t, "register", alice.do(http.MethodPost, "/me/push/subscriptions", input, &devices), 200)
	if len(devices) != 1 {
		t.Fatal(devices)
	}
	id := devices[0].Id
	expect(t, "upsert", alice.do(http.MethodPost, "/me/push/subscriptions", input, &devices), 200)
	if len(devices) != 1 {
		t.Fatal(devices)
	}
	expect(t, "other revoke", bob.do(http.MethodDelete, "/me/push/subscriptions/"+id, nil, nil), 200)
	expect(t, "list", alice.do(http.MethodGet, "/me/push/subscriptions", nil, &devices), 200)
	if len(devices) != 1 {
		t.Fatal("cross user delete")
	}
	expect(t, "revoke", alice.do(http.MethodDelete, "/me/push/subscriptions/"+id, nil, nil), 200)
	code, body := alice.rawJSON(http.MethodGet, "/me/push/subscriptions", nil)
	expect(t, "empty", code, 200)
	if json.Unmarshal([]byte(body), &devices) != nil || len(devices) != 0 {
		t.Fatal(body)
	}
	input["endpoint"] = "http://localhost/private"
	expect(t, "invalid endpoint", alice.do(http.MethodPost, "/me/push/subscriptions", input, nil), 400)
}
