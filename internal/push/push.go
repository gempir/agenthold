// Package push delivers encrypted RFC 8291 notifications authenticated with VAPID.
package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Notification is the visible browser notification payload.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
}

// Notifier queues notifications without blocking turns.
type Notifier interface {
	NotifyUser(context.Context, string, Notification)
}
type job struct {
	user         string
	notification Notification
}

// Service owns the installation key and background delivery queue.
type Service struct {
	store   *store.Store
	key     *ecdsa.PrivateKey
	public  string
	subject string
	client  *http.Client
	queue   chan job
}

var enc = base64.RawURLEncoding

// New loads or creates the encrypted installation VAPID key.
func New(ctx context.Context, st *store.Store, box *secrets.Box, subject string) (*Service, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	sealed, err := box.Seal(raw, []byte("push-vapid:default"))
	if err != nil {
		return nil, err
	}
	ecdhKey, err := key.ECDH()
	if err != nil {
		return nil, err
	}
	public := enc.EncodeToString(ecdhKey.PublicKey().Bytes())
	if err = st.CreatePushVAPIDKey(ctx, db.CreatePushVAPIDKeyParams{PublicKey: public, EncryptedPrivateKey: sealed, CreatedAt: time.Now().Unix()}); err != nil {
		return nil, err
	}
	row, err := st.GetPushVAPIDKey(ctx)
	if err != nil {
		return nil, err
	}
	raw, err = box.Open(row.EncryptedPrivateKey, []byte("push-vapid:default"))
	if err != nil {
		return nil, err
	}
	key, err = x509.ParseECPrivateKey(raw)
	if err != nil {
		return nil, err
	}
	s := &Service{store: st, key: key, public: row.PublicKey, subject: subject, client: &http.Client{Transport: &http.Transport{DialContext: dialPublic}, Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, queue: make(chan job, 128)}
	go s.run(ctx)
	return s, nil
}

// PublicKey returns the application server public key.
func (s *Service) PublicKey() string { return s.public }

// NotifyUser queues delivery, dropping alerts when the queue is full.
func (s *Service) NotifyUser(ctx context.Context, user string, n Notification) {
	select {
	case <-ctx.Done():
	case s.queue <- job{user, n}:
	default:
	}
}

func (s *Service) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.queue:
			rows, err := s.store.ListPushSubscriptions(ctx, j.user)
			if err != nil {
				continue
			}
			for _, row := range rows {
				_ = s.deliver(ctx, row, j.notification)
			}
		}
	}
}

// Validate checks browser subscription keys and endpoint syntax.
func Validate(endpoint, p256dh, auth string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("push endpoint must be HTTPS")
	}
	pub, err := enc.DecodeString(p256dh)
	if err != nil {
		return err
	}
	if _, err = ecdh.P256().NewPublicKey(pub); err != nil {
		return err
	}
	a, err := enc.DecodeString(auth)
	if err != nil || len(a) != 16 {
		return errors.New("invalid push auth key")
	}
	return nil
}

func derive(secret, salt, info []byte, n int) []byte {
	out := make([]byte, n)
	_, _ = io.ReadFull(hkdf.New(sha256.New, secret, salt, info), out)
	return out
}

func encrypt(payload []byte, subscription db.PushSubscription) ([]byte, error) {
	ua, err := enc.DecodeString(subscription.P256dh)
	if err != nil {
		return nil, err
	}
	auth, err := enc.DecodeString(subscription.Auth)
	if err != nil {
		return nil, err
	}
	pub, err := ecdh.P256().NewPublicKey(ua)
	if err != nil {
		return nil, err
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	secret, err := key.ECDH(pub)
	if err != nil {
		return nil, err
	}
	info := append([]byte("WebPush: info\x00"), ua...)
	info = append(info, key.PublicKey().Bytes()...)
	ikm := derive(secret, auth, info, 32)
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	cek := derive(ikm, salt, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := derive(ikm, salt, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(payload) > 3993 {
		return nil, errors.New("push payload too large")
	}
	header := append([]byte(nil), salt...)
	header = append(header, 0, 0, 16, 0, 65)
	header = append(header, key.PublicKey().Bytes()...)
	return gcm.Seal(header, nonce, append(payload, 2), nil), nil
}

func (s *Service) deliver(ctx context.Context, row db.PushSubscription, n Notification) error {
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	body, err := encrypt(payload, row)
	if err != nil {
		return err
	}
	u, err := url.Parse(row.Endpoint)
	if err != nil {
		return err
	}
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": s.subject})
	token := enc.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + enc.EncodeToString(claims)
	hash := sha256.Sum256([]byte(token))
	r, ss, err := ecdsa.Sign(rand.Reader, s.key, hash[:])
	if err != nil {
		return err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	ss.FillBytes(sig[32:])
	token += "." + enc.EncodeToString(sig)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, row.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "vapid t="+token+", k="+s.public)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "300")
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
		return s.store.DeletePushSubscription(ctx, db.DeletePushSubscriptionParams{ID: row.ID, UserID: row.UserID})
	}
	if res.StatusCode >= 300 {
		return fmt.Errorf("push returned %d", res.StatusCode)
	}
	return nil
}

// dialPublic pins delivery to a public resolved IP, preventing endpoint SSRF and DNS rebinding.
func dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return nil, errors.New("push endpoint resolves to a private address")
		}
	}
	var dialer net.Dialer
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
	}
	if err == nil {
		err = errors.New("push endpoint has no addresses")
	}
	return nil, err
}
