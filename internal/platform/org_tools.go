package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/executor"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tools"
)

var secretEnvVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var orgPlatformTools = []map[string]any{
	{
		"name": "request_secret",
		"description": "Ask the user for a secret (API key or token) through a secure inline card in chat. " +
			"The secret is encrypted directly into the user's secret store and injected by the egress proxy for the " +
			"specified hosts — it NEVER appears in chat history and NEVER enters the sandbox. " +
			"Use the returned placeholder or environment variable in requests to the allowed hosts.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"name", "description", "hosts"},
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Short secret name (e.g. \"stripe-api-key\")."},
				"description": map[string]any{"type": "string", "description": "Why this secret is needed and how the user can obtain it."},
				"hosts": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Destination hostnames or wildcard patterns (e.g. [\"api.stripe.com\"]).",
				},
				"env_var":      map[string]any{"type": "string", "description": "Optional environment variable name for the placeholder (e.g. \"STRIPE_API_KEY\")."},
				"header":       map[string]any{"type": "string", "description": "Optional HTTP header name for egress proxy injection (default \"Authorization\")."},
				"wait_seconds": map[string]any{"type": "integer", "description": "How long to wait for the user to respond (default 300, 0 returns pending immediately)."},
			},
		},
	},
	{
		"name": "request_account_connect",
		"description": "Prompt the user with an inline one-click Connect card in chat for an organization-provided " +
			"OAuth / Executor integration (such as linear, github, notion, slack) that is not yet connected.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"tool_slug"},
			"properties": map[string]any{
				"tool_slug": map[string]any{"type": "string", "description": "The organization tool's slug (e.g. \"linear\", \"github\", \"notion\")."},
				"reason":    map[string]any{"type": "string", "description": "Optional explanation of why this account is needed."},
			},
		},
	},
}

func isOrgPlatformTool(tool string) bool {
	return tool == "request_secret" || tool == "request_account_connect"
}

func (p *Service) orgPlatformCall(ctx context.Context, caller db.Agent, sessionID, tool string, args json.RawMessage) (string, error) {
	switch tool {
	case "request_secret":
		return p.requestSecret(ctx, caller, sessionID, args)
	case "request_account_connect":
		return p.requestAccountConnect(ctx, caller, sessionID, args)
	default:
		return "", fmt.Errorf("unknown tool %q", tool)
	}
}

