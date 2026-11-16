package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/logins"
	"github.com/gempir/agenthold/internal/store/db"
)

// Account logins. Every endpoint acts on the signed-in user's own logins;
// there is no way to name another user's.

// loginAdapter returns a harness with account logins that this server offers.
func (a *API) loginAdapter(provider string) (*harness.Adapter, harness.AccountLoginer, bool) {
	ad, ok := harness.Get(provider)
	if !ok || ad.Login == nil || (ad.Hidden && !a.ShowTestHarness) {
		return nil, nil, false
	}
	return ad, ad.Login, true
}

func apiProviderLogin(l db.ProviderLogin) *gen.ProviderLogin {
	out := &gen.ProviderLogin{
		Kind: l.Kind, Label: l.Label, Status: gen.ProviderLoginStatus(l.Status), StatusMessage: l.StatusMessage,
		ConnectedAt: ts(l.UpdatedAt),
	}
	if l.ExpiresAt > 0 {
		t := ts(l.ExpiresAt)
		out.ExpiresAt = &t
	}
	if l.LastUsedAt > 0 {
		t := ts(l.LastUsedAt)
		out.LastUsedAt = &t
	}
	return out
}

func (a *API) providerLoginInfo(ctx context.Context, userID string, ad *harness.Adapter, al harness.AccountLoginer, rows []db.ProviderLogin, agents []db.Agent) (gen.ProviderLoginInfo, error) {
	info := gen.ProviderLoginInfo{Provider: ad.ID, Name: ad.Name, Kinds: []gen.ProviderLoginKind{}, Agents: []gen.LoginAgent{}}
	for _, k := range al.LoginKinds() {
		gk := gen.ProviderLoginKind{Id: k.ID, Name: k.Name, Description: k.Description, InputLabel: k.InputLabel, Multiline: k.Multiline}
		if k.Command != "" {
			gk.Command = &k.Command
		}
		info.Kinds = append(info.Kinds, gk)
	}
	for _, l := range rows {
		if l.Provider == ad.ID {
			info.Login = apiProviderLogin(l)
		}
	}
	for _, ag := range agents {
		if ag.Harness != ad.ID {
			continue
		}
		// Only agents the user can still see (others do not get the login).
		role, err := a.authz.RoleOf(ctx, userID, ag)
		if err != nil {
			return info, err
		}
		if authz.Can(role, authz.AgentRead) {
			info.Agents = append(info.Agents, gen.LoginAgent{Id: ag.ID, Name: ag.Name})
		}
	}
	return info, nil
}

