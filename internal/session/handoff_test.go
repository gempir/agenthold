package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestSelectHistoryPrioritizesLatestAndOriginalWithinBudget(t *testing.T) {
	msgs := []HistoricalMessage{
		{Role: "user", Kind: "user_message", Text: "Original goal: build auth system", Seq: 1, Status: "completed"},
		{Role: "assistant", Kind: "assistant_message", Text: strings.Repeat("middle-a ", 200), Seq: 2, Status: "completed"},
		{Role: "user", Kind: "user_message", Text: strings.Repeat("middle-u ", 200), Seq: 3, Status: "completed"},
		{Role: "assistant", Kind: "assistant_message", Text: "Latest answer: JWT verified", Seq: 4, Status: "completed"},
		{Role: "user", Kind: "user_message", Text: "Latest request: add refresh tokens", Seq: 5, Status: "completed"},
	}

	// Tight budget fits original user (1), latest assistant (4), latest user (5),
	// and omits the large middle items (2, 3).
	sel := SelectHistory("ses_1", "claude-code", "codex", msgs, 2400)
	if len(sel.Messages) != 3 {
		t.Fatalf("expected 3 selected items, got %d: %+v", len(sel.Messages), sel.Messages)
	}
	if sel.Messages[0].Seq != 1 || sel.Messages[1].Seq != 4 || sel.Messages[2].Seq != 5 {
		t.Fatalf("wrong order/selection: seqs = %d, %d, %d", sel.Messages[0].Seq, sel.Messages[1].Seq, sel.Messages[2].Seq)
	}
	if sel.OmittedItems != 2 || len(sel.OmittedSeqs) != 2 || sel.OmittedSeqs[0] != 2 || sel.OmittedSeqs[1] != 3 {
		t.Fatalf("omitted = %d %v", sel.OmittedItems, sel.OmittedSeqs)
	}
	if !strings.Contains(sel.Context, "switched from claude-code to codex") || !strings.Contains(sel.Context, "read_thread") {
		t.Fatalf("context header missing expected hints: %s", sel.Context)
	}
	if !strings.Contains(sel.Context, "2, 3") {
		t.Fatalf("context header missing omitted seq IDs: %s", sel.Context)
	}
}

func TestFoldHistoryEventsCoalescesChunksAndTools(t *testing.T) {
	mk := func(seq int64, typ string, payload any) db.AgentSessionEvent {
		b, _ := json.Marshal(payload)
		return db.AgentSessionEvent{SessionID: "ses_1", Seq: seq, Type: typ, Data: string(b)}
	}
	rows := []db.AgentSessionEvent{
		mk(1, EventPrompt, map[string]any{"text": "check tests"}),
		mk(2, EventUpdate, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Running "}}),
		mk(3, EventUpdate, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "tests now."}}),
		mk(4, EventUpdate, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tc1", "title": "go test", "status": "running"}),
		mk(5, EventUpdate, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tc1", "status": "completed", "rawOutput": "PASS"}),
		mk(6, EventTurnEnd, map[string]any{"stopReason": "end_turn"}),
	}
	msgs := FoldHistoryEvents(rows)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Text != "check tests" {
		t.Fatalf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Text != "Running tests now." {
		t.Fatalf("msg[1] = %+v", msgs[1])
	}
	if msgs[2].Kind != "tool_call" || !strings.Contains(msgs[2].Text, "go test") || !strings.Contains(msgs[2].Text, "PASS") {
		t.Fatalf("msg[2] = %+v", msgs[2])
	}
}

func TestIsSlashCommand(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"/compact", true},
		{"  /plan build feature", true},
		{"/usr/bin/env bash", false},
		{"please run /compact", false},
	} {
		if got := IsSlashCommand(tc.in); got != tc.want {
			t.Errorf("IsSlashCommand(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
