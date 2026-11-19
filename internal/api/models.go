package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// catalog is an agent's model catalog: what its harness last reported, or
// the adapter's fallback list.
type catalog struct {
	api      gen.ModelCatalog
	reported bool
	// options by model id (nil: unknown); only for reported catalogs.
	options map[string][]session.ConfigOption
}

func (c catalog) model(id string) (gen.CatalogModel, bool) {
	for _, m := range c.api.Models {
		if m.Id == id {
			return m, true
		}
	}
	return gen.CatalogModel{}, false
}

func (a *API) catalog(ctx context.Context, agent db.Agent) (catalog, error) {
	ad, ok := harness.Get(agent.Harness)
	if !ok {
		return catalog{}, fmt.Errorf("unknown harness %q", agent.Harness)
	}
	c := catalog{api: gen.ModelCatalog{Harness: agent.Harness, CustomModels: ad.CustomModels, Models: []gen.CatalogModel{}}}
	rep, ok, err := a.Sessions.Catalog(ctx, agent.ID)
	if err != nil {
		return catalog{}, err
	}
	mo, hasModel := rep.ModelOption()
	if ok && rep.Harness == agent.Harness && hasModel {
		c.reported, c.options = true, rep.ByModel
		c.api.Source, c.api.Switchable = gen.Harness, true
		seen := rep.SeenAt
		c.api.SeenAt = &seen
		for _, v := range mo.Values {
			m := gen.CatalogModel{Id: v.Value, Name: v.Name, Description: opt(v.Description), Group: opt(v.Group)}
			if opts, ok := rep.ByModel[v.Value]; ok {
				list := apiModelOptions(opts)
				m.Options = &list
			}
			c.api.Models = append(c.api.Models, m)
		}
		if prov := provisionalEfforts(rep.ByModel); len(prov) > 0 && slices.ContainsFunc(c.api.Models, func(m gen.CatalogModel) bool { return m.Options == nil }) {
			list := apiModelOptions(prov)
			c.api.ProvisionalOptions = &list
		}
		return c, nil
	}
	// Never reported or reported by another harness: the fallback list, and
	// switching is expected (every harness in the image supports it). A
	// harness that ran and offered no model option takes a new default only
	// when it restarts, and no per-conversation choice.
	c.api.Source = gen.Fallback
	silent := ok && rep.Harness == agent.Harness
	c.api.Switchable = !silent
	if silent {
		seen := rep.SeenAt
		c.api.SeenAt = &seen
	}
	for _, m := range ad.FallbackModels {
		c.api.Models = append(c.api.Models, gen.CatalogModel{Id: m.ID, Name: m.Name, Description: opt(m.Description), Group: opt(m.Group)})
	}
	return c, nil
}