// ListProviderLogins implements gen.StrictServerInterface.
func (a *API) ListProviderLogins(ctx context.Context, _ gen.ListProviderLoginsRequestObject) (gen.ListProviderLoginsResponseObject, error) {
	u := me(ctx)
	rows, err := a.Store.ListUserProviderLogins(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	agents, err := a.Store.ListAgentsByLoginUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListProviderLogins200JSONResponse{}
	for _, ad := range harness.List(a.ShowTestHarness) {
		if ad.Login == nil {
			continue
		}
		info, err := a.providerLoginInfo(ctx, u.ID, ad, ad.Login, rows, agents)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// ConnectProviderLogin implements gen.StrictServerInterface.
func (a *API) ConnectProviderLogin(ctx context.Context, req gen.ConnectProviderLoginRequestObject) (gen.ConnectProviderLoginResponseObject, error) {
	if auth.ViaToken(ctx) {
		return nil, errStatus(http.StatusForbidden, "provider logins can only be connected when signed in, not with a token")
	}
	ad, al, ok := a.loginAdapter(req.Provider)
	if !ok {
		return nil, errNotFound
	}
	u := me(ctx)
	if len(req.Body.Credential) > 64*1024 {
		return nil, badRequest("credential too long")
	}
	_, err := a.Logins.Connect(ctx, u.ID, req.Provider, req.Body.Kind, req.Body.Credential)
	var in *logins.InputError
	switch {
	case errors.Is(err, logins.ErrUnknownKind):
		return nil, badRequest("unknown login kind")
	case errors.As(err, &in):
		return nil, badRequest(in.Error())
	case err != nil:
		return nil, err
	}
	rows, err := a.Store.ListUserProviderLogins(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	agents, err := a.Store.ListAgentsByLoginUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	info, err := a.providerLoginInfo(ctx, u.ID, ad, al, rows, agents)
	if err != nil {
		return nil, err
	}
	return gen.ConnectProviderLogin200JSONResponse(info), nil
}

// DisconnectProviderLogin implements gen.StrictServerInterface.
func (a *API) DisconnectProviderLogin(ctx context.Context, req gen.DisconnectProviderLoginRequestObject) (gen.DisconnectProviderLoginResponseObject, error) {
	if _, _, ok := a.loginAdapter(req.Provider); !ok {
		return nil, errNotFound
	}
	if err := a.Logins.Disconnect(ctx, me(ctx).ID, req.Provider); err != nil {
		if errors.Is(err, logins.ErrNotConnected) {
			return nil, errNotFound
		}
		return nil, err
	}
	return gen.DisconnectProviderLogin204Response{}, nil
}

// UseMyLogins implements gen.StrictServerInterface.
func (a *API) UseMyLogins(ctx context.Context, req gen.UseMyLoginsRequestObject) (gen.UseMyLoginsResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Agent(ctx, u.ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	if !authz.CanBindLogin(u.ID, u.ID, acc.Role) {
		return nil, errStatus(http.StatusForbidden, "only the agent's admins can make it use their logins")
	}
	if acc.Row.Agent.LoginUserID != u.ID {
		if err := a.Logins.Bind(ctx, acc.Row.Agent, u.ID); err != nil {
			return nil, err
		}
	}
	g, err := a.reloadAgent(ctx, req.AgentId, acc.Role)
	if err != nil {
		return nil, err
	}
	return gen.UseMyLogins200JSONResponse(g), nil
}

// StopUsingLogins implements gen.StrictServerInterface.
func (a *API) StopUsingLogins(ctx context.Context, req gen.StopUsingLoginsRequestObject) (gen.StopUsingLoginsResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Agent(ctx, u.ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	ag := acc.Row.Agent
	if ag.LoginUserID != "" {
		if !authz.CanUnbindLogin(u.ID, ag.LoginUserID, acc.Role) {
			return nil, errStatus(http.StatusForbidden, "only the agent's admins or the login's owner can do this")
		}
		if err := a.Logins.Unbind(ctx, ag, u.ID); err != nil {
			return nil, err
		}
	}
	g, err := a.reloadAgent(ctx, req.AgentId, acc.Role)
	if err != nil {
		return nil, err
	}
	return gen.StopUsingLogins200JSONResponse(g), nil
}

// agentLogin describes which login an agent's harness uses.
func (a *API) agentLogin(ctx context.Context, ag db.Agent) (gen.AgentLogin, error) {
	out := gen.AgentLogin{Source: gen.AgentLoginSource(logins.SourceUnsupported)}
	if a.Logins == nil {
		return out, nil
	}
	st, err := a.Logins.StateOf(ctx, ag)
	if err != nil {
		return out, err
	}
	out.Source = gen.AgentLoginSource(st.Source)
	if st.User != "" {
		u, err := a.Store.GetUser(ctx, st.User)
		if err != nil {
			return out, err
		}
		au := apiUser(u)
		out.User = &au
		out.Mine = me(ctx) != nil && me(ctx).ID == st.User
	}
	if st.Reason != "" {
		out.Reason = &st.Reason
	}
	if st.Login != nil {
		out.Connected = true
		out.Kind = &st.Login.Kind
		s := gen.AgentLoginStatus(st.Login.Status)
		out.Status = &s
	}
	return out, nil
}
