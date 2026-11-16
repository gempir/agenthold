package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

func newService(t *testing.T, publicURL string) (*Service, string) {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{PublicURL: publicURL}
	config.ApplyDefaults(cfg)
	s, err := NewService(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(context.Background(), db.CreateUserParams{ID: "usr_1", Name: "A", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	token, err := s.CreateSession(context.Background(), "usr_1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return s, token
}

// serve runs the middleware with a raw Cookie header and reports the user.
func serve(s *Service, path, cookie string) (*httptest.ResponseRecorder, string) {
	var user string
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := UserFrom(r.Context()); u != nil {
			user = u.ID
		}
	}))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, user
}

func TestSessionCookieHTTPS(t *testing.T) {
	s, token := newService(t, "https://agents.example.com")
	c := s.SessionCookie(token)
	if c.Name != "__Host-agenthold_session" || c.Domain != "" || c.Path != "/" || !c.Secure || !c.HttpOnly {
		t.Fatalf("cookie = %+v", c)
	}
	if _, u := serve(s, "/api/v1/me", "__Host-agenthold_session="+token); u != "usr_1" {
		t.Fatal("prefixed cookie not accepted")
	}
	// The plain name is a cookie any host of the parent domain could set:
	// it is not read over https.
	if _, u := serve(s, "/api/v1/me", "agenthold_session="+token); u != "" {
		t.Fatal("plain cookie accepted over https")
	}
	// Two cookies of the accepted name: refuse instead of guessing.
	rec, u := serve(s, "/api/v1/me", "__Host-agenthold_session=attacker; __Host-agenthold_session="+token)
	if rec.Code != 401 || u != "" {
		t.Fatalf("two cookies: %d %q", rec.Code, u)
	}
}

func TestSessionCookieHTTPPlanted(t *testing.T) {
	s, token := newService(t, "http://agents.example.test:8080")
	if s.SessionCookieName() != "agenthold_session" {
		t.Fatal(s.SessionCookieName())
	}
	if _, u := serve(s, "/api/v1/me", "agenthold_session="+token); u != "usr_1" {
		t.Fatal("cookie not accepted")
	}
	// A cookie planted with Domain=example.test; Path=/api is sent first.
	rec, u := serve(s, "/api/v1/agents", "agenthold_session=planted; agenthold_session="+token)
	if rec.Code != 401 || u != "" {
		t.Fatalf("planted cookie: %d %q", rec.Code, u)
	}
	set := strings.Join(rec.Header().Values("Set-Cookie"), "\n")
	for _, want := range []string{"Path=/api;", "Path=/api/v1;", "Domain=example.test", "Domain=agents.example.test"} {
		if !strings.Contains(set, want) {
			t.Fatalf("no clearing cookie with %s:\n%s", want, set)
		}
	}
}
