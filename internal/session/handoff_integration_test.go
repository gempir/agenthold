package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/acpstore"
	"github.com/gempir/agenthold/internal/store/db"
)

// TestHarnessSwitchContextHandoffAndSlashDeferral verifies that switching an
// agent's harness mid-thread triggers budgeted context handoff on the next
// non-slash turn, while deferring handoff across slash commands just like T3 Code.
func TestHarnessSwitchContextHandoffAndSlashDeferral(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Register a second local ACP harness (also backed by fakeBin) in custom_harnesses.
	now := time.Now().Unix()
	if err := e.st.CreateCustomHarness(ctx, db.CreateCustomHarnessParams{
		ID:               "loc_second",
		OwnerID:          "u_1",
		Kind:             "local",
		Name:             "Second ACP Harness",
		Command:          fakeBin,
		ArgsJson:         "[]",
		EnvJson:          "{}",
		CustomModelsJson: "[]",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatal(err)
	}
	e.m.SetResolver(&acpstore.Resolver{Store: e.st})

	// Turn 1 on harness "fake".
	evs1 := e.promptAndWait("remember secret token alpha-99")
	if got := assistantText(evs1); !strings.Contains(got, "echo: remember secret token alpha-99") {
		t.Fatalf("turn 1 reply = %q", got)
	}

	// Switch the agent to "loc_second" and disconnect the old harness process.
	if err := e.st.SetAgentHarness(ctx, db.SetAgentHarnessParams{
		Harness: "loc_second", UpdatedAt: now, ID: agentID,
	}); err != nil {
		t.Fatal(err)
	}
	e.m.Disconnect(agentID)

	// Turn 2 is a slash command: handoff MUST be deferred so the slash command
	// runs cleanly without being wrapped in a handoff transcript.
	evs2 := e.promptAndWait("/status")
	reply2 := assistantText(evs2)
	if strings.Contains(reply2, "remember secret token alpha-99") {
		t.Fatalf("slash command should defer context handoff, got reply: %q", reply2)
	}
	if !strings.Contains(reply2, "echo: /status") {
		t.Fatalf("expected slash command echo, got %q", reply2)
	}

	// Turn 3 is a normal prompt: the deferred handoff MUST now be delivered!
	evs3 := e.promptAndWait("what was my secret token?")
	reply3 := assistantText(evs3)
	if !strings.Contains(reply3, "remember secret token alpha-99") || !strings.Contains(reply3, "what was my secret token?") {
		t.Fatalf("expected deferred context handoff on turn 3, got %q", reply3)
	}
	var sawNotice bool
	for _, ev := range evs3 {
		if ev.Type == EventNotice && strings.Contains(string(ev.Data), "context handoff") {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Fatalf("expected context handoff notice on turn 3, got %+v", evs3)
	}
}

func (e *env) promptAndWait(text string) []Event {
	e.t.Helper()
	ctx := context.Background()
	_, sub, err := e.m.Subscribe(ctx, e.sid, 0, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer sub.Close()
	if err := e.m.Prompt(ctx, e.sid, "u_1", "U", text, nil); err != nil {
		e.t.Fatal(err)
	}
	deadline := time.After(15 * time.Second)
	var out []Event
	for {
		select {
		case ev := <-sub.C:
			out = append(out, ev)
			if ev.Type == EventTurnEnd {
				return out
			}
		case <-deadline:
			e.t.Fatalf("timed out waiting for turn_end after %q (got %d events)", text, len(out))
		}
	}
}

func assistantText(evs []Event) string {
	var b strings.Builder
	for _, ev := range evs {
		if ev.Type == EventUpdate {
			s := string(ev.Data)
			if strings.Contains(s, "agent_message_chunk") {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}
