package codex

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/harness"
)

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(b) + ".real-signature-from-openai"
}

var idToken = jwt(map[string]any{
	"email": "dev@example.com", "aud": "app_x",
	"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1"},
})

func tokenServer(t *testing.T, handle func(w http.ResponseWriter, body map[string]string)) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		handle(w, body)
	}))
	t.Cleanup(srv.Close)
	old := TokenURL
	TokenURL = srv.URL
	t.Cleanup(func() { TokenURL = old })
}

func TestChatGPTLoginIsTakenOverAndRefreshed(t *testing.T) {
	exp := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	var got map[string]string
	valid := "rt-1"
	tokenServer(t, func(w http.ResponseWriter, body map[string]string) {
		got = body
		if body["refresh_token"] != valid {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"refresh_token_reused","echo":"` + body["refresh_token"] + `"}`))
			return
		}
		valid = "rt-2" // rotation: rt-1 is spent
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": jwt(map[string]any{"exp": exp.Unix()}), "refresh_token": "rt-2"})
	})
	a := adapter{}
	authJSON, _ := json.Marshal(map[string]any{"OPENAI_API_KEY": nil, "tokens": map[string]any{
		"id_token": idToken, "access_token": jwt(map[string]any{"exp": 1}), "refresh_token": "rt-1", "account_id": "acct-1",
	}, "last_refresh": "2026-01-01T00:00:00Z"})
	c, err := a.ParseLogin(t.Context(), http.DefaultClient, KindChatGPT, string(authJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got["client_id"] != ClientID || got["grant_type"] != "refresh_token" {
		t.Fatalf("refresh request %v", got)
	}
	if !c.ExpiresAt.Equal(exp) || c.Label != "ChatGPT dev@example.com (plus)" || !strings.Contains(string(c.Secret), "rt-2") {
		t.Fatalf("credential %+v", c)
	}
	if !a.RefreshDue(c, exp.Add(-30*time.Minute)) || a.RefreshDue(c, exp.Add(-2*time.Hour)) {
		t.Fatal("RefreshDue")
	}

	// The old refresh token is spent: the login is reported as revoked, without the provider's body.
	_, err = a.ParseLogin(t.Context(), http.DefaultClient, KindChatGPT, string(authJSON))
	if !errors.Is(err, harness.ErrLoginRevoked) || strings.Contains(err.Error(), "rt-1") {
		t.Fatalf("reuse: %v", err)
	}

	d, err := a.Deliver(c, "seed123")
	if err != nil {
		t.Fatal(err)
	}
	file := string(d.Files[authFile])
	for _, secret := range []string{"rt-2", "real-signature-from-openai", jwtPart(c, "access")} {
		if strings.Contains(file, secret) || strings.Contains(strings.Join(mapValues(d.Env), " "), secret) {
			t.Fatalf("auth.json contains %q:\n%s", secret, file)
		}
	}
	var f struct {
		Tokens map[string]string `json:"tokens"`
	}
	if err := json.Unmarshal(d.Files[authFile], &f); err != nil {
		t.Fatal(err)
	}
	if f.Tokens["account_id"] != "acct-1" || !strings.Contains(f.Tokens["refresh_token"], harness.PlaceholderMarker) {
		t.Fatalf("tokens %v", f.Tokens)
	}
	// Codex reads the expiry and claims from these JWTs.
	if claims, err := jwtClaims(f.Tokens["access_token"]); err != nil || claims["exp"].(float64) < float64(time.Now().AddDate(50, 0, 0).Unix()) {
		t.Fatalf("placeholder access token %v %v", claims, err)
	}
	if claims, err := jwtClaims(f.Tokens["id_token"]); err != nil || idEmail(claims) != "dev@example.com" {
		t.Fatalf("placeholder id token %v %v", claims, err)
	}
	if len(d.Injections) != 1 || d.Injections[0].Hosts[0] != "chatgpt.com" || d.Injections[0].Placeholder != f.Tokens["access_token"] || d.Injections[0].Value != jwtPart(c, "access") {
		t.Fatalf("injections %+v", d.Injections)
	}
	if len(d.Intercepts) != 1 || d.Intercepts[0].Host != "auth.openai.com" || d.Intercepts[0].Path != "/oauth/token" || d.Intercepts[0].Placeholder != f.Tokens["refresh_token"] {
		t.Fatalf("intercepts %+v", d.Intercepts)
	}
	resp, _ := a.InterceptResponse(c, "seed123")
	var r map[string]string
	_ = json.Unmarshal(resp, &r)
	if r["access_token"] != f.Tokens["access_token"] || r["refresh_token"] != f.Tokens["refresh_token"] || strings.Contains(string(resp), "rt-2") {
		t.Fatalf("intercept response %s", resp)
	}
}

func jwtPart(c harness.Credential, which string) string {
	var t tokens
	_ = json.Unmarshal(c.Secret, &t)
	return map[string]string{"access": t.AccessToken, "refresh": t.RefreshToken}[which]
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestCodexKeysAndValidation(t *testing.T) {
	a := adapter{}
	for _, bad := range []struct{ kind, in string }{
		{KindAPIKey, "nope"},
		{KindAPIKey, "sk-a b"},
		{KindAccessToken, "sk-123"},
		{KindChatGPT, "{}"},
		{KindChatGPT, `{"OPENAI_API_KEY":"sk-1"}`},
		{KindChatGPT, "not json"},
		{"other", "x"},
	} {
		if _, err := a.ParseLogin(t.Context(), http.DefaultClient, bad.kind, bad.in); err == nil {
			t.Errorf("accepted %s %q", bad.kind, bad.in)
		}
	}
	c, err := a.ParseLogin(t.Context(), nil, KindAPIKey, " sk-proj-secretvalue9876 \n")
	if err != nil || c.Label != "API key …9876" {
		t.Fatal(c, err)
	}
	d, _ := a.Deliver(c, "s1")
	if ph := d.Env["OPENAI_API_KEY"]; !strings.HasPrefix(ph, "sk-agenthold-placeholder-") || strings.Contains(string(d.Files[authFile]), "secretvalue") {
		t.Fatalf("api key delivery %+v", d)
	}
	c, err = a.ParseLogin(t.Context(), nil, KindAccessToken, "at-longlivedtoken1111")
	if err != nil {
		t.Fatal(err)
	}
	d, _ = a.Deliver(c, "s2")
	if ph := d.Env["CODEX_ACCESS_TOKEN"]; !strings.HasPrefix(ph, "at-") || len(d.Files) != 0 || len(d.Injections) != 2 {
		t.Fatalf("access token delivery %+v", d)
	}
	for _, in := range d.Injections {
		if len(in.PathPrefixes) == 0 {
			t.Fatalf("access token injected on all of %v", in.Hosts)
		}
	}
}
