package claude

import (
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/harness"
)

func TestClaudeLogins(t *testing.T) {
	a := adapter{}
	for _, bad := range []struct{ kind, in string }{
		{KindOAuthToken, "sk-ant-api03-x"}, {KindOAuthToken, "sk-ant-oat01-a b"}, {KindAPIKey, "sk-ant-oat01-abc"}, {KindAPIKey, "sk-other"}, {"x", "sk-ant-1"},
	} {
		if _, err := a.ParseLogin(t.Context(), nil, bad.kind, bad.in); err == nil {
			t.Errorf("accepted %s %q", bad.kind, bad.in)
		}
	}
	for _, tc := range []struct{ kind, in, env, header, prefix string }{
		{KindOAuthToken, "sk-ant-oat01-subscriptiontoken-abcd", "CLAUDE_CODE_OAUTH_TOKEN", "Authorization", "sk-ant-oat01-"},
		{KindAPIKey, "sk-ant-api03-consolekey-wxyz", "ANTHROPIC_API_KEY", "X-Api-Key", "sk-ant-api03-"},
	} {
		c, err := a.ParseLogin(t.Context(), nil, tc.kind, tc.in+"\n")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(c.Label, tc.in[:20]) {
			t.Fatalf("label reveals the token: %q", c.Label)
		}
		d, err := a.Deliver(c, "seed")
		if err != nil {
			t.Fatal(err)
		}
		ph := d.Env[tc.env]
		if !strings.HasPrefix(ph, tc.prefix+harness.PlaceholderMarker) || strings.Contains(ph, tc.in) || len(d.Files) != 0 {
			t.Fatalf("%s: env %v", tc.kind, d.Env)
		}
		if len(d.Injections) != 1 || d.Injections[0].Hosts[0] != "api.anthropic.com" || d.Injections[0].Header != tc.header || d.Injections[0].Placeholder != ph || d.Injections[0].Value != tc.in {
			t.Fatalf("%s: injections %+v", tc.kind, d.Injections)
		}
	}
}
