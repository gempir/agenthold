package api_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/session"
)

func TestGetSessionVisualEndpoint(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")

	var sess gen.Session
	expect(t, "create session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)

	seq, err := e.sessions.RecordVisual(t.Context(), sess.Id, session.VisualPayload{
		Title: "Quarterly Chart!",
		HTML:  "<!doctype html><html><body><h1>Q4</h1></body></html>",
	})
	if err != nil {
		t.Fatal(err)
	}
	appSeq, err := e.sessions.RecordMCPApp(t.Context(), sess.Id, session.MCPAppPayload{
		Title:  "SQL Explorer",
		HTML:   "<div>sql ui</div>",
		Server: "postgres",
	})
	if err != nil {
		t.Fatal(err)
	}

	fetch := func(c *client, path string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}

	// Owner can fetch the visual HTML with strict CSP headers.
	res, body := fetch(alice, fmt.Sprintf("/sessions/%s/visuals/%d", sess.Id, seq))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "<h1>Q4</h1>") {
		t.Fatalf("body = %q", body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "sandbox allow-scripts") || !strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", res.Header.Get("X-Content-Type-Options"))
	}
	if res.Header.Get("Content-Disposition") != "" {
		t.Fatalf("unexpected Content-Disposition without download=true: %q", res.Header.Get("Content-Disposition"))
	}

	// ?download=true adds a sanitized attachment filename.
	res, _ = fetch(alice, fmt.Sprintf("/sessions/%s/visuals/%d?download=true", sess.Id, seq))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d", res.StatusCode)
	}
	if cd := res.Header.Get("Content-Disposition"); cd != `attachment; filename="quarterly-chart.html"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}

	// MCP App event HTML is also retrievable via the same endpoint.
	res, body = fetch(alice, fmt.Sprintf("/sessions/%s/visuals/%d", sess.Id, appSeq))
	if res.StatusCode != http.StatusOK || body != "<div>sql ui</div>" {
		t.Fatalf("mcp app visual: status=%d body=%q", res.StatusCode, body)
	}

	// Missing event seq returns 404.
	res, _ = fetch(alice, fmt.Sprintf("/sessions/%s/visuals/999", sess.Id))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing seq status = %d, want 404", res.StatusCode)
	}

	// Unshared user cannot read the visual.
	res, _ = fetch(bob, fmt.Sprintf("/sessions/%s/visuals/%d", sess.Id, seq))
	if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger status = %d, want 403 or 404", res.StatusCode)
	}
}
