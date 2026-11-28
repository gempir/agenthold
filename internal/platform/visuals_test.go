package platform

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestVisualAndMCPAppTools(t *testing.T) {
	p, ag := newExposureService(t, "")
	p.Sessions = session.NewManager(p.Store, nil, nil)
	t.Cleanup(p.Sessions.Close)
	ctx := context.Background()

	// Create another agent to verify cross-agent session isolation.
	if err := p.Store.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agt_2", OwnerID: "usr_1", Name: "other", Image: "x", Harness: "fake",
		Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := p.Sessions.Create(ctx, ag, "usr_1", "Charts")
	if err != nil {
		t.Fatal(err)
	}
	otherSess, err := p.Sessions.Create(ctx, "agt_2", "usr_1", "Other")
	if err != nil {
		t.Fatal(err)
	}

	// _list_tools exposes both html_render and render_mcp_app.
	tools, err := p.Call(ctx, ag, sess.ID, "_list_tools", nil)
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := json.Marshal(tools)
	if !strings.Contains(string(tb), `"html_render"`) || !strings.Contains(string(tb), `"render_mcp_app"`) {
		t.Fatalf("expected visual tools in _list_tools: %s", tb)
	}

	_, sub, err := p.Sessions.Subscribe(ctx, sess.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	callIn := func(sessionID, tool string, args map[string]any) (string, error) {
		b, _ := json.Marshal(args)
		out, err := p.Call(ctx, ag, sessionID, tool, b)
		s, _ := out.(string)
		return s, err
	}

	// html_render stores a visual event and broadcasts it live.
	out, err := callIn(sess.ID, "html_render", map[string]any{
		"title":  "CPU Usage",
		"html":   "<svg width='100' height='40'></svg>",
		"height": 260,
	})
	if err != nil {
		t.Fatalf("html_render: %v", err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal response %q: %v", out, err)
	}
	if res["ok"] != true || res["event_seq"] != float64(1) || res["session_id"] != sess.ID {
		t.Fatalf("unexpected response: %+v", res)
	}
	select {
	case ev := <-sub.C:
		if ev.Type != session.EventVisual || ev.Seq != 1 {
			t.Fatalf("unexpected event: %+v", ev)
		}
		var payload session.VisualPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Title != "CPU Usage" || payload.Height != 260 || !strings.Contains(payload.HTML, "<svg") {
			t.Fatalf("payload = %+v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for visual event")
	}

	// html_render rejects > 512 KiB HTML.
	if _, err := callIn(sess.ID, "html_render", map[string]any{
		"html": strings.Repeat("x", session.MaxVisualHTMLBytes+1),
	}); !errors.Is(err, session.ErrVisualTooLarge) {
		t.Fatalf("oversized html err = %v, want ErrVisualTooLarge", err)
	}

	// html_render rejects empty HTML.
	if _, err := callIn(sess.ID, "html_render", map[string]any{"html": ""}); !errors.Is(err, session.ErrVisualEmpty) {
		t.Fatalf("empty html err = %v, want ErrVisualEmpty", err)
	}

	// html_render refuses another agent's session.
	if _, err := callIn(otherSess.ID, "html_render", map[string]any{"html": "<p>x</p>"}); !errors.Is(err, ErrNoThread) {
		t.Fatalf("other agent session err = %v, want ErrNoThread", err)
	}

	// render_mcp_app validates title and records an mcp_app event with actions.
	if _, err := callIn(sess.ID, "render_mcp_app", map[string]any{"html": "<div>app</div>"}); err == nil {
		t.Fatal("render_mcp_app without title should fail")
	}
	out, err = callIn(sess.ID, "render_mcp_app", map[string]any{
		"title":        "Deploy Controls",
		"html":         "<div><button>Deploy</button></div>",
		"server":       "vercel",
		"tool_call_id": "tc_99",
		"resource_uri": "ui://vercel/deploy",
		"actions": []map[string]any{
			{"name": "status", "label": "Check status", "read_only": true},
			{"name": "deploy_prod", "label": "Deploy to prod", "description": "Triggers production deploy"},
		},
	})
	if err != nil {
		t.Fatalf("render_mcp_app: %v", err)
	}
	if !strings.Contains(out, `"event_seq": 2`) || !strings.Contains(out, `"server": "vercel"`) {
		t.Fatalf("render_mcp_app response = %s", out)
	}
	select {
	case ev := <-sub.C:
		if ev.Type != session.EventMCPApp || ev.Seq != 2 {
			t.Fatalf("unexpected mcp_app event: %+v", ev)
		}
		var payload session.MCPAppPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Title != "Deploy Controls" || payload.Server != "vercel" || payload.ToolCallID != "tc_99" || len(payload.Actions) != 2 {
			t.Fatalf("mcp_app payload = %+v", payload)
		}
		if !payload.Actions[0].ReadOnly || payload.Actions[1].ReadOnly {
			t.Fatalf("actions = %+v", payload.Actions)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mcp_app event")
	}

	// render_mcp_app refuses another agent's session.
	if _, err := callIn(otherSess.ID, "render_mcp_app", map[string]any{"title": "X", "html": "<p>x</p>"}); !errors.Is(err, ErrNoThread) {
		t.Fatalf("other agent mcp_app err = %v, want ErrNoThread", err)
	}
}
