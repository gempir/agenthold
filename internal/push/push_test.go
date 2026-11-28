package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestDeliveryEncryptionSignatureAndPruning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, "sqlite::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secrets.NewBox(make([]byte, 32))
	svc, err := New(ctx, st, box, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	again, err := New(ctx, st, box, "https://example.com")
	if err != nil || again.PublicKey() != svc.PublicKey() {
		t.Fatal("key not persistent", err)
	}
	// Seed the required owner without relying on unrelated user defaults.
	_, err = st.DB.ExecContext(ctx, `INSERT INTO users (id,email,name,created_at,updated_at) VALUES ('u','u@example.com','User',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	row := db.PushSubscription{ID: "p", UserID: "u", P256dh: enc.EncodeToString(ua.PublicKey().Bytes()), Auth: enc.EncodeToString(auth)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "vapid t="), ", k=")[0]
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Error("invalid JWT")
			return
		}
		sig, _ := enc.DecodeString(parts[2])
		hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if len(sig) != 64 || !ecdsa.Verify(&svc.key.PublicKey, hash[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			t.Error("invalid signature")
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) < 86 {
			t.Error("missing body")
			return
		}
		sender, err := ecdh.P256().NewPublicKey(body[21:86])
		if err != nil {
			t.Error(err)
			return
		}
		shared, _ := ua.ECDH(sender)
		info := append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...)
		info = append(info, sender.Bytes()...)
		ikm := derive(shared, auth, info, 32)
		cek := derive(ikm, body[:16], []byte("Content-Encoding: aes128gcm\x00"), 16)
		nonce := derive(ikm, body[:16], []byte("Content-Encoding: nonce\x00"), 12)
		block, _ := aes.NewCipher(cek)
		gcm, _ := cipher.NewGCM(block)
		plain, err := gcm.Open(nil, nonce, body[86:], nil)
		if err != nil {
			t.Error(err)
			return
		}
		var n Notification
		if plain[len(plain)-1] != 2 || json.Unmarshal(plain[:len(plain)-1], &n) != nil || n.Title != "Done" {
			t.Error("bad payload")
		}
		w.WriteHeader(http.StatusGone)
	}))
	defer server.Close()
	svc.client = server.Client()
	row.Endpoint = server.URL
	err = st.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{ID: row.ID, UserID: row.UserID, Endpoint: row.Endpoint, P256dh: row.P256dh, Auth: row.Auth})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.deliver(ctx, row, Notification{Title: "Done"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListPushSubscriptions(ctx, "u")
	if err != nil || len(rows) != 0 {
		t.Fatal("expired subscription retained", err)
	}
}

func TestPrivateDeliveryRejected(t *testing.T) {
	if conn, err := dialPublic(t.Context(), "tcp", "127.0.0.1:443"); err == nil {
		conn.Close()
		t.Fatal("private push endpoint allowed")
	}
}
