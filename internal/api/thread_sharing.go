package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

const guestLinkTTL = 24 * time.Hour

// ListMySharedThreads implements gen.StrictServerInterface.
func (a *API) ListMySharedThreads(ctx context.Context, _ gen.ListMySharedThreadsRequestObject) (gen.ListMySharedThreadsResponseObject, error) {
	rows, err := a.Store.ListUserSharedSessions(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListMySharedThreads200JSONResponse{}
	for _, r := range rows {
		qcount, _ := a.Store.CountSessionQueuedMessages(ctx, r.AgentSession.ID)
		s, err := a.apiSessionWith(ctx, r.AgentSession, r.AgentHarness, int(qcount))
		if err != nil {
			return nil, err
		}
		out = append(out, gen.SharedThread{
			Session:   s,
			AgentName: r.AgentName,
			Role:      gen.SessionShareRole(r.ShareRole),
			SharedAt:  ts(r.SharedAt),
		})
	}
	return out, nil
}

// ListSessionShares implements gen.StrictServerInterface.
func (a *API) ListSessionShares(ctx context.Context, req gen.ListSessionSharesRequestObject) (gen.ListSessionSharesResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionRead)
	if err != nil {
		return nil, err
	}
	owner, err := a.Store.GetUser(ctx, acc.AgentRow.Agent.OwnerID)
	if err != nil {
		return nil, err
	}
	rows, err := a.Store.ListSessionShares(ctx, acc.Session.ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListSessionShares200JSONResponse{
		Owner:  apiUser(owner),
		Shares: []gen.SessionShare{},
	}
	for _, r := range rows {
		out.Shares = append(out.Shares, gen.SessionShare{
			User:      apiUser(r.User),
			Role:      gen.SessionShareRole(r.Role),
			CreatedBy: r.CreatedBy,
			CreatedAt: ts(r.SharedAt),
		})
	}
	return out, nil
}

// ShareSession implements gen.StrictServerInterface.
func (a *API) ShareSession(ctx context.Context, req gen.ShareSessionRequestObject) (gen.ShareSessionResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionManageShares)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	role := strings.TrimSpace(string(req.Body.Role))
	if !authz.ValidSessionShareRole(role) {
		return nil, badRequest("role must be viewer or collaborator")
	}
	email := strings.TrimSpace(string(req.Body.Email))
	if email == "" || len(email) > 320 || !strings.Contains(email, "@") {
		return nil, badRequest("email must be an email address")
	}
	users, err := a.Store.ListUsersByVerifiedEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	switch len(users) {
	case 0:
		return nil, errStatus(http.StatusNotFound, "nobody with this verified email has signed in yet")
	case 1:
	default:
		return nil, errStatus(http.StatusConflict, "several accounts use this email address")
	}
	target := users[0]
	if target.ID == acc.AgentRow.Agent.OwnerID {
		return nil, badRequest("this user already owns the agent")
	}
	now := a.Now().Unix()
	if err := a.Store.UpsertSessionShare(ctx, db.UpsertSessionShareParams{
		SessionID: acc.Session.ID,
		UserID:    target.ID,
		Role:      role,
		CreatedBy: me(ctx).ID,
		CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	sh, err := a.Store.GetSessionShare(ctx, db.GetSessionShareParams{
		SessionID: acc.Session.ID,
		UserID:    target.ID,
	})
	if err != nil {
		return nil, err
	}
	a.record(ctx, acc.AgentRow.Agent, audit.SessionShareAdd, acc.Session.ID, map[string]any{
		"userId": target.ID, "email": target.Email, "role": role,
	})
	return gen.ShareSession200JSONResponse{
		User:      apiUser(target),
		Role:      gen.SessionShareRole(sh.Role),
		CreatedBy: sh.CreatedBy,
		CreatedAt: ts(sh.CreatedAt),
	}, nil
}

// UnshareSession implements gen.StrictServerInterface.
func (a *API) UnshareSession(ctx context.Context, req gen.UnshareSessionRequestObject) (gen.UnshareSessionResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Session(ctx, u.ID, req.SessionId, authz.SessionRead)
	if err != nil {
		return nil, err
	}
	if !authz.CanUnshareSession(acc.Role, u.ID, req.UserId) {
		return nil, authz.ErrForbidden
	}
	n, err := a.Store.DeleteSessionShare(ctx, db.DeleteSessionShareParams{
		SessionID: acc.Session.ID,
		UserID:    req.UserId,
	})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	a.record(ctx, acc.AgentRow.Agent, audit.SessionShareRemove, acc.Session.ID, map[string]any{"userId": req.UserId})
	return gen.UnshareSession204Response{}, nil
}

func (a *API) apiGuestLink(l db.SessionGuestLink) gen.GuestLink {
	now := a.Now().Unix()
	out := gen.GuestLink{
		Id:        l.ID,
		SessionId: l.SessionID,
		CreatedBy: l.CreatedBy,
		ExpiresAt: ts(l.ExpiresAt),
		CreatedAt: ts(l.CreatedAt),
		Active:    !l.RevokedAt.Valid && l.ExpiresAt > now,
	}
	if l.RevokedAt.Valid {
		r := ts(l.RevokedAt.Int64)
		out.RevokedAt = &r
	}
	return out
}

// ListSessionGuestLinks implements gen.StrictServerInterface.
func (a *API) ListSessionGuestLinks(ctx context.Context, req gen.ListSessionGuestLinksRequestObject) (gen.ListSessionGuestLinksResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionManageShares)
	if err != nil {
		return nil, err
	}
	rows, err := a.Store.ListSessionGuestLinks(ctx, acc.Session.ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListSessionGuestLinks200JSONResponse{}
	for _, r := range rows {
		out = append(out, a.apiGuestLink(r))
	}
	return out, nil
}

// CreateSessionGuestLink implements gen.StrictServerInterface.
func (a *API) CreateSessionGuestLink(ctx context.Context, req gen.CreateSessionGuestLinkRequestObject) (gen.CreateSessionGuestLinkResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionManageShares)
	if err != nil {
		return nil, err
	}
	now := a.Now()
	rawToken := randomHexToken(24)
	link := db.SessionGuestLink{
		ID:        id.New("gl"),
		SessionID: acc.Session.ID,
		TokenHash: sha256Hex(rawToken),
		CreatedBy: me(ctx).ID,
		ExpiresAt: now.Add(guestLinkTTL).Unix(),
		CreatedAt: now.Unix(),
	}
	if err := a.Store.CreateSessionGuestLink(ctx, db.CreateSessionGuestLinkParams{
		ID:        link.ID,
		SessionID: link.SessionID,
		TokenHash: link.TokenHash,
		CreatedBy: link.CreatedBy,
		ExpiresAt: link.ExpiresAt,
		CreatedAt: link.CreatedAt,
	}); err != nil {
		return nil, err
	}
	a.record(ctx, acc.AgentRow.Agent, audit.GuestLinkCreate, acc.Session.ID, map[string]any{
		"linkId": link.ID, "expiresAt": link.ExpiresAt,
	})
	return gen.CreateSessionGuestLink201JSONResponse{
		Link:  a.apiGuestLink(link),
		Token: rawToken,
		Url:   "/guest/" + rawToken,
	}, nil
}

// RevokeSessionGuestLink implements gen.StrictServerInterface.
func (a *API) RevokeSessionGuestLink(ctx context.Context, req gen.RevokeSessionGuestLinkRequestObject) (gen.RevokeSessionGuestLinkResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionManageShares)
	if err != nil {
		return nil, err
	}
	n, err := a.Store.RevokeSessionGuestLink(ctx, db.RevokeSessionGuestLinkParams{
		RevokedAt: sql.NullInt64{Int64: a.Now().Unix(), Valid: true},
		ID:        req.LinkId,
		SessionID: acc.Session.ID,
	})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	a.record(ctx, acc.AgentRow.Agent, audit.GuestLinkRevoke, acc.Session.ID, map[string]any{"linkId": req.LinkId})
	return gen.RevokeSessionGuestLink204Response{}, nil
}

