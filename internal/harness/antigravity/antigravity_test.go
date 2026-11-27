package antigravity

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/secrets"
)

func adapter(t *testing.T) *harness.Adapter {
	t.Helper()
	a, ok := harness.Get("antigravity")
	if !ok {
		t.Fatal("antigravity is not registered")
	}
	return a
}

func TestEnvironmentPinsTheVersion(t *testing.T) {
	env := adapter(t).Environment(harness.Config{Model: "gemini-4-neon"})
	if env["AGY_CLI_DISABLE_AUTO_UPDATE"] != "true" {
		t.Fatalf("AGY_CLI_DISABLE_AUTO_UPDATE = %q", env["AGY_CLI_DISABLE_AUTO_UPDATE"])
	}
	for k, v := range env {
		if strings.Contains(strings.ToLower(k), "key") {
			t.Fatalf("the adapter's environment carries %s=%q", k, v)
		}
	}
}

// The secret preset is the variable agy reads, sent where agy sends it.
func TestGeminiPresetMatchesTheHarness(t *testing.T) {
	a := adapter(t)
	for _, id := range a.SecretPresets {
		i := slices.IndexFunc(secrets.Presets, func(p secrets.Preset) bool { return p.ID == id })
		if i < 0 {
			t.Fatalf("secret preset %q does not exist", id)
		}
		p := secrets.Presets[i]
		if p.EnvVar != "GEMINI_API_KEY" || !p.Rule.MatchHost(GeminiAPIHost) || !strings.EqualFold(p.Rule.Header, "x-goog-api-key") {
			t.Fatalf("preset %+v", p)
		}
		if err := p.Rule.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

// advertised is what agy 1.5.14 answered to initialize with terminal auth.
const advertised = `[
 {"id": "oauth-personal", "name": "Log in with Google", "description": "Log in with your Google account"},
 {"id": "oauth-business", "name": "Log in with Gemini Enterprise", "description": "Log in with your Gemini Enterprise account"},
 {"id": "gemini-api-key", "name": "Gemini API key", "description": "Use an API key with Gemini Developer API"},
 {"id": "agent-platform", "name": "Gemini Enterprise Agent Platform", "description": "Use Gemini Enterprise Agent Platform (formerly Vertex AI) with Application Default Credentials"},
 {"id": "terminal-login", "name": "Log in from the terminal", "description": "Log in with your Google account by pasting a code in a terminal; works over SSH", "type": "terminal", "args": ["--login"]}
]`

// agy advertises its login methods without a type (agent methods) and
// terminal-login as a terminal method with "--login".
func TestLoginMethods(t *testing.T) {
	a := adapter(t)
	var methods []acp.AuthMethod
	if err := json.Unmarshal([]byte(advertised), &methods); err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, m := range methods {
		d, ok := harness.DescribeAuth(a, m)
		if !ok {
			t.Fatalf("%+v not understood", m)
		}
		types[d.ID] = d.Type
	}
	if types["gemini-api-key"] != "agent" || types["terminal-login"] != "terminal" {
		t.Fatalf("methods %v", types)
	}
	for _, id := range a.AutoAuthenticate {
		if types[id] != "agent" {
			t.Fatalf("auto-authenticate %q is not an advertised agent method", id)
		}
	}
	argv, _, ok := harness.LoginCommand(a, harness.Config{}, methods, "terminal-login")
	if !ok || strings.Join(argv, " ") != "agy acp --disable-telemetry --dangerously-skip-permissions --login" {
		t.Fatalf("terminal-login runs %q", argv)
	}
	if !slices.Contains(a.Command, "--dangerously-skip-permissions") {
		t.Fatalf("command missing --dangerously-skip-permissions: %v", a.Command)
	}
}

func TestAPIKeyLogin(t *testing.T) {
	l := logins{}
	key := "AIza" + strings.Repeat("x", 35)
	c, err := l.ParseLogin(t.Context(), nil, KindAPIKey, "  "+key+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if string(c.Secret) != key || strings.Contains(c.Label, key[:20]) {
		t.Fatalf("credential %+v", c)
	}
	for _, bad := range []string{"sk-ant-api03-xyz", "AIza short", "AIzaSy"} {
		if _, err := l.ParseLogin(t.Context(), nil, KindAPIKey, bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := l.ParseLogin(t.Context(), nil, "oauth-personal", key); err == nil {
		t.Fatal("unknown kind accepted")
	}

	d, err := l.Deliver(c, "seed1")
	if err != nil {
		t.Fatal(err)
	}
	ph := d.Env["GEMINI_API_KEY"]
	if !strings.Contains(ph, harness.PlaceholderMarker) || strings.Contains(ph, key) || d.InSandbox || len(d.Files) != 0 {
		t.Fatalf("delivery %+v", d)
	}
	if len(d.Injections) != 1 {
		t.Fatalf("injections %+v", d.Injections)
	}
	in := d.Injections[0]
	if in.Placeholder != ph || in.Value != key || !slices.Equal(in.Hosts, []string{GeminiAPIHost}) || !strings.EqualFold(in.Header, "x-goog-api-key") {
		t.Fatalf("injection %+v", in)
	}
}

func TestAuthorizationURLAndLoginResponse(t *testing.T) {
	raw := "https://accounts.google.com/o/oauth2/auth?response_type=code&state=abc123&redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback"
	parsed, ok := ExtractAuthorizationURL(AuthURLPrefix + raw)
	if !ok {
		t.Fatal("expected authorization URL to be extracted")
	}
	if parsed.State != "abc123" || parsed.RedirectURI != CodeRedirectURI {
		t.Fatalf("parsed = %+v", parsed)
	}
	if got, err := ValidateLoginResponse(parsed, "4/0AeanS-code_123"); err != nil || got != "4/0AeanS-code_123" {
		t.Fatalf("raw code: got=%q err=%v", got, err)
	}
	cb := CodeRedirectURI + "?state=abc123&code=xyz"
	if got, err := ValidateLoginResponse(parsed, cb); err != nil || got != cb {
		t.Fatalf("callback URL: got=%q err=%v", got, err)
	}
	if _, err := ValidateLoginResponse(parsed, CodeRedirectURI+"?state=wrong&code=xyz"); err == nil {
		t.Fatal("mismatched state accepted")
	}
}
