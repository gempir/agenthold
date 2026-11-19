package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// api is deliberately small: Firecracker has a local HTTP/1 Unix socket API.
// Response bodies are bounded and no request body (which could be sensitive) is
// included in errors. Each instance is short lived; close idle fds after use.
type api struct {
	client    *http.Client
	transport *http.Transport
}

func newAPI(socket string) *api {
	t := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialSocket(ctx, socket)
	}, DisableKeepAlives: true}
	return &api{client: &http.Client{Transport: t, Timeout: 3 * time.Second}, transport: t}
}
func (a *api) close() { a.transport.CloseIdleConnections() }
func (a *api) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("firecracker %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("firecracker %s %s: HTTP %d: %s", method, path, res.StatusCode, bytes.TrimSpace(data))
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			return fmt.Errorf("firecracker %s response: %w", path, err)
		}
	}
	return nil
}

func (a *api) put(ctx context.Context, path string, b any) error {
	return a.request(ctx, http.MethodPut, path, b, nil)
}

func (a *api) action(ctx context.Context, name string) error {
	return a.put(ctx, "/actions", map[string]string{"action_type": name})
}
