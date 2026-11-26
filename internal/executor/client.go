package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// UpstreamError represents an error returned by the local Executor server.
type UpstreamError struct {
	Status  int
	Code    string
	Message string
}

func (e *UpstreamError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("executor HTTP %d", e.Status)
}

// Overview is a normalized snapshot of a user's Executor apps and accounts.
type Overview struct {
	Apps     []App
	Accounts []Account
}

// App is an installed Executor app.
type App struct {
	ID            string
	Slug          string
	Name          string
	ActiveVersion string
	Builtin       bool
	Requirements  []Requirement
}

// Requirement is one account slot an Executor app declares.
type Requirement struct {
	Name             string
	Scope            string // "user" | "app"
	ProviderID       string
	ProviderName     string
	Many             bool
	AuthMethods      []AuthMethod
	LinkedAccountIDs []string
}

// AuthMethod describes how to authenticate with a provider.
type AuthMethod struct {
	Name   string
	Kind   string // "secrets" | "oauth2" | "other"
	Label  string
	Guide  string
	Fields []SecretField
}

// SecretField describes one credential field for a secrets auth method.
type SecretField struct {
	Name        string
	Title       string
	Description string
	Required    bool
	Secret      bool
}

// Account is a saved Executor account.
type Account struct {
	ID           string
	ProviderID   string
	ProviderName string
	Label        string
	Description  string
	Method       string
	Status       string // "ready" | "needs_reauth" | "error" | "unknown"
	Identity     string
	LinkedApps   []string
}

// AccountHealth is the result of checking an account's credentials.
type AccountHealth struct {
	Status  string
	Message string
}

// CatalogEntry is an installable app from the Executor catalog.
type CatalogEntry struct {
	ID          string
	Package     string
	Name        string
	Description string
	Category    string
	IconURL     string
	Auth        []string
}

// ConnectionSession is an active account-connection request.
type ConnectionSession struct {
	ConnectionID string
	Token        string
	BrowserURL   string
	ExpiresAt    time.Time
	ProviderID   string
	ProviderName string
	Methods      []AuthMethod
}

// doJSON sends an authenticated JSON request to userID's Executor instance.
func (m *Manager) doJSON(ctx context.Context, userID, method, path string, body any, dst any) error {
	inst, release, err := m.acquireInstance(ctx, userID)
	if err != nil {
		return err
	}
	defer release()
	return m.doJSONInst(ctx, inst, method, path, body, dst)
}

func (m *Manager) doJSONInst(ctx context.Context, inst *Instance, method, path string, body any, dst any) error {
	release := inst.acquire()
	defer release()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, inst.URL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+inst.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("executor request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseUpstreamError(resp.StatusCode, data)
	}
	if dst != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, dst); err != nil {
			return fmt.Errorf("decode executor response for %s %s: %w", method, path, err)
		}
	}
	return nil
}