func (p *Service) requestSecret(ctx context.Context, caller db.Agent, sessionID string, args json.RawMessage) (string, error) {
	if sessionID == "" {
		return "", errors.New("request_secret requires an active session")
	}
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Hosts       []string `json:"hosts"`
		EnvVar      string   `json:"env_var"`
		Header      string   `json:"header"`
		WaitSeconds *int     `json:"wait_seconds"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", errors.New("invalid arguments")
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return "", errors.New("name must be 1 to 100 characters")
	}
	desc := strings.TrimSpace(in.Description)
	if desc == "" || len(desc) > 2000 {
		return "", errors.New("description must be 1 to 2000 characters")
	}
	hosts := make([]string, 0, len(in.Hosts))
	for _, h := range in.Hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	header := strings.TrimSpace(in.Header)
	if header == "" {
		header = "Authorization"
	}
	tmpl := "{{.Value}}"
	if strings.EqualFold(header, "Authorization") {
		tmpl = "Bearer {{.Value}}"
	}
	rule := secrets.Rule{Hosts: hosts, Header: header, Template: tmpl}
	if err := rule.Validate(); err != nil {
		return "", err
	}
	envVar := strings.TrimSpace(in.EnvVar)
	if envVar != "" && !secretEnvVarRe.MatchString(envVar) {
		return "", errors.New("env_var must be a valid environment variable name")
	}

	wait := 5 * time.Minute
	if p.SecretRequestTimeout > 0 {
		wait = p.SecretRequestTimeout
	}
	if in.WaitSeconds != nil {
		ws := *in.WaitSeconds
		if ws < 0 {
			ws = 0
		}
		if ws > 900 {
			ws = 900
		}
		wait = time.Duration(ws) * time.Second
	}

	reqID, res, err := p.Sessions.RequestSecret(ctx, sessionID, caller.ID, session.SecretRequest{
		Name:        name,
		Description: desc,
		Hosts:       hosts,
		EnvVar:      envVar,
		Header:      header,
		Template:    tmpl,
	}, wait)
	if err != nil {
		return "", err
	}

	out := map[string]any{
		"requestId": reqID,
		"name":      name,
		"hosts":     hosts,
		"header":    header,
	}
	if envVar != "" {
		out["envVar"] = envVar
	}
	switch {
	case res == nil:
		out["status"] = "pending"
		out["message"] = "Secret request card shown in chat; waiting for the user to submit or decline."
	case res.Status == "submitted":
		out["status"] = "submitted"
		out["secretId"] = res.SecretID
		out["placeholder"] = res.Placeholder
		if res.EnvVar != "" {
			out["envVar"] = res.EnvVar
		}
		out["message"] = "Secret saved securely in the user's secret store and activated in the egress proxy. Use the placeholder or environment variable; the real value never enters the sandbox."
	default:
		out["status"] = "declined"
		out["message"] = "The user declined to provide this secret."
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (p *Service) requestAccountConnect(ctx context.Context, caller db.Agent, sessionID string, args json.RawMessage) (string, error) {
	if sessionID == "" {
		return "", errors.New("request_account_connect requires an active session")
	}
	var in struct {
		ToolSlug string `json:"tool_slug"`
		Reason   string `json:"reason"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return "", errors.New("invalid arguments")
		}
	}
	slug := strings.TrimSpace(in.ToolSlug)
	if slug == "" {
		return "", errors.New("tool_slug is required")
	}
	row, err := p.Store.GetOrgToolBySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("organization tool %q not found", slug)
	}
	if err != nil {
		return "", err
	}
	targetUserID := caller.OwnerID
	if p.Sessions != nil {
		if active := p.Sessions.ActiveUserID(caller.ID, sessionID); active != "" {
			targetUserID = active
		}
	}
	actingUser, err := p.Store.GetUser(ctx, targetUserID)
	if err != nil {
		return "", err
	}
	decoded := tools.DecodeOrgTool(row)
	if !tools.RoleAllowed(decoded.AllowedRoles, authz.UserRole(actingUser)) {
		return "", fmt.Errorf("organization tool %q is not available for your account", slug)
	}

	connected := false
	if decoded.Kind == tools.OrgToolKindExecutorApp && p.Executor != nil && p.Executor.Available() {
		if ov, err := p.Executor.Overview(ctx, actingUser.ID); err == nil {
			connected = ExecutorOrgToolConnected(decoded, ov)
		}
	}

	reqID := id.New("acreq")
	ev := map[string]any{
		"requestId":       reqID,
		"toolId":          row.ID,
		"slug":            row.Slug,
		"name":            row.Name,
		"description":     row.Description,
		"kind":            row.Kind,
		"authMode":        row.AuthMode,
		"connected":       connected,
		"connectEndpoint": "/api/v1/me/org-tools/" + row.ID + "/connect",
	}
	if reason := strings.TrimSpace(in.Reason); reason != "" {
		ev["reason"] = reason
	}
	if err := p.Sessions.RecordAccountConnectRequest(ctx, sessionID, ev); err != nil {
		return "", err
	}

	out := map[string]any{
		"requestId":       reqID,
		"toolId":          row.ID,
		"slug":            row.Slug,
		"name":            row.Name,
		"connected":       connected,
		"connectEndpoint": "/api/v1/me/org-tools/" + row.ID + "/connect",
	}
	if connected {
		out["message"] = fmt.Sprintf("%s is already connected for this user.", row.Name)
	} else {
		out["message"] = fmt.Sprintf("Rendered an inline Connect %s card in chat. Ask the user to click Connect and then retry.", row.Name)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// ExecutorOrgToolConnected reports whether the user's Executor overview shows
// an installed app matching tool with at least one linked account.
func ExecutorOrgToolConnected(tool tools.DecodedOrgTool, ov executor.Overview) bool {
	catID := strings.ToLower(strings.TrimSpace(tool.Config.ExecutorCatalogID))
	slug := strings.ToLower(strings.TrimSpace(tool.Slug))
	name := strings.ToLower(strings.TrimSpace(tool.Name))
	for _, app := range ov.Apps {
		if strings.ToLower(app.Slug) != catID && strings.ToLower(app.Slug) != slug &&
			strings.ToLower(app.ID) != catID && strings.ToLower(app.Name) != name {
			continue
		}
		if len(app.Requirements) == 0 {
			return true
		}
		for _, r := range app.Requirements {
			if len(r.LinkedAccountIDs) > 0 {
				return true
			}
		}
	}
	return false
}
