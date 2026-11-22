package platform

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestThreadTools(t *testing.T) {
	p, ag := newExposureService(t, "")
	ctx := context.Background()
	// A second agent of the same owner: its threads are not agt_1's.
	if err := p.Store.CreateAgent(ctx, db.CreateAgentParams{ID: "agt_2", OwnerID: "usr_1", Name: "other", Image: "x", Harness: "fake", Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ id, agent string }{{"ses_cur", ag}, {"ses_old", ag}, {"ses_x", "agt_2"}} {
		if err := p.Store.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: s.id, AgentID: s.agent, Title: "T " + s.id, CreatedBy: "usr_1", CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	callIn := func(session, tool string, args map[string]any) (string, error) {
		b, _ := json.Marshal(args)
		out, err := p.Call(ctx, ag, session, tool, b)
		s, _ := out.(string)
		return s, err
	}
	tools, _ := p.Call(ctx, ag, "", "_list_tools", nil)
	if b, _ := json.Marshal(tools); !strings.Contains(string(b), `"settle_thread"`) || !strings.Contains(string(b), `"list_threads"`) || !strings.Contains(string(b), `"unsettle_thread"`) {
		t.Fatalf("tools: %s", b)
	}

	list := func(includeSettled bool) []threadInfo {
		t.Helper()
		out, err := callIn("ses_cur", "list_threads", map[string]any{"include_settled": includeSettled})
		if err != nil {
			t.Fatal(err)
		}
		var ts []threadInfo
		if strings.HasPrefix(out, "[") {
			if err := json.Unmarshal([]byte(out), &ts); err != nil {
				t.Fatalf("%q: %v", out, err)
			}
		}
		return ts
	}
	if ts := list(false); len(ts) != 2 {
		t.Fatalf("list: %+v", ts)
	}
	for _, th := range list(false) {
		if th.Current != (th.ID == "ses_cur") {
			t.Fatalf("current: %+v", th)
		}
	}

	// Without thread_id: the current thread.
	if out, err := callIn("ses_cur", "settle_thread", nil); err != nil || !strings.Contains(out, "Settled thread ses_cur") {
		t.Fatalf("settle current: %q %v", out, err)
	}
	s, _ := p.Store.GetAgentSession(ctx, "ses_cur")
	if !s.SettledAt.Valid || s.SettledBy != "agent:"+ag {
		t.Fatalf("settled: %+v", s)
	}
	if out, _ := callIn("ses_cur", "settle_thread", nil); !strings.Contains(out, "settled already") {
		t.Fatalf("again: %q", out)
	}
	if ts := list(false); len(ts) != 1 || ts[0].ID != "ses_old" {
		t.Fatalf("active: %+v", ts)
	}
	if ts := list(true); len(ts) != 2 {
		t.Fatalf("all: %+v", ts)
	}
	if out, err := callIn("ses_cur", "settle_thread", map[string]any{"thread_id": "ses_old"}); err != nil || !strings.Contains(out, "ses_old") {
		t.Fatalf("settle by id: %q %v", out, err)
	}
	if out, _ := callIn("ses_cur", "list_threads", nil); out != "You have no active threads." {
		t.Fatalf("none active: %q", out)
	}

	// Other agents' threads are out of reach, even when the sandbox names one as current.
	if _, err := callIn("ses_x", "settle_thread", nil); !errors.Is(err, ErrNoThread) {
		t.Fatalf("other agent's thread: %v", err)
	}
	if _, err := callIn("", "unsettle_thread", map[string]any{"thread_id": "ses_x"}); !errors.Is(err, ErrNoThread) {
		t.Fatalf("unsettle other agent's thread: %v", err)
	}
	if _, err := callIn("ses_cur", "unsettle_thread", nil); err == nil {
		t.Fatal("unsettle without thread_id")
	}
	if out, err := callIn("", "unsettle_thread", map[string]any{"thread_id": "ses_cur"}); err != nil || !strings.Contains(out, "active again") {
		t.Fatalf("unsettle: %q %v", out, err)
	}
	if s, _ := p.Store.GetAgentSession(ctx, "ses_cur"); s.SettledAt.Valid {
		t.Fatalf("still settled: %+v", s)
	}

	rows, _ := p.Store.ListAgentAudit(ctx, db.ListAgentAuditParams{AgentID: ag, ID: 1 << 62, Limit: 100})
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Action]++
		if !strings.Contains(r.Details, `"fromAgentId":"`+ag+`"`) {
			t.Fatalf("audit without the agent: %+v", r)
		}
	}
	if counts[audit.SessionSettle] != 2 || counts[audit.SessionUnsettle] != 1 {
		t.Fatalf("audit: %v", counts)
	}
}
