package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/executor"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/logins"
	"github.com/gempir/agenthold/internal/platform"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tools"
)

func apiOrgTool(row db.OrgTool, prefMap map[string]bool, execOverview *executor.Overview) gen.OrgTool {
	d := tools.DecodeOrgTool(row)
	userEnabled := row.EnabledByDefault != 0
	if pref, ok := prefMap[row.ID]; ok {
		userEnabled = pref
	}
	connected := false
	if d.Kind == tools.OrgToolKindExecutorApp && d.AuthMode == tools.OrgToolAuthUserOAuth && execOverview != nil {
		connected = platform.ExecutorOrgToolConnected(d, *execOverview)
	}
	cfg := gen.OrgToolConfig{}
	if d.Config.URL != "" {
		u := d.Config.URL
		cfg.Url = &u
	}
	if len(d.Config.Headers) > 0 {
		h := d.Config.Headers
		cfg.Headers = &h
	}
	if d.Config.Command != "" {
		c := d.Config.Command
		cfg.Command = &c
	}
	if len(d.Config.Args) > 0 {
		a := d.Config.Args
		cfg.Args = &a
	}
	if len(d.Config.Env) > 0 {
		e := d.Config.Env
		cfg.Env = &e
	}
	if d.Config.ExecutorCatalogID != "" {
		cid := d.Config.ExecutorCatalogID
		cfg.ExecutorCatalogId = &cid
	}
	hosts := d.SecretHosts
	if hosts == nil {
		hosts = []string{}
	}
	roles := d.AllowedRoles
	if roles == nil {
		roles = []string{}
	}
	out := gen.OrgTool{
		Id:               row.ID,
		Slug:             row.Slug,
		Name:             row.Name,
		Description:      row.Description,
		Kind:             gen.OrgToolKind(row.Kind),
		AuthMode:         gen.OrgToolAuthMode(row.AuthMode),
		Config:           cfg,
		HasSecret:        len(row.EncryptedSecret) > 0,
		SecretHosts:      hosts,
		EnabledByDefault: row.EnabledByDefault != 0,
		UserEnabled:      userEnabled,
		Connected:        connected,
		AllowedRoles:     roles,
		CreatedAt:        ts(row.CreatedAt),
		UpdatedAt:        ts(row.UpdatedAt),
	}
	if row.CreatedBy.Valid && row.CreatedBy.String != "" {
		cb := row.CreatedBy.String
		out.CreatedBy = &cb
	}
	return out
}