func apiGuestRequest(r db.SessionGuestRequest) gen.GuestRequest {
	out := gen.GuestRequest{
		Id:          r.ID,
		SessionId:   r.SessionID,
		GuestLinkId: r.GuestLinkID,
		GuestName:   r.GuestName,
		Text:        r.Text,
		Status:      gen.GuestRequestStatus(r.Status),
		CreatedAt:   ts(r.CreatedAt),
	}
	if r.ResolvedBy.Valid && r.ResolvedBy.String != "" {
		rb := r.ResolvedBy.String
		out.ResolvedBy = &rb
	}
	if r.ResolvedAt.Valid {
		ra := ts(r.ResolvedAt.Int64)
		out.ResolvedAt = &ra
	}
	return out
}

// ListSessionGuestRequests implements gen.StrictServerInterface.
func (a *API) ListSessionGuestRequests(ctx context.Context, req gen.ListSessionGuestRequestsRequestObject) (gen.ListSessionGuestRequestsResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionRead)
	if err != nil {
		return nil, err
	}
	rows, err := a.Store.ListSessionGuestRequests(ctx, acc.Session.ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListSessionGuestRequests200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiGuestRequest(r))
	}
	return out, nil
}

// ApproveSessionGuestRequest implements gen.StrictServerInterface.
func (a *API) ApproveSessionGuestRequest(ctx context.Context, req gen.ApproveSessionGuestRequestRequestObject) (gen.ApproveSessionGuestRequestResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionApproveGuest)
	if err != nil {
		return nil, err
	}
	row, err := a.Store.GetSessionGuestRequest(ctx, db.GetSessionGuestRequestParams{
		ID:        req.RequestId,
		SessionID: acc.Session.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.Status != string(gen.GuestRequestPending) {
		return nil, errStatus(http.StatusConflict, "guest request is already "+row.Status)
	}
	u := me(ctx)
	now := a.Now().Unix()
	n, err := a.Store.ResolveSessionGuestRequest(ctx, db.ResolveSessionGuestRequestParams{
		Status:     string(gen.GuestRequestApproved),
		ResolvedBy: sql.NullString{String: u.ID, Valid: true},
		ResolvedAt: sql.NullInt64{Int64: now, Valid: true},
		ID:         row.ID,
		SessionID:  acc.Session.ID,
	})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errStatus(http.StatusConflict, "guest request was already resolved")
	}

	senderName := fmt.Sprintf("Guest (%s) · approved by %s", row.GuestName, u.Name)
	if a.Sessions.Running(acc.Session.ID) {
		if _, _, err := a.Sessions.Enqueue(ctx, acc.Session.ID, u.ID, senderName, row.Text, nil, false); err != nil {
			return nil, err
		}
	} else {
		if err := a.Sessions.PromptWith(ctx, acc.Session.ID, u.ID, senderName, row.Text, nil, session.PromptOpts{}); err != nil {
			if errors.Is(err, session.ErrBusy) {
				if _, _, qerr := a.Sessions.Enqueue(ctx, acc.Session.ID, u.ID, senderName, row.Text, nil, false); qerr != nil {
					return nil, qerr
				}
			} else {
				return nil, err
			}
		}
	}

	updated, err := a.Store.GetSessionGuestRequest(ctx, db.GetSessionGuestRequestParams{
		ID:        row.ID,
		SessionID: acc.Session.ID,
	})
	if err != nil {
		return nil, err
	}
	apiReq := apiGuestRequest(updated)
	a.Sessions.BroadcastNotice(acc.Session.ID, "guest_request", map[string]any{
		"sessionId": acc.Session.ID,
		"request":   apiReq,
	})
	a.record(ctx, acc.AgentRow.Agent, audit.GuestRequestApprove, acc.Session.ID, map[string]any{
		"requestId": row.ID, "guestName": row.GuestName, "text": truncateRunes(row.Text, 500),
	})
	return gen.ApproveSessionGuestRequest200JSONResponse(apiReq), nil
}

