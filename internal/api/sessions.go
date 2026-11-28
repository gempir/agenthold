package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// ListHarnesses implements gen.StrictServerInterface.
func (a *API) ListHarnesses(ctx context.Context, _ gen.ListHarnessesRequestObject) (gen.ListHarnessesResponseObject, error) {
	out := gen.ListHarnesses200JSONResponse{}
	builtinSrc := gen.HarnessTypeSource("builtin")
	for _, h := range harness.List(a.ShowTestHarness) {
		proto := gen.HarnessTypeProtocol(h.EffectiveProtocol())
		out = append(out, gen.HarnessType{
			Id:            h.ID,
			Name:          h.Name,
			Default:       h.ID == harness.Default,
			SecretPresets: append([]string{}, h.SecretPresets...),
			AccountLogins: h.Login != nil,
			Source:        &builtinSrc,
			Protocol:      &proto,
			Description:   opt(h.Description),
			Version:       opt(h.Version),
			IconUrl:       opt(h.IconURL),
		})
	}
	if u := me(ctx); u != nil {
		rows, err := a.Store.ListCustomHarnessesByOwner(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		acpProto := gen.HarnessTypeProtocol(harness.ProtocolACP)
		for _, r := range rows {
			src := gen.HarnessTypeSource(r.Kind)
			out = append(out, gen.HarnessType{
				Id:            r.ID,
				Name:          r.Name,
				Default:       false,
				SecretPresets: []string{},
				AccountLogins: false,
				Source:        &src,
				Protocol:      &acpProto,
				Description:   opt(r.Description),
				Version:       opt(r.RegistryVersion),
				IconUrl:       opt(r.IconUrl),
			})
		}
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
	ad, _, _ := a.resolveAdapter(ctx, acc.Row.Agent.Harness)
	for _, m := range info.AuthMethods {
		if ad == nil {
			break
		}
		if am, ok := harness.DescribeAuth(ad, m); ok {
			out.AuthMethods = append(out.AuthMethods, gen.AuthMethodInfo{Id: am.ID, Name: am.Name, Description: am.Description, Type: am.Type})
		}
	}
	return out, nil
}

// loginCommand returns the command that runs a terminal login method.
func (a *API) loginCommand(ctx context.Context, agent db.Agent, methodID string) ([]string, map[string]string, error) {
	info, err := a.Sessions.Harness(ctx, agent.ID, true)
	if err != nil {
		return nil, nil, err
	}
	ad, ok, err := a.resolveAdapter(ctx, agent.Harness)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, errors.New("unknown harness")
	}
	argv, env, ok := harness.LoginCommand(ad, harness.Config{Model: agent.Model}, info.AuthMethods, methodID)
	if !ok {
		return nil, nil, errStatus(http.StatusNotFound, "unknown login method")
	}
	return argv, env, nil
}

func (a *API) apiSession(ctx context.Context, s db.AgentSession) (gen.Session, error) {
	qcount, _ := a.Store.CountSessionQueuedMessages(ctx, s.ID)
	return a.apiSessionWith(ctx, s, "", int(qcount))
}

func (a *API) apiSessionWith(ctx context.Context, s db.AgentSession, agentHarness string, queuedCount int) (gen.Session, error) {
	u, err := a.Store.GetUser(ctx, s.CreatedBy)
	if err != nil {
		return gen.Session{}, err
	}
	pm := gen.PermissionMode(session.NormalizePermissionMode(s.PermissionMode))
	pk := gen.SessionParentKind(s.ParentKind)
	qc := queuedCount
	out := gen.Session{
		Id: s.ID, AgentId: s.AgentID, Title: s.Title, Running: a.Sessions.Running(s.ID),
		CreatedBy: apiUser(u), CreatedAt: ts(s.CreatedAt), UpdatedAt: ts(s.UpdatedAt),
		PermissionMode: &pm, ParentKind: &pk, QueuedCount: &qc,
	}
	if s.PinnedAt.Valid {
		p := ts(s.PinnedAt.Int64)
		out.PinnedAt = &p
	}
	if s.SnoozedUntil.Valid {
		if s.SnoozedUntil.Int64 == 0 {
			sn := true
			out.Snoozed = &sn
		} else if s.SnoozedUntil.Int64 > a.Now().Unix() {
			sn := true
			u := ts(s.SnoozedUntil.Int64)
			out.Snoozed = &sn
			out.SnoozedUntil = &u
		}
	}
	if s.SettledAt.Valid {
		at, by := ts(s.SettledAt.Int64), settledBy(s.SettledBy)
		out.SettledAt, out.SettledBy = &at, &by
	}
	if c := session.OwnChoice(s); !c.Empty() {
		m := apiChoice(c)
		out.Model = &m
	}
	if s.Cwd != "" {
		cwd := s.Cwd
		out.Cwd = &cwd
	}
	if s.GitProject != "" || s.GitBranch != "" {
		out.Git = &gen.SessionGit{Project: s.GitProject, Branch: s.GitBranch}
		if s.GitRepoPath != "" {
			p := s.GitRepoPath
			out.Git.RepoPath = &p
		}
	}
	if s.ProjectID != "" {
		if p, err := a.Store.GetProject(ctx, s.ProjectID); err == nil {
			out.Project = &gen.SessionProject{Id: p.ID, Name: p.Name, Direct: s.ProjectBranch == "", Branch: opt(s.ProjectBranch)}
		}
	}
	effectiveHarness := s.Harness
	if effectiveHarness == "" {
		if agentHarness != "" {
			effectiveHarness = agentHarness
		} else if ag, err := a.Store.GetAgent(ctx, s.AgentID); err == nil {
			effectiveHarness = ag.Harness
		}
	}
	if effectiveHarness != "" {
		h := effectiveHarness
		out.Harness = &h
	}
	if s.OriginAgentID != "" {
		name := "(deleted agent)"
		if ag, err := a.Store.GetAgent(ctx, s.OriginAgentID); err == nil {
			name = ag.Name
		}
		var parentTitle *string
		if s.OriginSessionID != "" {
			if ps, err := a.Store.GetAgentSession(ctx, s.OriginSessionID); err == nil && ps.Title != "" {
				parentTitle = &ps.Title
			}
		}
		out.Origin = &struct {
			AgentId      string  `json:"agentId"`
			AgentName    string  `json:"agentName"`
			SessionId    string  `json:"sessionId"`
			SessionTitle *string `json:"sessionTitle,omitempty"`
		}{AgentId: s.OriginAgentID, AgentName: name, SessionId: s.OriginSessionID, SessionTitle: parentTitle}
	}
	return out, nil
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
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAgentSessions(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	qcounts := map[string]int{}
	if counts, err := a.Store.CountAgentQueuedMessages(ctx, req.AgentId); err == nil {
		for _, c := range counts {
			qcounts[c.SessionID] = int(c.Count)
		}
	}
	out := gen.ListSessions200JSONResponse{}
	for _, r := range rows {
		s, err := a.apiSessionWith(ctx, r, acc.Row.Agent.Harness, qcounts[r.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// CreateSession implements gen.StrictServerInterface.
func (a *API) CreateSession(ctx context.Context, req gen.CreateSessionRequestObject) (gen.CreateSessionResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if acc.Row.Agent.IsTemplate == 1 {
		return nil, sandbox.ErrTemplate
	}
	title := ""
	var opts session.CreateOpts
	var model string
	modelOpts := map[string]string{}
	effectiveHarness := acc.Row.Agent.Harness
	if b := req.Body; b != nil {
		if b.Title != nil {
			title = strings.TrimSpace(*b.Title)
		}
		if opts, err = a.sessionGitStart(ctx, acc.Row.Agent, *b); err != nil {
			return nil, err
		}
		if b.Harness != nil && strings.TrimSpace(*b.Harness) != "" {
			h := strings.TrimSpace(*b.Harness)
			ad, ok, err := a.resolveAdapter(ctx, h)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, badRequest("unknown harness " + h)
			}
			effectiveHarness = ad.ID
			if ad.ID != acc.Row.Agent.Harness {
				opts.Harness = ad.ID
			}
		}
		if b.PermissionMode != nil && *b.PermissionMode != "" {
			pm := string(*b.PermissionMode)
			if !session.ValidPermissionMode(pm) {
				return nil, badRequest("invalid permissionMode " + pm)
			}
			opts.PermissionMode = pm
		}
		model = strings.TrimSpace(deref(b.Model))
		if b.ModelOptions != nil {
			for k, v := range *b.ModelOptions {
				modelOpts[k] = v
			}
		}
	}
	if model != "" || len(modelOpts) > 0 {
		choice := session.Choice{Model: model, Options: modelOpts}
		targetAgent := acc.Row.Agent
		targetAgent.Harness = effectiveHarness
		_, cerr := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
		if err := a.validateChoice(ctx, targetAgent, choice, session.Choice{}, cerr == nil); err != nil {
			return nil, err
		}
	}
	s, err := a.Sessions.CreateWith(ctx, req.AgentId, me(ctx).ID, title, opts)
	if err != nil {
		return nil, err
	}
	if model != "" || len(modelOpts) > 0 {
		if err := a.Sessions.SetSessionChoice(ctx, s.ID, session.Choice{Model: model, Options: modelOpts}); err != nil {
			return nil, err
		}
		s, err = a.Store.GetAgentSession(ctx, s.ID)
		if err != nil {
			return nil, err
		}
	}
	if opts.Harness != "" && a.Egress != nil {
		a.Egress.Invalidate()
	}
	a.record(ctx, acc.Row.Agent, audit.SessionCreate, s.ID, nil)
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.CreateSession201JSONResponse(g), nil
}

// sessionGitStart prepares where a new session works: its working directory
// and, in a git repository, the branch (checked out, or in a new worktree).
func (a *API) sessionGitStart(ctx context.Context, ag db.Agent, b gen.CreateSessionJSONRequestBody) (session.CreateOpts, error) {
	var opts session.CreateOpts
	cwd, err := cleanRelPath("cwd", deref(b.Cwd))
	if err != nil {
		return opts, err
	}
	branch := strings.TrimSpace(deref(b.GitBranch))
	createBranch, createWorktree := deref(b.CreateBranch), deref(b.CreateWorktree)
	switch {
	case createWorktree && (branch == "" || cwd == ""):
		return opts, badRequest("createWorktree needs cwd (the repository) and gitBranch")
	case branch != "" && cwd == "":
		return opts, badRequest("gitBranch needs cwd (the repository)")
	case createBranch && branch == "":
		return opts, badRequest("createBranch needs gitBranch")
	case cwd == "":
		return opts, nil
	}
	if branch == "" {
		opts.Cwd = cwd
		// What the directory holds is detected now if the machine runs, or
		// after the first turn.
		if ok, err := a.Node.Connected(ctx, ag.ID); err == nil && ok {
			if reply, err := node.Client(a.Node, ag.ID).Git(ctx, tunnel.GitRequest{Action: tunnel.GitStatus, Path: cwd}); err == nil && reply.Repo != nil {
				opts.GitProject, opts.GitBranch, opts.GitRepoPath = reply.Repo.Project, reply.Repo.Branch, reply.Repo.Path
			}
		}
		return opts, nil
	}
	ts, err := a.session(ctx, ag.ID)
	if err != nil {
		return opts, err
	}
	action, req := audit.GitCheckout, tunnel.GitRequest{Action: tunnel.GitCheckout, Path: cwd, Branch: branch, Create: createBranch}
	if createWorktree {
		action, req = audit.GitWorktreeAdd, tunnel.GitRequest{Action: tunnel.GitWorktreeAdd, Path: cwd, Branch: branch, Create: createBranch}
	}
	reply, err := ts.Git(ctx, req)
	if err != nil {
		return opts, err
	}
	repo := reply.Repo
	if repo == nil {
		return opts, errStatus(http.StatusBadGateway, "the sandbox did not describe the repository")
	}
	a.record(ctx, ag, action, repo.Path, map[string]any{"path": repo.Path, "branch": repo.Branch, "create": createBranch, "repo": cwd})
	opts.Cwd = cwd
	if createWorktree {
		opts.Cwd = repo.Path
	} else {
		a.noteBranch(ctx, ag.ID, *repo)
	}
	opts.GitProject, opts.GitBranch, opts.GitRepoPath = repo.Project, repo.Branch, repo.Path
	return opts, nil
}

// StartSubthread implements gen.StrictServerInterface.
func (a *API) StartSubthread(ctx context.Context, req gen.StartSubthreadRequestObject) (gen.StartSubthreadResponseObject, error) {
	parent, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	agent, err := a.Store.GetAgent(ctx, parent.AgentID)
	if err != nil {
		return nil, err
	}
	var title, harnessID, model, text string
	modelOpts := map[string]string{}
	if b := req.Body; b != nil {
		title = strings.TrimSpace(deref(b.Title))
		if len(title) > 200 {
			return nil, badRequest("title must be at most 200 characters")
		}
		harnessID = strings.TrimSpace(deref(b.Harness))
		model = strings.TrimSpace(deref(b.Model))
		if b.ModelOptions != nil {
			for k, v := range *b.ModelOptions {
				modelOpts[k] = v
			}
		}
		text = strings.TrimSpace(deref(b.Text))
	}
	effectiveHarness := agent.Harness
	pinnedHarness := ""
	if harnessID != "" {
		ad, ok, err := a.resolveAdapter(ctx, harnessID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("unknown harness " + harnessID)
		}
		effectiveHarness = ad.ID
		if ad.ID != agent.Harness {
			pinnedHarness = ad.ID
		}
	}
	opts := session.CreateOpts{
		Cwd:         parent.Cwd,
		GitProject:  parent.GitProject,
		GitBranch:   parent.GitBranch,
		GitRepoPath: parent.GitRepoPath,
		Harness:     pinnedHarness,
		ParentKind:  session.ParentKindSubthread,
	}
	if parent.ProjectID != "" {
		if opts.GitBranch == "" {
			opts.GitBranch = parent.ProjectBranch
		}
		if opts.GitProject == "" {
			if proj, perr := a.Store.GetProject(ctx, parent.ProjectID); perr == nil {
				opts.GitProject = proj.Name
			}
		}
	}
	child, err := a.Sessions.CreateWith(ctx, parent.AgentID, me(ctx).ID, title, opts)
	if err != nil {
		return nil, err
	}
	if err := a.Store.SetSessionOrigin(ctx, db.SetSessionOriginParams{
		OriginAgentID:   parent.AgentID,
		OriginSessionID: parent.ID,
		ID:              child.ID,
	}); err != nil {
		return nil, err
	}
	if model != "" || len(modelOpts) > 0 {
		choice := session.Choice{Model: model, Options: modelOpts}
		targetAgent := agent
		targetAgent.Harness = effectiveHarness
		_, cerr := a.authz.Agent(ctx, me(ctx).ID, parent.AgentID, authz.AgentConfigure)
		if err := a.validateChoice(ctx, targetAgent, choice, session.Choice{}, cerr == nil); err != nil {
			_ = a.Sessions.Delete(ctx, child.ID)
			return nil, err
		}
		if err := a.Sessions.SetSessionChoice(ctx, child.ID, choice); err != nil {
			return nil, err
		}
	}
	displayTitle := child.Title
	if text != "" && session.IsPlaceholderTitle(displayTitle) {
		displayTitle = truncateRunes(strings.Join(strings.Fields(text), " "), 60)
	}
	ev := map[string]any{
		"sessionId": child.ID,
		"agentId":   parent.AgentID,
		"title":     displayTitle,
		"harness":   effectiveHarness,
		"status":    "idle",
	}
	if text != "" {
		ev["status"] = "running"
	}
	if model != "" {
		ev["model"] = model
	}
	if child.Cwd != "" {
		ev["cwd"] = child.Cwd
	}
	if child.GitBranch != "" {
		ev["branch"] = child.GitBranch
	}
	_ = a.Sessions.RecordSubthread(ctx, parent.ID, ev)
	if pinnedHarness != "" && a.Egress != nil {
		a.Egress.Invalidate()
	}
	a.record(ctx, agent, audit.SessionCreate, child.ID, map[string]any{"subthread": true, "parentSession": parent.ID, "harness": effectiveHarness})
	if text != "" {
		u := me(ctx)
		if err := a.Sessions.PromptWith(ctx, child.ID, u.ID, u.Name, text, nil, session.PromptOpts{}); err != nil {
			return nil, err
		}
	}
	child, err = a.Store.GetAgentSession(ctx, child.ID)
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, child)
	if err != nil {
		return nil, err
	}
	return gen.StartSubthread201JSONResponse(g), nil
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
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SessionDelete, s.ID, map[string]any{"title": s.Title})
	return gen.DeleteSession204Response{}, nil
}

// PromptSession implements gen.StrictServerInterface.
func (a *API) PromptSession(ctx context.Context, req gen.PromptSessionRequestObject) (gen.PromptSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(req.Body.Text)
	hasImages := req.Body.Images != nil && len(*req.Body.Images) > 0
	if (text == "" && !hasImages) || len(text) > 256*1024 {
		return nil, badRequest("text must be 1 byte to 256 KiB")
	}
	if hasImages && len(*req.Body.Images) > 4 {
		return nil, badRequest("at most 4 images per message")
	}
	var images []session.PromptImage
	if req.Body.Images != nil {
		for _, im := range *req.Body.Images {
			pi, err := promptImage(string(im.MimeType), im.Data)
			if err != nil {
				return nil, err
			}
			images = append(images, pi)
		}
	}
	u := me(ctx)
	if err := a.Sessions.PromptWith(ctx, s.ID, u.ID, u.Name, text, nil, session.PromptOpts{Images: images}); err != nil {
		if errors.Is(err, session.ErrBusy) {
			return nil, errStatus(http.StatusConflict, err.Error())
		}
		return nil, err
	}
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.Prompt, s.ID, map[string]any{"text": truncateRunes(text, 500), "session": s.Title, "images": len(images)})
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
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.Cancel, s.ID, nil)
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
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.Permission, s.ID, map[string]any{"requestId": req.RequestId, "optionId": req.Body.OptionId})
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
	agent := mustAgent(ctx, a, s.AgentID)
	role, err := a.authz.RoleOf(ctx, u.ID, agent)
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

	viewer := &session.Viewer{ID: u.ID, Name: u.Name, AvatarURL: u.AvatarUrl, Role: string(role)}
	backlog, sub, err := a.Sessions.Subscribe(ctx, s.ID, after, viewer)
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
	tick := time.NewTicker(roleRecheck)
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
			// Access can be withdrawn (unshared, or the account disabled) while watching.
			if !a.stillAllowed(ctx, u.ID) {
				_ = ws.Close(websocket.StatusPolicyViolation, accessEnded)
				return
			}
			if role, err := a.authz.RoleOf(ctx, u.ID, agent); err == nil && !authz.Can(role, authz.AgentRead) {
				_ = ws.Close(websocket.StatusPolicyViolation, "access removed")
				return
			}
			if now := a.Sessions.Running(s.ID); now != running {
				running = now
				_ = wsjson.Write(ctx, ws, map[string]any{"type": "status", "running": running})
			}
		case vs := <-sub.Presence:
			if wsjson.Write(ctx, ws, map[string]any{"type": "presence", "viewers": vs}) != nil {
				return
			}
		case q := <-sub.Queue:
			if wsjson.Write(ctx, ws, map[string]any{
				"type":      "queue_updated",
				"sessionId": q.SessionID,
				"messages":  apiQueuedMessages(q.Messages),
				"count":     q.Count,
			}) != nil {
				return
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

func mustAgent(ctx context.Context, a *API, agentID string) db.Agent {
	ag, _ := a.Store.GetAgent(ctx, agentID)
	return ag
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// maxPromptImageBytes caps one attached image after decoding.
const maxPromptImageBytes = 5 << 20

// promptImage validates one attached image: base64 that decodes to at most
// maxPromptImageBytes and whose bytes really are the image type it claims.
func promptImage(mimeType, data string) (session.PromptImage, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return session.PromptImage{}, badRequest("image data must be base64")
	}
	if len(raw) == 0 || len(raw) > maxPromptImageBytes {
		return session.PromptImage{}, badRequest("each image must be 1 byte to 5 MiB")
	}
	if http.DetectContentType(raw) != mimeType {
		return session.PromptImage{}, badRequest("image content does not match its type " + mimeType)
	}
	return session.PromptImage{MimeType: mimeType, Data: data}, nil
}