func parseUpstreamError(status int, data []byte) error {
	var raw struct {
		Tag         string `json:"_tag"`
		Code        string `json:"code"`
		Message     string `json:"message"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Reason      string `json:"reason"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(data, &raw)
	msg := raw.Message
	if msg == "" {
		switch {
		case raw.Title != "" && raw.Description != "":
			msg = raw.Title + ": " + raw.Description
		case raw.Description != "":
			msg = raw.Description
		case raw.Reason != "":
			msg = raw.Reason
		case raw.Error != "":
			msg = raw.Error
		default:
			msg = strings.TrimSpace(string(data))
			if len(msg) > 400 {
				msg = msg[:400]
			}
		}
	}
	code := raw.Code
	if code == "" {
		code = raw.Tag
	}
	return &UpstreamError{Status: status, Code: code, Message: msg}
}

// --- raw Executor response structures ---

type rawOverview struct {
	Apps     []rawApp     `json:"apps"`
	Links    []rawLink    `json:"links"`
	AppLinks []rawLink    `json:"appLinks"`
	Accounts []rawAccount `json:"accounts"`
}

type rawApp struct {
	ID            string           `json:"id"`
	Slug          string           `json:"slug"`
	Name          string           `json:"name"`
	ActiveVersion string           `json:"activeVersion"`
	Requirements  *rawRequirements `json:"requirements"`
}

type rawRequirements struct {
	Accounts map[string]rawAccountReq `json:"accounts"`
}

type rawAccountReq struct {
	Scope       string         `json:"scope"`
	Provider    string         `json:"provider"`
	Cardinality string         `json:"cardinality"`
	Definition  *rawProviderDf `json:"definition"`
}

type rawProviderDf struct {
	Name string                       `json:"name"`
	Auth map[string]rawAuthMethodSpec `json:"auth"`
}

type rawAuthMethodSpec struct {
	Type   string           `json:"type"`
	Label  string           `json:"label"`
	Guide  string           `json:"guide"`
	Fields *rawFieldsSchema `json:"fields"`
}

type rawFieldsSchema struct {
	Properties map[string]rawFieldProp `json:"properties"`
	Required   []string                `json:"required"`
}

type rawFieldProp struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type rawLink struct {
	App         string `json:"app"`
	Account     string `json:"account"`
	Requirement string `json:"requirement"`
}

type rawAccount struct {
	ID           string  `json:"id"`
	Provider     string  `json:"provider"`
	ProviderName string  `json:"providerName"`
	Method       string  `json:"method"`
	Label        string  `json:"label"`
	Description  *string `json:"description"`
	SignIn       *struct {
		State string `json:"state"`
	} `json:"signIn"`
	Health *struct {
		Info map[string]any `json:"info"`
		Apps []struct {
			App   string `json:"app"`
			Check *struct {
				Status  string `json:"status"`
				OK      *bool  `json:"ok"`
				Message string `json:"message"`
			} `json:"check"`
		} `json:"apps"`
	} `json:"health"`
}

// Overview loads the user's apps, requirements, links and connected accounts.
func (m *Manager) Overview(ctx context.Context, userID string) (Overview, error) {
	var raw rawOverview
	if err := m.doJSON(ctx, userID, http.MethodGet, "/dashboard/api/overview", nil, &raw); err != nil {
		return Overview{}, err
	}

	// Build account->apps and (app,scope,req)->accounts maps.
	accountProvider := map[string]string{}
	for _, acc := range raw.Accounts {
		accountProvider[acc.ID] = acc.Provider
	}
	appNames := map[string]string{}
	for _, app := range raw.Apps {
		appNames[app.ID] = app.Name
	}

	userLinksByApp := map[string][]rawLink{}
	for _, l := range raw.Links {
		userLinksByApp[l.App] = append(userLinksByApp[l.App], l)
	}
	appLinksByApp := map[string][]rawLink{}
	for _, l := range raw.AppLinks {
		appLinksByApp[l.App] = append(appLinksByApp[l.App], l)
	}

	accountApps := map[string][]string{}
	addAccApp := func(accID, appID string) {
		name := appNames[appID]
		if name == "" {
			name = appID
		}
		for _, existing := range accountApps[accID] {
			if existing == name {
				return
			}
		}
		accountApps[accID] = append(accountApps[accID], name)
	}
	for _, l := range raw.Links {
		addAccApp(l.Account, l.App)
	}
	for _, l := range raw.AppLinks {
		addAccApp(l.Account, l.App)
	}

	apps := make([]App, 0, len(raw.Apps))
	for _, ra := range raw.Apps {
		a := convertApp(ra, userLinksByApp[ra.ID], appLinksByApp[ra.ID], accountProvider)
		apps = append(apps, a)
	}

	accounts := make([]Account, 0, len(raw.Accounts))
	for _, rc := range raw.Accounts {
		acc := convertAccount(rc)
		if linked := accountApps[rc.ID]; len(linked) > 0 {
			sort.Strings(linked)
			acc.LinkedApps = linked
		}
		accounts = append(accounts, acc)
	}

	return Overview{Apps: apps, Accounts: accounts}, nil
}

func convertApp(ra rawApp, userLinks, appLinks []rawLink, accountProvider map[string]string) App {
	a := App{
		ID:            ra.ID,
		Slug:          ra.Slug,
		Name:          ra.Name,
		ActiveVersion: ra.ActiveVersion,
		Builtin:       ra.Slug == "executor",
		Requirements:  []Requirement{},
	}
	if ra.Requirements == nil {
		return a
	}
	reqNames := make([]string, 0, len(ra.Requirements.Accounts))
	for name := range ra.Requirements.Accounts {
		reqNames = append(reqNames, name)
	}
	sort.Strings(reqNames)

	for _, name := range reqNames {
		rr := ra.Requirements.Accounts[name]
		scope := rr.Scope
		if scope != "app" {
			scope = "user"
		}
		req := Requirement{
			Name:             name,
			Scope:            scope,
			ProviderID:       rr.Provider,
			Many:             rr.Cardinality == "many",
			AuthMethods:      []AuthMethod{},
			LinkedAccountIDs: []string{},
		}
		if rr.Definition != nil {
			req.ProviderName = rr.Definition.Name
			req.AuthMethods = convertAuthMethods(rr.Definition.Auth)
		}
		if req.ProviderName == "" {
			req.ProviderName = name
		}
		links := userLinks
		if scope == "app" {
			links = appLinks
		}
		seen := map[string]bool{}
		for _, l := range links {
			if seen[l.Account] {
				continue
			}
			// Match explicit requirement name when present, else match by provider ID.
			if l.Requirement != "" {
				if l.Requirement != name {
					continue
				}
			} else if rr.Provider != "" && accountProvider[l.Account] != "" && accountProvider[l.Account] != rr.Provider {
				continue
			}
			seen[l.Account] = true
			req.LinkedAccountIDs = append(req.LinkedAccountIDs, l.Account)
		}
		a.Requirements = append(a.Requirements, req)
	}
	return a
}

func convertAuthMethods(raw map[string]rawAuthMethodSpec) []AuthMethod {
	if len(raw) == 0 {
		return []AuthMethod{}
	}
	names := make([]string, 0, len(raw))
	for k := range raw {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]AuthMethod, 0, len(names))
	for _, name := range names {
		spec := raw[name]
		kind := "other"
		switch strings.ToLower(spec.Type) {
		case "secrets", "secret", "apikey":
			kind = "secrets"
		case "oauth2", "oauth":
			kind = "oauth2"
		}
		m := AuthMethod{
			Name:   name,
			Kind:   kind,
			Label:  spec.Label,
			Guide:  spec.Guide,
			Fields: []SecretField{},
		}
		if m.Label == "" {
			m.Label = name
		}
		if spec.Fields != nil && len(spec.Fields.Properties) > 0 {
			reqSet := map[string]bool{}
			for _, r := range spec.Fields.Required {
				reqSet[r] = true
			}
			fnames := make([]string, 0, len(spec.Fields.Properties))
			for fn := range spec.Fields.Properties {
				fnames = append(fnames, fn)
			}
			sort.Strings(fnames)
			for _, fn := range fnames {
				fp := spec.Fields.Properties[fn]
				m.Fields = append(m.Fields, SecretField{
					Name:        fn,
					Title:       fp.Title,
					Description: fp.Description,
					Required:    reqSet[fn],
					Secret:      looksSecretField(fn),
				})
			}
		}
		out = append(out, m)
	}
	return out
}

