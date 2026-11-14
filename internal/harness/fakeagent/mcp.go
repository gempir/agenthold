package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// callMCP starts a stdio MCP server, initializes it and calls one tool.
func callMCP(srv *acp.McpServerStdio, tool string, args map[string]any) (string, error) {
	cmd := exec.CommandContext(context.Background(), srv.Command, srv.Args...)
	cmd.Env = os.Environ()
	for _, e := range srv.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	rd := bufio.NewScanner(stdout)
	rd.Buffer(make([]byte, 64*1024), 16<<20)
	enc := json.NewEncoder(stdin)
	id := 0
	rpc := func(method string, params any) (json.RawMessage, error) {
		id++
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for rd.Scan() {
			var m struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(rd.Bytes(), &m) != nil || m.ID != id {
				continue
			}
			if m.Error != nil {
				return nil, errors.New(m.Error.Message)
			}
			return m.Result, nil
		}
		return nil, errors.New("MCP server closed")
	}
	if _, err := rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fakeagent"}}); err != nil {
		return "", err
	}
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	res, err := rpc("tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var r struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	if r.IsError {
		return "", fmt.Errorf("tool error: %s", b.String())
	}
	return b.String(), nil
}
