package secrets

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBox(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	b, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := b.Seal([]byte("sk-ant-123"), []byte("sec_1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("sk-ant")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	pt, err := b.Open(ct, []byte("sec_1"))
	if err != nil || string(pt) != "sk-ant-123" {
		t.Fatalf("open = %q, %v", pt, err)
	}
	if _, err := b.Open(ct, []byte("sec_2")); err == nil {
		t.Fatal("opened with wrong aad")
	}
	other, _ := NewBox(bytes.Repeat([]byte{8}, KeySize))
	if _, err := other.Open(ct, []byte("sec_1")); err == nil {
		t.Fatal("opened with wrong key")
	}
	if _, err := NewBox([]byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestLoadMasterKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "master.key")
	k1, gen, err := LoadMasterKey(path)
	if err != nil || !gen || len(k1) != KeySize {
		t.Fatalf("generate: %v %v %d", err, gen, len(k1))
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	k2, gen, err := LoadMasterKey(path)
	if err != nil || gen || !bytes.Equal(k1, k2) {
		t.Fatal("reload differs")
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern, host string
		want          bool
	}{
		{"api.anthropic.com", "api.anthropic.com", true},
		{"api.anthropic.com", "API.Anthropic.com", false}, // callers lowercase
		{"api.anthropic.com", "evil-api.anthropic.com", false},
		{"*.github.com", "api.github.com", true},
		{"*.github.com", "a.b.github.com", true},
		{"*.github.com", "github.com", false},
		{"*.github.com", "evilgithub.com", false},
	}
	for _, tt := range tests {
		if got := MatchPattern(tt.pattern, tt.host); got != tt.want {
			t.Errorf("MatchPattern(%q, %q) = %v", tt.pattern, tt.host, got)
		}
	}
}

func TestInjectionApply(t *testing.T) {
	inj := Injection{
		Rule:        Rule{Hosts: []string{"api.example.com"}, Header: "Authorization", Template: "Bearer {{.Value}}"},
		Value:       "real-secret",
		Placeholder: "agenthold-placeholder-abc",
	}
	req := httptest.NewRequest(http.MethodGet, "https://api.example.com/v1?key=agenthold-placeholder-abc", nil)
	req.Header.Set("X-Other", "x agenthold-placeholder-abc y")
	ok, err := inj.Apply(req)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if req.Header.Get("Authorization") != "Bearer real-secret" || req.Header.Get("X-Other") != "x real-secret y" || req.URL.Query().Get("key") != "real-secret" {
		t.Fatalf("headers %v url %s", req.Header, req.URL)
	}

	other := httptest.NewRequest(http.MethodGet, "https://evil.example.net/?key=agenthold-placeholder-abc", nil)
	if ok, _ := inj.Apply(other); ok || other.Header.Get("Authorization") != "" || other.URL.Query().Get("key") != "agenthold-placeholder-abc" {
		t.Fatal("injected into non-matching host")
	}
}

func TestPresetsAndValidation(t *testing.T) {
	for _, p := range Presets {
		if err := p.Rule.Validate(); err != nil {
			t.Errorf("preset %s: %v", p.ID, err)
		}
	}
	gh := Presets[2].Rule
	v, err := gh.Render("ghp_x")
	if err != nil || v != "Basic eC1hY2Nlc3MtdG9rZW46Z2hwX3g=" {
		t.Fatalf("github render = %q %v", v, err)
	}
	bad := []Rule{
		{Hosts: nil, Header: "A"},
		{Hosts: []string{"*"}, Header: "A"},
		{Hosts: []string{"a.com"}, Header: "Bad Header"},
		{Hosts: []string{"a.com"}, Header: "A", Template: "{{.Nope"},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("bad rule %d accepted", i)
		}
	}
	if _, err := (Rule{Hosts: []string{"a.com"}, Header: "A"}).Render("x\r\nInjected: 1"); err == nil {
		t.Fatal("header injection via value")
	}
}

func TestInjectionHeaderOnly(t *testing.T) {
	inj := Injection{
		Rule:        Rule{Hosts: []string{"chatgpt.com"}, Header: "Authorization"},
		Value:       "real-access",
		Placeholder: "agenthold-placeholder-xyz",
		HeaderOnly:  true, PathPrefixes: []string{"/backend-api/"},
	}
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses?q=agenthold-placeholder-xyz", nil)
	req.Header.Set("Authorization", "Bearer agenthold-placeholder-xyz")
	req.Header.Set("X-Echo", "agenthold-placeholder-xyz")
	if ok, err := inj.Apply(req); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if req.Header.Get("Authorization") != "Bearer real-access" {
		t.Fatalf("authorization %q", req.Header.Get("Authorization"))
	}
	// Nowhere else: not in other headers, not in the URL.
	if req.Header.Get("X-Echo") != "agenthold-placeholder-xyz" || req.URL.Query().Get("q") != "agenthold-placeholder-xyz" {
		t.Fatalf("replaced outside its header: %v %s", req.Header, req.URL)
	}

	// No placeholder in the header: nothing is added.
	plain := httptest.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/x", nil)
	if ok, _ := inj.Apply(plain); ok || plain.Header.Get("Authorization") != "" {
		t.Fatal("header-only injection added a header")
	}
	// Other paths and hosts: untouched.
	for _, u := range []string{"https://chatgpt.com/other", "https://evil.example/backend-api/x"} {
		r := httptest.NewRequest(http.MethodGet, u, nil)
		r.Header.Set("Authorization", "Bearer agenthold-placeholder-xyz")
		if ok, _ := inj.Apply(r); ok || r.Header.Get("Authorization") != "Bearer agenthold-placeholder-xyz" {
			t.Fatalf("injected into %s", u)
		}
	}
}
