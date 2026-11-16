package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	spec "github.com/gempir/agenthold/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/trigger"
)

const maxWebhookBody = 256 * 1024

func tsPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := ts(v.Int64)
	return &t
}

func apiToken(t db.ApiToken) gen.APIToken {
	return gen.APIToken{Id: t.ID, Name: t.Name, Prefix: t.Prefix, CreatedAt: ts(t.CreatedAt), LastUsedAt: tsPtr(t.LastUsedAt), ExpiresAt: tsPtr(t.ExpiresAt)}
}

// ListTokens implements gen.StrictServerInterface.
func (a *API) ListTokens(ctx context.Context, _ gen.ListTokensRequestObject) (gen.ListTokensResponseObject, error) {
	rows, err := a.Store.ListAPITokens(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListTokens200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiToken(r))
	}
	return out, nil
}

// CreateToken implements gen.StrictServerInterface.
func (a *API) CreateToken(ctx context.Context, req gen.CreateTokenRequestObject) (gen.CreateTokenResponseObject, error) {
	if auth.ViaToken(ctx) {
		// A token must not be able to outlive its own revocation.
		return nil, errStatus(http.StatusForbidden, "tokens can only be created when signed in, not with another token")
	}
	name := strings.TrimSpace(req.Body.Name)
	if name == "" || len(name) > 100 {
		return nil, badRequest("name must be 1 to 100 characters")
	}
	var exp *time.Time
	if d := req.Body.ExpiresInDays; d != nil {
		if *d < 1 || *d > 3650 {
			return nil, badRequest("expiresInDays must be between 1 and 3650")
		}
		t := a.Now().Add(time.Duration(*d) * 24 * time.Hour)
		exp = &t
	}
	t, token, err := a.Auth.CreateToken(ctx, me(ctx).ID, name, exp)
	if err != nil {
		return nil, err
	}
	g := apiToken(t)
	return gen.CreateToken201JSONResponse{Id: g.Id, Name: g.Name, Prefix: g.Prefix, CreatedAt: g.CreatedAt, ExpiresAt: g.ExpiresAt, Token: token}, nil
}

// DeleteToken implements gen.StrictServerInterface.
func (a *API) DeleteToken(ctx context.Context, req gen.DeleteTokenRequestObject) (gen.DeleteTokenResponseObject, error) {
	n, err := a.Store.DeleteAPIToken(ctx, db.DeleteAPITokenParams{ID: req.TokenId, UserID: me(ctx).ID})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	return gen.DeleteToken204Response{}, nil
}

func (a *API) userOf(ctx context.Context, userID string) gen.User {
	if u, err := a.Store.GetUser(ctx, userID); err == nil {
		return apiUser(u)
	}
	return gen.User{Id: userID, Name: "(deleted user)"}
}

func (a *API) apiSchedule(ctx context.Context, s db.Schedule) gen.Schedule {
	return gen.Schedule{
		Id: s.ID, AgentId: s.AgentID, SessionId: s.SessionID, Name: s.Name, Cron: s.Cron, Timezone: s.Timezone,
		Prompt: s.Prompt, Enabled: s.Enabled == 1, CreatedBy: a.userOf(ctx, s.CreatedBy), NextRunAt: ts(s.NextRunAt),
		LastRunAt: tsPtr(s.LastRunAt), LastError: s.LastError, CreatedAt: ts(s.CreatedAt),
	}
}

type scheduleFields struct {
	name, cron, tz, prompt string
	enabled                bool
	next                   time.Time
}

