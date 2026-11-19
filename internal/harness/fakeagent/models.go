package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// Models of the fake harness, advertised as ACP session config options the
// way the real adapters do it: a "model" select (grouped), an "effort"
// select (category thought_level) with the levels of the current model, and
// a "fast" on/off select while the current model supports it. Script flags:
//
//	no-config      advertise no config options (like a harness without them)
//	session/set_config_option <action> [arg]   fail one set request (see main.go)
//
// A "mode" select (category mode) stands for a harness's permission mode:
// "bypass" would skip permission requests. The "mode" prompt reports it.
//
// "broken-1" is listed but refuses to be selected. FAKEAGENT_MODEL sets the
// model new sessions start with, like ANTHROPIC_MODEL for Claude Code.
type fakeModel struct {
	id, name, group string
	efforts         []string
	fast            bool
}

var fakeModels = []fakeModel{
	{id: "swift-1", name: "Swift 1", group: "Fake Labs", efforts: []string{"low", "medium", "high"}, fast: true},
	{id: "deep-1", name: "Deep 1", group: "Fake Labs", efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{id: "tiny-1", name: "Tiny 1", group: "Other"},
	{id: "broken-1", name: "Broken 1", group: "Other"},
}

func findModel(id string) (fakeModel, bool) {
	for _, m := range fakeModels {
		if m.id == id {
			return m, true
		}
	}
	return fakeModel{}, false
}

// sessionConfig is a session's current choice.
type sessionConfig struct {
	model, effort, mode string
	fast                bool
}

func defaultConfig() sessionConfig {
	c := sessionConfig{model: "swift-1", effort: "medium", mode: "default"}
	if m, ok := findModel(os.Getenv("FAKEAGENT_MODEL")); ok {
		c.model = m.id
		c.effort = ""
		if len(m.efforts) > 0 {
			c.effort = m.efforts[0]
		}
	}
	return c
}

func (a *agent) config(sid string) sessionConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.configs[sid]
	if !ok {
		c = defaultConfig()
		a.configs[sid] = c
	}
	return c
}

// configOptions is what the session advertises (nil with "no-config").
func (a *agent) configOptions(sid string) []acp.SessionConfigOption {
	if a.flag("no-config") {
		return nil
	}
	c := a.config(sid)
	groups := acp.SessionConfigSelectOptionsGrouped{}
	for _, m := range fakeModels {
		if len(groups) == 0 || string(groups[len(groups)-1].Group) != m.group {
			groups = append(groups, acp.SessionConfigSelectGroup{Group: acp.SessionConfigGroupId(strings.ToLower(strings.ReplaceAll(m.group, " ", "-"))), Name: m.group})
		}
		g := &groups[len(groups)-1]
		g.Options = append(g.Options, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(m.id), Name: m.name})
	}
	model := acp.SessionConfigOptionCategoryModel
	opts := []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "model", Name: "Model", Category: &model, Type: "select",
		CurrentValue: acp.SessionConfigValueId(c.model), Options: acp.SessionConfigSelectOptions{Grouped: &groups},
	}}}
	m, _ := findModel(c.model)
	if len(m.efforts) > 0 {
		levels := acp.SessionConfigSelectOptionsUngrouped{}
		for _, e := range m.efforts {
			levels = append(levels, acp.SessionConfigSelectOption{Value: acp.SessionConfigValueId(e), Name: strings.ToUpper(e[:1]) + e[1:]})
		}
		thought := acp.SessionConfigOptionCategoryThoughtLevel
		opts = append(opts, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: "effort", Name: "Effort", Category: &thought, Type: "select",
			CurrentValue: acp.SessionConfigValueId(c.effort), Options: acp.SessionConfigSelectOptions{Ungrouped: &levels},
		}})
	}
	if m.fast {
		cur := "off"
		if c.fast {
			cur = "on"
		}
		opts = append(opts, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
			Id: "fast", Name: "Fast mode", Type: "select", CurrentValue: acp.SessionConfigValueId(cur),
			Options: acp.SessionConfigSelectOptions{Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
				{Value: "off", Name: "Off"}, {Value: "on", Name: "On"},
			}},
		}})
	}
	mode := acp.SessionConfigOptionCategoryMode
	opts = append(opts, acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id: "mode", Name: "Mode", Category: &mode, Type: "select", CurrentValue: acp.SessionConfigValueId(c.mode),
		Options: acp.SessionConfigSelectOptions{Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
			{Value: "default", Name: "Default"}, {Value: "bypass", Name: "Bypass permissions"},
		}},
	}})
	return opts
}

// SetSessionConfigOption implements acp.Agent.
func (a *agent) SetSessionConfigOption(ctx context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if a.flag("no-config") {
		return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
	}
	if err := a.failAt("session/set_config_option"); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if p.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams("only select options")
	}
	sid, id, value := string(p.ValueId.SessionId), string(p.ValueId.ConfigId), string(p.ValueId.Value)
	c := a.config(sid)
	m, _ := findModel(c.model)
	switch id {
	case "model":
		nm, ok := findModel(value)
		if !ok {
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(fmt.Sprintf("unknown model %q", value))
		}
		if nm.id == "broken-1" {
			return acp.SetSessionConfigOptionResponse{}, acp.NewInternalError(map[string]any{"error": "Broken 1 is not available on this plan"})
		}
		c.model = nm.id
		if !contains(nm.efforts, c.effort) {
			c.effort = ""
			if len(nm.efforts) > 0 {
				c.effort = "medium"
				if !contains(nm.efforts, "medium") {
					c.effort = nm.efforts[0]
				}
			}
		}
		if !nm.fast {
			c.fast = false
		}
	case "effort":
		if !contains(m.efforts, value) {
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(fmt.Sprintf("invalid effort %q", value))
		}
		c.effort = value
	case "mode":
		if value != "default" && value != "bypass" {
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(fmt.Sprintf("invalid mode %q", value))
		}
		c.mode = value
	case "fast":
		if !m.fast || (value != "on" && value != "off") {
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(fmt.Sprintf("invalid fast mode %q", value))
		}
		c.fast = value == "on"
	default:
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(fmt.Sprintf("unknown config option %q", id))
	}
	a.mu.Lock()
	a.configs[sid] = c
	a.mu.Unlock()
	opts := a.configOptions(sid)
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: acp.SessionId(sid), Update: acp.SessionUpdate{
		ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{SessionUpdate: "config_option_update", ConfigOptions: opts},
	}})
	return acp.SetSessionConfigOptionResponse{ConfigOptions: opts}, nil
}

// describeConfig answers the "model" prompt.
func (a *agent) describeConfig(sid string) string {
	c := a.config(sid)
	fast := "off"
	if c.fast {
		fast = "on"
	}
	return fmt.Sprintf("model=%s effort=%s fast=%s", c.model, c.effort, fast)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
