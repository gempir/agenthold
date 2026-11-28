package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// visualTools let agents render inline HTML visuals and interactive MCP App UIs
// directly into the current thread's event stream.
var visualTools = []map[string]any{
	{
		"name": "html_render",
		"description": "Render a self-contained HTML visual (chart, diagram, table, mockup, etc.) inline in the current thread. " +
			"The HTML runs in a tightly sandboxed iframe that adapts to the user's light/dark theme via CSS variables " +
			"(--bg, --fg, --muted, --border, --accent). Maximum HTML size is 512 KiB.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"html"},
			"properties": map[string]any{
				"title":  map[string]any{"type": "string", "description": "Optional short title shown in the visual card header (up to 200 characters)."},
				"html":   map[string]any{"type": "string", "description": "Self-contained HTML document or fragment to render inline (max 512 KiB)."},
				"height": map[string]any{"type": "integer", "description": "Optional initial height in pixels (clamped to 120–900; auto-sizes by default)."},
			},
		},
	},
	{
		"name": "render_mcp_app",
		"description": "Render an interactive MCP App UI card inline in the current thread (optionally attached below a tool call). " +
			"Non-read-only actions sent from the embedded UI require explicit user Allow/Decline confirmation before executing. " +
			"Maximum HTML size is 512 KiB.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"title", "html"},
			"properties": map[string]any{
				"title":        map[string]any{"type": "string", "description": "Short title shown in the MCP App card header."},
				"html":         map[string]any{"type": "string", "description": "Self-contained HTML UI for the interactive MCP App (max 512 KiB)."},
				"server":       map[string]any{"type": "string", "description": "Optional MCP server name providing this UI."},
				"tool_call_id": map[string]any{"type": "string", "description": "Optional tool call id to attach this UI directly below."},
				"resource_uri": map[string]any{"type": "string", "description": "Optional ui:// resource URI."},
				"height":       map[string]any{"type": "integer", "description": "Optional initial height in pixels (clamped to 120–900; auto-sizes by default)."},
				"actions": map[string]any{
					"type":        "array",
					"description": "Optional list of actions this MCP App can invoke. Actions not marked read_only require user confirmation.",
					"items": map[string]any{
						"type":     "object",
						"required": []string{"name"},
						"properties": map[string]any{
							"name":        map[string]any{"type": "string", "description": "Action identifier."},
							"label":       map[string]any{"type": "string", "description": "Human-friendly label."},
							"description": map[string]any{"type": "string", "description": "Short description shown in the confirmation prompt."},
							"read_only":   map[string]any{"type": "boolean", "description": "Whether the action is strictly read-only (default false)."},
						},
					},
				},
			},
		},
	},
}

func isVisualTool(name string) bool {
	return name == "html_render" || name == "render_mcp_app"
}

type visualActionArg struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	ReadOnlyCC  bool   `json:"readOnly"`
}

type visualArgs struct {
	Title       string            `json:"title"`
	HTML        string            `json:"html"`
	Height      int               `json:"height"`
	Server      string            `json:"server"`
	ToolCallID  string            `json:"tool_call_id"`
	ToolCallCC  string            `json:"toolCallId"`
	ResourceURI string            `json:"resource_uri"`
	ResourceCC  string            `json:"resourceUri"`
	Actions     []visualActionArg `json:"actions"`
}

func (p *Service) visualCall(ctx context.Context, caller db.Agent, sessionID, tool string, raw json.RawMessage) (any, error) {
	if p.Sessions == nil {
		return nil, errors.New("sessions are not available on this server")
	}
	var in visualArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, errors.New("invalid arguments")
		}
	}
	s, err := p.ownThread(ctx, caller, "", sessionID)
	if err != nil {
		return nil, err
	}
	switch tool {
	case "html_render":
		seq, err := p.Sessions.RecordVisual(ctx, s.ID, session.VisualPayload{
			Title:  in.Title,
			HTML:   in.HTML,
			Height: in.Height,
		})
		if err != nil {
			return nil, err
		}
		resp := map[string]any{
			"ok":         true,
			"event_seq":  seq,
			"session_id": s.ID,
			"url":        fmt.Sprintf("/api/v1/sessions/%s/visuals/%d", s.ID, seq),
		}
		if t := strings.TrimSpace(in.Title); t != "" {
			resp["title"] = t
		}
		b, _ := json.MarshalIndent(resp, "", "  ")
		return string(b), nil

	case "render_mcp_app":
		title := strings.TrimSpace(in.Title)
		if title == "" {
			return nil, errors.New("title is required")
		}
		toolCallID := strings.TrimSpace(in.ToolCallID)
		if toolCallID == "" {
			toolCallID = strings.TrimSpace(in.ToolCallCC)
		}
		resourceURI := strings.TrimSpace(in.ResourceURI)
		if resourceURI == "" {
			resourceURI = strings.TrimSpace(in.ResourceCC)
		}
		actions := make([]session.MCPAppAction, 0, len(in.Actions))
		for _, a := range in.Actions {
			actions = append(actions, session.MCPAppAction{
				Name:        a.Name,
				Label:       a.Label,
				Description: a.Description,
				ReadOnly:    a.ReadOnly || a.ReadOnlyCC,
			})
		}
		seq, err := p.Sessions.RecordMCPApp(ctx, s.ID, session.MCPAppPayload{
			Title:       title,
			HTML:        in.HTML,
			Server:      in.Server,
			ToolCallID:  toolCallID,
			ResourceURI: resourceURI,
			Actions:     actions,
			Height:      in.Height,
		})
		if err != nil {
			return nil, err
		}
		resp := map[string]any{
			"ok":         true,
			"event_seq":  seq,
			"session_id": s.ID,
			"title":      title,
			"url":        fmt.Sprintf("/api/v1/sessions/%s/visuals/%d", s.ID, seq),
		}
		if srv := strings.TrimSpace(in.Server); srv != "" {
			resp["server"] = srv
		}
		if toolCallID != "" {
			resp["tool_call_id"] = toolCallID
		}
		b, _ := json.MarshalIndent(resp, "", "  ")
		return string(b), nil
	}
	return nil, fmt.Errorf("unknown tool %q", tool)
}
