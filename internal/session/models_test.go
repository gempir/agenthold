package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

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
	if len(r.notices) != 1 || !strings.Contains(r.notices[0], "refused to switch the model \"broken-1\": Broken 1 is not available on this plan") || !strings.Contains(r.notices[0], "uses Swift 1") {
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
	// The model is not refused in advance (aliases resolve in the harness):
	// the harness decides.
	if len(r.notices) != 2 || !strings.Contains(r.notices[0], `refused to switch the model "gone-0"`) || !strings.Contains(r.notices[1], `Effort: "max" is not available`) {
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
	// Recorded as "reported, no model option": a new default needs a restart.
	if c := e.catalog(); len(c.Options) != 0 {
		t.Fatalf("catalog %+v", c)
	}
	if e.m.LiveSwitchable(agentID) {
		t.Fatal("a harness without a model option is switchable")
	}
	e.setSessionChoice(Choice{})
	e.setAgentChoice(Choice{Model: "tiny-1"})
	e.m.Restart(agentID)
	if r := e.ok("model"); !strings.HasPrefix(r.text, "model=tiny-1 ") {
		t.Fatalf("after restart %q", r.text)
	}
}

func TestChoiceNeverSetsMode(t *testing.T) {
	e := newEnv(t)
	// A prompt-only user must not switch the harness's permission mode
	// through a model choice: only effort and toggles are ever set.
	e.setSessionChoice(Choice{Model: "swift-1", Options: map[string]string{"mode": "bypass", "effort": "high"}})
	if r := e.ok("mode"); r.text != "mode=default" {
		t.Fatalf("text %q", r.text)
	}
	if r := e.ok("model"); r.text != "model=swift-1 effort=high fast=off" {
		t.Fatalf("text %q", r.text)
	}
}

func TestClearingRevertsToHarnessDefaults(t *testing.T) {
	e := newEnv(t)
	e.setSessionChoice(Choice{Model: "swift-1", Options: map[string]string{"effort": "low", "fast": "on"}})
	if r := e.ok("model"); r.text != "model=swift-1 effort=low fast=on" {
		t.Fatalf("text %q", r.text)
	}
	// Fast mode removed from the choice: off again.
	e.setSessionChoice(Choice{Model: "swift-1", Options: map[string]string{"effort": "low"}})
	if r := e.ok("model"); r.text != "model=swift-1 effort=low fast=off" {
		t.Fatalf("text %q", r.text)
	}
	e.setSessionChoice(Choice{Model: "deep-1", Options: map[string]string{"effort": "max"}})
	e.ok("model")
	// Nothing chosen: the model and options a fresh session starts with.
	e.setSessionChoice(Choice{})
	if r := e.ok("model"); r.text != "model=swift-1 effort=medium fast=off" {
		t.Fatalf("text %q", r.text)
	}
}

func TestSetHangsKeepsTheTurn(t *testing.T) {
	setTimeout(t, &setOptionTimeout, 300*time.Millisecond)
	e := newEnv(t)
	e.ok("hello")
	e.script("session/set_config_option hang")
	e.setSessionChoice(Choice{Model: "deep-1"})
	r := e.ok("model")
	if r.text != "model=swift-1 effort=medium fast=off" || len(r.notices) != 1 || !strings.Contains(r.notices[0], "did not answer") {
		t.Fatalf("text %q, notices %q", r.text, r.notices)
	}
	// Given up for this connection: not tried (and waited for) again.
	start := time.Now()
	if r := e.ok("model"); r.text != "model=swift-1 effort=medium fast=off" || len(r.notices) != 0 {
		t.Fatalf("text %q, notices %q", r.text, r.notices)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatal("the hung switch was tried again")
	}
	if n := len(e.pids()); n != 1 {
		t.Fatalf("%d harness processes: a hung switch must not restart the harness", n)
	}
	// A new connection tries again.
	e.m.Disconnect(agentID)
	if r := e.ok("model"); !strings.HasPrefix(r.text, "model=deep-1 ") {
		t.Fatalf("after reconnect %q", r.text)
	}
}

func TestCatalogBounded(t *testing.T) {
	long := strings.Repeat("x", 300)
	vals := acp.SessionConfigSelectOptionsUngrouped{}
	for i := range 600 {
		vals = append(vals, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(fmt.Sprint(i)), Name: long})
	}
	vals = append(vals, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(long), Name: "too long"})
	in := []acp.SessionConfigOption{}
	for i := range 30 {
		in = append(in, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: acp.SessionConfigId(fmt.Sprint("o", i)), Name: long, Type: "select", CurrentValue: "0",
			Options: acp.SessionConfigSelectOptions{Ungrouped: &vals},
		}})
	}
	out := fromACP(in)
	if len(out) != maxOptions || len(out[0].Values) != maxValues || len(out[0].Name) != maxString || len(out[0].Values[0].Name) != maxString {
		t.Fatalf("%d options, %d values, name %d", len(out), len(out[0].Values), len(out[0].Name))
	}
}

