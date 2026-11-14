package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// ListHarnesses implements gen.StrictServerInterface.
func (a *API) ListHarnesses(context.Context, gen.ListHarnessesRequestObject) (gen.ListHarnessesResponseObject, error) {
	out := gen.ListHarnesses200JSONResponse{}
	for _, h := range harness.List(a.ShowTestHarness) {
		out = append(out, gen.HarnessType{Id: h.ID(), Name: h.Name(), SecretPresets: append([]string{}, h.SecretPresets()...), InstallHint: h.InstallHint()})
	}
	return out, nil
}

// GetHarness implements gen.StrictServerInterface.
func (a *API) GetHarness(ctx context.Context, req gen.GetHarnessRequestObject) (gen.GetHarnessResponseObject, error) {
	connect := req.Params.Connect != nil && *req.Params.Connect
	action := authz.AgentRead
	if connect {
		action = authz.AgentPrompt
	}
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, action)
	if err != nil {
		return nil, err
	}
	info, err := a.Sessions.Harness(ctx, req.AgentId, connect)
	if err != nil {
		return nil, errStatus(http.StatusConflict, err.Error())
	}
	out := gen.GetHarness200JSONResponse{
		Harness: acc.Row.Agent.Harness, Connected: info.Connected, AgentName: info.AgentName,
		AgentVersion: info.AgentVer, LoadSession: info.LoadSession, AuthMethods: []gen.AuthMethodInfo{},
	}
	for _, m := range info.AuthMethods {
		if am, ok := authMethodInfo(m); ok {
			out.AuthMethods = append(out.AuthMethods, am)
		}
	}
	return out, nil
}

func authMethodInfo(m acp.AuthMethod) (gen.AuthMethodInfo, bool) {
	switch {
	case m.Terminal != nil:
		d := ""
		if m.Terminal.Description != nil {
			d = *m.Terminal.Description
		}
		return gen.AuthMethodInfo{Id: m.Terminal.Id, Name: m.Terminal.Name, Description: d, Type: "terminal"}, true
	case m.Agent != nil:
		d := ""
		if m.Agent.Description != nil {
			d = *m.Agent.Description
		}
		return gen.AuthMethodInfo{Id: m.Agent.Id, Name: m.Agent.Name, Description: d, Type: "agent"}, true
	case m.EnvVar != nil:
		return gen.AuthMethodInfo{Id: m.EnvVar.Id, Name: m.EnvVar.Name, Description: "Set as an environment variable (attach a secret)", Type: "env_var"}, true
	}
	return gen.AuthMethodInfo{}, false
}

// loginCommand returns the command that runs a terminal login method.
func (a *API) loginCommand(ctx context.Context, agent db.Agent, methodID string) ([]string, map[string]string, error) {
	info, err := a.Sessions.Harness(ctx, agent.ID, true)
	if err != nil {
		return nil, nil, err
	}
	ad, ok := harness.Get(agent.Harness)
	if !ok {
		return nil, nil, errors.New("unknown harness")
	}
	for _, m := range info.AuthMethods {
		t := m.Terminal
		if t == nil || t.Id != methodID {
			continue
		}
		env := map[string]string{}
		for k, v := range ad.Env(harness.Config{Model: agent.Model}) {
			env[k] = v
		}
		for k, v := range t.Env {
			if s, ok := v.(string); ok {
				env[k] = s
			}
		}
		// Prefer the exact command the harness reports for terminal login.
		if ta, ok := t.Meta["terminal-auth"].(map[string]any); ok {
			if cmd, ok := ta["command"].(string); ok && cmd != "" {
				argv := []string{cmd}
				if args, ok := ta["args"].([]any); ok {
					for _, x := range args {
						if s, ok := x.(string); ok {
							argv = append(argv, s)
						}
					}
				}
				return argv, env, nil
			}
		}
		return append(ad.Command(harness.Config{Model: agent.Model}), t.Args...), env, nil
	}
	return nil, nil, errStatus(http.StatusNotFound, "unknown login method")
}

func (a *API) apiSession(ctx context.Context, s db.AgentSession) (gen.Session, error) {
	u, err := a.Store.GetUser(ctx, s.CreatedBy)
	if err != nil {
		return gen.Session{}, err
	}
	return gen.Session{
		Id: s.ID, AgentId: s.AgentID, Title: s.Title, Running: a.Sessions.Running(s.ID),
		CreatedBy: apiUser(u), CreatedAt: ts(s.CreatedAt), UpdatedAt: ts(s.UpdatedAt),
	}, nil
}

// sessionAccess loads a session after checking action on its agent.
func (a *API) sessionAccess(ctx context.Context, sessionID string, action authz.Action) (db.AgentSession, error) {
	s, err := a.Store.GetAgentSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return s, errNotFound
	}
	if err != nil {
		return s, err
	}
	_, err = a.authz.Agent(ctx, me(ctx).ID, s.AgentID, action)
	return s, err
}

