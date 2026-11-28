package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

func apiQueuedMessage(m session.QueuedMessage) gen.QueuedMessage {
	out := gen.QueuedMessage{
		Id:        m.ID,
		SessionId: m.SessionID,
		UserId:    m.UserID,
		UserName:  m.UserName,
		Text:      m.Text,
		Steer:     m.Steer,
		CreatedAt: m.CreatedAt,
	}
	if len(m.Images) > 0 {
		imgs := make([]gen.PromptImageInput, 0, len(m.Images))
		for _, im := range m.Images {
			imgs = append(imgs, gen.PromptImageInput{
				MimeType: gen.PromptImageInputMimeType(im.MimeType),
				Data:     im.Data,
			})
		}
		out.Images = &imgs
	}
	return out
}

func apiQueuedMessages(in []session.QueuedMessage) []gen.QueuedMessage {
	out := make([]gen.QueuedMessage, 0, len(in))
	for _, m := range in {
		out = append(out, apiQueuedMessage(m))
	}
	return out
}

// UpdateSessionSettings implements gen.StrictServerInterface.
func (a *API) UpdateSessionSettings(ctx context.Context, req gen.UpdateSessionSettingsRequestObject) (gen.UpdateSessionSettingsResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	b := req.Body
	agent, err := a.Store.GetAgent(ctx, s.AgentID)
	if err != nil {
		return nil, err
	}
	details := map[string]any{}

	if b.Harness != nil && strings.TrimSpace(*b.Harness) != "" {
		h := strings.TrimSpace(*b.Harness)
		ad, ok, err := a.resolveAdapter(ctx, h)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("unknown harness " + h)
		}
		if err := a.Sessions.SetSessionHarness(ctx, s.ID, ad.ID); err != nil {
			if errors.Is(err, session.ErrBusy) {
				return nil, errStatus(http.StatusConflict, err.Error())
			}
			return nil, err
		}
		if a.Egress != nil {
			a.Egress.Invalidate()
		}
		details["harness"] = ad.ID
		s, err = a.Store.GetAgentSession(ctx, s.ID)
		if err != nil {
			return nil, err
		}
	}

	if b.PermissionMode != nil && *b.PermissionMode != "" {
		pm := string(*b.PermissionMode)
		if !session.ValidPermissionMode(pm) {
			return nil, badRequest("invalid permissionMode " + pm)
		}
		if err := a.Sessions.SetSessionPermissionMode(ctx, s.ID, pm); err != nil {
			return nil, err
		}
		details["permissionMode"] = pm
		s.PermissionMode = pm
	}

	if b.Model != nil || b.ModelOptions != nil {
		targetAgent := agent
		if s.Harness != "" {
			targetAgent.Harness = s.Harness
		}
		choice := session.Choice{Model: strings.TrimSpace(deref(b.Model)), Options: map[string]string{}}
		if b.ModelOptions != nil {
			for k, v := range *b.ModelOptions {
				choice.Options[k] = v
			}
		}
		_, cerr := a.authz.Agent(ctx, me(ctx).ID, s.AgentID, authz.AgentConfigure)
		if err := a.validateChoice(ctx, targetAgent, choice, session.OwnChoice(s), cerr == nil); err != nil {
			return nil, err
		}
		if err := a.Sessions.SetSessionChoice(ctx, s.ID, choice); err != nil {
			return nil, err
		}
		details["model"] = choice.Model
	}

	if len(details) > 0 {
		a.record(ctx, agent, audit.SessionSettings, s.ID, details)
	}
	s, err = a.Store.GetAgentSession(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.UpdateSessionSettings200JSONResponse(g), nil
}

