package preview

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestMatch(t *testing.T) {
	dot := NewHosts(".apps.example.com", "https://agents.example.com")
	dash := NewHosts("-demo.agenthold.dev", "https://demo.agenthold.dev")
	off := NewHosts("", "https://agents.example.com")
	for _, tc := range []struct {
		h    Hosts
		host string
		name string
		ok   bool
	}{
		{dot, "web.apps.example.com", "web", true},
		{dot, "WEB.Apps.Example.com:443", "web", true},
		{dot, "web.apps.example.com.", "web", true},
		{dot, "a.b.apps.example.com", "", false}, // one level only
		{dot, "apps.example.com", "", false},
		{dot, ".apps.example.com", "", false},
		{dot, "agents.example.com", "", false},
		{dot, "-web.apps.example.com", "", false},
		{dot, "web_x.apps.example.com", "", false},
		{dot, strings.Repeat("a", 41) + ".apps.example.com", "", false},
		{dot, "web.apps.example.com.evil.com", "", false},
		{dash, "web-demo.agenthold.dev", "web", true},
		{dash, "demo.agenthold.dev", "", false},
		{dash, "x.web-demo.agenthold.dev", "", false},
		{off, "web.apps.example.com", "", false},
	} {
		name, ok := tc.h.Match(tc.host)
		if name != tc.name || ok != tc.ok {
			t.Errorf("%q Match(%q) = %q, %v", tc.h.Suffix, tc.host, name, ok)
		}
	}
	if u := dot.URL("web"); u != "https://web.apps.example.com/" {
		t.Fatalf("url = %s", u)
	}
	if u := NewHosts(".localhost", "http://localhost:8080").URL("web"); u != "http://web.localhost:8080/" {
		t.Fatalf("url = %s", u)
	}
}

