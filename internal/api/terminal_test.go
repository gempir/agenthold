package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/gempir/agenthold/internal/api/gen"
)

func (e *env) agentFor(c *client, name string) gen.Agent {
	e.t.Helper()
	var ag gen.Agent
	expect(e.t, "create agent", c.do("POST", "/agents", gen.AgentInput{Name: name}, &ag), 201)
	e.sandboxes.Wait()
	got := c.agent(ag.Id)
	if got.State != gen.SandboxStateRunning {
		e.t.Fatalf("agent state %s: %s", got.State, got.StateError)
	}
	return got
}

func (c *client) raw(method, path string, body io.Reader) *http.Response {
	c.t.Helper()
	req, _ := http.NewRequestWithContext(c.t.Context(), method, c.base+"/api/v1"+path, body)
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return res
}

func TestFilesAPI(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.agentFor(alice, "files")
	base := "/agents/" + ag.Id + "/files"

	res := alice.raw("PUT", base+"/content?path=src/main.go", strings.NewReader("package main\n"))
	res.Body.Close()
	expect(t, "upload", res.StatusCode, 204)
	expect(t, "mkdir", alice.do("POST", base+"/mkdir", map[string]string{"path": "docs/empty"}, nil), 204)

	var ls gen.DirListing
	expect(t, "list", alice.do("GET", base, nil, &ls), 200)
	names := []string{}
	for _, e := range ls.Entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "docs,src" || !ls.Entries[0].IsDir {
		t.Fatalf("listing = %v", names)
	}

	res = alice.raw("GET", base+"/content?path=src/main.go", nil)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(b) != "package main\n" || !strings.Contains(res.Header.Get("Content-Disposition"), "main.go") {
		t.Fatalf("download: %d %q %v", res.StatusCode, b, res.Header)
	}
	res = alice.raw("GET", base+"/content?path=nope.txt", nil)
	res.Body.Close()
	expect(t, "missing file", res.StatusCode, 404)

	expect(t, "delete", alice.do("DELETE", base+"?path=docs&recursive=true", nil, nil), 204)

	// Others cannot see the agent's files; a sleeping agent's files are unavailable.
	bob := e.login("bob", "Bob", "b@example.com")
	expect(t, "outsider list", bob.do("GET", base, nil, nil), 404)
	expect(t, "stop", alice.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "list while sleeping", alice.do("GET", base, nil, nil), 409)
}

func TestTerminalWakesAgent(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.agentFor(alice, "term")
	expect(t, "stop", alice.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/agents/" + ag.Id + "/terminal?cols=80&rows=24"
	hdr := http.Header{"Origin": {e.srv.URL}}
	ws, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: alice.http, HTTPHeader: hdr})
	if res != nil && res.Body != nil {
		res.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()

	var out bytes.Buffer
	sentCmd := false
	var exit struct {
		Type    string `json:"type"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	for exit.Type != "exit" {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (output %q)", err, out.String())
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
			if !sentCmd {
				sentCmd = true
				_ = wsjson.Write(ctx, ws, map[string]any{"type": "resize", "cols": 100, "rows": 30})
				_ = ws.Write(ctx, websocket.MessageBinary, []byte("echo term-$((40+2)); stty size; exit 7\n"))
			}
			continue
		}
		if err := jsonUnmarshalTest(data, &exit); err != nil {
			t.Fatal(err)
		}
		if exit.Type == "error" {
			t.Fatalf("terminal error: %s", exit.Message)
		}
		if exit.Type == "status" && !sentCmd {
			continue
		}
	}
	if !strings.Contains(out.String(), "term-42") || !strings.Contains(out.String(), "30 100") || exit.Code != 7 {
		t.Fatalf("output %q exit %d", out.String(), exit.Code)
	}
	if got := alice.agent(ag.Id); got.State != gen.SandboxStateRunning {
		t.Fatalf("agent not woken: %s", got.State)
	}
}

func TestTerminalRequiresOperator(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.agentFor(alice, "term")
	bob := e.login("bob", "Bob", "b@example.com")
	res := bob.raw("GET", "/agents/"+ag.Id+"/terminal", nil)
	res.Body.Close()
	expect(t, "outsider terminal", res.StatusCode, 404)

	// A cross-site page cannot open the socket with the user's cookie.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/agents/" + ag.Id + "/terminal"
	_, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: alice.http, HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	if res != nil && res.Body != nil {
		res.Body.Close()
	}
	if err == nil {
		t.Fatal("cross-origin websocket accepted")
	}
}
