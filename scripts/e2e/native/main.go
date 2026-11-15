// Command native checks the public API against a running native development
// server. It installs the scripted harness only in its disposable test VM.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type smoke struct {
	base string
	http *http.Client
}

// options are the edge-node checks (scripts/e2e/edge.sh); all optional.
type options struct {
	// node is the name of the node the agent must be placed on.
	node string
	// secretHost is an HTTPS echo service: a secret for it must reach it
	// while the guest only ever sees the placeholder.
	secretHost string
	// nodeDown and nodeUp are shell commands that stop and restart the node.
	nodeDown, nodeUp string
}

func main() {
	base := flag.String("base", "http://localhost:8080", "development server URL")
	harness := flag.String("fakeagent", "build/guest/arm64/bin/fakeagent", "Linux scripted harness binary for the guest architecture")
	var o options
	flag.StringVar(&o.node, "node", "", "require the agent to run on the node with this name")
	flag.StringVar(&o.secretHost, "secret-host", "", "HTTPS echo host (GET /headers) for the secret injection check")
	flag.StringVar(&o.nodeDown, "node-down", "", "shell command that stops the agent's node")
	flag.StringVar(&o.nodeUp, "node-up", "", "shell command that starts the agent's node again")
	flag.Parse()
	if err := run(*base, *harness, o); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(base, harness string, o options) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	s := &smoke{strings.TrimRight(base, "/"), &http.Client{Jar: jar, Timeout: 90 * time.Second}}
	password := os.Getenv("AGENTHOLD_SMOKE_PASSWORD")
	if password == "" {
		password = "admin"
	}
	if _, err = s.request(ctx, http.MethodPost, "/auth/local", map[string]any{"username": "admin", "password": password}); err != nil {
		return err
	}
	created, err := s.request(ctx, http.MethodPost, "/agents", map[string]any{"name": "native-smoke", "harness": "fake", "cpus": 1, "memoryMb": 1024, "permissionPolicy": "auto-approve"})
	if err != nil {
		return err
	}
	var a struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(created, &a); err != nil || a.ID == "" {
		return fmt.Errorf("create agent: %s: %w", created, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = s.request(cleanup, http.MethodDelete, "/agents/"+a.ID, nil)
	}()
	path := "/agents/" + a.ID
	if err = s.wait(ctx, path, "running"); err != nil {
		return err
	}
	fmt.Println("PASS: agent boots through API and authenticated tunnel")
	if o.node != "" {
		var placed struct {
			NodeName string `json:"nodeName"`
		}
		data, err := s.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(data, &placed); err != nil || placed.NodeName != o.node {
			return fmt.Errorf("agent runs on %q, want %q (%w)", placed.NodeName, o.node, err)
		}
		fmt.Printf("PASS: agent placed on edge node %s\n", o.node)
	}
	binary, err := os.ReadFile(harness)
	if err != nil {
		return err
	}
	if err = s.upload(ctx, path, "fakeagent", binary); err != nil {
		return err
	}
	if err = s.terminal(ctx, path, "chmod +x /home/agent/fakeagent && sudo mv /home/agent/fakeagent /opt/agenthold/fakeagent && sudo touch /etc/native-smoke-system"); err != nil {
		return err
	}
	fmt.Println("PASS: terminal and file upload")
	if err = s.upload(ctx, path, "persist.txt", []byte("persistent home\n")); err != nil {
		return err
	}
	session, err := s.request(ctx, http.MethodPost, path+"/sessions", map[string]any{})
	if err != nil {
		return err
	}
	var ses struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(session, &ses); err != nil {
		return err
	}
	if err = s.prompt(ctx, ses.ID, "write chat.txt chat-created"); err != nil {
		return err
	}
	if err = s.fileEquals(ctx, path, "chat.txt", "chat-created\n"); err != nil {
		return err
	}
	fmt.Println("PASS: scripted harness chat writes a file")
	if _, err = s.request(ctx, http.MethodPost, path+"/stop", nil); err != nil {
		return err
	}
	if err = s.wait(ctx, path, "sleeping"); err != nil {
		return err
	}
	if _, err = s.request(ctx, http.MethodPost, path+"/start", nil); err != nil {
		return err
	}
	if err = s.wait(ctx, path, "running"); err != nil {
		return err
	}
	if err = s.fileEquals(ctx, path, "persist.txt", "persistent home\n"); err != nil {
		return err
	}
	if err = s.terminal(ctx, path, "test -e /etc/native-smoke-system"); err != nil {
		return err
	}
	if err = s.prompt(ctx, ses.ID, "write after-wake.txt continued"); err != nil {
		return err
	}
	if err = s.fileEquals(ctx, path, "after-wake.txt", "continued\n"); err != nil {
		return err
	}
	fmt.Println("PASS: sleep/wake preserves home, system and conversation")
	if o.nodeDown != "" {
		if err = s.nodeOutage(ctx, path, ses.ID, o); err != nil {
			return err
		}
	}
	const secretValue = "e2e-real-secret-value-kept-on-the-node"
	var placeholder string
	if o.secretHost != "" {
		// Attached secrets reach the environment with the next system reset.
		if placeholder, err = s.attachSecret(ctx, a.ID, o.secretHost, secretValue); err != nil {
			return err
		}
	}
	if _, err = s.request(ctx, http.MethodPost, path+"/rebuild", nil); err != nil {
		return err
	}
	if err = s.wait(ctx, path, "running"); err != nil {
		return err
	}
	if err = s.fileEquals(ctx, path, "persist.txt", "persistent home\n"); err != nil {
		return err
	}
	if err = s.terminal(ctx, path, "test ! -e /etc/native-smoke-system"); err != nil {
		return err
	}
	fmt.Println("PASS: reset system clears system changes and preserves home")
	if o.secretHost != "" {
		check := fmt.Sprintf(`test "$E2E_SECRET" = %q && ! env | grep -q %q && ! grep -rqs %q /etc/agenthold && `+
			`curl -fsS --max-time 30 https://%s/headers | tee /tmp/echo.json | grep -q %q`, placeholder, secretValue, secretValue, o.secretHost, secretValue)
		if err = s.terminal(ctx, path, check); err != nil {
			return fmt.Errorf("secret injection: %w", err)
		}
		fmt.Println("PASS: guest sees only the placeholder; the node's proxy injected the secret on the wire")
	}
	return nil
}

// nodeOutage stops the node, expects the agent to be unreachable (not in
// error) with operations failing fast, restarts the node and expects the
// agent, its files and its conversation back.
func (s *smoke) nodeOutage(ctx context.Context, path, sessionID string, o options) error {
	if err := shell(ctx, o.nodeDown); err != nil {
		return fmt.Errorf("node down: %w", err)
	}
	if err := s.wait(ctx, path, "unreachable"); err != nil {
		return err
	}
	began := time.Now()
	_, err := s.request(ctx, http.MethodPost, path+"/stop", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "offline") {
		return fmt.Errorf("stop on an offline node: %w", err)
	}
	if _, err = s.request(ctx, http.MethodGet, path+"/files/content?path=persist.txt", nil); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		return fmt.Errorf("file read on an offline node: %w", err)
	}
	if time.Since(began) > 5*time.Second {
		return errors.New("operations on an offline node did not fail fast")
	}
	fmt.Println("PASS: node down: agent unreachable, operations fail fast with a clear message")
	if err = shell(ctx, o.nodeUp); err != nil {
		return fmt.Errorf("node up: %w", err)
	}
	if err = s.waitAny(ctx, path, "running", "sleeping"); err != nil {
		return err
	}
	if err = s.ensureRunning(ctx, path); err != nil {
		return err
	}
	// sandboxd redials the restarted node's tunnel endpoint with backoff.
	for began := time.Now(); ; {
		if err = s.fileEquals(ctx, path, "persist.txt", "persistent home\n"); err == nil {
			fmt.Printf("  sandboxd reconnected after %s\n", time.Since(began).Round(100*time.Millisecond))
			break
		}
		if time.Since(began) > time.Minute {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err = s.fileEquals(ctx, path, "persist.txt", "persistent home\n"); err != nil {
		return err
	}
	if err = s.prompt(ctx, sessionID, "write after-outage.txt reconnected"); err != nil {
		return err
	}
	if err = s.fileEquals(ctx, path, "after-outage.txt", "reconnected\n"); err != nil {
		return err
	}
	fmt.Println("PASS: node reconnected: agent, files and conversation are back")
	return nil
}

// ensureRunning wakes the agent if its VM did not survive the node restart.
func (s *smoke) ensureRunning(ctx context.Context, path string) error {
	data, err := s.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var a struct {
		State string `json:"state"`
	}
	if err = json.Unmarshal(data, &a); err != nil {
		return err
	}
	fmt.Printf("  agent is %s after the node restart\n", a.State)
	if a.State == "running" {
		return nil
	}
	if _, err = s.request(ctx, http.MethodPost, path+"/start", nil); err != nil {
		return err
	}
	return s.wait(ctx, path, "running")
}

func shell(ctx context.Context, command string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (s *smoke) attachSecret(ctx context.Context, agentID, host, value string) (string, error) {
	data, err := s.request(ctx, http.MethodPost, "/secrets", map[string]any{
		"name": "e2e-edge-secret", "value": value,
		"rule": map[string]any{"hosts": []string{host}, "header": "X-Agenthold-E2e", "template": "{{.Value}}"},
	})
	if err != nil {
		return "", err
	}
	var sec struct {
		ID          string `json:"id"`
		Placeholder string `json:"placeholder"`
	}
	if err = json.Unmarshal(data, &sec); err != nil || sec.ID == "" || sec.Placeholder == "" {
		return "", fmt.Errorf("create secret: %s: %w", data, err)
	}
	if _, err = s.request(ctx, http.MethodPut, "/agents/"+agentID+"/secrets/"+sec.ID, map[string]any{"envVar": "E2E_SECRET"}); err != nil {
		return "", err
	}
	return sec.Placeholder, nil
}

func (s *smoke) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	contentType := "application/json"
	switch b := body.(type) {
	case []byte:
		r = bytes.NewReader(b)
		contentType = "application/octet-stream"
	case nil:
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+"/api/v1"+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	res, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, res.StatusCode, data)
	}
	return data, nil
}

func (s *smoke) upload(ctx context.Context, path, name string, data []byte) error {
	_, err := s.request(ctx, http.MethodPut, path+"/files/content?path="+url.QueryEscape(name), data)
	return err
}

func (s *smoke) fileEquals(ctx context.Context, path, name, want string) error {
	data, err := s.request(ctx, http.MethodGet, path+"/files/content?path="+url.QueryEscape(name), nil)
	if err != nil {
		return err
	}
	if string(data) != want {
		return fmt.Errorf("file %s: got %q, want %q", name, data, want)
	}
	return nil
}

func (s *smoke) wait(ctx context.Context, path, want string) error {
	return s.waitAny(ctx, path, want)
}

func (s *smoke) waitAny(ctx context.Context, path string, want ...string) error {
	for {
		data, err := s.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		var a struct {
			State string `json:"state"`
			Error string `json:"stateError"`
		}
		if err = json.Unmarshal(data, &a); err != nil {
			return err
		}
		for _, w := range want {
			if a.State == w {
				return nil
			}
		}
		if a.State == "error" {
			return fmt.Errorf("agent failed: %s", a.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *smoke) dial(ctx context.Context, path string) (*websocket.Conn, error) {
	u, err := url.Parse(s.base)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	for _, c := range s.http.Jar.Cookies(u) {
		header.Add("Cookie", c.String())
	}
	header.Set("Origin", s.base)
	wsURL := "ws" + strings.TrimPrefix(s.base, "http") + "/api/v1" + path
	c, res, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: header})
	if err != nil && res != nil {
		_ = res.Body.Close()
	}
	return c, err
}

func (s *smoke) terminal(ctx context.Context, path, command string) error {
	c, err := s.dial(ctx, path+"/terminal?cols=100&rows=30")
	if err != nil {
		return err
	}
	defer func() { _ = c.CloseNow() }()
	if err = c.Write(ctx, websocket.MessageBinary, []byte(command+"; code=$?; exit $code\n")); err != nil {
		return err
	}
	var output strings.Builder
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return fmt.Errorf("terminal: %w: %s", err, output.String())
		}
		if typ == websocket.MessageBinary {
			output.Write(data)
			continue
		}
		var msg struct {
			Type, Message string
			Code          int
		}
		if err = json.Unmarshal(data, &msg); err != nil {
			return err
		}
		switch msg.Type {
		case "error":
			return errors.New(msg.Message)
		case "exit":
			if msg.Code != 0 {
				return fmt.Errorf("terminal command exit %d: %s", msg.Code, output.String())
			}
			return nil
		}
	}
}

func (s *smoke) prompt(ctx context.Context, id, text string) error {
	c, err := s.dial(ctx, "/sessions/"+id+"/stream?after=0")
	if err != nil {
		return err
	}
	defer func() { _ = c.CloseNow() }()
	// Remember the current cursor so an old turn_end cannot complete a later prompt.
	data, err := s.request(ctx, http.MethodGet, "/sessions/"+id+"/events", nil)
	if err != nil {
		return err
	}
	var events []struct {
		Seq int64 `json:"seq"`
	}
	if err = json.Unmarshal(data, &events); err != nil {
		return err
	}
	var after int64
	for _, e := range events {
		after = max(after, e.Seq)
	}
	if _, err = s.request(ctx, http.MethodPost, "/sessions/"+id+"/prompt", map[string]string{"text": text}); err != nil {
		return err
	}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return err
		}
		var e struct {
			Seq  int64  `json:"seq"`
			Type string `json:"type"`
		}
		if err = json.Unmarshal(data, &e); err != nil {
			return err
		}
		if e.Seq > after && e.Type == "turn_end" {
			return nil
		}
	}
}