// ListSessions implements gen.StrictServerInterface.
func (a *API) ListSessions(ctx context.Context, req gen.ListSessionsRequestObject) (gen.ListSessionsResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAgentSessions(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.ListSessions200JSONResponse{}
	for _, r := range rows {
		s, err := a.apiSession(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// CreateSession implements gen.StrictServerInterface.
func (a *API) CreateSession(ctx context.Context, req gen.CreateSessionRequestObject) (gen.CreateSessionResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentPrompt); err != nil {
		return nil, err
	}
	title := ""
	if req.Body != nil && req.Body.Title != nil {
		title = strings.TrimSpace(*req.Body.Title)
	}
	s, err := a.Sessions.Create(ctx, req.AgentId, me(ctx).ID, title)
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.CreateSession201JSONResponse(g), nil
}

// GetSession implements gen.StrictServerInterface.
func (a *API) GetSession(ctx context.Context, req gen.GetSessionRequestObject) (gen.GetSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.GetSession200JSONResponse(g), nil
}

// RenameSession implements gen.StrictServerInterface.
func (a *API) RenameSession(ctx context.Context, req gen.RenameSessionRequestObject) (gen.RenameSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Body.Title)
	if title == "" || len(title) > 200 {
		return nil, badRequest("title must be 1 to 200 characters")
	}
	if err := a.Store.RenameAgentSessionTitle(ctx, s.ID, title); err != nil {
		return nil, err
	}
	s.Title = title
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.RenameSession200JSONResponse(g), nil
}

// DeleteSession implements gen.StrictServerInterface.
func (a *API) DeleteSession(ctx context.Context, req gen.DeleteSessionRequestObject) (gen.DeleteSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if err := a.Sessions.Delete(ctx, s.ID); err != nil {
		if errors.Is(err, session.ErrBusy) {
			return nil, errStatus(http.StatusConflict, err.Error())
		}
		return nil, err
	}
	return gen.DeleteSession204Response{}, nil
}

// PromptSession implements gen.StrictServerInterface.
func (a *API) PromptSession(ctx context.Context, req gen.PromptSessionRequestObject) (gen.PromptSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(req.Body.Text)
	if text == "" {
		return nil, badRequest("text is required")
	}
	u := me(ctx)
	if err := a.Sessions.Prompt(ctx, s.ID, u.ID, u.Name, text, nil); err != nil {
		if errors.Is(err, session.ErrBusy) {
			return nil, errStatus(http.StatusConflict, err.Error())
		}
		return nil, err
	}
	return gen.PromptSession202Response{}, nil
}

// CancelSession implements gen.StrictServerInterface.
func (a *API) CancelSession(ctx context.Context, req gen.CancelSessionRequestObject) (gen.CancelSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if err := a.Sessions.Cancel(ctx, s.ID); err != nil {
		if errors.Is(err, session.ErrNoTurn) {
			return nil, errStatus(http.StatusConflict, err.Error())
		}
		return nil, err
	}
	return gen.CancelSession202Response{}, nil
}

// AnswerPermission implements gen.StrictServerInterface.
func (a *API) AnswerPermission(ctx context.Context, req gen.AnswerPermissionRequestObject) (gen.AnswerPermissionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if err := a.Sessions.AnswerPermission(ctx, s.ID, req.RequestId, req.Body.OptionId, me(ctx).ID); err != nil {
		if errors.Is(err, session.ErrUnknownPermission) {
			return nil, errStatus(http.StatusNotFound, err.Error())
		}
		return nil, err
	}
	return gen.AnswerPermission204Response{}, nil
}

func apiEvent(e session.Event) gen.SessionEvent {
	var data map[string]any
	_ = json.Unmarshal(e.Data, &data)
	g := gen.SessionEvent{Seq: e.Seq, At: e.At, Type: gen.SessionEventType(e.Type), Data: data}
	if e.UserID != "" {
		g.UserId = &e.UserID
	}
	return g
}

// ListSessionEvents implements gen.StrictServerInterface.
func (a *API) ListSessionEvents(ctx context.Context, req gen.ListSessionEventsRequestObject) (gen.ListSessionEventsResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	after := int64(0)
	if req.Params.After != nil {
		after = *req.Params.After
	}
	limit := 1000
	if req.Params.Limit != nil {
		limit = min(max(*req.Params.Limit, 1), 5000)
	}
	rows, err := a.Store.ListSessionEvents(ctx, db.ListSessionEventsParams{SessionID: s.ID, Seq: after, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := gen.ListSessionEvents200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiEvent(session.Event{Seq: r.Seq, At: time.UnixMilli(r.At).UTC(), Type: r.Type, UserID: r.UserID, Data: json.RawMessage(r.Data)}))
	}
	return out, nil
}

// stream sends a session's events over a WebSocket, from a cursor on.
func (a *API) stream(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeError(w, r, errUnauthorized)
		return
	}
	ctx := auth.WithUser(r.Context(), u)
	s, err := a.sessionAccess(ctx, chi.URLParam(r, "sessionId"), authz.AgentRead)
	if err != nil {
		writeError(w, r, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx = ws.CloseRead(ctx) // we only write; this also detects the client leaving

	backlog, sub, err := a.Sessions.Subscribe(ctx, s.ID, after)
	if err != nil {
		return
	}
	defer sub.Close()
	running := a.Sessions.Running(s.ID)
	if wsjson.Write(ctx, ws, map[string]any{"type": "status", "running": running}) != nil {
		return
	}
	for _, e := range backlog {
		if wsjson.Write(ctx, ws, apiEvent(e)) != nil {
			return
		}
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case e := <-sub.C:
			if wsjson.Write(ctx, ws, apiEvent(e)) != nil {
				return
			}
			if e.Type == session.EventPrompt || e.Type == session.EventTurnEnd {
				running = e.Type == session.EventPrompt
				_ = wsjson.Write(ctx, ws, map[string]any{"type": "status", "running": running})
			}
		case <-tick.C:
			if now := a.Sessions.Running(s.ID); now != running {
				running = now
				_ = wsjson.Write(ctx, ws, map[string]any{"type": "status", "running": running})
			}
		case <-sub.Lost():
			// Too slow: the client reconnects with its last seq.
			_ = ws.Close(websocket.StatusTryAgainLater, "resubscribe")
			return
		case <-ctx.Done():
			return
		}
	}
}