func TestNames(t *testing.T) {
	for _, tc := range []struct {
		agent string
		port  int
		want  string
	}{
		{"My Agent!", 3000, "my-agent-3000"},
		{"___", 80, "app-80"},
		{"Ünïcode äpp", 8080, "n-code-pp-8080"},
		{strings.Repeat("long-", 20), 65535, "long-long-long-long-long-long-long-65535"},
	} {
		got := DefaultName(tc.agent, tc.port)
		if got != tc.want || ValidName(got) != nil {
			t.Errorf("DefaultName(%q, %d) = %q (valid: %v), want %q", tc.agent, tc.port, got, ValidName(got), tc.want)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a.b", "a_b", strings.Repeat("a", 41), "a b"} {
		if ValidName(bad) == nil {
			t.Errorf("ValidName(%q) accepted", bad)
		}
	}
}

func TestIdentityToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preview", "identity.key")
	s, err := LoadOrCreateSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateSigner(path)
	if err != nil || again.JWKS().Keys[0].X != s.JWKS().Keys[0].X {
		t.Fatalf("key not persisted: %v", err)
	}
	now := time.Now()
	tok, err := s.Sign(Claims{Issuer: "https://agents.example.com", Subject: "usr_1", Audience: "web.apps.example.com", Email: "a@x", IssuedAt: now.Unix(), Expires: now.Add(IdentityTTL).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(tok, s.JWKS(), "web.apps.example.com", "https://agents.example.com", now)
	if err != nil || c.Subject != "usr_1" || c.Email != "a@x" {
		t.Fatalf("verify = %+v, %v", c, err)
	}
	if _, err := Verify(tok, s.JWKS(), "other.apps.example.com", "https://agents.example.com", now); err == nil {
		t.Fatal("wrong audience accepted")
	}
	if _, err := Verify(tok, s.JWKS(), "web.apps.example.com", "https://evil", now); err == nil {
		t.Fatal("wrong issuer accepted")
	}
	if _, err := Verify(tok, s.JWKS(), "web.apps.example.com", "https://agents.example.com", now.Add(IdentityTTL)); err == nil {
		t.Fatal("expired token accepted")
	}
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	other := NewSigner(otherKey)
	if _, err := Verify(tok, other.JWKS(), "web.apps.example.com", "https://agents.example.com", now); err == nil {
		t.Fatal("token verified with another key set")
	}
	parts := strings.Split(tok, ".")
	forged, _ := other.Sign(Claims{Issuer: "https://agents.example.com", Subject: "usr_admin", Audience: "web.apps.example.com", Expires: now.Add(time.Minute).Unix()})
	fp := strings.Split(forged, ".")
	if _, err := Verify(parts[0]+"."+fp[1]+"."+parts[2], s.JWKS(), "web.apps.example.com", "https://agents.example.com", now); err == nil {
		t.Fatal("swapped payload accepted")
	}
}

type fixture struct {
	svc   *Service
	agent db.Agent
	user  string
	clock *time.Time
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Unix(1_800_000_000, 0)
	if err := st.CreateUser(ctx, db.CreateUserParams{ID: "usr_1", Name: "A", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	ag := db.Agent{ID: "agt_1", OwnerID: "usr_1", Name: "Web Agent"}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{ID: ag.ID, OwnerID: ag.OwnerID, Name: ag.Name, Image: "x", Harness: "fake", Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	f := fixture{agent: ag, user: "usr_1", clock: &now}
	f.svc = &Service{Store: st, Hosts: NewHosts(".apps.test", "http://main.test"), Now: func() time.Time { return *f.clock }}
	return f
}

func TestCreateNames(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e1, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 3000, CreatedBy: f.user})
	if err != nil || e1.Name != "web-agent-3000" || e1.Access != "private" {
		t.Fatalf("e1 = %+v, %v", e1, err)
	}
	if _, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 3000, CreatedBy: f.user}); !errors.Is(err, ErrPortTaken) {
		t.Fatalf("same port: %v", err)
	}
	if _, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 4000, Name: "web-agent-3000", CreatedBy: f.user}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("explicit taken name: %v", err)
	}
	// The default name of port 4000 is taken: a suffix is added.
	if _, err := f.svc.Update(ctx, e1, "web-agent-4000", ""); err != nil {
		t.Fatal(err)
	}
	e2, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 4000, CreatedBy: f.user})
	if err != nil || !strings.HasPrefix(e2.Name, "web-agent-4000-") || ValidName(e2.Name) != nil {
		t.Fatalf("e2 = %+v, %v", e2, err)
	}
	if _, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 0, CreatedBy: f.user}); err == nil {
		t.Fatal("port 0 accepted")
	}
	if _, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 5000, Access: "everyone", CreatedBy: f.user}); err == nil {
		t.Fatal("bad access accepted")
	}
	// The lookup cache follows writes.
	if got, err := f.svc.Lookup(ctx, "web-agent-4000"); err != nil || got.ID != e1.ID {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if err := f.svc.Delete(ctx, e1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Lookup(ctx, "web-agent-4000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted exposure still found: %v", err)
	}
	disabled := &Service{Store: f.svc.Store}
	if _, err := disabled.Create(ctx, CreateParams{Agent: f.agent, Port: 1}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}

func TestCodes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	host := "web.apps.test"
	code, err := f.svc.IssueCode(ctx, f.user, host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.TakeCode(ctx, code, "other.apps.test"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("wrong host: %v", err)
	}
	// A failed attempt for the wrong host consumed the code too.
	if _, err := f.svc.TakeCode(ctx, code, host); !errors.Is(err, ErrBadCode) {
		t.Fatalf("reuse after wrong host: %v", err)
	}
	code, _ = f.svc.IssueCode(ctx, f.user, host)
	if u, err := f.svc.TakeCode(ctx, code, host); err != nil || u != f.user {
		t.Fatalf("take = %q, %v", u, err)
	}
	if _, err := f.svc.TakeCode(ctx, code, host); !errors.Is(err, ErrBadCode) {
		t.Fatalf("second use: %v", err)
	}
	code, _ = f.svc.IssueCode(ctx, f.user, host)
	*f.clock = f.clock.Add(CodeTTL)
	if _, err := f.svc.TakeCode(ctx, code, host); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := f.svc.TakeCode(ctx, "", host); !errors.Is(err, ErrBadCode) {
		t.Fatalf("empty: %v", err)
	}
}

func TestSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tok, err := f.svc.CreateSession(ctx, f.user, "web.apps.test")
	if err != nil {
		t.Fatal(err)
	}
	if u, err := f.svc.SessionUser(ctx, tok, "web.apps.test"); err != nil || u.ID != f.user {
		t.Fatalf("user = %+v, %v", u, err)
	}
	if _, err := f.svc.SessionUser(ctx, tok, "other.apps.test"); err == nil {
		t.Fatal("cookie of one host accepted on another")
	}
	*f.clock = f.clock.Add(SessionTTL)
	if _, err := f.svc.SessionUser(ctx, tok, "web.apps.test"); err == nil {
		t.Fatal("expired session accepted")
	}
	tok, _ = f.svc.CreateSession(ctx, f.user, "web.apps.test")
	_ = f.svc.EndSession(ctx, tok)
	if _, err := f.svc.SessionUser(ctx, tok, "web.apps.test"); err == nil {
		t.Fatal("ended session accepted")
	}
}
