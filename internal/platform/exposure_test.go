package platform

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

func newExposureService(t *testing.T, suffix string) (*Service, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateUser(ctx, db.CreateUserParams{ID: "usr_1", Name: "A", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{ID: "agt_1", OwnerID: "usr_1", Name: "web", Image: "x", Harness: "fake", Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	p := &Service{Store: st, Audit: &audit.Log{Store: st}, Preview: &preview.Service{Store: st, Hosts: preview.NewHosts(suffix, "https://agents.example.com")}}
	return p, "agt_1"
}

func call(t *testing.T, p *Service, agentID, tool string, args map[string]any) (string, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	out, err := p.Call(context.Background(), agentID, "", tool, b)
	s, _ := out.(string)
	return s, err
}

func TestExposureTools(t *testing.T) {
	p, ag := newExposureService(t, ".apps.example.com")
	tools, _ := p.Call(context.Background(), ag, "", "_list_tools", nil)
	if b, _ := json.Marshal(tools); !strings.Contains(string(b), `"expose_port"`) || !strings.Contains(string(b), `"unexpose"`) {
		t.Fatalf("tools: %s", b)
	}
	if _, err := call(t, p, ag, "expose_port", map[string]any{"port": 8000, "access": "public"}); !errors.Is(err, ErrPublic) {
		t.Fatalf("public: %v", err)
	}
	out, err := call(t, p, ag, "expose_port", map[string]any{"port": 8000, "access": "authenticated"})
	if err != nil || !strings.Contains(out, "https://web-8000.apps.example.com/") || !strings.Contains(out, "access: authenticated") {
		t.Fatalf("expose: %q %v", out, err)
	}
	if out, _ := call(t, p, ag, "expose_port", map[string]any{"port": 8000}); !strings.Contains(out, "already exposed") {
		t.Fatalf("again: %q", out)
	}
	if _, err := call(t, p, ag, "expose_port", map[string]any{"port": 8001, "name": "www"}); err == nil {
		t.Fatal("reserved name accepted")
	}
	out, _ = call(t, p, ag, "list_exposures", nil)
	var list []map[string]any
	if json.Unmarshal([]byte(out), &list) != nil || len(list) != 1 || list[0]["port"] != float64(8000) {
		t.Fatalf("list: %q", out)
	}
	e, _ := p.Preview.ByPort(context.Background(), ag, 8000)
	if e.CreatedBy != "agent:"+ag || e.WakeOnRequest != 0 {
		t.Fatalf("exposure = %+v", e)
	}
	if out, err := call(t, p, ag, "unexpose", map[string]any{"port": 8000}); err != nil || !strings.Contains(out, "no longer exposed") {
		t.Fatalf("unexpose: %q %v", out, err)
	}
	if _, err := call(t, p, ag, "unexpose", map[string]any{"port": 8000}); err == nil {
		t.Fatal("unexpose twice succeeded")
	}
	if out, _ := call(t, p, ag, "list_exposures", nil); out != "No ports are exposed." {
		t.Fatalf("empty list: %q", out)
	}
}

func TestExposureToolsDisabled(t *testing.T) {
	p, ag := newExposureService(t, "")
	tools, _ := p.Call(context.Background(), ag, "", "_list_tools", nil)
	if b, _ := json.Marshal(tools); strings.Contains(string(b), "expose_port") {
		t.Fatal("exposure tools listed without preview URLs")
	}
	if _, err := call(t, p, ag, "expose_port", map[string]any{"port": 1}); !errors.Is(err, preview.ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}
