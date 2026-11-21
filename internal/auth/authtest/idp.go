// Package authtest provides fake identity providers for tests: an OIDC
// provider that signs real ID tokens and a GitHub OAuth server.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// User is a person the fake IdP signs in.
type User struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Username      string
	// Claims are added to the ID token (roles, resource_access, ...).
	Claims map[string]any
	// UserInfo claims are served only by the userinfo endpoint.
	UserInfo map[string]any
}

type pendingCode struct {
	user      User
	nonce     string
	challenge string
	redirect  string
}

// IdP is a minimal OIDC provider: discovery, authorize (signs in Next
// immediately), token (with PKCE S256 check) and JWKS.
type IdP struct {
	*httptest.Server
	ClientID, ClientSecret string

	mu     sync.Mutex
	next   User
	codes  map[string]pendingCode
	tokens map[string]User
	key    *rsa.PrivateKey
}

// NewIdP starts a fake OIDC provider.
func NewIdP(t testing.TB) *IdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &IdP{ClientID: "test-client", ClientSecret: "test-secret", codes: map[string]pendingCode{}, tokens: map[string]User{}, key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("GET /userinfo", p.userinfo)
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// SignInAs makes the next authorization request sign in u.
func (p *IdP) SignInAs(u User) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next = u
}

func (p *IdP) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/jwks",
		"userinfo_endpoint":                     p.URL + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *IdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	code := randString()
	p.codes[code] = pendingCode{user: p.next, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	p.mu.Unlock()
	u, _ := url.Parse(q.Get("redirect_uri"))
	v := u.Query()
	v.Set("code", code)
	v.Set("state", q.Get("state"))
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (p *IdP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	p.mu.Lock()
	pc, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	if !ok || pc.redirect != r.PostForm.Get("redirect_uri") || s256(r.PostForm.Get("code_verifier")) != pc.challenge {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": p.URL, "aud": p.ClientID, "sub": pc.user.Subject, "nonce": pc.nonce,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"email": pc.user.Email, "email_verified": pc.user.EmailVerified,
		"name": pc.user.Name, "preferred_username": pc.user.Username,
	}
	for k, v := range pc.user.Claims {
		claims[k] = v
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: p.key, KeyID: "k1"}}, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	payload, _ := json.Marshal(claims)
	jws, err := signer.Sign(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idToken, _ := jws.CompactSerialize()
	access := randString()
	p.mu.Lock()
	p.tokens[access] = pc.user
	p.mu.Unlock()
	writeJSON(w, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
}

func (p *IdP) userinfo(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	u, ok := p.tokens[tok]
	p.mu.Unlock()
	if !ok {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	out := map[string]any{"sub": u.Subject, "email": u.Email}
	for k, v := range u.UserInfo {
		out[k] = v
	}
	writeJSON(w, out)
}

func (p *IdP) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
}

// GitHub is a fake GitHub OAuth app server and REST API.
type GitHub struct {
	*httptest.Server
	ClientID, ClientSecret string

	mu     sync.Mutex
	next   GitHubUser
	codes  map[string]GitHubUser
	tokens map[string]GitHubUser
}

// GitHubUser is a GitHub account.
type GitHubUser struct {
	ID            int64
	Login, Name   string
	PrimaryEmail  string
	EmailVerified bool
}

// NewGitHub starts a fake GitHub.
func NewGitHub(t testing.TB) *GitHub {
	t.Helper()
	g := &GitHub{ClientID: "gh-client", ClientSecret: "gh-secret", codes: map[string]GitHubUser{}, tokens: map[string]GitHubUser{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		g.mu.Lock()
		code := randString()
		g.codes[code] = g.next
		g.mu.Unlock()
		u, _ := url.Parse(q.Get("redirect_uri"))
		v := u.Query()
		v.Set("code", code)
		v.Set("state", q.Get("state"))
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		u, ok := g.codes[r.PostForm.Get("code")]
		if !ok || id != g.ClientID || secret != g.ClientSecret {
			writeJSON(w, map[string]string{"error": "bad_verification_code"})
			return
		}
		delete(g.codes, r.PostForm.Get("code"))
		tok := randString()
		g.tokens[tok] = u
		writeJSON(w, map[string]string{"access_token": tok, "token_type": "bearer", "scope": "read:user,user:email"})
	})
	auth := func(r *http.Request) (GitHubUser, bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if len(h) <= len(prefix) {
			return GitHubUser{}, false
		}
		u, ok := g.tokens[h[len(prefix):]]
		return u, ok
	}
	mux.HandleFunc("GET /api/user", func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"id": u.ID, "login": u.Login, "name": u.Name, "avatar_url": "https://avatars.example/" + u.Login})
	})
	mux.HandleFunc("GET /api/user/emails", func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, []map[string]any{
			{"email": "other@example.com", "primary": false, "verified": true},
			{"email": u.PrimaryEmail, "primary": true, "verified": u.EmailVerified},
		})
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// SignInAs makes the next authorization sign in u.
func (g *GitHub) SignInAs(u GitHubUser) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next = u
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randString() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
