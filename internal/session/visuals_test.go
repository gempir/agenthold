package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestRecordVisualAndGetVisualEvent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	sess, err := e.m.Create(ctx, agentID, "u_1", "Visuals")
	if err != nil {
		t.Fatal(err)
	}
	_, sub, err := e.m.Subscribe(ctx, sess.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// Empty HTML is rejected.
	if _, err := e.m.RecordVisual(ctx, sess.ID, VisualPayload{Title: "Empty", HTML: "   "}); !errors.Is(err, ErrVisualEmpty) {
		t.Fatalf("empty html err = %v, want ErrVisualEmpty", err)
	}
	// Oversized HTML (> 512 KiB) is rejected.
	tooBig := strings.Repeat("a", MaxVisualHTMLBytes+1)
	if _, err := e.m.RecordVisual(ctx, sess.ID, VisualPayload{HTML: tooBig}); !errors.Is(err, ErrVisualTooLarge) {
		t.Fatalf("oversized html err = %v, want ErrVisualTooLarge", err)
	}
	// Missing session returns ErrNotFound.
	if _, err := e.m.RecordVisual(ctx, "ses_missing", VisualPayload{HTML: "<p>hi</p>"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session err = %v, want ErrNotFound", err)
	}

	// Exact 512 KiB boundary is accepted.
	atLimit := "<p>" + strings.Repeat("x", MaxVisualHTMLBytes-7) + "</p>"
	if len(atLimit) != MaxVisualHTMLBytes {
		t.Fatalf("test fixture len = %d, want %d", len(atLimit), MaxVisualHTMLBytes)
	}
	seq1, err := e.m.RecordVisual(ctx, sess.ID, VisualPayload{
		Title:  "  Revenue Chart  ",
		HTML:   atLimit,
		Height: 50, // below min -> clamped to 120
	})
	if err != nil {
		t.Fatalf("RecordVisual at limit: %v", err)
	}
	if seq1 != 1 {
		t.Fatalf("seq1 = %d, want 1", seq1)
	}

	// Live subscriber receives the visual event immediately.
	select {
	case ev := <-sub.C:
		if ev.Type != EventVisual || ev.Seq != seq1 {
			t.Fatalf("unexpected live event: %+v", ev)
		}
		var p VisualPayload
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			t.Fatal(err)
		}
		if p.Title != "Revenue Chart" || p.Height != MinVisualHeight || len(p.HTML) != MaxVisualHTMLBytes {
			t.Fatalf("unexpected payload: title=%q height=%d htmlLen=%d", p.Title, p.Height, len(p.HTML))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for visual event")
	}

	// Height above max is clamped to 900.
	seq2, err := e.m.RecordVisual(ctx, sess.ID, VisualPayload{
		Title:  strings.Repeat("T", 250),
		HTML:   "<div>chart</div>",
		Height: 5000,
	})
	if err != nil {
		t.Fatalf("RecordVisual 2: %v", err)
	}
	ev2 := <-sub.C
	var p2 VisualPayload
	_ = json.Unmarshal(ev2.Data, &p2)
	if p2.Height != MaxVisualHeight {
		t.Fatalf("height = %d, want %d", p2.Height, MaxVisualHeight)
	}
	if len([]rune(p2.Title)) != MaxVisualTitleLen {
		t.Fatalf("title len = %d, want %d", len([]rune(p2.Title)), MaxVisualTitleLen)
	}

	// GetVisualEvent loads stored visual HTML.
	doc, err := e.m.GetVisualEvent(ctx, sess.ID, seq2)
	if err != nil {
		t.Fatalf("GetVisualEvent: %v", err)
	}
	if doc.Type != EventVisual || doc.HTML != "<div>chart</div>" || doc.Seq != seq2 {
		t.Fatalf("doc = %+v", doc)
	}
	if _, err := e.m.GetVisualEvent(ctx, sess.ID, 999); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("missing seq err = %v, want ErrEventNotFound", err)
	}

	// Non-visual events are not served as visuals.
	if err := e.m.log.append(ctx, sess.ID, EventNotice, "", map[string]string{"message": "hi"}); err != nil {
		t.Fatal(err)
	}
	noticeEv := <-sub.C
	if _, err := e.m.GetVisualEvent(ctx, sess.ID, noticeEv.Seq); !errors.Is(err, ErrEventNotFound) {
		t.Fatalf("non-visual event err = %v, want ErrEventNotFound", err)
	}
}

func TestRecordMCPAppAndHistoryFold(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	sess, err := e.m.Create(ctx, agentID, "u_1", "MCP Apps")
	if err != nil {
		t.Fatal(err)
	}
	_, sub, err := e.m.Subscribe(ctx, sess.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	seq, err := e.m.RecordMCPApp(ctx, sess.ID, MCPAppPayload{
		Title:       "Issue Triage",
		HTML:        "<button>Close issue</button>",
		Server:      "github",
		ToolCallID:  "tc_42",
		ResourceURI: "ui://github/issue/42",
		Height:      420,
		Actions: []MCPAppAction{
			{Name: "refresh", Label: "Refresh", ReadOnly: true},
			{Name: "close_issue", Label: "Close issue", Description: "Closes issue #42", ReadOnly: false},
			{Name: "   ", Label: "ignored empty"},
		},
	})
	if err != nil {
		t.Fatalf("RecordMCPApp: %v", err)
	}
	ev := <-sub.C
	if ev.Type != EventMCPApp || ev.Seq != seq {
		t.Fatalf("ev = %+v", ev)
	}
	var got MCPAppPayload
	if err := json.Unmarshal(ev.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Server != "github" || got.ToolCallID != "tc_42" || got.ResourceURI != "ui://github/issue/42" || len(got.Actions) != 2 {
		t.Fatalf("got = %+v", got)
	}
	if !got.Actions[0].ReadOnly || got.Actions[1].ReadOnly {
		t.Fatalf("actions = %+v", got.Actions)
	}

	// GetVisualEvent also serves mcp_app events.
	doc, err := e.m.GetVisualEvent(ctx, sess.ID, seq)
	if err != nil {
		t.Fatalf("GetVisualEvent(mcp_app): %v", err)
	}
	if doc.Type != EventMCPApp || doc.Title != "Issue Triage" || doc.HTML != "<button>Close issue</button>" {
		t.Fatalf("doc = %+v", doc)
	}

	// FoldHistoryEvents summarizes visual and mcp_app events without dumping HTML.
	visRaw, _ := json.Marshal(VisualPayload{Title: "Latency Chart", HTML: "<div>HUGE_SECRET_HTML</div>"})
	appRaw, _ := json.Marshal(got)
	msgs := FoldHistoryEvents([]db.AgentSessionEvent{
		{Seq: 1, Type: EventVisual, Data: string(visRaw)},
		{Seq: 2, Type: EventMCPApp, Data: string(appRaw)},
	})
	if len(msgs) != 2 {
		t.Fatalf("msgs = %+v", msgs)
	}
	if msgs[0].Kind != "visual" || !strings.Contains(msgs[0].Text, "Latency Chart") || strings.Contains(msgs[0].Text, "HUGE_SECRET_HTML") {
		t.Fatalf("visual fold = %+v", msgs[0])
	}
	if msgs[1].Kind != "mcp_app" || !strings.Contains(msgs[1].Text, "Issue Triage (github)") || strings.Contains(msgs[1].Text, "<button>") {
		t.Fatalf("mcp_app fold = %+v", msgs[1])
	}
}