// DismissSessionGuestRequest implements gen.StrictServerInterface.
func (a *API) DismissSessionGuestRequest(ctx context.Context, req gen.DismissSessionGuestRequestRequestObject) (gen.DismissSessionGuestRequestResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionApproveGuest)
	if err != nil {
		return nil, err
	}
	row, err := a.Store.GetSessionGuestRequest(ctx, db.GetSessionGuestRequestParams{
		ID:        req.RequestId,
		SessionID: acc.Session.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.Status != string(gen.GuestRequestPending) {
		return nil, errStatus(http.StatusConflict, "guest request is already "+row.Status)
	}
	u := me(ctx)
	now := a.Now().Unix()
	n, err := a.Store.ResolveSessionGuestRequest(ctx, db.ResolveSessionGuestRequestParams{
		Status:     string(gen.GuestRequestDismissed),
		ResolvedBy: sql.NullString{String: u.ID, Valid: true},
		ResolvedAt: sql.NullInt64{Int64: now, Valid: true},
		ID:         row.ID,
		SessionID:  acc.Session.ID,
	})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errStatus(http.StatusConflict, "guest request was already resolved")
	}
	updated, err := a.Store.GetSessionGuestRequest(ctx, db.GetSessionGuestRequestParams{
		ID:        row.ID,
		SessionID: acc.Session.ID,
	})
	if err != nil {
		return nil, err
	}
	apiReq := apiGuestRequest(updated)
	a.Sessions.BroadcastNotice(acc.Session.ID, "guest_request", map[string]any{
		"sessionId": acc.Session.ID,
		"request":   apiReq,
	})
	a.record(ctx, acc.AgentRow.Agent, audit.GuestRequestDismiss, acc.Session.ID, map[string]any{
		"requestId": row.ID, "guestName": row.GuestName,
	})
	return gen.DismissSessionGuestRequest200JSONResponse(apiReq), nil
}

