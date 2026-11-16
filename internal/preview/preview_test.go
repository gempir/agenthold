package preview

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
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
		{dot, "web.apps.example.com..", "web", true},
		{dot, "www.apps.example.com", "", false},
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
	// Every host under the suffix belongs to the preview router.
	for _, h := range []string{"a.b.apps.example.com", "-x.apps.example.com", "web_x.apps.example.com.", "www.apps.example.com", "WEB.apps.example.com..:80"} {
		if _, under := dot.Classify(h); !under {
			t.Errorf("Classify(%q) not under the suffix", h)
		}
	}
	if _, under := dot.Classify("agents.example.com"); under {
		t.Error("main host classified as an exposure host")
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
	if got := DefaultName("xn--80ak6aa92e", 80); got != "xn-80ak6aa92e-80" || ValidName(got) != nil {
		t.Errorf("punycode agent name: %q", got)
	}
	for _, bad := range []string{"", "-a", "a-", "A", "a.b", "a_b", strings.Repeat("a", 41), "a b", "www", "api", "agenthold", "xn--abc"} {
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
	other db.Agent // owned by usr_2
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
	if err := st.CreateUser(ctx, db.CreateUserParams{ID: "usr_2", Name: "B", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	ag := db.Agent{ID: "agt_1", OwnerID: "usr_1", Name: "Web Agent"}
	other := db.Agent{ID: "agt_2", OwnerID: "usr_2", Name: "Other"}
	for _, a := range []db.Agent{ag, other} {
		if err := st.CreateAgent(ctx, db.CreateAgentParams{ID: a.ID, OwnerID: a.OwnerID, Name: a.Name, Image: "x", Harness: "fake", Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	f := fixture{agent: ag, other: other, user: "usr_1", clock: &now}
	f.svc = &Service{Store: st, Hosts: NewHosts(".apps.test", "http://main.test"), Now: func() time.Time { return *f.clock }}
	return f
}

func TestCreateNames(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e1, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 3000, CreatedBy: f.user})
	if err != nil || e1.Name != "web-agent-3000" || e1.Access != "private" || e1.WakeOnRequest != 0 {
		t.Fatalf("e1 = %+v, %v", e1, err)
	}
	if _, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 3000, CreatedBy: f.user}); !errors.Is(err, ErrPortTaken) {
		t.Fatalf("same port: %v", err)
	}
	if _, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 4000, Name: "web-agent-3000", CreatedBy: f.user}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("explicit taken name: %v", err)
	}
	// The default name of port 4000 is taken: a suffix is added.
	if e1, err = f.svc.Update(ctx, e1, f.user, Patch{Name: "web-agent-4000"}); err != nil {
		t.Fatal(err)
	}
	e2, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 4000, CreatedBy: f.user})
	if err != nil || !strings.HasPrefix(e2.Name, "web-agent-4000-") || ValidName(e2.Name) != nil {
		t.Fatalf("e2 = %+v, %v", e2, err)
	}
	for _, bad := range []CreateParams{{Port: 0}, {Port: 5000, Access: "everyone"}, {Port: 5000, Name: "www"}, {Port: 5000, Name: "xn--80ak6aa92e"}} {
		bad.Agent, bad.CreatedBy = f.agent, f.user
		var invalid *InvalidError
		if _, err := f.svc.Create(ctx, bad); !errors.As(err, &invalid) {
			t.Fatalf("%+v accepted: %v", bad, err)
		}
	}
	// The lookup cache follows writes.
	if got, err := f.svc.Lookup(ctx, "web-agent-4000"); err != nil || got.ID != e1.ID {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if err := f.svc.Delete(ctx, e1, f.user); err != nil {
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

func TestLimitPerAgent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for port := 1; port <= MaxPerAgent; port++ {
		if _, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: port, CreatedBy: f.user}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 999, CreatedBy: f.user}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("21st: %v", err)
	}
}