func (a *API) parseOrgToolInput(in gen.OrgToolInput) (db.OrgTool, string, bool, error) {
	slug := strings.TrimSpace(in.Slug)
	if !tools.OrgToolSlug.MatchString(slug) {
		return db.OrgTool{}, "", false, badRequest("slug must be lowercase letters, digits and dashes (up to 64)")
	}
	name, err := validName(in.Name)
	if err != nil {
		return db.OrgTool{}, "", false, err
	}
	desc := ""
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
		if len(desc) > 1024 {
			return db.OrgTool{}, "", false, badRequest("description must be at most 1024 characters")
		}
	}
	kind := string(in.Kind)
	switch kind {
	case tools.OrgToolKindMCPHTTP, tools.OrgToolKindMCPStdio, tools.OrgToolKindExecutorApp:
	default:
		return db.OrgTool{}, "", false, badRequest("kind must be mcp_http, mcp_stdio or executor_app")
	}
	authMode := tools.OrgToolAuthNone
	if in.AuthMode != nil && *in.AuthMode != "" {
		authMode = string(*in.AuthMode)
	}
	switch authMode {
	case tools.OrgToolAuthNone, tools.OrgToolAuthOrgShared, tools.OrgToolAuthUserOAuth:
	default:
		return db.OrgTool{}, "", false, badRequest("authMode must be none, org_shared or user_oauth")
	}

	var cfg tools.OrgToolConfig
	if in.Config != nil {
		if in.Config.Url != nil {
			cfg.URL = strings.TrimSpace(*in.Config.Url)
		}
		if in.Config.Headers != nil {
			cfg.Headers = map[string]string{}
			for k, v := range *in.Config.Headers {
				if k == "" || strings.ContainsAny(k, " :\r\n") {
					return db.OrgTool{}, "", false, badRequest("invalid header name")
				}
				cfg.Headers[k] = v
			}
		}
		if in.Config.Command != nil {
			cfg.Command = strings.TrimSpace(*in.Config.Command)
		}
		if in.Config.Args != nil {
			cfg.Args = append([]string{}, (*in.Config.Args)...)
		}
		if in.Config.Env != nil {
			cfg.Env = map[string]string{}
			for k, v := range *in.Config.Env {
				cfg.Env[k] = v
			}
		}
		if in.Config.ExecutorCatalogId != nil {
			cfg.ExecutorCatalogID = strings.TrimSpace(*in.Config.ExecutorCatalogId)
		}
	}

	switch kind {
	case tools.OrgToolKindMCPHTTP:
		u, err := url.Parse(cfg.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return db.OrgTool{}, "", false, badRequest("config.url must be an http(s) URL for mcp_http tools")
		}
	case tools.OrgToolKindMCPStdio:
		if cfg.Command == "" {
			return db.OrgTool{}, "", false, badRequest("config.command is required for mcp_stdio tools")
		}
	case tools.OrgToolKindExecutorApp:
		if cfg.ExecutorCatalogID == "" {
			return db.OrgTool{}, "", false, badRequest("config.executorCatalogId is required for executor_app tools")
		}
	}

	hosts := []string{}
	if in.SecretHosts != nil {
		for _, h := range *in.SecretHosts {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "" {
				continue
			}
			r := secrets.Rule{Hosts: []string{h}, Header: "Authorization"}
			if err := r.Validate(); err != nil {
				return db.OrgTool{}, "", false, badRequest(err.Error())
			}
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 && authMode == tools.OrgToolAuthOrgShared && kind == tools.OrgToolKindMCPHTTP && cfg.URL != "" {
		if u, err := url.Parse(cfg.URL); err == nil && u.Hostname() != "" {
			hosts = []string{strings.ToLower(u.Hostname())}
		}
	}

	roles := []string{string(config.RoleAdmin), string(config.RoleUser)}
	if in.AllowedRoles != nil && len(*in.AllowedRoles) > 0 {
		roles = make([]string, 0, len(*in.AllowedRoles))
		for _, r := range *in.AllowedRoles {
			parsed, ok := config.ParseRole(r)
			if !ok || parsed == config.RoleDisabled {
				return db.OrgTool{}, "", false, badRequest("allowedRoles must contain admin and/or user")
			}
			roles = append(roles, string(parsed))
		}
	}

	enabledByDefault := int64(1)
	if in.EnabledByDefault != nil && !*in.EnabledByDefault {
		enabledByDefault = 0
	}

	rawSecret := ""
	if in.Secret != nil {
		rawSecret = *in.Secret
		if rawSecret != "" {
			if err := validSecretValue(rawSecret); err != nil {
				return db.OrgTool{}, "", false, err
			}
		}
	}
	clearSecret := in.ClearSecret != nil && *in.ClearSecret

	cfgJSON, _ := json.Marshal(cfg)
	hostsJSON, _ := json.Marshal(hosts)
	rolesJSON, _ := json.Marshal(roles)

	row := db.OrgTool{
		Slug:             slug,
		Name:             name,
		Description:      desc,
		Kind:             kind,
		AuthMode:         authMode,
		ConfigJson:       string(cfgJSON),
		SecretHostsJson:  string(hostsJSON),
		EnabledByDefault: enabledByDefault,
		AllowedRolesJson: string(rolesJSON),
	}
	return row, rawSecret, clearSecret, nil
}

func (a *API) userToolPrefMap(ctx context.Context, userID string) (map[string]bool, error) {
	prefs, err := a.Store.ListUserToolPreferences(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(prefs))
	for _, p := range prefs {
		out[p.OrgToolID] = p.Enabled != 0
	}
	return out, nil
}

func (a *API) runningExecutorOverview(ctx context.Context, userID string) *executor.Overview {
	if a.Executor == nil || !a.Executor.Status(userID).Running {
		return nil
	}
	ov, err := a.Executor.Overview(ctx, userID)
	if err != nil {
		return nil
	}
	return &ov
}

// ListOrgTools implements gen.StrictServerInterface.
func (a *API) ListOrgTools(ctx context.Context, _ gen.ListOrgToolsRequestObject) (gen.ListOrgToolsResponseObject, error) {
	u := me(ctx)
	role := auth.Role(*u)
	rows, err := a.Store.ListOrgTools(ctx)
	if err != nil {
		return nil, err
	}
	prefs, err := a.userToolPrefMap(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	ov := a.runningExecutorOverview(ctx, u.ID)
	out := gen.ListOrgTools200JSONResponse{}
	for _, r := range rows {
		d := tools.DecodeOrgTool(r)
		if !tools.RoleAllowed(d.AllowedRoles, role) {
			continue
		}
		out = append(out, apiOrgTool(r, prefs, ov))
	}
	return out, nil
}

// CreateOrgTool implements gen.StrictServerInterface.
func (a *API) CreateOrgTool(ctx context.Context, req gen.CreateOrgToolRequestObject) (gen.CreateOrgToolResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	row, rawSecret, _, err := a.parseOrgToolInput(*req.Body)
	if err != nil {
		return nil, err
	}
	u := me(ctx)
	now := a.Now().Unix()
	row.ID = id.New("ot")
	row.CreatedBy = sql.NullString{String: u.ID, Valid: true}
	row.CreatedAt = now
	row.UpdatedAt = now
	if rawSecret != "" {
		enc, err := a.Box.Seal([]byte(rawSecret), tools.OrgToolAAD(row.ID))
		if err != nil {
			return nil, err
		}
		row.EncryptedSecret = enc
	}
	if err := a.Store.CreateOrgTool(ctx, db.CreateOrgToolParams(row)); err != nil {
		if store.IsUniqueViolation(err) {
			return nil, errStatus(http.StatusConflict, "an organization tool with this slug already exists")
		}
		return nil, err
	}
	if a.Egress != nil {
		a.Egress.Invalidate()
	}
	a.Audit.Record(ctx, audit.Entry{
		UserID: u.ID, Action: audit.OrgToolCreate, Target: row.ID,
		Details: map[string]any{"slug": row.Slug, "kind": row.Kind, "authMode": row.AuthMode},
	})
	return gen.CreateOrgTool201JSONResponse(apiOrgTool(row, nil, a.runningExecutorOverview(ctx, u.ID))), nil
}

// UpdateOrgTool implements gen.StrictServerInterface.
func (a *API) UpdateOrgTool(ctx context.Context, req gen.UpdateOrgToolRequestObject) (gen.UpdateOrgToolResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	cur, err := a.Store.GetOrgTool(ctx, req.ToolId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	row, rawSecret, clearSecret, err := a.parseOrgToolInput(*req.Body)
	if err != nil {
		return nil, err
	}
	row.ID = cur.ID
	row.CreatedBy = cur.CreatedBy
	row.CreatedAt = cur.CreatedAt
	row.UpdatedAt = a.Now().Unix()
	switch {
	case rawSecret != "":
		enc, err := a.Box.Seal([]byte(rawSecret), tools.OrgToolAAD(row.ID))
		if err != nil {
			return nil, err
		}
		row.EncryptedSecret = enc
	case clearSecret:
		row.EncryptedSecret = nil
	default:
		row.EncryptedSecret = cur.EncryptedSecret
	}
	if err := a.Store.UpdateOrgTool(ctx, db.UpdateOrgToolParams{
		Slug:             row.Slug,
		Name:             row.Name,
		Description:      row.Description,
		Kind:             row.Kind,
		AuthMode:         row.AuthMode,
		ConfigJson:       row.ConfigJson,
		EncryptedSecret:  row.EncryptedSecret,
		SecretHostsJson:  row.SecretHostsJson,
		EnabledByDefault: row.EnabledByDefault,
		AllowedRolesJson: row.AllowedRolesJson,
		UpdatedAt:        row.UpdatedAt,
		ID:               row.ID,
	}); err != nil {
		if store.IsUniqueViolation(err) {
			return nil, errStatus(http.StatusConflict, "an organization tool with this slug already exists")
		}
		return nil, err
	}
	if a.Egress != nil {
		a.Egress.Invalidate()
	}
	u := me(ctx)
	a.Audit.Record(ctx, audit.Entry{
		UserID: u.ID, Action: audit.OrgToolUpdate, Target: row.ID,
		Details: map[string]any{"slug": row.Slug, "kind": row.Kind, "authMode": row.AuthMode},
	})
	prefs, _ := a.userToolPrefMap(ctx, u.ID)
	return gen.UpdateOrgTool200JSONResponse(apiOrgTool(row, prefs, a.runningExecutorOverview(ctx, u.ID))), nil
}

// DeleteOrgTool implements gen.StrictServerInterface.
func (a *API) DeleteOrgTool(ctx context.Context, req gen.DeleteOrgToolRequestObject) (gen.DeleteOrgToolResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	n, err := a.Store.DeleteOrgTool(ctx, req.ToolId)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	if a.Egress != nil {
		a.Egress.Invalidate()
	}
	a.Audit.Record(ctx, audit.Entry{UserID: me(ctx).ID, Action: audit.OrgToolDelete, Target: req.ToolId})
	return gen.DeleteOrgTool204Response{}, nil
}

// SetUserOrgToolPreference implements gen.StrictServerInterface.
func (a *API) SetUserOrgToolPreference(ctx context.Context, req gen.SetUserOrgToolPreferenceRequestObject) (gen.SetUserOrgToolPreferenceResponseObject, error) {
	u := me(ctx)
	row, err := a.Store.GetOrgTool(ctx, req.ToolId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	d := tools.DecodeOrgTool(row)
	if !tools.RoleAllowed(d.AllowedRoles, auth.Role(*u)) {
		return nil, errNotFound
	}
	if err := a.Store.UpsertUserToolPreference(ctx, db.UpsertUserToolPreferenceParams{
		UserID:    u.ID,
		OrgToolID: row.ID,
		Enabled:   b2i(req.Body.Enabled),
	}); err != nil {
		return nil, err
	}
	if a.Egress != nil {
		a.Egress.Invalidate()
	}
	prefs := map[string]bool{row.ID: req.Body.Enabled}
	return gen.SetUserOrgToolPreference200JSONResponse(apiOrgTool(row, prefs, a.runningExecutorOverview(ctx, u.ID))), nil
}

// ConnectOrgTool implements gen.StrictServerInterface.
// For an executor_app org tool with user_oauth, transparently installs the
// catalog app into the user's Executor instance (starting Executor lazily if
// needed) and starts/returns an OAuth/connection session in one step.
func (a *API) ConnectOrgTool(ctx context.Context, req gen.ConnectOrgToolRequestObject) (gen.ConnectOrgToolResponseObject, error) {
	u := me(ctx)
	row, err := a.Store.GetOrgTool(ctx, req.ToolId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	d := tools.DecodeOrgTool(row)
	if !tools.RoleAllowed(d.AllowedRoles, auth.Role(*u)) {
		return nil, errNotFound
	}
	if d.Kind != tools.OrgToolKindExecutorApp || d.AuthMode != tools.OrgToolAuthUserOAuth {
		return nil, badRequest("only executor_app organization tools with user_oauth support one-step connect")
	}
	catID := strings.TrimSpace(d.Config.ExecutorCatalogID)
	if catID == "" {
		return nil, badRequest("organization tool has no executorCatalogId configured")
	}
	mgr, err := a.requireExecutor()
	if err != nil {
		return nil, err
	}

	// Ensure the tool is enabled for this user when they connect it.
	_ = a.Store.UpsertUserToolPreference(ctx, db.UpsertUserToolPreferenceParams{
		UserID: u.ID, OrgToolID: row.ID, Enabled: 1,
	})

	ov, err := mgr.Overview(ctx, u.ID)
	if err != nil {
		return nil, executorError(err)
	}
	findApp := func(apps []executor.App) *executor.App {
		for i := range apps {
			ap := &apps[i]
			if strings.EqualFold(ap.Slug, catID) || strings.EqualFold(ap.Slug, row.Slug) ||
				strings.EqualFold(ap.ID, catID) || strings.EqualFold(ap.Name, row.Name) {
				return ap
			}
		}
		return nil
	}
	app := findApp(ov.Apps)
	if app == nil {
		imported, err := mgr.ImportCatalogApp(ctx, u.ID, catID, row.Name)
		if err != nil {
			return nil, executorError(err)
		}
		a.Audit.Record(ctx, audit.Entry{
			UserID: u.ID, Action: audit.ExecutorAppImport, Target: imported.ID,
			Details: map[string]any{"entry": catID, "name": imported.Name, "orgTool": row.Slug},
		})
		if refreshed, err := mgr.Overview(ctx, u.ID); err == nil {
			ov = refreshed
			if found := findApp(ov.Apps); found != nil {
				app = found
			}
		}
		if app == nil {
			app = &imported
		}
	}

	gApp := apiExecutorApp(*app)
	resp := gen.ConnectOrgTool200JSONResponse{
		ToolId:    row.ID,
		Slug:      row.Slug,
		Connected: platform.ExecutorOrgToolConnected(d, ov),
		App:       &gApp,
	}

	// Pick the first unconnected requirement (or first requirement if reconnecting).
	var targetReq *executor.Requirement
	for i := range app.Requirements {
		if len(app.Requirements[i].LinkedAccountIDs) == 0 {
			targetReq = &app.Requirements[i]
			break
		}
	}
	if targetReq == nil && len(app.Requirements) > 0 && !resp.Connected {
		targetReq = &app.Requirements[0]
	}
	if targetReq != nil {
		sess, err := mgr.StartAccountConnect(ctx, u.ID, app.ID, targetReq.Scope, targetReq.Name, "")
		if err != nil {
			return nil, executorError(err)
		}
		gs := gen.ExecutorConnectionSession{
			ConnectionId: sess.ConnectionID,
			Token:        sess.Token,
			ProviderId:   sess.ProviderID,
			ProviderName: sess.ProviderName,
			Methods:      apiExecutorAuthMethods(sess.Methods),
		}
		if sess.BrowserURL != "" {
			bu := sess.BrowserURL
			gs.BrowserUrl = &bu
		}
		if !sess.ExpiresAt.IsZero() {
			exp := sess.ExpiresAt.UTC()
			gs.ExpiresAt = &exp
		}
		resp.Session = &gs
	}
	return resp, nil
}

// --- Org Skills ---

func apiOrgSkill(s db.OrgSkill) gen.OrgSkill {
	return gen.OrgSkill{
		Id:               s.ID,
		Name:             s.Name,
		Description:      s.Description,
		Content:          s.Content,
		EnabledByDefault: s.EnabledByDefault != 0,
		CreatedAt:        ts(s.CreatedAt),
		UpdatedAt:        ts(s.UpdatedAt),
	}
}

func validOrgSkill(in gen.OrgSkillInput) (db.OrgSkill, error) {
	base, err := validSkill(gen.SkillInput{Name: in.Name, Description: in.Description, Content: in.Content})
	if err != nil {
		return db.OrgSkill{}, err
	}
	enabled := int64(1)
	if in.EnabledByDefault != nil && !*in.EnabledByDefault {
		enabled = 0
	}
	return db.OrgSkill{
		Name:             base.Name,
		Description:      base.Description,
		Content:          base.Content,
		EnabledByDefault: enabled,
	}, nil
}

// ListOrgSkills implements gen.StrictServerInterface.
func (a *API) ListOrgSkills(ctx context.Context, _ gen.ListOrgSkillsRequestObject) (gen.ListOrgSkillsResponseObject, error) {
	rows, err := a.Store.ListOrgSkills(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ListOrgSkills200JSONResponse{}
	for _, r := range rows {
		out = append(out, apiOrgSkill(r))
	}
	return out, nil
}

// CreateOrgSkill implements gen.StrictServerInterface.
func (a *API) CreateOrgSkill(ctx context.Context, req gen.CreateOrgSkillRequestObject) (gen.CreateOrgSkillResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	s, err := validOrgSkill(*req.Body)
	if err != nil {
		return nil, err
	}
	now := a.Now().Unix()
	s.ID, s.CreatedAt, s.UpdatedAt = id.New("osk"), now, now
	if err := a.Store.CreateOrgSkill(ctx, db.CreateOrgSkillParams(s)); err != nil {
		if store.IsUniqueViolation(err) {
			return nil, errStatus(http.StatusConflict, "an organization skill with this name already exists")
		}
		return nil, err
	}
	a.Audit.Record(ctx, audit.Entry{UserID: me(ctx).ID, Action: audit.OrgSkillCreate, Target: s.ID, Details: map[string]any{"name": s.Name}})
	return gen.CreateOrgSkill201JSONResponse(apiOrgSkill(s)), nil
}

// UpdateOrgSkill implements gen.StrictServerInterface.
func (a *API) UpdateOrgSkill(ctx context.Context, req gen.UpdateOrgSkillRequestObject) (gen.UpdateOrgSkillResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	cur, err := a.Store.GetOrgSkill(ctx, req.SkillId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	s, err := validOrgSkill(*req.Body)
	if err != nil {
		return nil, err
	}
	s.ID, s.CreatedAt, s.UpdatedAt = cur.ID, cur.CreatedAt, a.Now().Unix()
	if err := a.Store.UpdateOrgSkill(ctx, db.UpdateOrgSkillParams{
		Name:             s.Name,
		Description:      s.Description,
		Content:          s.Content,
		EnabledByDefault: s.EnabledByDefault,
		UpdatedAt:        s.UpdatedAt,
		ID:               s.ID,
	}); err != nil {
		if store.IsUniqueViolation(err) {
			return nil, errStatus(http.StatusConflict, "an organization skill with this name already exists")
		}
		return nil, err
	}
	a.Audit.Record(ctx, audit.Entry{UserID: me(ctx).ID, Action: audit.OrgSkillUpdate, Target: s.ID, Details: map[string]any{"name": s.Name}})
	return gen.UpdateOrgSkill200JSONResponse(apiOrgSkill(s)), nil
}

// DeleteOrgSkill implements gen.StrictServerInterface.
func (a *API) DeleteOrgSkill(ctx context.Context, req gen.DeleteOrgSkillRequestObject) (gen.DeleteOrgSkillResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	n, err := a.Store.DeleteOrgSkill(ctx, req.SkillId)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	a.Audit.Record(ctx, audit.Entry{UserID: me(ctx).ID, Action: audit.OrgSkillDelete, Target: req.SkillId})
	return gen.DeleteOrgSkill204Response{}, nil
}

// --- Org Provider Logins ---

func apiOrgProviderLogin(provider string, row *db.OrgProviderLogin) gen.OrgProviderLogin {
	if row == nil {
		return gen.OrgProviderLogin{
			Provider:   provider,
			Configured: false,
			Enabled:    false,
		}
	}
	kind := gen.OrgProviderLoginKind(row.Kind)
	label := row.Label
	updatedAt := ts(row.UpdatedAt)
	out := gen.OrgProviderLogin{
		Provider:   row.Provider,
		Configured: true,
		Kind:       &kind,
		Label:      &label,
		Enabled:    row.Enabled != 0,
		UpdatedAt:  &updatedAt,
	}
	if row.UpdatedBy.Valid && row.UpdatedBy.String != "" {
		ub := row.UpdatedBy.String
		out.UpdatedBy = &ub
	}
	return out
}

// ListOrgProviders implements gen.StrictServerInterface.
func (a *API) ListOrgProviders(ctx context.Context, _ gen.ListOrgProvidersRequestObject) (gen.ListOrgProvidersResponseObject, error) {
	rows, err := a.Store.ListOrgProviderLogins(ctx)
	if err != nil {
		return nil, err
	}
	byProvider := make(map[string]db.OrgProviderLogin, len(rows))
	for _, r := range rows {
		byProvider[r.Provider] = r
	}
	providers := append([]string{}, logins.SupportedOrgProviders...)
	if a.ShowTestHarness {
		providers = append(providers, "fake")
	}
	out := gen.ListOrgProviders200JSONResponse{}
	for _, p := range providers {
		if row, ok := byProvider[p]; ok {
			out = append(out, apiOrgProviderLogin(p, &row))
		} else {
			out = append(out, apiOrgProviderLogin(p, nil))
		}
	}
	return out, nil
}

// UpsertOrgProvider implements gen.StrictServerInterface.
func (a *API) UpsertOrgProvider(ctx context.Context, req gen.UpsertOrgProviderRequestObject) (gen.UpsertOrgProviderResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Logins == nil {
		return nil, errStatus(http.StatusServiceUnavailable, "provider logins service unavailable")
	}
	b := req.Body
	in := logins.OrgLoginInput{
		Kind:    string(b.Kind),
		Enabled: true,
	}
	if b.Label != nil {
		in.Label = *b.Label
	}
	if b.Credential != nil {
		in.Credential = *b.Credential
	}
	if b.BaseUrl != nil {
		in.BaseURL = *b.BaseUrl
	}
	if b.Header != nil {
		in.Header = *b.Header
	}
	if b.Hosts != nil {
		in.Hosts = *b.Hosts
	}
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	row, err := a.Logins.SetOrgLogin(ctx, me(ctx).ID, req.Provider, in, a.ShowTestHarness)
	var inputErr *logins.InputError
	switch {
	case errors.Is(err, logins.ErrUnknownProvider):
		return nil, errNotFound
	case errors.Is(err, logins.ErrUnknownKind):
		return nil, badRequest("kind must be api_key, oauth_token or gateway")
	case errors.As(err, &inputErr):
		return nil, badRequest(inputErr.Error())
	case err != nil:
		return nil, err
	}
	return gen.UpsertOrgProvider200JSONResponse(apiOrgProviderLogin(req.Provider, &row)), nil
}

// DeleteOrgProvider implements gen.StrictServerInterface.
func (a *API) DeleteOrgProvider(ctx context.Context, req gen.DeleteOrgProviderRequestObject) (gen.DeleteOrgProviderResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if a.Logins == nil {
		return nil, errStatus(http.StatusServiceUnavailable, "provider logins service unavailable")
	}
	if err := a.Logins.DeleteOrgLogin(ctx, me(ctx).ID, req.Provider, a.ShowTestHarness); err != nil {
		if errors.Is(err, logins.ErrUnknownProvider) || errors.Is(err, logins.ErrNotConnected) {
			return nil, errNotFound
		}
		return nil, err
	}
	return gen.DeleteOrgProvider204Response{}, nil
}

// --- In-Thread Secret Request Submission ---

// RespondSecretRequest implements gen.StrictServerInterface.
func (a *API) RespondSecretRequest(ctx context.Context, req gen.RespondSecretRequestRequestObject) (gen.RespondSecretRequestResponseObject, error) {
	s, err := a.sessionAccess(ctx, req.SessionId, authz.AgentPrompt)
	if err != nil {
		return nil, err
	}
	pending, ok := a.Sessions.GetSecretRequest(s.ID, req.RequestId)
	if !ok {
		return nil, errStatus(http.StatusNotFound, session.ErrUnknownSecretRequest.Error())
	}
	u := me(ctx)
	b := req.Body
	if b != nil && b.Decline != nil && *b.Decline {
		if err := a.Sessions.ResolveSecretRequest(ctx, s.ID, req.RequestId, u.ID, session.SecretResolution{
			Status: "declined",
		}); err != nil {
			if errors.Is(err, session.ErrUnknownSecretRequest) {
				return nil, errStatus(http.StatusNotFound, err.Error())
			}
			return nil, err
		}
		a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SecretRequest, req.RequestId, map[string]any{"status": "declined", "name": pending.Name})
		return gen.RespondSecretRequest200JSONResponse{Status: gen.Declined}, nil
	}

	// Submitting a secret saves into the user's secret store and attaches it to
	// the agent: only the agent's owner may attach secrets to it.
	if _, err := a.authz.Agent(ctx, u.ID, s.AgentID, authz.AgentSecrets); err != nil {
		return nil, err
	}
	if b == nil || b.Value == nil || *b.Value == "" {
		return nil, badRequest("value is required unless declining")
	}
	if err := validSecretValue(*b.Value); err != nil {
		return nil, err
	}
	secName := pending.Name
	if b.Name != nil && strings.TrimSpace(*b.Name) != "" {
		secName = *b.Name
	}
	secName, err = validSecretName(secName)
	if err != nil {
		return nil, err
	}
	envVar := pending.EnvVar
	if b.EnvVar != nil {
		envVar = strings.TrimSpace(*b.EnvVar)
	}
	if envVar != "" && (!envVarRe.MatchString(envVar) || reservedEnv[strings.ToUpper(envVar)]) {
		return nil, badRequest("envVar must be a valid, non-reserved environment variable name")
	}

	header := pending.Header
	if header == "" {
		header = "Authorization"
	}
	tmpl := pending.Template
	if tmpl == "" {
		if strings.EqualFold(header, "Authorization") {
			tmpl = "Bearer {{.Value}}"
		} else {
			tmpl = "{{.Value}}"
		}
	}
	rule := secrets.Rule{Hosts: pending.Hosts, Header: header, Template: tmpl}
	if err := rule.Validate(); err != nil {
		return nil, badRequest(err.Error())
	}
	ruleJSON, _ := json.Marshal(rule)
	now := a.Now().Unix()

	var sec db.Secret
	existing, err := a.Store.GetSecretByName(ctx, db.GetSecretByNameParams{OwnerID: u.ID, Name: secName})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		secID := id.New("sec")
		ct, err := a.Box.Seal([]byte(*b.Value), []byte(secID))
		if err != nil {
			return nil, err
		}
		sec = db.Secret{
			ID: secID, OwnerID: u.ID, Name: secName, Ciphertext: ct,
			Rule: string(ruleJSON), Placeholder: "agenthold-placeholder-" + id.Random(16),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := a.Store.CreateSecret(ctx, db.CreateSecretParams{
			ID: sec.ID, OwnerID: sec.OwnerID, Name: sec.Name, Ciphertext: sec.Ciphertext,
			Rule: sec.Rule, Placeholder: sec.Placeholder, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		ct, err := a.Box.Seal([]byte(*b.Value), []byte(existing.ID))
		if err != nil {
			return nil, err
		}
		existing.Ciphertext, existing.Rule, existing.UpdatedAt = ct, string(ruleJSON), now
		if err := a.Store.UpdateSecret(ctx, db.UpdateSecretParams{
			Name: existing.Name, Ciphertext: existing.Ciphertext, Rule: existing.Rule, UpdatedAt: now, ID: existing.ID,
		}); err != nil {
			return nil, err
		}
		sec = existing
	}

	if err := a.Store.AttachSecret(ctx, db.AttachSecretParams{
		AgentID: s.AgentID, SecretID: sec.ID, EnvVar: envVar, CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	if a.Egress != nil {
		a.Egress.Invalidate()
	}

	res := session.SecretResolution{
		Status:      "submitted",
		SecretID:    sec.ID,
		Name:        sec.Name,
		EnvVar:      envVar,
		Placeholder: sec.Placeholder,
	}
	if err := a.Sessions.ResolveSecretRequest(ctx, s.ID, req.RequestId, u.ID, res); err != nil {
		if errors.Is(err, session.ErrUnknownSecretRequest) {
			return nil, errStatus(http.StatusNotFound, err.Error())
		}
		return nil, err
	}
	a.record(ctx, mustAgent(ctx, a, s.AgentID), audit.SecretRequest, req.RequestId, map[string]any{
		"status": "submitted", "secretId": sec.ID, "name": sec.Name, "envVar": envVar,
	})
	out := gen.RespondSecretRequest200JSONResponse{
		Status:      gen.Submitted,
		SecretId:    &sec.ID,
		Name:        &sec.Name,
		Placeholder: &sec.Placeholder,
	}
	if envVar != "" {
		out.EnvVar = &envVar
	}
	return out, nil
}