// --- Public unauthenticated guest endpoints ---

func guestTokenFromRequest(r *http.Request) string {
	if tok := strings.TrimSpace(chi.URLParam(r, "token")); tok != "" && tok != "header" {
		return tok
	}
	if tok := strings.TrimSpace(r.Header.Get("X-Guest-Token")); tok != "" {
		return tok
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

func (a *API) resolveGuestLink(ctx context.Context, r *http.Request) (db.SessionGuestLink, db.AgentSession, db.Agent, error) {
	raw := guestTokenFromRequest(r)
	if len(raw) < 16 || len(raw) > 256 {
		return db.SessionGuestLink{}, db.AgentSession{}, db.Agent{}, errNotFound
	}
	link, err := a.Store.GetSessionGuestLinkByTokenHash(ctx, sha256Hex(raw))
	if errors.Is(err, sql.ErrNoRows) {
		return db.SessionGuestLink{}, db.AgentSession{}, db.Agent{}, errNotFound
	}
	if err != nil {
		return db.SessionGuestLink{}, db.AgentSession{}, db.Agent{}, err
	}
	if link.RevokedAt.Valid || link.ExpiresAt <= a.Now().Unix() {
		return db.SessionGuestLink{}, db.AgentSession{}, db.Agent{}, errNotFound
	}
	sess, err := a.Store.GetAgentSession(ctx, link.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.SessionGuestLink{}, db.AgentSession{}, db.Agent{}, errNotFound
	}
	if err != nil {
		return db.SessionGuestLink{}, db.AgentSession{}, db.Agent{}, err
	}
	ag, err := a.Store.GetAgent(ctx, sess.AgentID)
	if err != nil {
		return db.SessionGuestLink{}, db.AgentSession{}, db.Agent{}, errNotFound
	}
	if owner, err := a.Store.GetUser(ctx, ag.OwnerID); err != nil || authz.UserRole(owner) == config.RoleDisabled {
		return db.SessionGuestLink{}, db.AgentSession{}, db.Agent{}, errNotFound
	}
	return link, sess, ag, nil
}

// isGuestVisibleEvent filters out internal secret/account connection prompts so
// unauthenticated guests never see secret or OAuth connection internals.
func isGuestVisibleEvent(typ string) bool {
	switch typ {
	case session.EventSecretRequest, session.EventSecretRequestResolved, session.EventAccountConnectRequest:
		return false
	default:
		return true
	}
}

func (a *API) getGuestThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	link, sess, ag, err := a.resolveGuestLink(ctx, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var events []gen.SessionEvent
	var after int64
	for {
		rows, err := a.Store.ListSessionEvents(ctx, db.ListSessionEventsParams{
			SessionID: sess.ID,
			Seq:       after,
			Limit:     1000,
		})
		if err != nil {
			writeError(w, r, err)
			return
		}
		for _, row := range rows {
			after = row.Seq
			if !isGuestVisibleEvent(row.Type) {
				continue
			}
			events = append(events, apiEvent(session.Event{
				Seq:    row.Seq,
				At:     time.UnixMilli(row.At).UTC(),
				Type:   row.Type,
				UserID: row.UserID,
				Data:   json.RawMessage(row.Data),
			}))
		}
		if len(rows) < 1000 {
			break
		}
	}
	if events == nil {
		events = []gen.SessionEvent{}
	}
	pendingRows, err := a.Store.ListPendingSessionGuestRequests(ctx, sess.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	pending := make([]gen.GuestRequest, 0, len(pendingRows))
	for _, pr := range pendingRows {
		pending = append(pending, apiGuestRequest(pr))
	}
	agentName := ag.Name
	view := struct {
		SessionId       string             `json:"sessionId"`
		Title           string             `json:"title"`
		AgentName       *string            `json:"agentName,omitempty"`
		Running         bool               `json:"running"`
		ExpiresAt       time.Time          `json:"expiresAt"`
		Events          []gen.SessionEvent `json:"events"`
		PendingRequests []gen.GuestRequest `json:"pendingRequests"`
	}{
		SessionId:       sess.ID,
		Title:           sess.Title,
		AgentName:       &agentName,
		Running:         a.Sessions.Running(sess.ID),
		ExpiresAt:       ts(link.ExpiresAt),
		Events:          events,
		PendingRequests: pending,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(view)
}

func (a *API) createGuestRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	link, sess, _, err := a.resolveGuestLink(ctx, r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, r, badRequest("could not read body"))
		return
	}
	var in struct {
		Name *string `json:"name,omitempty"`
		Text string  `json:"text"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		writeError(w, r, badRequest("invalid json body"))
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" || len(text) > 16384 {
		writeError(w, r, badRequest("text must be 1 to 16384 characters"))
		return
	}
	name := "Guest"
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n != "" {
			if len(n) > 80 {
				writeError(w, r, badRequest("name must be at most 80 characters"))
				return
			}
			name = n
		}
	}
	now := a.Now().Unix()
	row := db.SessionGuestRequest{
		ID:          id.New("greq"),
		SessionID:   sess.ID,
		GuestLinkID: link.ID,
		GuestName:   name,
		Text:        text,
		Status:      string(gen.GuestRequestPending),
		CreatedAt:   now,
	}
	if err := a.Store.CreateSessionGuestRequest(ctx, db.CreateSessionGuestRequestParams{
		ID:          row.ID,
		SessionID:   row.SessionID,
		GuestLinkID: row.GuestLinkID,
		GuestName:   row.GuestName,
		Text:        row.Text,
		CreatedAt:   row.CreatedAt,
	}); err != nil {
		writeError(w, r, err)
		return
	}
	apiReq := apiGuestRequest(row)
	a.Sessions.BroadcastNotice(sess.ID, "guest_request", map[string]any{
		"sessionId": sess.ID,
		"request":   apiReq,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(apiReq)
}

func (a *API) streamGuestThread(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	link, sess, ag, err := a.resolveGuestLink(ctx, r)
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
	ctx = ws.CloseRead(ctx)

	guestViewer := &session.Viewer{
		ID:       "guest:" + link.ID,
		Name:     "Guest",
		Role:     "guest",
		Activity: "idle",
	}
	backlog, sub, err := a.Sessions.Subscribe(ctx, sess.ID, after, guestViewer)
	if err != nil {
		return
	}
	defer sub.Close()

	running := a.Sessions.Running(sess.ID)
	if wsjson.Write(ctx, ws, map[string]any{"type": "status", "running": running}) != nil {
		return
	}
	for _, e := range backlog {
		if !isGuestVisibleEvent(e.Type) {
			continue
		}
		if wsjson.Write(ctx, ws, apiEvent(e)) != nil {
			return
		}
	}
	tick := time.NewTicker(roleRecheck)
	defer tick.Stop()
	for {
		select {
		case e := <-sub.C:
			if !isGuestVisibleEvent(e.Type) {
				continue
			}
			if wsjson.Write(ctx, ws, apiEvent(e)) != nil {
				return
			}
			if e.Type == session.EventPrompt || e.Type == session.EventTurnEnd {
				running = e.Type == session.EventPrompt
				_ = wsjson.Write(ctx, ws, map[string]any{"type": "status", "running": running})
			}
		case <-tick.C:
			cur, err := a.Store.GetSessionGuestLink(ctx, db.GetSessionGuestLinkParams{ID: link.ID, SessionID: sess.ID})
			if err != nil || cur.RevokedAt.Valid || cur.ExpiresAt <= a.Now().Unix() {
				_ = ws.Close(websocket.StatusPolicyViolation, "guest link ended")
				return
			}
			if owner, err := a.Store.GetUser(ctx, ag.OwnerID); err != nil || authz.UserRole(owner) == config.RoleDisabled {
				_ = ws.Close(websocket.StatusPolicyViolation, accessEnded)
				return
			}
			if now := a.Sessions.Running(sess.ID); now != running {
				running = now
				_ = wsjson.Write(ctx, ws, map[string]any{"type": "status", "running": running})
			}
		case n := <-sub.Notice:
			if n.Type != "guest_request" {
				continue
			}
			frame := map[string]any{"type": n.Type}
			for k, v := range n.Payload {
				frame[k] = v
			}
			if wsjson.Write(ctx, ws, frame) != nil {
				return
			}
		case <-sub.Lost():
			_ = ws.Close(websocket.StatusTryAgainLater, "resubscribe")
			return
		case <-ctx.Done():
			return
		}
	}
}

// --- Persistent Collaborative Diff Review Comments ---

func apiReviewComment(r db.SessionReviewComment) gen.SessionReviewComment {
	out := gen.SessionReviewComment{
		Id:        r.ID,
		SessionId: r.SessionID,
		UserId:    r.UserID,
		UserName:  r.UserName,
		FilePath:  r.FilePath,
		StartLine: int(r.StartLine),
		EndLine:   int(r.EndLine),
		Side:      gen.ReviewCommentSide(r.Side),
		Body:      r.Body,
		CreatedAt: ts(r.CreatedAt),
	}
	if r.ResolvedAt.Valid {
		ra := ts(r.ResolvedAt.Int64)
		out.ResolvedAt = &ra
	}
	return out
}

func (a *API) broadcastReviewComments(ctx context.Context, sessionID string) {
	rows, err := a.Store.ListSessionReviewComments(ctx, sessionID)
	if err != nil {
		return
	}
	list := make([]gen.SessionReviewComment, 0, len(rows))
	for _, r := range rows {
		list = append(list, apiReviewComment(r))
	}
	a.Sessions.BroadcastNotice(sessionID, "review_comments_updated", map[string]any{
		"sessionId": sessionID,
		"comments":  list,
	})
}

// ListSessionReviewComments implements gen.StrictServerInterface.
func (a *API) ListSessionReviewComments(ctx context.Context, req gen.ListSessionReviewCommentsRequestObject) (gen.ListSessionReviewCommentsResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionRead)
	if err != nil {
		return nil, err
	}
	rows, err := a.Store.ListSessionReviewComments(ctx, acc.Session.ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListSessionReviewComments200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiReviewComment(r))
	}
	return out, nil
}

// CreateSessionReviewComment implements gen.StrictServerInterface.
func (a *API) CreateSessionReviewComment(ctx context.Context, req gen.CreateSessionReviewCommentRequestObject) (gen.CreateSessionReviewCommentResponseObject, error) {
	acc, err := a.authz.Session(ctx, me(ctx).ID, req.SessionId, authz.SessionComment)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, badRequest("body is required")
	}
	filePath := strings.TrimSpace(req.Body.FilePath)
	if filePath == "" || len(filePath) > 512 {
		return nil, badRequest("filePath must be 1 to 512 characters")
	}
	if req.Body.StartLine < 1 || req.Body.EndLine < req.Body.StartLine {
		return nil, badRequest("invalid line range")
	}
	body := strings.TrimSpace(req.Body.Body)
	if body == "" || len(body) > 8192 {
		return nil, badRequest("body must be 1 to 8192 characters")
	}
	side := string(gen.ShareCommentSideNew)
	if req.Body.Side != nil && *req.Body.Side != "" {
		if *req.Body.Side != gen.ShareCommentSideNew && *req.Body.Side != gen.ShareCommentSideOld {
			return nil, badRequest("side must be new or old")
		}
		side = string(*req.Body.Side)
	}
	u := me(ctx)
	row := db.SessionReviewComment{
		ID:        id.New("rc"),
		SessionID: acc.Session.ID,
		UserID:    u.ID,
		UserName:  u.Name,
		FilePath:  filePath,
		StartLine: int64(req.Body.StartLine),
		EndLine:   int64(req.Body.EndLine),
		Side:      side,
		Body:      body,
		CreatedAt: a.Now().Unix(),
	}
	if err := a.Store.CreateSessionReviewComment(ctx, db.CreateSessionReviewCommentParams{
		ID:        row.ID,
		SessionID: row.SessionID,
		UserID:    row.UserID,
		UserName:  row.UserName,
		FilePath:  row.FilePath,
		StartLine: row.StartLine,
		EndLine:   row.EndLine,
		Side:      row.Side,
		Body:      row.Body,
		CreatedAt: row.CreatedAt,
	}); err != nil {
		return nil, err
	}
	a.broadcastReviewComments(ctx, acc.Session.ID)
	return gen.CreateSessionReviewComment201JSONResponse(apiReviewComment(row)), nil
}

// ResolveSessionReviewComment implements gen.StrictServerInterface.
func (a *API) ResolveSessionReviewComment(ctx context.Context, req gen.ResolveSessionReviewCommentRequestObject) (gen.ResolveSessionReviewCommentResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Session(ctx, u.ID, req.SessionId, authz.SessionRead)
	if err != nil {
		return nil, err
	}
	row, err := a.Store.GetSessionReviewComment(ctx, db.GetSessionReviewCommentParams{
		ID:        req.CommentId,
		SessionID: acc.Session.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	// Author, collaborator, shared user, or owner may resolve a review comment.
	if row.UserID != u.ID && !authz.CanSession(acc.Role, authz.SessionPrompt) {
		return nil, authz.ErrForbidden
	}
	if !row.ResolvedAt.Valid {
		now := a.Now().Unix()
		if _, err := a.Store.ResolveSessionReviewComment(ctx, db.ResolveSessionReviewCommentParams{
			ResolvedAt: sql.NullInt64{Int64: now, Valid: true},
			ID:         row.ID,
			SessionID:  acc.Session.ID,
		}); err != nil {
			return nil, err
		}
		row.ResolvedAt = sql.NullInt64{Int64: now, Valid: true}
		a.broadcastReviewComments(ctx, acc.Session.ID)
	}
	return gen.ResolveSessionReviewComment200JSONResponse(apiReviewComment(row)), nil
}

// DeleteSessionReviewComment implements gen.StrictServerInterface.
func (a *API) DeleteSessionReviewComment(ctx context.Context, req gen.DeleteSessionReviewCommentRequestObject) (gen.DeleteSessionReviewCommentResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Session(ctx, u.ID, req.SessionId, authz.SessionRead)
	if err != nil {
		return nil, err
	}
	row, err := a.Store.GetSessionReviewComment(ctx, db.GetSessionReviewCommentParams{
		ID:        req.CommentId,
		SessionID: acc.Session.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.UserID != u.ID && !authz.CanSession(acc.Role, authz.SessionPrompt) {
		return nil, authz.ErrForbidden
	}
	if _, err := a.Store.DeleteSessionReviewComment(ctx, db.DeleteSessionReviewCommentParams{
		ID:        row.ID,
		SessionID: acc.Session.ID,
	}); err != nil {
		return nil, err
	}
	a.broadcastReviewComments(ctx, acc.Session.ID)
	return gen.DeleteSessionReviewComment204Response{}, nil
}

// --- @teammate Handoff Notices ---

func (a *API) sessionCollaborators(ctx context.Context, sess db.AgentSession, ag db.Agent) []db.User {
	seen := map[string]bool{}
	var out []db.User
	add := func(u db.User) {
		if u.ID != "" && !seen[u.ID] {
			seen[u.ID] = true
			out = append(out, u)
		}
	}
	if owner, err := a.Store.GetUser(ctx, ag.OwnerID); err == nil {
		add(owner)
	}
	if agShares, err := a.Store.ListAgentShares(ctx, ag.ID); err == nil {
		for _, sh := range agShares {
			add(sh.User)
		}
	}
	if sessShares, err := a.Store.ListSessionShares(ctx, sess.ID); err == nil {
		for _, sh := range sessShares {
			add(sh.User)
		}
	}
	return out
}

func matchTeammate(users []db.User, query string) (db.User, bool) {
	q := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	if q == "" {
		return db.User{}, false
	}
	lower := strings.ToLower(q)
	for _, u := range users {
		if strings.EqualFold(u.Email, lower) || strings.EqualFold(u.Name, lower) {
			return u, true
		}
	}
	// Also match unique first name or email local-part for convenient @bob mentions.
	var matches []db.User
	for _, u := range users {
		local, _, _ := strings.Cut(strings.ToLower(u.Email), "@")
		first := strings.ToLower(strings.Fields(u.Name)[0])
		if lower == local || (first != "" && lower == first) {
			matches = append(matches, u)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return db.User{}, false
}

// HandoffSession implements gen.StrictServerInterface.
func (a *API) HandoffSession(ctx context.Context, req gen.HandoffSessionRequestObject) (gen.HandoffSessionResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Session(ctx, u.ID, req.SessionId, authz.SessionPrompt)
	if err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Target) == "" {
		return nil, badRequest("target is required")
	}
	note := strings.TrimSpace(deref(req.Body.Note))
	if len(note) > 1000 {
		return nil, badRequest("note must be at most 1000 characters")
	}
	teammates := a.sessionCollaborators(ctx, acc.Session, acc.AgentRow.Agent)
	target, ok := matchTeammate(teammates, req.Body.Target)
	if !ok {
		return nil, errStatus(http.StatusNotFound, "no collaborator with access to this thread matches "+strings.TrimSpace(req.Body.Target))
	}
	msg := fmt.Sprintf("%s handed off to @%s", u.Name, target.Name)
	if note != "" {
		msg += ": " + note
	}
	if err := a.Sessions.RecordNotice(ctx, acc.Session.ID, u.ID, map[string]any{
		"kind":    "handoff",
		"message": msg,
		"from":    map[string]string{"id": u.ID, "name": u.Name},
		"to":      map[string]string{"id": target.ID, "name": target.Name, "email": target.Email},
		"note":    note,
	}); err != nil {
		return nil, err
	}
	a.record(ctx, acc.AgentRow.Agent, audit.SessionHandoff, acc.Session.ID, map[string]any{
		"toUserId": target.ID, "toEmail": target.Email, "note": note,
	})
	return gen.HandoffSession200JSONResponse{
		To:      apiUser(target),
		Message: msg,
	}, nil
}

var mentionRe = regexp.MustCompile(`(?:^|[\s(])@([A-Za-z0-9._%+\-]+(?:@[A-Za-z0-9.\-]+\.[A-Za-z]{2,})?)`)

// recordMentionHandoffs scans a prompt for @teammate mentions matching a
// collaborator with access to the thread and appends a clean handoff notice.
func (a *API) recordMentionHandoffs(ctx context.Context, sess db.AgentSession, actor db.User, text string) {
	if !strings.Contains(text, "@") {
		return
	}
	matches := mentionRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return
	}
	ag, err := a.Store.GetAgent(ctx, sess.AgentID)
	if err != nil {
		return
	}
	teammates := a.sessionCollaborators(ctx, sess, ag)
	notified := map[string]bool{actor.ID: true}
	for _, m := range matches {
		token := strings.TrimRight(m[1], ".,;:!?")
		target, ok := matchTeammate(teammates, token)
		if !ok || notified[target.ID] {
			continue
		}
		notified[target.ID] = true
		msg := fmt.Sprintf("%s handed off to @%s", actor.Name, target.Name)
		_ = a.Sessions.RecordNotice(ctx, sess.ID, actor.ID, map[string]any{
			"kind":    "handoff",
			"message": msg,
			"from":    map[string]string{"id": actor.ID, "name": actor.Name},
			"to":      map[string]string{"id": target.ID, "name": target.Name, "email": target.Email},
		})
	}
}