func (a *API) validSchedule(in gen.ScheduleInput) (scheduleFields, error) {
	f := scheduleFields{name: strings.TrimSpace(in.Name), cron: strings.TrimSpace(in.Cron), tz: "UTC", prompt: strings.TrimSpace(in.Prompt), enabled: true}
	if in.Timezone != nil && *in.Timezone != "" {
		f.tz = *in.Timezone
	}
	if in.Enabled != nil {
		f.enabled = *in.Enabled
	}
	if f.name == "" || len(f.name) > 100 {
		return f, badRequest("name must be 1 to 100 characters")
	}
	if f.prompt == "" {
		return f, badRequest("prompt is required")
	}
	next, err := trigger.Next(f.cron, f.tz, a.Now())
	if err != nil {
		return f, badRequest(err.Error())
	}
	f.next = next
	return f, nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ListSchedules implements gen.StrictServerInterface.
func (a *API) ListSchedules(ctx context.Context, req gen.ListSchedulesRequestObject) (gen.ListSchedulesResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAgentSchedules(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.ListSchedules200JSONResponse{}
	for _, r := range rows {
		out = append(out, a.apiSchedule(ctx, r))
	}
	return out, nil
}

// CreateSchedule implements gen.StrictServerInterface.
func (a *API) CreateSchedule(ctx context.Context, req gen.CreateScheduleRequestObject) (gen.CreateScheduleResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	if acc.Row.Agent.IsTemplate == 1 {
		return nil, sandbox.ErrTemplate
	}
	f, err := a.validSchedule(*req.Body)
	if err != nil {
		return nil, err
	}
	sess, err := a.Sessions.Create(ctx, req.AgentId, me(ctx).ID, "Schedule: "+f.name)
	if err != nil {
		return nil, err
	}
	now := a.Now().Unix()
	s := db.Schedule{
		ID: id.New("sch"), AgentID: req.AgentId, SessionID: sess.ID, Name: f.name, Cron: f.cron, Timezone: f.tz,
		Prompt: f.prompt, Enabled: b2i(f.enabled), CreatedBy: me(ctx).ID, CreatedAt: now, UpdatedAt: now, NextRunAt: f.next.Unix(),
	}
	if err := a.Store.CreateSchedule(ctx, db.CreateScheduleParams{
		ID: s.ID, AgentID: s.AgentID, SessionID: s.SessionID, Name: s.Name, Cron: s.Cron, Timezone: s.Timezone,
		Prompt: s.Prompt, Enabled: s.Enabled, CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, NextRunAt: s.NextRunAt,
	}); err != nil {
		return nil, err
	}
	return gen.CreateSchedule201JSONResponse(a.apiSchedule(ctx, s)), nil
}

func (a *API) loadSchedule(ctx context.Context, scheduleID string, action authz.Action) (db.Schedule, error) {
	s, err := a.Store.GetSchedule(ctx, scheduleID)
	if errors.Is(err, sql.ErrNoRows) {
		return s, errNotFound
	}
	if err != nil {
		return s, err
	}
	_, err = a.authz.Agent(ctx, me(ctx).ID, s.AgentID, action)
	return s, err
}

// UpdateSchedule implements gen.StrictServerInterface.
func (a *API) UpdateSchedule(ctx context.Context, req gen.UpdateScheduleRequestObject) (gen.UpdateScheduleResponseObject, error) {
	s, err := a.loadSchedule(ctx, req.ScheduleId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	f, err := a.validSchedule(*req.Body)
	if err != nil {
		return nil, err
	}
	if f.enabled {
		if err := a.notTemplate(ctx, s.AgentID); err != nil {
			return nil, err
		}
	}
	s.Name, s.Cron, s.Timezone, s.Prompt, s.Enabled, s.NextRunAt, s.UpdatedAt = f.name, f.cron, f.tz, f.prompt, b2i(f.enabled), f.next.Unix(), a.Now().Unix()
	if err := a.Store.UpdateSchedule(ctx, db.UpdateScheduleParams{
		Name: s.Name, Cron: s.Cron, Timezone: s.Timezone, Prompt: s.Prompt, Enabled: s.Enabled, NextRunAt: s.NextRunAt, UpdatedAt: s.UpdatedAt, ID: s.ID,
	}); err != nil {
		return nil, err
	}
	return gen.UpdateSchedule200JSONResponse(a.apiSchedule(ctx, s)), nil
}

// DeleteSchedule implements gen.StrictServerInterface.
func (a *API) DeleteSchedule(ctx context.Context, req gen.DeleteScheduleRequestObject) (gen.DeleteScheduleResponseObject, error) {
	s, err := a.loadSchedule(ctx, req.ScheduleId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	if err := a.Store.DeleteSchedule(ctx, s.ID); err != nil {
		return nil, err
	}
	return gen.DeleteSchedule204Response{}, nil
}

// RunSchedule implements gen.StrictServerInterface.
func (a *API) RunSchedule(ctx context.Context, req gen.RunScheduleRequestObject) (gen.RunScheduleResponseObject, error) {
	s, err := a.loadSchedule(ctx, req.ScheduleId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	if err := a.notTemplate(ctx, s.AgentID); err != nil {
		return nil, err
	}
	if err := a.Triggers.Fire(ctx, s.AgentID, s.SessionID, s.CreatedBy, "schedule: "+s.Name, s.Prompt, map[string]any{"schedule": s.Name, "runBy": me(ctx).ID}); err != nil {
		return nil, triggerErr(err)
	}
	return gen.RunSchedule202Response{}, nil
}

func triggerErr(err error) error {
	switch {
	case errors.Is(err, trigger.ErrBusy):
		return errStatus(http.StatusConflict, err.Error())
	case errors.Is(err, trigger.ErrNoAccess):
		return errStatus(http.StatusForbidden, err.Error())
	}
	return err
}

func (a *API) apiWebhook(ctx context.Context, w db.Webhook) gen.Webhook {
	return gen.Webhook{
		Id: w.ID, AgentId: w.AgentID, SessionId: w.SessionID, Name: w.Name, PromptTemplate: w.PromptTemplate,
		Signed: len(w.SigningSecret) > 0, SignatureHeader: w.SignatureHeader, Enabled: w.Enabled == 1,
		CreatedBy: a.userOf(ctx, w.CreatedBy), LastDeliveryAt: tsPtr(w.LastDeliveryAt), LastError: w.LastError, CreatedAt: ts(w.CreatedAt),
	}
}

// applyWebhookInput validates input into w (the signing secret is encrypted).
func (a *API) applyWebhookInput(w *db.Webhook, in gen.WebhookInput) error {
	w.Name, w.PromptTemplate = strings.TrimSpace(in.Name), in.PromptTemplate
	if w.Name == "" || len(w.Name) > 100 {
		return badRequest("name must be 1 to 100 characters")
	}
	if strings.TrimSpace(w.PromptTemplate) == "" {
		return badRequest("promptTemplate is required")
	}
	if in.Enabled != nil {
		w.Enabled = b2i(*in.Enabled)
	}
	if in.SignatureHeader != nil {
		w.SignatureHeader = strings.TrimSpace(*in.SignatureHeader)
	}
	if w.SignatureHeader == "" {
		w.SignatureHeader = "X-Hub-Signature-256"
	}
	if in.SigningSecret != nil {
		if *in.SigningSecret == "" {
			w.SigningSecret = nil
		} else {
			ct, err := a.Box.Seal([]byte(*in.SigningSecret), []byte(w.ID))
			if err != nil {
				return err
			}
			w.SigningSecret = ct
		}
	}
	return nil
}

// ListWebhooks implements gen.StrictServerInterface.
func (a *API) ListWebhooks(ctx context.Context, req gen.ListWebhooksRequestObject) (gen.ListWebhooksResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAgentWebhooks(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.ListWebhooks200JSONResponse{}
	for _, r := range rows {
		out = append(out, a.apiWebhook(ctx, r))
	}
	return out, nil
}

// CreateWebhook implements gen.StrictServerInterface.
func (a *API) CreateWebhook(ctx context.Context, req gen.CreateWebhookRequestObject) (gen.CreateWebhookResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentConfigure)
	if err != nil {
		return nil, err
	}
	if acc.Row.Agent.IsTemplate == 1 {
		return nil, sandbox.ErrTemplate
	}
	now := a.Now().Unix()
	token := id.Random(24)
	w := db.Webhook{ID: id.New("whk"), AgentID: req.AgentId, TokenHash: auth.HashToken(token), Enabled: 1, CreatedBy: me(ctx).ID, CreatedAt: now, UpdatedAt: now}
	if err := a.applyWebhookInput(&w, *req.Body); err != nil {
		return nil, err
	}
	sess, err := a.Sessions.Create(ctx, req.AgentId, me(ctx).ID, "Webhook: "+w.Name)
	if err != nil {
		return nil, err
	}
	w.SessionID = sess.ID
	if err := a.Store.CreateWebhook(ctx, db.CreateWebhookParams{
		ID: w.ID, AgentID: w.AgentID, SessionID: w.SessionID, Name: w.Name, TokenHash: w.TokenHash, PromptTemplate: w.PromptTemplate,
		SigningSecret: w.SigningSecret, SignatureHeader: w.SignatureHeader, Enabled: w.Enabled, CreatedBy: w.CreatedBy, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}); err != nil {
		return nil, err
	}
	g := a.apiWebhook(ctx, w)
	return gen.CreateWebhook201JSONResponse{
		Id: g.Id, AgentId: g.AgentId, SessionId: g.SessionId, Name: g.Name, PromptTemplate: g.PromptTemplate, Signed: g.Signed,
		SignatureHeader: g.SignatureHeader, Enabled: g.Enabled, CreatedBy: g.CreatedBy, LastError: g.LastError, CreatedAt: g.CreatedAt,
		Url: a.PublicURL + "/api/v1/hooks/" + token,
	}, nil
}

func (a *API) loadWebhook(ctx context.Context, webhookID string) (db.Webhook, error) {
	w, err := a.Store.GetWebhook(ctx, webhookID)
	if errors.Is(err, sql.ErrNoRows) {
		return w, errNotFound
	}
	if err != nil {
		return w, err
	}
	_, err = a.authz.Agent(ctx, me(ctx).ID, w.AgentID, authz.AgentConfigure)
	return w, err
}

// UpdateWebhook implements gen.StrictServerInterface.
func (a *API) UpdateWebhook(ctx context.Context, req gen.UpdateWebhookRequestObject) (gen.UpdateWebhookResponseObject, error) {
	w, err := a.loadWebhook(ctx, req.WebhookId)
	if err != nil {
		return nil, err
	}
	if err := a.applyWebhookInput(&w, *req.Body); err != nil {
		return nil, err
	}
	if w.Enabled == 1 {
		if err := a.notTemplate(ctx, w.AgentID); err != nil {
			return nil, err
		}
	}
	w.UpdatedAt = a.Now().Unix()
	if err := a.Store.UpdateWebhook(ctx, db.UpdateWebhookParams{
		Name: w.Name, PromptTemplate: w.PromptTemplate, SigningSecret: w.SigningSecret, SignatureHeader: w.SignatureHeader, Enabled: w.Enabled, UpdatedAt: w.UpdatedAt, ID: w.ID,
	}); err != nil {
		return nil, err
	}
	return gen.UpdateWebhook200JSONResponse(a.apiWebhook(ctx, w)), nil
}

// DeleteWebhook implements gen.StrictServerInterface.
func (a *API) DeleteWebhook(ctx context.Context, req gen.DeleteWebhookRequestObject) (gen.DeleteWebhookResponseObject, error) {
	w, err := a.loadWebhook(ctx, req.WebhookId)
	if err != nil {
		return nil, err
	}
	if err := a.Store.DeleteWebhook(ctx, w.ID); err != nil {
		return nil, err
	}
	return gen.DeleteWebhook204Response{}, nil
}

// deliver handles POST /api/v1/hooks/{token}.
func (a *API) deliver(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hook, err := a.Store.GetWebhookByToken(ctx, auth.HashToken(chi.URLParam(r, "token")))
	if err != nil || hook.Enabled != 1 {
		writeError(w, r, errNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil {
		writeError(w, r, badRequest("could not read the body"))
		return
	}
	if len(body) > maxWebhookBody {
		writeError(w, r, errStatus(http.StatusRequestEntityTooLarge, "the body is larger than 256 KiB"))
		return
	}
	record := func(msg string) {
		_ = a.Store.RecordWebhookDelivery(ctx, db.RecordWebhookDeliveryParams{LastDeliveryAt: sql.NullInt64{Int64: a.Now().Unix(), Valid: true}, LastError: msg, ID: hook.ID})
	}
	if len(hook.SigningSecret) > 0 {
		key, err := a.Box.Open(hook.SigningSecret, []byte(hook.ID))
		if err != nil || !trigger.VerifySignature(key, body, r.Header.Get(hook.SignatureHeader)) {
			record("rejected: invalid signature")
			writeError(w, r, errStatus(http.StatusUnauthorized, "invalid signature"))
			return
		}
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "unknown content type"
	}
	text := trigger.RenderWebhookPrompt(hook.PromptTemplate, ct, body)
	if err := a.Triggers.Fire(ctx, hook.AgentID, hook.SessionID, hook.CreatedBy, "webhook: "+hook.Name, text, map[string]any{"webhook": hook.Name}); err != nil {
		record(err.Error())
		writeError(w, r, triggerErr(err))
		return
	}
	record("")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "session": hook.SessionID})
}

// openAPI serves the API description.
func openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(spec.OpenAPI)
}

// apiDocs is a reference page for the API, rendered from the OpenAPI document.
func apiDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net 'unsafe-inline'; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net https://fonts.googleapis.com; font-src https://fonts.gstatic.com https://cdn.jsdelivr.net data:; img-src 'self' data: https:; connect-src 'self'; frame-ancestors 'none'")
	_, _ = io.WriteString(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>agenthold API reference</title>
</head>
<body>
<script id="api-reference" data-url="/api/v1/openapi.yaml"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.25.0/dist/browser/standalone.js"></script>
</body>
</html>
`)
}

// notTemplate refuses what would make a template run: it is never prompted.
func (a *API) notTemplate(ctx context.Context, agentID string) error {
	ag, err := a.Store.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if ag.IsTemplate == 1 {
		return sandbox.ErrTemplate
	}
	return nil
}