func looksSecretField(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range []string{"token", "secret", "password", "pass", "key", "credential", "auth"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func convertAccount(rc rawAccount) Account {
	status := "ready"
	if rc.SignIn != nil {
		switch rc.SignIn.State {
		case "saved", "connected", "valid", "ok", "":
			status = "ready"
		case "reconnect", "expired", "needs_sign_in", "needs-sign-in":
			status = "needs_reauth"
		default:
			status = rc.SignIn.State
		}
	}
	identity := ""
	if rc.Health != nil {
		if rc.Health.Info != nil {
			for _, k := range []string{"email", "username", "login", "name", "handle"} {
				if v, ok := rc.Health.Info[k].(string); ok && strings.TrimSpace(v) != "" {
					identity = strings.TrimSpace(v)
					break
				}
			}
		}
		for _, ah := range rc.Health.Apps {
			if ah.Check != nil && ah.Check.OK != nil && !*ah.Check.OK {
				status = "error"
			}
		}
	}
	desc := ""
	if rc.Description != nil {
		desc = *rc.Description
	}
	provName := rc.ProviderName
	if provName == "" {
		provName = rc.Provider
	}
	return Account{
		ID:           rc.ID,
		ProviderID:   rc.Provider,
		ProviderName: provName,
		Label:        rc.Label,
		Description:  desc,
		Method:       rc.Method,
		Status:       status,
		Identity:     identity,
		LinkedApps:   []string{},
	}
}

// Catalog fetches the list of apps available in Executor's catalog.
func (m *Manager) Catalog(ctx context.Context, userID string) ([]CatalogEntry, error) {
	var raw struct {
		Apps []struct {
			ID          string `json:"id"`
			Package     string `json:"package"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Category    string `json:"category"`
			Auth        []struct {
				Method string `json:"method"`
				Kind   string `json:"kind"`
			} `json:"auth"`
			Icons []struct {
				Src string `json:"src"`
			} `json:"icons"`
		} `json:"apps"`
	}
	if err := m.doJSON(ctx, userID, http.MethodGet, "/dashboard/api/catalog", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]CatalogEntry, 0, len(raw.Apps))
	for _, a := range raw.Apps {
		auths := make([]string, 0, len(a.Auth))
		for _, au := range a.Auth {
			if au.Kind != "" {
				auths = append(auths, au.Kind)
			} else if au.Method != "" {
				auths = append(auths, au.Method)
			}
		}
		icon := ""
		if len(a.Icons) > 0 {
			icon = a.Icons[0].Src
		}
		out = append(out, CatalogEntry{
			ID:          a.ID,
			Package:     a.Package,
			Name:        a.Name,
			Description: a.Description,
			Category:    a.Category,
			IconURL:     icon,
			Auth:        auths,
		})
	}
	return out, nil
}

// ImportCatalogApp installs an official catalog app into the user's Executor instance.
func (m *Manager) ImportCatalogApp(ctx context.Context, userID, entry, name string) (App, error) {
	var deployed struct {
		App rawApp `json:"app"`
		// Some Executor responses flatten the app fields at top level.
		ID            string           `json:"id"`
		Slug          string           `json:"slug"`
		Name          string           `json:"name"`
		ActiveVersion string           `json:"activeVersion"`
		Requirements  *rawRequirements `json:"requirements"`
	}
	payload := map[string]any{"entry": entry, "name": name}
	if err := m.doJSON(ctx, userID, http.MethodPost, "/dashboard/api/catalog/import", payload, &deployed); err != nil {
		return App{}, err
	}
	ra := deployed.App
	if ra.ID == "" {
		ra = rawApp{
			ID:            deployed.ID,
			Slug:          deployed.Slug,
			Name:          deployed.Name,
			ActiveVersion: deployed.ActiveVersion,
			Requirements:  deployed.Requirements,
		}
	}
	return convertApp(ra, nil, nil, nil), nil
}

// ImportCustomApp imports a remote MCP server by URL into the user's Executor instance.
// Custom app imports require executor.allow_private_fetch: true because Executor's
// embedded workerd runtime allows private-network fetches for app code.
func (m *Manager) ImportCustomApp(ctx context.Context, userID, name, rawURL string) (App, error) {
	if !m.cfg.AllowPrivateFetch {
		return App{}, ErrCustomAppsDisabled
	}
	var deployed struct {
		App           rawApp           `json:"app"`
		ID            string           `json:"id"`
		Slug          string           `json:"slug"`
		Name          string           `json:"name"`
		ActiveVersion string           `json:"activeVersion"`
		Requirements  *rawRequirements `json:"requirements"`
	}
	payload := map[string]any{
		"source": map[string]any{
			"kind": "mcp",
			"name": name,
			"url":  rawURL,
		},
	}
	if err := m.doJSON(ctx, userID, http.MethodPost, "/dashboard/api/apps/import", payload, &deployed); err != nil {
		return App{}, err
	}
	ra := deployed.App
	if ra.ID == "" {
		ra = rawApp{
			ID:            deployed.ID,
			Slug:          deployed.Slug,
			Name:          deployed.Name,
			ActiveVersion: deployed.ActiveVersion,
			Requirements:  deployed.Requirements,
		}
	}
	return convertApp(ra, nil, nil, nil), nil
}

// DeleteApp deletes an installed app from the user's Executor instance.
func (m *Manager) DeleteApp(ctx context.Context, userID, appID string) error {
	return m.doJSON(ctx, userID, http.MethodDelete, "/dashboard/api/apps/"+url.PathEscape(appID), nil, nil)
}

// StartAccountConnect creates an account-connection request for an app requirement
// and reads its auth methods and field schemas so the caller can render either a
// native secret form or launch the browser OAuth2 flow.
func (m *Manager) StartAccountConnect(ctx context.Context, userID, appID, scope, requirement, reconnectAccountID string) (ConnectionSession, error) {
	if scope != "user" && scope != "app" {
		return ConnectionSession{}, &UpstreamError{Status: http.StatusBadRequest, Message: "scope must be user or app"}
	}
	inst, release, err := m.acquireInstance(ctx, userID)
	if err != nil {
		return ConnectionSession{}, err
	}
	defer release()
	body := map[string]any{}
	if reconnectAccountID != "" {
		body["account"] = reconnectAccountID
	}
	path := fmt.Sprintf("/account-connect/api/apps/%s/accounts/%s/%s/connections",
		url.PathEscape(appID), scope, url.PathEscape(requirement))
	var created struct {
		Connection string    `json:"connection"`
		URL        string    `json:"url"`
		ExpiresAt  time.Time `json:"expiresAt"`
	}
	if err := m.doJSONInst(ctx, inst, http.MethodPost, path, body, &created); err != nil {
		return ConnectionSession{}, err
	}
	token := extractFragmentParam(created.URL, "token")
	if token == "" {
		return ConnectionSession{}, errors.New("executor connection link did not carry a token")
	}

	var conn struct {
		Provider           string         `json:"provider"`
		ProviderDefinition *rawProviderDf `json:"providerDefinition"`
	}
	if err := m.doJSONInst(ctx, inst, http.MethodPost, "/account-connect/api/read", map[string]any{
		"connection": created.Connection,
		"token":      token,
	}, &conn); err != nil {
		return ConnectionSession{}, err
	}

	browserURL := ""
	if m.cfg.DashboardOrigin != "" {
		browserURL = rewriteURLOrigin(created.URL, m.cfg.DashboardOrigin)
	}
	sess := ConnectionSession{
		ConnectionID: created.Connection,
		Token:        token,
		BrowserURL:   browserURL,
		ExpiresAt:    created.ExpiresAt,
		ProviderID:   conn.Provider,
		Methods:      []AuthMethod{},
	}
	if conn.ProviderDefinition != nil {
		sess.ProviderName = conn.ProviderDefinition.Name
		sess.Methods = convertAuthMethods(conn.ProviderDefinition.Auth)
	}
	if sess.ProviderName == "" {
		sess.ProviderName = requirement
	}
	return sess, nil
}

// SubmitAccountConnect submits secret fields for a pending connection session.
func (m *Manager) SubmitAccountConnect(ctx context.Context, userID, connectionID, token, method, label, description string, fields map[string]string) (Account, error) {
	payload := map[string]any{
		"connection": connectionID,
		"token":      token,
		"method":     method,
		"fields":     fields,
	}
	if strings.TrimSpace(label) != "" {
		payload["label"] = strings.TrimSpace(label)
	}
	var raw rawAccount
	if err := m.doJSON(ctx, userID, http.MethodPost, "/account-connect/api/submit", payload, &raw); err != nil {
		return Account{}, err
	}
	acc := convertAccount(raw)
	if desc := strings.TrimSpace(description); desc != "" && acc.ID != "" {
		updated, err := m.UpdateAccount(ctx, userID, acc.ID, nil, true, &desc)
		if err != nil {
			return acc, fmt.Errorf("account connected (%s) but failed to save description: %w", acc.ID, err)
		}
		acc = updated
	}
	return acc, nil
}

// LinkAccount links a saved account to an app requirement.
func (m *Manager) LinkAccount(ctx context.Context, userID, appID, scope, requirement, accountID string) error {
	if scope != "user" && scope != "app" {
		return &UpstreamError{Status: http.StatusBadRequest, Message: "scope must be user or app"}
	}
	path := fmt.Sprintf("/dashboard/api/apps/%s/accounts/%s/%s/%s",
		url.PathEscape(appID), scope, url.PathEscape(requirement), url.PathEscape(accountID))
	return m.doJSON(ctx, userID, http.MethodPut, path, nil, nil)
}

// UnlinkAccount unlinks a saved account from an app requirement.
func (m *Manager) UnlinkAccount(ctx context.Context, userID, appID, scope, requirement, accountID string) error {
	if scope != "user" && scope != "app" {
		return &UpstreamError{Status: http.StatusBadRequest, Message: "scope must be user or app"}
	}
	path := fmt.Sprintf("/dashboard/api/apps/%s/accounts/%s/%s/%s",
		url.PathEscape(appID), scope, url.PathEscape(requirement), url.PathEscape(accountID))
	return m.doJSON(ctx, userID, http.MethodDelete, path, nil, nil)
}

// UpdateAccount renames or updates the description of an Executor account.
func (m *Manager) UpdateAccount(ctx context.Context, userID, accountID string, label *string, descSet bool, desc *string) (Account, error) {
	payload := map[string]any{}
	if label != nil {
		payload["label"] = strings.TrimSpace(*label)
	}
	if descSet {
		if desc == nil || strings.TrimSpace(*desc) == "" {
			payload["description"] = nil
		} else {
			payload["description"] = strings.TrimSpace(*desc)
		}
	}
	var raw rawAccount
	if err := m.doJSON(ctx, userID, http.MethodPatch, "/dashboard/api/accounts/"+url.PathEscape(accountID), payload, &raw); err != nil {
		return Account{}, err
	}
	return convertAccount(raw), nil
}

// CheckAccountHealth runs Executor's credential health check for an account.
func (m *Manager) CheckAccountHealth(ctx context.Context, userID, accountID string) (AccountHealth, error) {
	var raw struct {
		Status string `json:"status"`
		Apps   []struct {
			Check *struct {
				OK      *bool  `json:"ok"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"check"`
		} `json:"apps"`
	}
	if err := m.doJSON(ctx, userID, http.MethodPost, "/dashboard/api/accounts/"+url.PathEscape(accountID)+"/health", map[string]any{}, &raw); err != nil {
		return AccountHealth{}, err
	}
	h := AccountHealth{Status: "ok"}
	if raw.Status != "" {
		h.Status = raw.Status
	}
	for _, a := range raw.Apps {
		if a.Check != nil {
			if a.Check.OK != nil && !*a.Check.OK {
				h.Status = "error"
			}
			if a.Check.Message != "" && h.Message == "" {
				h.Message = a.Check.Message
			}
		}
	}
	return h, nil
}

// DeleteAccount removes a saved Executor account.
func (m *Manager) DeleteAccount(ctx context.Context, userID, accountID string) error {
	return m.doJSON(ctx, userID, http.MethodDelete, "/dashboard/api/accounts/"+url.PathEscape(accountID), nil, nil)
}

// PairBrowser exchanges a server-side pairing link for an Executor dashboard
// session cookie so an authenticated Agenthold user lands straight inside their
// Executor dashboard without manually running `executor service pair`.
func (m *Manager) PairBrowser(ctx context.Context, inst *Instance) ([]*http.Cookie, error) {
	release := inst.acquire()
	defer release()
	var paired struct {
		URL string `json:"url"`
	}
	if err := m.doJSONInst(ctx, inst, http.MethodPost, "/auth/pair", map[string]any{}, &paired); err != nil {
		return nil, err
	}
	pairToken := extractFragmentParam(paired.URL, "pair")
	if pairToken == "" {
		return nil, errors.New("executor pair response did not carry a pairing token")
	}
	body, _ := json.Marshal(map[string]string{"token": pairToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inst.URL+"/auth/exchange", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if inst.BrowserOrigin != "" {
		if bu, err := url.Parse(inst.BrowserOrigin); err == nil && bu.Host != "" {
			req.Host = bu.Host
			req.Header.Set("Origin", inst.BrowserOrigin)
		}
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executor exchange pairing token: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseUpstreamError(resp.StatusCode, data)
	}
	return resp.Cookies(), nil
}

func extractFragmentParam(rawURL, key string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Fragment == "" {
		return ""
	}
	vals, err := url.ParseQuery(u.Fragment)
	if err == nil {
		if v := vals.Get(key); v != "" {
			return v
		}
	}
	if v, ok := strings.CutPrefix(u.Fragment, key+"="); ok {
		return v
	}
	return ""
}

func rewriteURLOrigin(rawURL, newOrigin string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	o, err := url.Parse(newOrigin)
	if err != nil || o.Host == "" {
		return rawURL
	}
	u.Scheme = o.Scheme
	u.Host = o.Host
	return u.String()
}
