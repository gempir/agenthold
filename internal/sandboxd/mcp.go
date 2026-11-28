package sandboxd

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ServeMCP runs the platform MCP server on stdio ("sandboxd mcp"). The
// harness starts it for every session; it forwards tool calls through the
// platform socket of the sandbox's sandboxd to the agenthold server. The tools
// themselves (and their descriptions) come from the server.
func ServeMCP(in io.Reader, out io.Writer) error {
	session := os.Getenv("AGENTHOLD_SESSION")
	home, _ := os.UserHomeDir()
	if h := os.Getenv("HOME"); h != "" {
		home = h
	}
	sock := filepath.Join(home, PlatformSocket)
	if s := os.Getenv("AGENTHOLD_PLATFORM_SOCKET"); s != "" {
		sock = s
	}

	call := func(tool string, args json.RawMessage) (json.RawMessage, error) {
		var d net.Dialer
		c, err := d.DialContext(context.Background(), "unix", sock)
		if err != nil {
			return nil, fmt.Errorf("agenthold platform unavailable: %w", err)
		}
		defer c.Close()
		if err := json.NewEncoder(c).Encode(PlatformRequest{Tool: tool, Session: session, Args: args}); err != nil {
			return nil, err
		}
		var res PlatformResponse
		dec := json.NewDecoder(c)
		if err := dec.Decode(&res); err != nil {
			return nil, err
		}
		if res.Error != "" {
			return nil, errors.New(res.Error)
		}
		return res.Result, nil
	}

	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if len(msg.ID) == 0 {
			continue // notification (initialized, cancelled, ...)
		}
		reply := func(result any) {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
		}
		replyErr := func(code int, m string) {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": code, "message": m}})
		}
		switch msg.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			reply(map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "agenthold", "version": "1"},
				"instructions":    "Tools to work with your other agenthold agents and agents shared with your owner.",
			})
		case "ping":
			reply(map[string]any{})
		case "tools/list":
			res, err := call("_list_tools", nil)
			if err != nil {
				replyErr(-32603, err.Error())
				continue
			}
			var tools any
			_ = json.Unmarshal(res, &tools)
			reply(map[string]any{"tools": tools})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				replyErr(-32602, "invalid params")
				continue
			}
			res, err := call(p.Name, p.Arguments)
			if err != nil {
				reply(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": err.Error()}}})
				continue
			}
			if blocks, ok := mcpContent(res, home); ok {
				reply(map[string]any{"content": blocks})
				continue
			}
			var text string
			if json.Unmarshal(res, &text) != nil {
				text = string(res)
			}
			reply(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
		default:
			replyErr(-32601, "method not found")
		}
	}
	return sc.Err()
}

// mcpMaxImage bounds an image file inlined into a tool result.
const mcpMaxImage = 8 << 20

// mcpContent turns a result of the form {"mcpContent": [blocks]} into MCP
// content blocks. An image block naming a file (below the screenshots
// directory of the home) is read and inlined here, in the sandbox.
func mcpContent(res json.RawMessage, home string) ([]any, bool) {
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			File     string `json:"file"`
		} `json:"mcpContent"`
	}
	if len(res) == 0 || res[0] != '{' || json.Unmarshal(res, &r) != nil || len(r.Content) == 0 {
		return nil, false
	}
	dir := filepath.Join(home, ScreenshotDir) + string(filepath.Separator)
	out := make([]any, 0, len(r.Content))
	for _, b := range r.Content {
		switch b.Type {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": b.Text})
		case "image":
			data := b.Data
			if b.File != "" {
				f := filepath.Clean(b.File)
				fi, err := os.Stat(f)
				if !strings.HasPrefix(f, dir) || err != nil || fi.Size() > mcpMaxImage {
					continue
				}
				raw, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				data = base64.StdEncoding.EncodeToString(raw)
			}
			if data != "" {
				out = append(out, map[string]any{"type": "image", "data": data, "mimeType": b.MimeType})
			}
		}
	}
	return out, len(out) > 0
}
