package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/store/db"
)

// Models and their options reach the client as ACP session config options
// (session/new, session/load and session/resume answers, config_option_update
// notifications, session/set_config_option answers). All three harnesses
// advertise a "model" option (category "model"), a reasoning effort option
// (category "thought_level") and, for some models, a fast mode toggle; the
// options of a model are only known while it is the current one.

// Option kinds the model picker shows. Other config options (session mode,
// collaboration mode, ...) are not model settings and are left alone.
const (
	KindModel  = "model"
	KindEffort = "effort"
	KindToggle = "toggle"
)

// ConfigOption is a harness's session config option, as stored and served.
type ConfigOption struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Category    string        `json:"category,omitempty"`
	Kind        string        `json:"kind,omitempty"`
	Type        string        `json:"type"` // "select" or "boolean"
	Current     string        `json:"current"`
	Values      []ConfigValue `json:"values,omitempty"`
}

// ConfigValue is one value of a select option.
type ConfigValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Group       string `json:"group,omitempty"`
}

// Has reports whether v is a value of the option.
func (o ConfigOption) Has(v string) bool {
	if o.Type == "boolean" {
		return v == "true" || v == "false"
	}
	return slices.ContainsFunc(o.Values, func(x ConfigValue) bool { return x.Value == v })
}

// kindOf classifies an option for the picker.
func kindOf(o ConfigOption) string {
	switch {
	case o.Category == "model" || (o.Category == "" && o.ID == "model"):
		return KindModel
	case o.Category == "thought_level":
		return KindEffort
	}
	// Fast mode: Claude Code "fast", Codex "fast-mode" (category model_config).
	if !strings.Contains(strings.ToLower(o.ID), "fast") && o.Category != "model_config" {
		return ""
	}
	if o.Type == "boolean" {
		return KindToggle
	}
	if len(o.Values) == 2 && o.Has("on") && o.Has("off") {
		return KindToggle
	}
	return ""
}

