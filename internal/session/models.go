package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
// Bounds of what is kept of a harness's report: what is beyond them is
// dropped (ids and values that are too long, since shortening them would
// change them; names and descriptions are shortened).
const (
	maxOptions      = 20
	maxValues       = 500
	maxString       = 200
	maxCatalogModel = 500
)

func clip(s string) string {
	if len(s) <= maxString {
		return s
	}
	return strings.ToValidUTF8(s[:maxString], "")
}

func fromACP(in []acp.SessionConfigOption) []ConfigOption {
	out := []ConfigOption{}
	for _, o := range in {
		if len(out) == maxOptions {
			break
		}
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
		if len(c.ID) > maxString || len(c.Current) > maxString {
			continue
		}
		c.Name, c.Description, c.Category = clip(c.Name), clip(c.Description), clip(c.Category)
		vals := c.Values[:0]
		for _, v := range c.Values {
			if len(vals) == maxValues {
				break
			}
			if len(v.Value) > maxString {
				continue
			}
			v.Name, v.Description, v.Group = clip(v.Name), clip(v.Description), clip(v.Group)
			vals = append(vals, v)
		}
		c.Values = vals
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

// Picker reports whether clients may set the option through a model choice:
// effort and toggles only. Mode, agent and other options are not model
// settings (a session mode can switch the harness's permission handling) and
// are never set from a choice.
func (o ConfigOption) Picker() bool { return o.Kind == KindEffort || o.Kind == KindToggle }

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

// OwnChoice is the session's own choice (empty: the agent's default).
func OwnChoice(s db.AgentSession) Choice {
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
	// Defaults are the values a fresh session started with (session/new):
	// the harness's own default model and options, which clearing a choice
	// returns to.
	Defaults map[string]string `json:"defaults,omitempty"`
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

// catalogView is what the catalog stores of a report: the options with the
// model option reduced to base models when the harness encodes effort levels
// in model ids, and the effort option of each base model.
func catalogView(opts []ConfigOption) ([]ConfigOption, map[string][]ConfigOption) {
	mo, ok := modelOption(opts)
	if !ok {
		return opts, nil
	}
	s, ok := splitVariants(mo)
	if !ok {
		return opts, nil
	}
	view := slices.Clone(opts)
	extras := map[string][]ConfigOption{}
	for i, o := range view {
		if o.ID == mo.ID && o.Kind == KindModel {
			view[i] = s.view()
		}
	}
	// Every model is described: those without effort levels have no options.
	for _, v := range view[slices.IndexFunc(view, func(o ConfigOption) bool { return o.ID == mo.ID && o.Kind == KindModel })].Values {
		extras[v.Value] = []ConfigOption{}
		if s.efforts[v.Value] != nil {
			extras[v.Value] = []ConfigOption{s.effortOption(v.Value)}
		}
	}
	return view, extras
}

// observe records the config options a harness reported for a session.
// fresh: the report is a session/new answer, whose values are the harness's
// defaults. A report without options is recorded too: the harness offers no
// model choice, so a new default needs a restart.
func (m *Manager) observe(conn *harnessConn, acpID string, in []acp.SessionConfigOption, fresh bool) {
	opts := fromACP(in)
	conn.mu.Lock()
	conn.config[acpID] = opts
	if fresh && conn.defaults == nil {
		conn.defaults = map[string]string{}
		for _, o := range opts {
			conn.defaults[o.ID] = o.Current
		}
	}
	conn.mu.Unlock()
	// The catalog shows effort levels encoded in model ids as a separate
	// option (see variants.go); the session keeps the harness's own ids.
	view, extras := catalogView(opts)
	m.catMu.Lock()
	defer m.catMu.Unlock()
	ctx := context.WithoutCancel(m.ctx)
	row, err := m.store.GetModelCatalog(ctx, conn.agentID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.lg.Warn("read model catalog", "agent", conn.agentID, "err", err)
		return
	}
	var c Catalog
	if err == nil && row.Harness == conn.adapter.ID {
		_ = json.Unmarshal([]byte(row.Data), &c)
	}
	if c.ByModel == nil {
		c.ByModel = map[string][]ConfigOption{}
	}
	c.Options = view
	if fresh {
		c.Defaults = map[string]string{}
		for _, o := range opts {
			c.Defaults[o.ID] = o.Current
		}
	}
	if mo, ok := modelOption(view); ok && mo.Current != "" {
		picker := []ConfigOption{}
		for _, o := range opts {
			if o.Picker() {
				picker = append(picker, o)
			}
		}
		for base, ex := range extras {
			var keep []ConfigOption
			if base == mo.Current {
				keep = picker
			} else {
				for _, o := range c.ByModel[base] {
					if o.ID != VariantEffortID {
						keep = append(keep, o)
					}
				}
			}
			if _, known := c.ByModel[base]; known || len(c.ByModel) < maxCatalogModel {
				c.ByModel[base] = append(slices.Clone(keep), ex...)
			}
		}
		if _, split := extras[mo.Current]; !split {
			if _, known := c.ByModel[mo.Current]; known || len(c.ByModel) < maxCatalogModel {
				c.ByModel[mo.Current] = picker
			}
		}
		// Forget models the harness no longer lists.
		for id := range c.ByModel {
			if !mo.Has(id) && id != mo.Current {
				delete(c.ByModel, id)
			}
		}
	}
	b, _ := json.Marshal(c)
	now := time.Now()
	// Unchanged: written again only to refresh "seen at" now and then.
	if err == nil && row.Harness == conn.adapter.ID && row.Data == string(b) && now.Sub(time.Unix(row.SeenAt, 0)) < catalogRefresh {
		return
	}
	if err := m.store.UpsertModelCatalog(ctx, db.UpsertModelCatalogParams{
		AgentID: conn.agentID, Harness: conn.adapter.ID, Data: string(b), SeenAt: now.Unix(),
	}); err != nil {
		m.lg.Warn("store model catalog", "agent", conn.agentID, "err", err)
	}
}

// catalogRefresh is how stale "seen at" of an unchanged catalog may get.
const catalogRefresh = time.Hour

// LiveSwitchable reports whether the agent's running harness switches models
// within its sessions as they are (it reported a model option and did not
// start with a model from the environment). Otherwise a new default model
// needs the harness to restart.
func (m *Manager) LiveSwitchable(agentID string) bool {
	m.mu.Lock()
	c := m.conns[agentID]
	m.mu.Unlock()
	if c == nil {
		return true // nothing running: the next start reads the new default
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.startModel != "" {
		return false // its sessions start on the old default
	}
	for _, opts := range c.config {
		if _, ok := modelOption(opts); ok {
			return true
		}
	}
	return false
}

// SetSessionChoice stores a session's model choice; the next turn applies it.
func (m *Manager) SetSessionChoice(ctx context.Context, sessionID string, c Choice) error {
	return m.store.SetSessionModel(ctx, db.SetSessionModelParams{Model: c.Model, ModelOptions: EncodeOptions(c.Options), ID: sessionID})
}

// setOptionTimeout bounds one session/set_config_option request. A harness that
// does not answer it in time keeps serving the turn on its current settings.
var setOptionTimeout = 20 * time.Second

// applyChoice brings the harness session in line with the session's model
// choice (or the agent's default) before a prompt. Only the model and
// options of kind effort and toggle are ever set. What is not chosen returns
// to the harness's defaults (what a fresh session started with), so clearing
// a choice reverts it.
//
// A value the harness refuses, or a request it does not answer within
// setOptionTimeout, leaves the session as it is with a notice, and is not tried
// again on this connection: the turn runs on what the harness reports. Only
// a harness that ended fails the turn.
func (m *Manager) applyChoice(ctx context.Context, conn *harnessConn, sessionID, acpID string, notice func(string)) error {
	sess, err := m.store.GetAgentSession(ctx, sessionID)
	if err != nil {
		return err
	}
	agent, err := m.store.GetAgent(ctx, sess.AgentID)
	if err != nil {
		return err
	}
	want, own := OwnChoice(sess), true
	if want.Empty() {
		want, own = AgentChoice(agent), false
	}
	current := conn.configFor(acpID)
	mo, hasModel := modelOption(current)
	if !hasModel {
		if own && want.Model != "" && conn.warnOnce(acpID, "nomodels") {
			notice(fmt.Sprintf("%s does not let clients choose a model during a conversation, so this conversation uses the agent's default model.", conn.name))
		}
		return nil
	}
	defaults := conn.defaultValues()
	if defaults == nil {
		if c, ok, _ := m.Catalog(ctx, conn.agentID); ok && c.Harness == conn.adapter.ID {
			defaults = c.Defaults
		}
	}
	targetModel := want.Model
	if targetModel == "" {
		targetModel = defaults[mo.ID] // '' while unknown: keep the current model
	} else if sp, ok := splitVariants(mo); ok {
		// The harness encodes effort levels in model ids: the chosen model
		// and effort are one value.
		v, valid := sp.compose(targetModel, want.Options[VariantEffortID], mo.Current)
		if !valid && conn.warnOnce(acpID, "opt:"+VariantEffortID) {
			notice(fmt.Sprintf("Reasoning effort: %q is not available for %s, so this turn uses %q.", want.Options[VariantEffortID], valueName(sp.view(), targetModel), valueName(mo, v)))
		}
		targetModel = v
	}
	// fatal: the connection or the turn is over.
	fatal := func(err error) bool {
		var se *stoppedError
		return errors.As(err, &se) || conn.ended() || ctx.Err() != nil
	}
	set := func(o ConfigOption, value string) error {
		key := o.ID + "=" + value
		if conn.gaveUpOn(key) {
			return nil
		}
		err := m.setOption(ctx, conn, acpID, o, value)
		if err == nil {
			return nil
		}
		if fatal(err) {
			if conn.ended() {
				return conn.failure()
			}
			return err
		}
		conn.giveUpOn(key)
		what := fmt.Sprintf("the model %q", value)
		if o.Kind != KindModel {
			what = fmt.Sprintf("%s to %q", o.Name, value)
		}
		now := valueName(o, o.Current)
		var de *deadlineError
		if errors.As(err, &de) {
			notice(fmt.Sprintf("%s did not answer switching %s within %s, so this turn uses %s.", conn.name, what, setOptionTimeout, now))
		} else {
			notice(fmt.Sprintf("%s refused to switch %s: %s. This turn uses %s.", conn.name, what, refusal(err), now))
		}
		return nil
	}
	if targetModel != "" && targetModel != mo.Current {
		if err := set(mo, targetModel); err != nil {
			return err
		}
	}
	current = conn.configFor(acpID)
	mo, _ = modelOption(current)
	onDefaultModel := mo.Current == defaults[mo.ID]
	for _, o := range current {
		if !o.Picker() {
			continue // never set from a choice (see Picker)
		}
		v, chosen := want.Options[o.ID]
		switch {
		case chosen && !o.Has(v):
			if conn.warnOnce(acpID, "opt:"+o.ID+"="+v) {
				notice(fmt.Sprintf("%s: %q is not available for %s, so this turn uses %q.", o.Name, v, valueName(mo, mo.Current), valueName(o, o.Current)))
			}
			continue
		case chosen:
		case onDefaultModel && o.Has(defaults[o.ID]):
			v = defaults[o.ID]
		case o.Kind == KindToggle && len(o.Values) > 0:
			v = o.Values[0].Value // off
			if o.Type == "boolean" {
				v = "false"
			}
		default:
			continue // another model's effort: as the harness chose it
		}
		if v == o.Current {
			continue
		}
		if err := set(o, v); err != nil {
			return err
		}
	}
	return nil
}

// deadlineError: the harness did not answer within setOptionTimeout.
type deadlineError struct{}

func (deadlineError) Error() string { return "no answer" }

// setOption sends one session/set_config_option and records the result.
func (m *Manager) setOption(ctx context.Context, conn *harnessConn, acpID string, o ConfigOption, value string) error {
	req := acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: acp.SessionId(acpID), ConfigId: acp.SessionConfigId(o.ID), Value: acp.SessionConfigValueId(value),
	}}
	if o.Type == "boolean" {
		req = acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{
			SessionId: acp.SessionId(acpID), ConfigId: acp.SessionConfigId(o.ID), Value: value == "true", Type: "boolean",
		}}
	}
	sctx, cancel := context.WithTimeout(ctx, setOptionTimeout)
	defer cancel()
	res, err := conn.acp.SetSessionConfigOption(sctx, req)
	if err != nil {
		if ctx.Err() == nil && errors.Is(sctx.Err(), context.DeadlineExceeded) {
			return &deadlineError{}
		}
		return err
	}
	if res.ConfigOptions != nil {
		m.observe(conn, acpID, res.ConfigOptions, false)
	} else {
		// No options in the answer: assume the value took (a harness that
		// changes more sends config_option_update).
		conn.setCurrent(acpID, o.ID, value)
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
