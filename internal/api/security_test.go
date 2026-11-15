package api_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
)

func TestTokensCannotMintTokens(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	var tok gen.CreatedAPIToken
	alice.do("POST", "/me/tokens", map[string]any{"name": "ci"}, &tok)
	expect(t, "token creates token", e.tokenClient(tok.Token).do("POST", "/me/tokens", map[string]any{"name": "persist"}, nil), 403)
}

// Downloads keep credentials out of reach of anyone without a terminal. Shared
// users have a terminal, so they read what the owner reads; strangers nothing.
func TestCredentialDownloads(t *testing.T) {
	tm := newTeam(t)
	tm.share("carol")
	ag := tm.agent.Id
	home := tm.e.home(ag)
	cred := filepath.Join(home, ".fakeagent", "credentials")
	_ = os.MkdirAll(filepath.Dir(cred), 0o755)
	_ = os.WriteFile(cred, []byte("subscription-token"), 0o600)
	_ = os.Symlink(cred, filepath.Join(home, "innocent.txt")) // the agent could plant this

	get := func(c *client, path string) (int, string) {
		r := c.raw("GET", "/agents/"+ag+"/files/content?path="+path, nil)
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	for _, p := range []string{".fakeagent/credentials", "~/.fakeagent/credentials", "innocent.txt", "/etc/agenthold/boot.json"} {
		if code, body := get(tm.bob, p); code != http.StatusNotFound || strings.Contains(body, "subscription-token") {
			t.Fatalf("stranger read %s: %d %q", p, code, body)
		}
	}
	if code, body := get(tm.carol, ".fakeagent/credentials"); code != http.StatusOK || body != "subscription-token" {
		t.Fatalf("shared user: %d %q", code, body)
	}
	_ = os.WriteFile(filepath.Join(home, "notes.txt"), []byte("ok"), 0o644)
	if code, body := get(tm.carol, "notes.txt"); code != http.StatusOK || body != "ok" {
		t.Fatalf("shared user normal file: %d %q", code, body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	res, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for k, want := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "same-origin"} {
		if res.Header.Get(k) != want {
			t.Errorf("%s = %q", k, res.Header.Get(k))
		}
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("CSP %q", res.Header.Get("Content-Security-Policy"))
	}
}