// A freed name goes back to its owner at once, to anybody else only after
// the cooldown, so browsers that trusted it never meet a stranger's app.
func TestNameCooldown(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 1, Name: "shop", CreatedBy: f.user})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, e, f.user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(ctx, CreateParams{Agent: f.other, Port: 1, Name: "shop", CreatedBy: "usr_2"}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("stranger took a freed name: %v", err)
	}
	again, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 2, Name: "shop", CreatedBy: f.user})
	if err != nil {
		t.Fatalf("owner reuse: %v", err)
	}
	// Renaming frees the old name the same way.
	if _, err := f.svc.Update(ctx, again, f.user, Patch{Name: "shop2"}); err != nil {
		t.Fatal(err)
	}
	ox, err := f.svc.Create(ctx, CreateParams{Agent: f.other, Port: 1, CreatedBy: "usr_2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, ox, "usr_2", Patch{Name: "shop"}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("stranger renamed onto a freed name: %v", err)
	}
	*f.clock = f.clock.Add(NameCooldown)
	if _, err := f.svc.Update(ctx, ox, "usr_2", Patch{Name: "shop"}); err != nil {
		t.Fatalf("after cooldown: %v", err)
	}
	// The sweep forgets old reservations.
	if err := f.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCodes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e, _ := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 1, Name: "web", CreatedBy: f.user})
	o, _ := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 2, Name: "other", CreatedBy: f.user})
	state := "browser-state-cookie-value-0123456789"
	issue := func() string {
		t.Helper()
		code, err := f.svc.IssueCode(ctx, f.user, e, Hash(state))
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	code := issue()
	if _, err := f.svc.TakeCode(ctx, code, o, state); !errors.Is(err, ErrBadCode) {
		t.Fatalf("wrong exposure: %v", err)
	}
	// A failed attempt consumed the code too.
	if _, err := f.svc.TakeCode(ctx, code, e, state); !errors.Is(err, ErrBadCode) {
		t.Fatalf("reuse after a failed attempt: %v", err)
	}
	if _, err := f.svc.TakeCode(ctx, issue(), e, "another-browser"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("other browser's state: %v", err)
	}
	if _, err := f.svc.TakeCode(ctx, issue(), e, ""); !errors.Is(err, ErrBadCode) {
		t.Fatalf("no state: %v", err)
	}
	code = issue()
	if u, err := f.svc.TakeCode(ctx, code, e, state); err != nil || u != f.user {
		t.Fatalf("take = %q, %v", u, err)
	}
	if _, err := f.svc.TakeCode(ctx, code, e, state); !errors.Is(err, ErrBadCode) {
		t.Fatalf("second use: %v", err)
	}
	code = issue()
	*f.clock = f.clock.Add(CodeTTL)
	if _, err := f.svc.TakeCode(ctx, code, e, state); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired: %v", err)
	}
	// A renamed exposure does not take codes issued for its old host.
	code = issue()
	e, _ = f.svc.Update(ctx, e, f.user, Patch{Name: "web2"})
	if _, err := f.svc.TakeCode(ctx, code, e, state); !errors.Is(err, ErrBadCode) {
		t.Fatalf("renamed: %v", err)
	}
}

func TestSessions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e, _ := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 1, Name: "web", CreatedBy: f.user})
	o, _ := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 2, Name: "other", CreatedBy: f.user})
	tok, err := f.svc.CreateSession(ctx, f.user, e)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := f.svc.SessionUser(ctx, tok, e); err != nil || u.ID != f.user {
		t.Fatalf("user = %+v, %v", u, err)
	}
	if _, err := f.svc.SessionUser(ctx, tok, o); err == nil {
		t.Fatal("cookie of one exposure accepted on another")
	}
	// Deleting the exposure and creating a new one with the same name
	// (same owner, allowed at once) does not revive old cookies.
	_ = f.svc.Delete(ctx, e, f.user)
	n, err := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 3, Name: "web", CreatedBy: f.user})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SessionUser(ctx, tok, n); err == nil {
		t.Fatal("old cookie accepted by a new exposure with the same name")
	}
	// At most ten live sessions per user and exposure; the oldest go.
	var toks []string
	for i := 0; i < 12; i++ {
		*f.clock = f.clock.Add(time.Second)
		tk, err := f.svc.CreateSession(ctx, f.user, n)
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, tk)
	}
	for i, tk := range toks {
		_, err := f.svc.SessionUser(ctx, tk, n)
		if (err == nil) != (i >= 2) {
			t.Fatalf("session %d alive = %v", i, err == nil)
		}
	}
	*f.clock = f.clock.Add(SessionTTL)
	if _, err := f.svc.SessionUser(ctx, toks[11], n); err == nil {
		t.Fatal("expired session accepted")
	}
	tok, _ = f.svc.CreateSession(ctx, f.user, n)
	_ = f.svc.EndSession(ctx, tok)
	if _, err := f.svc.SessionUser(ctx, tok, n); err == nil {
		t.Fatal("ended session accepted")
	}
	tok, _ = f.svc.CreateSession(ctx, f.user, n)
	_ = f.svc.EndUserSessions(ctx, f.user)
	if _, err := f.svc.SessionUser(ctx, tok, n); err == nil {
		t.Fatal("session survived the user's logout")
	}
}

func TestConsent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	e, _ := f.svc.Create(ctx, CreateParams{Agent: f.agent, Port: 1, Access: Authenticated, CreatedBy: f.user})
	if ok, _ := f.svc.Consented(ctx, "usr_2", e); ok {
		t.Fatal("consent before asking")
	}
	if err := f.svc.Consent(ctx, "usr_2", e); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Consent(ctx, "usr_2", e); err != nil {
		t.Fatal("second consent:", err)
	}
	if ok, _ := f.svc.Consented(ctx, "usr_2", e); !ok {
		t.Fatal("consent not remembered")
	}
}

func TestKeyFileModesAndRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "preview")
	path := filepath.Join(dir, "identity.key")
	first, err := LoadOrCreateSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(path, 0o644)
	_ = os.Chmod(dir, 0o755)
	if _, err := LoadOrCreateSigner(path); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, dir: 0o700} {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != want {
			t.Fatalf("%s mode %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
	// Rotation: move the key to the previous path and restart.
	if err := os.Rename(path, PreviousKeyPath(path)); err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	set := second.JWKS()
	if len(set.Keys) != 2 || set.Keys[1].X != first.JWKS().Keys[0].X || set.Keys[0].X == set.Keys[1].X {
		t.Fatalf("jwks = %+v", set)
	}
	now := time.Now()
	old, _ := first.Sign(Claims{Issuer: "i", Audience: "a", Subject: "s", Expires: now.Add(time.Minute).Unix()})
	if _, err := Verify(old, set, "a", "i", now); err != nil {
		t.Fatalf("token of the previous key: %v", err)
	}
}
