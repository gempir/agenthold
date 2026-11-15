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
	"strings"
	"time"

	"github.com/coder/websocket"
)

type smoke struct {
	base string
	http *http.Client
}

func main() {
	base := flag.String("base", "http://localhost:8080", "development server URL")
	harness := flag.String("fakeagent", "build/guest/arm64/bin/fakeagent", "Linux scripted harness binary for the guest architecture")
	flag.Parse()
	if err := run(*base, *harness); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(base, harness string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
	return nil
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
		if a.State == want {
			return nil
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