// SetSessionHarness implements gen.StrictServerInterface.
func (a *API) SetSessionHarness(ctx context.Context, req gen.SetSessionHarnessRequestObject) (gen.SetSessionHarnessResponseObject, error) {
	if req.Body == nil || strings.TrimSpace(req.Body.Harness) == "" {
		return nil, badRequest("harness is required")
	}
	h := req.Body.Harness
	res, err := a.UpdateSessionSettings(ctx, gen.UpdateSessionSettingsRequestObject{
		SessionId: req.SessionId,
		Body: &gen.UpdateSessionSettingsJSONRequestBody{
			Harness:      &h,
			Model:        req.Body.Model,
			ModelOptions: req.Body.ModelOptions,
		},
	})
	if err != nil {
		return nil, err
	}
	ok, _ := res.(gen.UpdateSessionSettings200JSONResponse)
	return gen.SetSessionHarness200JSONResponse(ok), nil
}

// ListSessionQueue implements gen.StrictServerInterface.
func (a *API) ListSessionQueue(ctx context.Context, req gen.ListSessionQueueRequestObject) (gen.ListSessionQueueResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentRead)
	if err != nil {
		return nil, err
	}
	msgs, err := a.Sessions.ListQueue(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	return gen.ListSessionQueue200JSONResponse(apiQueuedMessages(msgs)), nil
}

// EnqueueSessionMessage implements gen.StrictServerInterface.
func (a *API) EnqueueSessionMessage(ctx context.Context, req gen.EnqueueSessionMessageRequestObject) (gen.EnqueueSessionMessageResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
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
	steer := deref(req.Body.Steer)
	started, qm, err := a.Sessions.Enqueue(ctx, s.ID, u.ID, u.Name, text, images, steer)
	if err != nil {
		return nil, err
	}
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.Prompt, s.ID, map[string]any{
		"text": truncateRunes(text, 500), "queued": !started, "steer": steer, "images": len(images),
	})
	resp := gen.EnqueueSessionMessage201JSONResponse{Started: started}
	if qm != nil {
		m := apiQueuedMessage(*qm)
		resp.Message = &m
	}
	return resp, nil
}