// provisionalEfforts merges the effort options the harness reported for
// its models, by option id: what a model not described yet probably offers.
// Values are kept in the order of the longest list, then those only shorter
// lists have.
func provisionalEfforts(byModel map[string][]session.ConfigOption) []session.ConfigOption {
	var out []session.ConfigOption
	ids := make([]string, 0, len(byModel))
	for id := range byModel {
		ids = append(ids, id)
	}
	slices.Sort(ids) // deterministic
	for _, id := range ids {
		for _, o := range byModel[id] {
			if o.Kind != session.KindEffort {
				continue
			}
			i := slices.IndexFunc(out, func(x session.ConfigOption) bool { return x.ID == o.ID })
			if i < 0 {
				o.Values = slices.Clone(o.Values)
				out = append(out, o)
				continue
			}
			merged := out[i].Values
			if len(o.Values) > len(merged) {
				merged, o.Values = slices.Clone(o.Values), merged
			}
			for _, v := range o.Values {
				if !slices.ContainsFunc(merged, func(x session.ConfigValue) bool { return x.Value == v.Value }) {
					merged = append(merged, v)
				}
			}
			out[i].Values = merged
		}
	}
	return out
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func apiModelOptions(in []session.ConfigOption) []gen.ModelOption {
	out := []gen.ModelOption{}
	for _, o := range in {
		m := gen.ModelOption{Id: o.ID, Name: o.Name, Description: opt(o.Description), Values: []gen.ModelOptionValue{}}
		switch o.Kind {
		case session.KindEffort:
			m.Kind = gen.Effort
			for _, v := range o.Values {
				m.Values = append(m.Values, gen.ModelOptionValue{Value: v.Value, Name: v.Name, Description: opt(v.Description)})
			}
		case session.KindToggle:
			m.Kind = gen.Toggle
			off, on := "off", "on"
			if o.Type == "boolean" {
				off, on = "false", "true"
			}
			m.Values = []gen.ModelOptionValue{{Value: off, Name: "Off"}, {Value: on, Name: "On"}}
		default:
			continue
		}
		out = append(out, m)
	}
	return out
}

// ListModels implements gen.StrictServerInterface.
func (a *API) ListModels(ctx context.Context, req gen.ListModelsRequestObject) (gen.ListModelsResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	if acc.Row.Agent.Kind == "app" {
		return nil, errStatus(http.StatusConflict, session.ErrNoHarness.Error())
	}
	c, err := a.catalog(ctx, acc.Row.Agent)
	if err != nil {
		return nil, err
	}
	return gen.ListModels200JSONResponse(c.api), nil
}

// SetSessionModel implements gen.StrictServerInterface.
func (a *API) SetSessionModel(ctx context.Context, req gen.SetSessionModelRequestObject) (gen.SetSessionModelResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	agent, err := a.Store.GetAgent(ctx, s.AgentID)
	if err != nil {
		return nil, err
	}
	choice := choiceFrom(*req.Body)
	_, cerr := a.authz.Agent(ctx, me(ctx).ID, s.AgentID, authz.AgentConfigure)
	if err := a.validateChoice(ctx, agent, choice, session.OwnChoice(s), cerr == nil); err != nil {
		return nil, err
	}
	if err := a.Sessions.SetSessionChoice(ctx, s.ID, choice); err != nil {
		return nil, err
	}
	s, err = a.Store.GetAgentSession(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	out, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.SetSessionModel200JSONResponse(out), nil
}

func choiceFrom(b gen.ModelChoice) session.Choice {
	c := session.Choice{Model: strings.TrimSpace(b.Model), Options: map[string]string{}}
	if b.Options != nil {
		for k, v := range *b.Options {
			c.Options[k] = v
		}
	}
	return c
}

func apiChoice(c session.Choice) gen.ModelChoice {
	opts := map[string]string{}
	for k, v := range c.Options {
		opts[k] = v
	}
	return gen.ModelChoice{Model: c.Model, Options: &opts}
}

// validModelID is the shape check every model id passes (old free-text
// settings included).
func validModelID(id string) bool {
	return len(id) <= 200 && !strings.ContainsAny(id, " \t\r\n")
}

// validateChoice checks a model choice. Options must be effort or toggle
// options the harness reported (never mode or other options: a session mode
// can change the harness's permission handling). A model id is only checked
// for its shape when configure is set (the owner may use ids the harness
// does not list, such as aliases; the harness decides at the next turn);
// others choose among the models the harness reported. What is unchanged
// from prev is not checked again: an old setting stays until it is changed.
func (a *API) validateChoice(ctx context.Context, agent db.Agent, c, prev session.Choice, configure bool) error {
	if !validModelID(c.Model) {
		return badRequest("invalid model id")
	}
	if len(c.Options) > 20 {
		return badRequest("too many model options")
	}
	for k, v := range c.Options {
		if k == "" || len(k) > 100 || len(v) > 100 {
			return badRequest("invalid model option")
		}
	}
	cat, err := a.catalog(ctx, agent)
	if err != nil {
		return err
	}
	if c.Model != "" && c.Model != prev.Model && !configure {
		if _, listed := cat.model(c.Model); !cat.reported || !listed {
			return errStatus(http.StatusForbidden, "only the agent's owner can choose a model the harness has not reported")
		}
	}
	known, ok := cat.options[c.Model]
	exact := c.Model != "" && ok
	if !exact {
		// The model's options are not known yet: any effort or toggle option
		// the harness reported for one of its models, with any value (the
		// harness decides at the next turn).
		known = nil
		for _, opts := range cat.options {
			known = append(known, opts...)
		}
	}
	for k, v := range c.Options {
		if prev.Options[k] == v && prev.Model == c.Model {
			continue
		}
		valid, found := false, false
		for _, o := range known {
			if o.ID == k && o.Picker() {
				found = true
				valid = valid || !exact || o.Has(v)
			}
		}
		switch {
		case !found && !cat.reported:
			return badRequest("model options can be chosen once the harness has reported them (after the agent's first conversation)")
		case !found:
			return badRequest(fmt.Sprintf("%q is not a model option of this harness", k))
		case !valid:
			return badRequest(fmt.Sprintf("option %q cannot be %q for this model", k, v))
		}
	}
	return nil
}