func TestModelProbeDescribesEveryModel(t *testing.T) {
	e := newEnv(t)
	e.m.SetModelProbe(true)
	e.ok("hello") // the harness has only described swift-1 (the current model)
	waitFor(t, "every model described", func() bool {
		c, ok, _ := e.m.Catalog(context.Background(), agentID)
		if !ok {
			return false
		}
		_, deep := c.ByModel["deep-1"]
		_, tiny := c.ByModel["tiny-1"]
		return deep && tiny
	})
	c := e.catalog()
	if opts := c.ByModel["deep-1"]; len(opts) != 1 || opts[0].Kind != KindEffort || len(opts[0].Values) != 5 {
		t.Fatalf("deep-1 options %+v", opts)
	}
	if opts := c.ByModel["tiny-1"]; opts == nil || len(opts) != 0 {
		t.Fatalf("tiny-1 has no options, got %+v", opts)
	}
	// broken-1 refuses to be selected: not described, not tried again.
	if _, ok := c.ByModel["broken-1"]; ok {
		t.Fatal("a refused model has options")
	}
	// The probe used a session of its own: the conversation did not move and
	// the harness's defaults are those of its first session.
	if r := e.ok("model"); r.text != "model=swift-1 effort=medium fast=off" || len(r.notices) != 0 {
		t.Fatalf("text %q, notices %q", r.text, r.notices)
	}
	if c.Defaults["model"] != "swift-1" || c.Defaults["effort"] != "medium" {
		t.Fatalf("defaults %v", c.Defaults)
	}
	if n := len(e.pids()); n != 1 {
		t.Fatalf("%d harness processes", n)
	}
}

func TestModelProbeOffByDefault(t *testing.T) {
	e := newEnv(t)
	e.ok("hello")
	if err := e.m.ProbeModels(context.Background(), agentID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.catalog().ByModel["deep-1"]; !ok {
		t.Fatal("an explicit probe describes the models")
	}
}

func TestModelProbeYieldsToTurns(t *testing.T) {
	e := newEnv(t)
	e.ok("hello")
	sub := e.startAndWait("slow 20")
	// A turn uses the harness: nothing is probed.
	if err := e.m.ProbeModels(context.Background(), agentID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.catalog().ByModel["deep-1"]; ok {
		t.Fatal("probed while a turn runs")
	}
	if err := e.m.Cancel(context.Background(), e.sid); err != nil {
		t.Fatal(err)
	}
	e.wait(sub, 5*time.Second)
}

func TestEffortInModelIdsIsSplit(t *testing.T) {
	e := newEnv(t)
	e.script("effort-in-model")
	e.ok("hello")
	c := e.catalog()
	mo, _ := c.ModelOption()
	var ids []string
	for _, v := range mo.Values {
		ids = append(ids, v.Value)
	}
	if strings.Join(ids, " ") != "swift-1 deep-1 tiny-1 broken-1" || mo.Current != "swift-1" {
		t.Fatalf("model option %q current %q", ids, mo.Current)
	}
	if mo.Values[0].Name != "Swift 1" {
		t.Fatalf("name %q", mo.Values[0].Name)
	}
	var levels []string
	for _, o := range c.ByModel["deep-1"] {
		if o.ID != VariantEffortID || o.Kind != KindEffort {
			t.Fatalf("option %+v", o)
		}
		for _, v := range o.Values {
			levels = append(levels, v.Value)
		}
	}
	if strings.Join(levels, " ") != "low medium high xhigh max" {
		t.Fatalf("deep-1 efforts %q", levels)
	}
	if opts, ok := c.ByModel["tiny-1"]; !ok || len(opts) != 0 {
		t.Fatalf("tiny-1 %+v", c.ByModel["tiny-1"])
	}
	// The choice is one model id on the harness.
	e.setSessionChoice(Choice{Model: "deep-1", Options: map[string]string{VariantEffortID: "xhigh"}})
	if r := e.ok("model"); r.text != "model=deep-1 effort=xhigh fast=off" || len(r.notices) != 0 {
		t.Fatalf("text %q, notices %q", r.text, r.notices)
	}
	e.setSessionChoice(Choice{Model: "deep-1", Options: map[string]string{VariantEffortID: "nope"}})
	r := e.ok("model")
	if !strings.HasPrefix(r.text, "model=deep-1 ") || len(r.notices) != 1 || !strings.Contains(r.notices[0], `"nope" is not available`) {
		t.Fatalf("text %q, notices %q", r.text, r.notices)
	}
	// Nothing to probe: the catalog knew the efforts from the start.
	if err := e.m.ProbeModels(context.Background(), agentID); err != nil {
		t.Fatal(err)
	}
}

func TestSplitVariantsNeedsTwoLevels(t *testing.T) {
	mo := ConfigOption{ID: "model", Kind: KindModel, Type: "select", Current: "a/high", Values: []ConfigValue{
		{Value: "a/high", Name: "A"}, {Value: "b/low", Name: "B"}, {Value: "b/medium", Name: "B"},
	}}
	s, ok := splitVariants(mo)
	if !ok {
		t.Fatal("not split")
	}
	if _, isBase := s.efforts["a"]; isBase {
		t.Fatal("a model with one effort-like suffix is taken apart")
	}
	v := s.view()
	if len(v.Values) != 2 || v.Values[0].Value != "a/high" || v.Values[1].Value != "b" || v.Current != "a/high" {
		t.Fatalf("view %+v", v)
	}
	if got, ok := s.compose("b", "medium", "a/high"); got != "b/medium" || !ok {
		t.Fatalf("compose %q %v", got, ok)
	}
	if got, ok := s.compose("b", "", "b/medium"); got != "b/medium" || !ok {
		t.Fatalf("compose keeps the current variant: %q %v", got, ok)
	}
	if got, ok := s.compose("b", "max", "a/high"); got != "b/low" || ok {
		t.Fatalf("compose %q %v", got, ok)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
