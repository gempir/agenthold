package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/store/db"
)

func (e *env) setAgentChoice(c Choice) {
	e.t.Helper()
	if err := e.st.SetAgentModel(context.Background(), db.SetAgentModelParams{Model: c.Model, ModelOptions: EncodeOptions(c.Options), UpdatedAt: time.Now().Unix(), ID: agentID}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) setSessionChoice(c Choice) {
	e.t.Helper()
	if err := e.m.SetSessionChoice(context.Background(), e.sid, c); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) catalog() Catalog {
	e.t.Helper()
	c, ok, err := e.m.Catalog(context.Background(), agentID)
	if err != nil || !ok {
		e.t.Fatalf("catalog: ok %v, err %v", ok, err)
	}
	return c
}

func TestModelCatalogCaptured(t *testing.T) {
	e := newEnv(t)
	if _, ok, _ := e.m.Catalog(context.Background(), agentID); ok {
		t.Fatal("catalog before the harness ever ran")
	}
	if r := e.ok("model"); r.text != "model=swift-1 effort=medium fast=off" {
		t.Fatalf("text %q", r.text)
	}
	c := e.catalog()
	mo, ok := c.ModelOption()
	if !ok || mo.Current != "swift-1" || len(mo.Values) != 4 || mo.Values[0].Group != "Fake Labs" || mo.Values[2].Group != "Other" {
		t.Fatalf("model option %+v", mo)
	}
	opts := c.ByModel["swift-1"]
	if len(opts) != 2 || opts[0].Kind != KindEffort || opts[1].Kind != KindToggle || len(opts[0].Values) != 3 {
		t.Fatalf("swift-1 options %+v", opts)
	}
	if c.Harness != "fake" || time.Since(c.SeenAt) > time.Minute {
		t.Fatalf("catalog %s %v", c.Harness, c.SeenAt)
	}
}

func TestAgentDefaultApplied(t *testing.T) {
	e := newEnv(t)
	e.setAgentChoice(Choice{Model: "deep-1", Options: map[string]string{"effort": "max"}})
	if r := e.ok("model"); r.text != "model=deep-1 effort=max fast=off" || len(r.notices) != 0 {
		t.Fatalf("text %q, notices %q", r.text, r.notices)
	}
	// The catalog learned deep-1's options too.
	if opts := e.catalog().ByModel["deep-1"]; len(opts) != 1 || len(opts[0].Values) != 5 {
		t.Fatalf("deep-1 options %+v", opts)
	}
}

func TestSessionChoiceSwitchesMidSession(t *testing.T) {
	e := newEnv(t)
	e.ok("hello")
	e.setSessionChoice(Choice{Model: "swift-1", Options: map[string]string{"effort": "high", "fast": "on"}})
	if r := e.ok("model"); r.text != "model=swift-1 effort=high fast=on" {
		t.Fatalf("text %q", r.text)
	}
	// The session's choice wins over the agent's default.
	e.setAgentChoice(Choice{Model: "deep-1"})
	if r := e.ok("model"); r.text != "model=swift-1 effort=high fast=on" {
		t.Fatalf("text %q", r.text)
	}
	e.setSessionChoice(Choice{})
	if r := e.ok("model"); !strings.HasPrefix(r.text, "model=deep-1 ") {
		t.Fatalf("text %q", r.text)
	}
	if n := len(e.pids()); n != 1 {
		t.Fatalf("%d harness processes: switching must not restart the harness", n)
	}
}

func TestChoiceSurvivesHarnessRestart(t *testing.T) {
	e := newEnv(t)
	e.setSessionChoice(Choice{Model: "deep-1", Options: map[string]string{"effort": "low"}})
	e.ok("hello")
	e.m.Disconnect(agentID) // the reloaded session starts on the harness default again
	if r := e.ok("model"); r.text != "model=deep-1 effort=low fast=off" {
		t.Fatalf("text %q", r.text)
	}
}

func TestModelSwitchRefused(t *testing.T) {
	e := newEnv(t)
	e.setSessionChoice(Choice{Model: "broken-1"})
	r := e.ok("model")
	if r.text != "model=swift-1 effort=medium fast=off" {
		t.Fatalf("text %q", r.text)
	}
	if len(r.notices) != 1 || !strings.Contains(r.notices[0], "refused to switch to the model \"broken-1\": Broken 1 is not available on this plan") || !strings.Contains(r.notices[0], "uses Swift 1") {
		t.Fatalf("notices %q", r.notices)
	}
	// Said once per session, and the session keeps working.
	if r := e.ok("again"); len(r.notices) != 0 {
		t.Fatalf("notices %q", r.notices)
	}
}

func TestUnknownModelAndOption(t *testing.T) {
	e := newEnv(t)
	// An old free-text setting the harness does not list, and an effort the
	// model does not have: the turn runs on what the harness has.
	e.setAgentChoice(Choice{Model: "gone-0", Options: map[string]string{"effort": "max", "fast": "on"}})
	r := e.ok("model")
	if r.text != "model=swift-1 effort=medium fast=on" {
		t.Fatalf("text %q", r.text)
	}
	if len(r.notices) != 2 || !strings.Contains(r.notices[0], `does not offer the model "gone-0"`) || !strings.Contains(r.notices[1], `Effort: "max" is not available`) {
		t.Fatalf("notices %q", r.notices)
	}
}

func TestHarnessWithoutConfigOptions(t *testing.T) {
	e := newEnv(t)
	e.script("no-config")
	// The agent default reaches the harness through its environment.
	e.setAgentChoice(Choice{Model: "deep-1"})
	if r := e.ok("model"); !strings.HasPrefix(r.text, "model=deep-1 ") || len(r.notices) != 0 {
		t.Fatalf("text %q, notices %q", r.text, r.notices)
	}
	e.setSessionChoice(Choice{Model: "tiny-1"})
	r := e.ok("model")
	if !strings.HasPrefix(r.text, "model=deep-1 ") || len(r.notices) != 1 || !strings.Contains(r.notices[0], "uses the agent's default model") {
		t.Fatalf("text %q, notices %q", r.text, r.notices)
	}
	if _, ok, _ := e.m.Catalog(context.Background(), agentID); ok {
		t.Fatal("catalog from a harness that reported nothing")
	}
}