// fromACP converts what a harness advertises. Options of unknown variants
// are dropped.
func fromACP(in []acp.SessionConfigOption) []ConfigOption {
	out := []ConfigOption{}
	for _, o := range in {
		var c ConfigOption
		switch {
		case o.Select != nil:
			s := o.Select
			c = ConfigOption{ID: string(s.Id), Name: s.Name, Type: "select", Current: string(s.CurrentValue)}
			c.Description = deref(s.Description)
			if s.Category != nil {
				c.Category = string(*s.Category)
			}
			if s.Options.Ungrouped != nil {
				for _, v := range *s.Options.Ungrouped {
					c.Values = append(c.Values, ConfigValue{Value: string(v.Value), Name: v.Name, Description: deref(v.Description)})
				}
			}
			if s.Options.Grouped != nil {
				for _, g := range *s.Options.Grouped {
					for _, v := range g.Options {
						c.Values = append(c.Values, ConfigValue{Value: string(v.Value), Name: v.Name, Description: deref(v.Description), Group: g.Name})
					}
				}
			}
		case o.Boolean != nil:
			b := o.Boolean
			c = ConfigOption{ID: string(b.Id), Name: b.Name, Type: "boolean", Current: fmt.Sprint(b.CurrentValue)}
			c.Description = deref(b.Description)
			if b.Category != nil {
				c.Category = string(*b.Category)
			}
		default:
			continue
		}
		c.Kind = kindOf(c)
		out = append(out, c)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func findOption(opts []ConfigOption, pred func(ConfigOption) bool) (ConfigOption, bool) {
	for _, o := range opts {
		if pred(o) {
			return o, true
		}
	}
	return ConfigOption{}, false
}

func modelOption(opts []ConfigOption) (ConfigOption, bool) {
	return findOption(opts, func(o ConfigOption) bool { return o.Kind == KindModel })
}

// Choice is a model and its options (config option id -> value). An empty
// Model keeps the harness's current model.
type Choice struct {
	Model   string            `json:"model"`
	Options map[string]string `json:"options"`
}

// Empty reports whether the choice selects nothing.
func (c Choice) Empty() bool { return c.Model == "" && len(c.Options) == 0 }

// ParseOptions reads a model_options column.
func ParseOptions(s string) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// EncodeOptions writes a model_options column.
func EncodeOptions(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// AgentChoice is the agent's default choice.
func AgentChoice(a db.Agent) Choice {
	return Choice{Model: a.Model, Options: ParseOptions(a.ModelOptions)}
}

// SessionChoice is the session's own choice (empty: the agent's default).
func SessionChoice(s db.AgentSession) Choice {
	return Choice{Model: s.Model, Options: ParseOptions(s.ModelOptions)}
}

// Catalog is what an agent's harness last reported.
type Catalog struct {
	Harness string    `json:"harness"`
	SeenAt  time.Time `json:"-"`
	// Options is the full set of the last report.
	Options []ConfigOption `json:"options"`
	// ByModel holds the picker options (effort, toggles) last seen while each
	// model was the current one: a harness describes only those of the
	// current model.
	ByModel map[string][]ConfigOption `json:"byModel"`
}

// ModelOption is the catalog's model option, if the harness has one.
func (c Catalog) ModelOption() (ConfigOption, bool) { return modelOption(c.Options) }

// Catalog returns what the agent's harness last reported (ok false: never).
func (m *Manager) Catalog(ctx context.Context, agentID string) (Catalog, bool, error) {
	row, err := m.store.GetModelCatalog(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return Catalog{}, false, nil
	}
	if err != nil {
		return Catalog{}, false, err
	}
	var c Catalog
	if err := json.Unmarshal([]byte(row.Data), &c); err != nil {
		return Catalog{}, false, nil // unreadable: as if never reported
	}
	c.Harness, c.SeenAt = row.Harness, time.Unix(row.SeenAt, 0)
	return c, true, nil
}

// observe records the config options a harness reported for a session.
func (m *Manager) observe(conn *harnessConn, acpID string, in []acp.SessionConfigOption) {
	opts := fromACP(in)
	conn.mu.Lock()
	conn.config[acpID] = opts
	conn.mu.Unlock()
	if len(opts) == 0 {
		return
	}
	m.catMu.Lock()
	defer m.catMu.Unlock()
	ctx := context.WithoutCancel(m.ctx)
	c, ok, err := m.Catalog(ctx, conn.agentID)
	if err != nil {
		m.lg.Warn("read model catalog", "agent", conn.agentID, "err", err)
		return
	}
	if !ok || c.Harness != conn.adapter.ID {
		c = Catalog{}
	}
	if c.ByModel == nil {
		c.ByModel = map[string][]ConfigOption{}
	}
	c.Options = opts
	if mo, ok := modelOption(opts); ok && mo.Current != "" {
		picker := []ConfigOption{}
		for _, o := range opts {
			if o.Kind == KindEffort || o.Kind == KindToggle {
				picker = append(picker, o)
			}
		}
		c.ByModel[mo.Current] = picker
		// Forget models the harness no longer lists.
		for id := range c.ByModel {
			if !mo.Has(id) && id != mo.Current {
				delete(c.ByModel, id)
			}
		}
	}
	b, _ := json.Marshal(c)
	if err := m.store.UpsertModelCatalog(ctx, db.UpsertModelCatalogParams{
		AgentID: conn.agentID, Harness: conn.adapter.ID, Data: string(b), SeenAt: time.Now().Unix(),
	}); err != nil {
		m.lg.Warn("store model catalog", "agent", conn.agentID, "err", err)
	}
}

// SetSessionChoice stores a session's model choice; the next turn applies it.
func (m *Manager) SetSessionChoice(ctx context.Context, sessionID string, c Choice) error {
	return m.store.SetSessionModel(ctx, db.SetSessionModelParams{Model: c.Model, ModelOptions: EncodeOptions(c.Options), ID: sessionID})
}

// applyChoice brings the harness session in line with the session's model
// choice (or the agent's default) before a prompt. A choice the harness does
// not offer or refuses leaves the session as it is and says so in a notice:
// the turn runs on, on the model the harness reports. Only a harness that
// stops answering ends the turn (as for any setup request).
func (m *Manager) applyChoice(ctx context.Context, t *turn, conn *harnessConn, sessionID, acpID string, notice func(string)) error {
	sess, err := m.store.GetAgentSession(ctx, sessionID)
	if err != nil {
		return err
	}
	agent, err := m.store.GetAgent(ctx, sess.AgentID)
	if err != nil {
		return err
	}
	want, own := SessionChoice(sess), true
	if want.Empty() {
		want, own = AgentChoice(agent), false
	}
	if want.Empty() {
		return nil
	}
	current := conn.configFor(acpID)
	warn := func(key, msg string) {
		if conn.warnOnce(acpID, key) {
			notice(msg)
		}
	}
	mo, hasModel := modelOption(current)
	if !hasModel {
		if own && want.Model != "" {
			warn("nomodels", fmt.Sprintf("%s does not let clients choose a model during a conversation, so this conversation uses the agent's default model.", conn.name))
		}
		return nil
	}
	set := func(o ConfigOption, value string) error {
		req := acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: acp.SessionId(acpID), ConfigId: acp.SessionConfigId(o.ID), Value: acp.SessionConfigValueId(value),
		}}
		if o.Type == "boolean" {
			req = acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{
				SessionId: acp.SessionId(acpID), ConfigId: acp.SessionConfigId(o.ID), Value: value == "true", Type: "boolean",
			}}
		}
		var res acp.SetSessionConfigOptionResponse
		err := m.step(ctx, conn, "session/set_config_option", func(ctx context.Context) error {
			var err error
			res, err = conn.acp.SetSessionConfigOption(ctx, req)
			return err
		})
		var st *stepTimeout
		if errors.As(err, &st) {
			return m.giveUp(t, conn, st.msg)
		}
		if err != nil {
			return err
		}
		if res.ConfigOptions != nil {
			m.observe(conn, acpID, res.ConfigOptions)
		}
		return nil
	}
	// fatal: the connection or the turn is over; anything else was refused.
	fatal := func(err error) bool {
		var se *stoppedError
		return errors.As(err, &se) || conn.closed() || ctx.Err() != nil
	}
	if want.Model != "" && want.Model != mo.Current {
		switch {
		case !mo.Has(want.Model) && !conn.adapter.CustomModels:
			warn("model:"+want.Model, fmt.Sprintf("%s does not offer the model %q (for this login), so this turn uses %s.", conn.name, want.Model, valueName(mo, mo.Current)))
		default:
			if err := set(mo, want.Model); err != nil {
				if fatal(err) {
					return err
				}
				warn("model:"+want.Model, fmt.Sprintf("%s refused to switch to the model %q: %s. This turn uses %s.", conn.name, want.Model, refusal(err), valueName(mo, mo.Current)))
			}
		}
	}
	ids := make([]string, 0, len(want.Options))
	for id := range want.Options {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v := want.Options[id]
		o, ok := findOption(conn.configFor(acpID), func(o ConfigOption) bool { return o.ID == id })
		if !ok || o.Current == v || o.Kind == KindModel {
			continue // not an option of the current model (fast mode of another model), or already set
		}
		if !o.Has(v) {
			warn("opt:"+id+"="+v, fmt.Sprintf("%s: %q is not available for the current model, so this turn uses %q.", o.Name, v, valueName(o, o.Current)))
			continue
		}
		if err := set(o, v); err != nil {
			if fatal(err) {
				return err
			}
			warn("opt:"+id+"="+v, fmt.Sprintf("%s refused to set %s to %q: %s.", conn.name, o.Name, v, refusal(err)))
		}
	}
	return nil
}

// valueName is the display name of an option value.
func valueName(o ConfigOption, v string) string {
	for _, x := range o.Values {
		if x.Value == v && x.Name != "" {
			return x.Name
		}
	}
	return v
}

// refusal is the readable part of a harness's error.
func refusal(err error) string {
	var re *acp.RequestError
	if errors.As(err, &re) {
		msg := re.Message
		if d, ok := re.Data.(map[string]any); ok {
			if s, ok := d["error"].(string); ok && s != "" {
				msg = s
			}
		} else if s, ok := re.Data.(string); ok && s != "" {
			msg = s
		}
		return msg
	}
	return err.Error()
}