// UpdateQueuedMessage implements gen.StrictServerInterface.
func (a *API) UpdateQueuedMessage(ctx context.Context, req gen.UpdateQueuedMessageRequestObject) (gen.UpdateQueuedMessageResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	text := strings.TrimSpace(req.Body.Text)
	if text == "" || len(text) > 256*1024 {
		return nil, badRequest("text must be 1 byte to 256 KiB")
	}
	qm, err := a.Sessions.UpdateQueuedMessage(ctx, s.ID, req.MessageId, text)
	if errors.Is(err, session.ErrQueueMessageNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateQueuedMessage200JSONResponse(apiQueuedMessage(qm)), nil
}

// DeleteQueuedMessage implements gen.StrictServerInterface.
func (a *API) DeleteQueuedMessage(ctx context.Context, req gen.DeleteQueuedMessageRequestObject) (gen.DeleteQueuedMessageResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	err = a.Sessions.DeleteQueuedMessage(ctx, s.ID, req.MessageId)
	if errors.Is(err, session.ErrQueueMessageNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteQueuedMessage204Response{}, nil
}

// SendQueuedMessageNow implements gen.StrictServerInterface.
func (a *API) SendQueuedMessageNow(ctx context.Context, req gen.SendQueuedMessageNowRequestObject) (gen.SendQueuedMessageNowResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	started, steered, err := a.Sessions.SendQueuedMessageNow(ctx, s.ID, req.MessageId)
	if errors.Is(err, session.ErrQueueMessageNotFound) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return gen.SendQueuedMessageNow202JSONResponse{Started: started, Steered: steered}, nil
}

// RewindSession implements gen.StrictServerInterface.
func (a *API) RewindSession(ctx context.Context, req gen.RewindSessionRequestObject) (gen.RewindSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if req.Body == nil || req.Body.Seq < 1 {
		return nil, badRequest("seq must be >= 1")
	}
	res, err := a.Sessions.Rewind(ctx, s.ID, req.Body.Seq, me(ctx).ID)
	if err != nil {
		switch {
		case errors.Is(err, session.ErrBusy):
			return nil, errStatus(http.StatusConflict, err.Error())
		case errors.Is(err, session.ErrEventNotFound):
			return nil, errNotFound
		}
		return nil, err
	}

	filesReverted := false
	revertNote := ""
	if deref(req.Body.RevertFiles) {
		filesReverted, revertNote = a.revertSessionWorktree(ctx, s)
	}

	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SessionRewind, s.ID, map[string]any{
		"seq": res.Seq, "deletedEvents": res.DeletedEvents, "filesReverted": filesReverted,
	})

	out := gen.RewindSession200JSONResponse{
		Seq:           res.Seq,
		DeletedEvents: res.DeletedEvents,
		FilesReverted: filesReverted,
	}
	if res.PromptText != "" {
		pt := res.PromptText
		out.PromptText = &pt
	}
	if revertNote != "" {
		rn := revertNote
		out.RevertNote = &rn
	}
	return out, nil
}

// revertSessionWorktree best-effort reverts uncommitted edits ONLY when the
// session works inside a dedicated git worktree (never mutating shared repos).
func (a *API) revertSessionWorktree(ctx context.Context, s db.AgentSession) (bool, string) {
	if s.Cwd == "" {
		return false, "Files were not reverted because this thread is not in a dedicated git worktree."
	}
	ts, err := a.session(ctx, s.AgentID)
	if err != nil {
		if errors.Is(err, tunnel.ErrNotConnected) || errors.Is(err, sandbox.ErrUnreachable) {
			return false, "Sandbox is not running; files were not reverted."
		}
		return false, "Could not reach sandbox to revert files."
	}
	gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reply, err := ts.Git(gctx, tunnel.GitRequest{Action: tunnel.GitStatus, Path: s.Cwd})
	if err != nil || reply.Repo == nil {
		return false, "Files were not reverted because the working directory is not a git repository."
	}
	if !reply.Repo.IsWorktree {
		return false, "Files were not reverted because this thread is not in a dedicated git worktree."
	}
	if !reply.Repo.Dirty {
		return true, "Worktree was already clean."
	}
	proc, err := ts.StartHarnessInDir(gctx, []string{"git", "reset", "--hard", "HEAD"}, s.Cwd, nil, nil)
	if err != nil {
		return false, "Could not run git reset in worktree: " + err.Error()
	}
	defer func() { _ = proc.Close() }()
	select {
	case <-proc.Done():
	case <-gctx.Done():
		return false, "Timed out reverting worktree files."
	}
	if code, perr := proc.ExitCode(); perr != nil || code != 0 {
		return false, "git reset failed in worktree."
	}
	return true, ""
}

// StartSideChat implements gen.StrictServerInterface.
func (a *API) StartSideChat(ctx context.Context, req gen.StartSideChatRequestObject) (gen.StartSideChatResponseObject, error) {
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

	effectiveHarness := parent.Harness
	if effectiveHarness == "" {
		effectiveHarness = agent.Harness
	}
	pinnedHarness := parent.Harness
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
		} else {
			pinnedHarness = ""
		}
	}

	opts := session.CreateOpts{
		Cwd:            parent.Cwd,
		GitProject:     parent.GitProject,
		GitBranch:      parent.GitBranch,
		GitRepoPath:    parent.GitRepoPath,
		Harness:        pinnedHarness,
		PermissionMode: parent.PermissionMode,
		ParentKind:     session.ParentKindSideChat,
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
	if model != "" || len(modelOpts) > 0 {
		choice := session.Choice{Model: model, Options: modelOpts}
		targetAgent := agent
		targetAgent.Harness = effectiveHarness
		_, cerr := a.authz.Agent(ctx, me(ctx).ID, parent.AgentID, authz.AgentConfigure)
		if err := a.validateChoice(ctx, targetAgent, choice, session.Choice{}, cerr == nil); err != nil {
			return nil, err
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
		if err := a.Sessions.SetSessionChoice(ctx, child.ID, session.Choice{Model: model, Options: modelOpts}); err != nil {
			return nil, err
		}
	}
	if pinnedHarness != "" && a.Egress != nil {
		a.Egress.Invalidate()
	}
	a.record(ctx, agent, audit.SessionSideChat, child.ID, map[string]any{
		"parentSession": parent.ID, "harness": effectiveHarness,
	})
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
	return gen.StartSideChat201JSONResponse(g), nil
}

// PinSession implements gen.StrictServerInterface.
func (a *API) PinSession(ctx context.Context, req gen.PinSessionRequestObject) (gen.PinSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	n, err := a.Store.PinSession(ctx, db.PinSessionParams{
		PinnedAt: sql.NullInt64{Int64: a.Now().Unix(), Valid: true},
		ID:       s.ID,
	})
	if err != nil {
		return nil, err
	}
	if n > 0 {
		a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SessionPin, s.ID, map[string]any{"session": s.Title})
	}
	s, err = a.Store.GetAgentSession(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.PinSession200JSONResponse(g), nil
}

// UnpinSession implements gen.StrictServerInterface.
func (a *API) UnpinSession(ctx context.Context, req gen.UnpinSessionRequestObject) (gen.UnpinSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	n, err := a.Store.UnpinSession(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SessionUnpin, s.ID, map[string]any{"session": s.Title})
	}
	s, err = a.Store.GetAgentSession(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.UnpinSession200JSONResponse(g), nil
}

// SnoozeSession implements gen.StrictServerInterface.
func (a *API) SnoozeSession(ctx context.Context, req gen.SnoozeSessionRequestObject) (gen.SnoozeSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	untilUnix := int64(0) // 0 = indefinite snooze
	if req.Body != nil && req.Body.Until != nil {
		if !req.Body.Until.After(a.Now()) {
			return nil, badRequest("until must be in the future")
		}
		untilUnix = req.Body.Until.Unix()
	}
	if err := a.Store.SnoozeSession(ctx, db.SnoozeSessionParams{
		SnoozedUntil: sql.NullInt64{Int64: untilUnix, Valid: true},
		ID:           s.ID,
	}); err != nil {
		return nil, err
	}
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SessionSnooze, s.ID, map[string]any{"until": untilUnix})
	s, err = a.Store.GetAgentSession(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.SnoozeSession200JSONResponse(g), nil
}

// UnsnoozeSession implements gen.StrictServerInterface.
func (a *API) UnsnoozeSession(ctx context.Context, req gen.UnsnoozeSessionRequestObject) (gen.UnsnoozeSessionResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	n, err := a.Store.UnsnoozeSession(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SessionUnsnooze, s.ID, map[string]any{"session": s.Title})
	}
	s, err = a.Store.GetAgentSession(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	g, err := a.apiSession(ctx, s)
	if err != nil {
		return nil, err
	}
	return gen.UnsnoozeSession200JSONResponse(g), nil
}

// SearchSessions implements gen.StrictServerInterface.
func (a *API) SearchSessions(ctx context.Context, req gen.SearchSessionsRequestObject) (gen.SearchSessionsResponseObject, error) {
	q := strings.TrimSpace(req.Params.Q)
	if q == "" || len(q) > 200 {
		return nil, badRequest("q must be 1 to 200 characters")
	}
	limit := 20
	if req.Params.Limit != nil {
		limit = min(max(*req.Params.Limit, 1), 100)
	}
	needle := strings.ToLower(q)
	userID := me(ctx).ID

	type hit struct {
		sess       db.AgentSession
		agentName  string
		matchField gen.SessionSearchResultMatchField
		snippet    string
		matchSeq   int64
	}
	seen := map[string]bool{}
	var hits []hit

	metaRows, err := a.Store.SearchUserSessionsByMetadata(ctx, db.SearchUserSessionsByMetadataParams{
		UserID:  userID,
		Needle:  needle,
		MaxRows: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	for _, r := range metaRows {
		field, snippet := classifyMetadataMatch(r.AgentSession, r.ProjectName, needle)
		seen[r.AgentSession.ID] = true
		hits = append(hits, hit{
			sess:       r.AgentSession,
			agentName:  r.AgentName,
			matchField: field,
			snippet:    snippet,
		})
	}

	if len(hits) < limit {
		evRows, err := a.Store.SearchUserSessionEvents(ctx, db.SearchUserSessionEventsParams{
			UserID:  userID,
			Needle:  needle,
			MaxRows: int64(limit * 10),
		})
		if err != nil {
			return nil, err
		}
		for _, r := range evRows {
			if len(hits) >= limit {
				break
			}
			if seen[r.AgentSession.ID] {
				continue
			}
			txt := extractSearchableEventText(r.EventType, r.EventData)
			if txt == "" || !strings.Contains(strings.ToLower(txt), needle) {
				continue
			}
			seen[r.AgentSession.ID] = true
			hits = append(hits, hit{
				sess:       r.AgentSession,
				agentName:  r.AgentName,
				matchField: gen.SessionSearchResultMatchFieldMessage,
				snippet:    makeSnippet(txt, needle, 160),
				matchSeq:   r.EventSeq,
			})
		}
	}

	out := gen.SearchSessions200JSONResponse{}
	for _, h := range hits {
		gs, err := a.apiSession(ctx, h.sess)
		if err != nil {
			return nil, err
		}
		item := gen.SessionSearchResult{
			Session:    gs,
			MatchField: h.matchField,
		}
		if h.agentName != "" {
			an := h.agentName
			item.AgentName = &an
		}
		if h.snippet != "" {
			sn := h.snippet
			item.Snippet = &sn
		}
		if h.matchSeq > 0 {
			ms := h.matchSeq
			item.MatchSeq = &ms
		}
		out = append(out, item)
	}
	return out, nil
}

func classifyMetadataMatch(s db.AgentSession, projectName, needle string) (gen.SessionSearchResultMatchField, string) {
	if strings.Contains(strings.ToLower(s.Title), needle) {
		return gen.SessionSearchResultMatchFieldTitle, makeSnippet(s.Title, needle, 160)
	}
	if strings.Contains(strings.ToLower(s.GitBranch), needle) {
		return gen.SessionSearchResultMatchFieldBranch, s.GitBranch
	}
	if strings.Contains(strings.ToLower(s.ProjectBranch), needle) {
		return gen.SessionSearchResultMatchFieldBranch, s.ProjectBranch
	}
	if strings.Contains(strings.ToLower(s.GitProject), needle) {
		return gen.SessionSearchResultMatchFieldProject, s.GitProject
	}
	return gen.SessionSearchResultMatchFieldProject, projectName
}

func extractSearchableEventText(typ, data string) string {
	switch typ {
	case session.EventPrompt:
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(data), &p) == nil {
			return p.Text
		}
	case session.EventError:
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(data), &e) == nil {
			return e.Message
		}
	case session.EventUpdate:
		var u struct {
			SessionUpdate string `json:"sessionUpdate"`
			Title         string `json:"title"`
			Content       struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal([]byte(data), &u) == nil {
			if u.Content.Text != "" {
				return u.Content.Text
			}
			if u.Title != "" {
				return u.Title
			}
		}
	}
	return ""
}

func makeSnippet(text, needle string, maxRunes int) string {
	clean := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(clean) <= maxRunes {
		return clean
	}
	lower := strings.ToLower(clean)
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return truncateRunes(clean, maxRunes)
	}
	// Find rune index corresponding to byte offset idx.
	runeIdx := utf8.RuneCountInString(clean[:idx])
	runes := []rune(clean)
	start := max(0, runeIdx-maxRunes/3)
	end := min(len(runes), start+maxRunes)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
