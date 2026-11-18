package api

import (
	"context"
	"fmt"
	"net/http"
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
	if err := a.validateChoice(ctx, agent, choice, session.SessionChoice(s)); err != nil {
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

// validateChoice checks a model choice against the harness's catalog when
// the harness reported one. What is unchanged from prev is not checked again:
// an old setting the harness no longer lists stays until it is changed.
func (a *API) validateChoice(ctx context.Context, agent db.Agent, c, prev session.Choice) error {
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
	if !cat.reported {
		return nil // nothing to check against: the harness says so at the next turn
	}
	if c.Model != "" && c.Model != prev.Model {
		if _, ok := cat.model(c.Model); !ok && !cat.api.CustomModels {
			return badRequest(fmt.Sprintf("the harness does not offer the model %q", c.Model))
		}
	}
	known, ok := cat.options[c.Model]
	if c.Model == "" || !ok {
		return nil // the options of the model are not known yet
	}
	for k, v := range c.Options {
		if prev.Options[k] == v && prev.Model == c.Model {
			continue
		}
		var found *session.ConfigOption
		for i := range known {
			if known[i].ID == k {
				found = &known[i]
			}
		}
		if found == nil {
			return badRequest(fmt.Sprintf("the model %q has no option %q", c.Model, k))
		}
		if !found.Has(v) {
			return badRequest(fmt.Sprintf("%s cannot be %q for the model %q", found.Name, v, c.Model))
		}
	}
	return nil
}
