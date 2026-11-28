package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/executor"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Organization tool kinds.
const (
	OrgToolKindMCPHTTP     = "mcp_http"
	OrgToolKindMCPStdio    = "mcp_stdio"
	OrgToolKindExecutorApp = "executor_app"
)

// Organization tool auth modes.
const (
	OrgToolAuthNone      = "none"
	OrgToolAuthOrgShared = "org_shared"
	OrgToolAuthUserOAuth = "user_oauth"
)

// OrgToolSlug matches DNS/identifier slugs for org tools (e.g. "linear", "internal-docs").
var OrgToolSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

var orgSecretRef = regexp.MustCompile(`\{\{\s*org_secret\s*\}\}`)

// OrgToolConfig is stored as JSON in org_tools.config_json.
type OrgToolConfig struct {
	URL               string            `json:"url,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	ExecutorCatalogID string            `json:"executorCatalogId,omitempty"`
}

// DecodedOrgTool is a parsed org_tools row.
type DecodedOrgTool struct {
	db.OrgTool
	Config       OrgToolConfig
	SecretHosts  []string
	AllowedRoles []string
}

// DecodeOrgTool parses the JSON columns of an org tool row.
func DecodeOrgTool(t db.OrgTool) DecodedOrgTool {
	out := DecodedOrgTool{
		OrgTool:      t,
		SecretHosts:  []string{},
		AllowedRoles: []string{},
	}
	_ = json.Unmarshal([]byte(t.ConfigJson), &out.Config)
	if out.Config.Headers == nil {
		out.Config.Headers = map[string]string{}
	}
	if out.Config.Env == nil {
		out.Config.Env = map[string]string{}
	}
	if out.Config.Args == nil {
		out.Config.Args = []string{}
	}
	_ = json.Unmarshal([]byte(t.SecretHostsJson), &out.SecretHosts)
	_ = json.Unmarshal([]byte(t.AllowedRolesJson), &out.AllowedRoles)
	if len(out.AllowedRoles) == 0 {
		out.AllowedRoles = []string{string(config.RoleAdmin), string(config.RoleUser)}
	}
	return out
}

// OrgToolPlaceholder returns the deterministic placeholder placed into sandbox
// MCP headers/env when an org tool uses auth_mode = 'org_shared'.
func OrgToolPlaceholder(toolID string) string {
	return "agenthold-org-placeholder-" + toolID
}

// OrgToolAAD returns the AEAD associated data for an org tool's encrypted_secret.
func OrgToolAAD(toolID string) []byte {
	return []byte("org-tool:" + toolID)
}

// SubstituteOrgSecret replaces {{org_secret}} with placeholder.
func SubstituteOrgSecret(s, placeholder string) string {
	if placeholder == "" {
		return s
	}
	return orgSecretRef.ReplaceAllString(s, placeholder)
}

// RoleAllowed reports whether role may see and use an org tool with allowedRoles.
func RoleAllowed(allowedRoles []string, role config.Role) bool {
	if role == config.RoleDisabled {
		return false
	}
	if len(allowedRoles) == 0 {
		return role == config.RoleAdmin || role == config.RoleUser
	}
	for _, r := range allowedRoles {
		if parsed, ok := config.ParseRole(r); ok && parsed == role {
			return true
		}
	}
	return false
}

// EffectiveOrgToolsForUser returns all org tools visible to user's role that are
// enabled for user (per user_tool_preferences or enabled_by_default).
func EffectiveOrgToolsForUser(ctx context.Context, st *store.Store, user db.User) ([]DecodedOrgTool, error) {
	role := authz.UserRole(user)
	if role == config.RoleDisabled {
		return nil, nil
	}
	all, err := st.ListOrgTools(ctx)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}
	prefs, err := st.ListUserToolPreferences(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	prefMap := make(map[string]bool, len(prefs))
	for _, p := range prefs {
		prefMap[p.OrgToolID] = p.Enabled != 0
	}
	var out []DecodedOrgTool
	for _, row := range all {
		d := DecodeOrgTool(row)
		if !RoleAllowed(d.AllowedRoles, role) {
			continue
		}
		enabled := row.EnabledByDefault != 0
		if pref, hasPref := prefMap[row.ID]; hasPref {
			enabled = pref
		}
		if !enabled {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// OrgToolACPServers builds ACP mcpServers entries for all enabled mcp_http and
// mcp_stdio org tools for user.
func OrgToolACPServers(tools []DecodedOrgTool, caps acp.McpCapabilities) []acp.McpServer {
	var out []acp.McpServer
	for _, t := range tools {
		ph := ""
		if t.AuthMode == OrgToolAuthOrgShared && len(t.EncryptedSecret) > 0 {
			ph = OrgToolPlaceholder(t.ID)
		}
		switch t.Kind {
		case OrgToolKindMCPHTTP:
			if !caps.Http || strings.TrimSpace(t.Config.URL) == "" {
				continue
			}
			headers := []acp.HttpHeader{}
			for _, k := range sortedKeys(t.Config.Headers) {
				headers = append(headers, acp.HttpHeader{
					Name:  k,
					Value: SubstituteOrgSecret(t.Config.Headers[k], ph),
				})
			}
			out = append(out, acp.McpServer{Http: &acp.McpServerHttpInline{
				Name:    t.Slug,
				Type:    "http",
				Url:     SubstituteOrgSecret(t.Config.URL, ph),
				Headers: headers,
			}})
		case OrgToolKindMCPStdio:
			if strings.TrimSpace(t.Config.Command) == "" {
				continue
			}
			env := []acp.EnvVariable{}
			for _, k := range sortedKeys(t.Config.Env) {
				env = append(env, acp.EnvVariable{
					Name:  k,
					Value: SubstituteOrgSecret(t.Config.Env[k], ph),
				})
			}
			args := make([]string, len(t.Config.Args))
			for i, a := range t.Config.Args {
				args[i] = SubstituteOrgSecret(a, ph)
			}
			out = append(out, acp.McpServer{Stdio: &acp.McpServerStdio{
				Name:    t.Slug,
				Command: t.Config.Command,
				Args:    args,
				Env:     env,
			}})
		}
	}
	return out
}

// OrgToolInjections resolves egress proxy placeholder injections for all
// enabled org_shared org tools accessible to owner. Real org secrets stay on
// the host and are only swapped into requests targeting secret_hosts.
func OrgToolInjections(ctx context.Context, st *store.Store, box *secrets.Box, owner db.User) ([]secrets.Injection, error) {
	if box == nil {
		return nil, nil
	}
	tools, err := EffectiveOrgToolsForUser(ctx, st, owner)
	if err != nil {
		return nil, err
	}
	var out []secrets.Injection
	for _, t := range tools {
		if t.AuthMode != OrgToolAuthOrgShared || len(t.EncryptedSecret) == 0 {
			continue
		}
		hosts := t.SecretHosts
		if len(hosts) == 0 && t.Kind == OrgToolKindMCPHTTP && t.Config.URL != "" {
			if u, err := url.Parse(t.Config.URL); err == nil && u.Hostname() != "" {
				hosts = []string{strings.ToLower(u.Hostname())}
			}
		}
		if len(hosts) == 0 {
			continue
		}
		val, err := box.Open(t.EncryptedSecret, OrgToolAAD(t.ID))
		if err != nil {
			continue
		}
		out = append(out, secrets.Injection{
			SecretID:    "org-tool:" + t.ID,
			Rule:        secrets.Rule{Hosts: hosts},
			Value:       string(val),
			Placeholder: OrgToolPlaceholder(t.ID),
		})
	}
	return out, nil
}

// ExecutorSyncer is the subset of *executor.Manager used to sync executor_app org tools.
type ExecutorSyncer interface {
	Available() bool
	Overview(ctx context.Context, userID string) (executor.Overview, error)
	ImportCatalogApp(ctx context.Context, userID, entry, name string) (executor.App, error)
}

// SyncExecutorOrgApps ensures every enabled executor_app org tool is installed
// in userID's Executor instance (starting Executor lazily if needed).
func SyncExecutorOrgApps(ctx context.Context, exec ExecutorSyncer, userID string, orgTools []DecodedOrgTool) error {
	if exec == nil || !exec.Available() {
		return nil
	}
	var wanted []DecodedOrgTool
	for _, t := range orgTools {
		if t.Kind == OrgToolKindExecutorApp && strings.TrimSpace(t.Config.ExecutorCatalogID) != "" {
			wanted = append(wanted, t)
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	ov, err := exec.Overview(ctx, userID)
	if err != nil {
		return err
	}
	installed := map[string]bool{}
	for _, app := range ov.Apps {
		installed[strings.ToLower(app.Slug)] = true
		installed[strings.ToLower(app.ID)] = true
		installed[strings.ToLower(app.Name)] = true
	}
	var firstErr error
	for _, t := range wanted {
		catID := strings.TrimSpace(t.Config.ExecutorCatalogID)
		if installed[strings.ToLower(catID)] || installed[strings.ToLower(t.Slug)] || installed[strings.ToLower(t.Name)] {
			continue
		}
		if _, err := exec.ImportCatalogApp(ctx, userID, catID, t.Name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SyncOrgConfig upserts declarative org.tools and org.skills from config.yaml
// into the database on server startup.
func SyncOrgConfig(ctx context.Context, st *store.Store, box *secrets.Box, cfg config.Org) error {
	now := time.Now().Unix()
	for _, tc := range cfg.Tools {
		slug := strings.TrimSpace(tc.Slug)
		name := strings.TrimSpace(tc.Name)
		authMode := strings.TrimSpace(tc.AuthMode)
		if authMode == "" {
			authMode = OrgToolAuthNone
		}
		enabledByDefault := int64(1)
		if tc.EnabledByDefault != nil && !*tc.EnabledByDefault {
			enabledByDefault = 0
		}
		roles := tc.AllowedRoles
		if len(roles) == 0 {
			roles = []string{string(config.RoleAdmin), string(config.RoleUser)}
		}
		rolesJSON, _ := json.Marshal(roles)

		toolCfg := OrgToolConfig{
			URL:               strings.TrimSpace(tc.URL),
			Headers:           tc.Headers,
			Command:           strings.TrimSpace(tc.Command),
			Args:              tc.Args,
			Env:               tc.Env,
			ExecutorCatalogID: strings.TrimSpace(tc.ExecutorCatalogID),
		}
		cfgJSON, _ := json.Marshal(toolCfg)

		hosts := make([]string, 0, len(tc.SecretHosts))
		for _, h := range tc.SecretHosts {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				hosts = append(hosts, h)
			}
		}
		hostsJSON, _ := json.Marshal(hosts)

		existing, err := st.GetOrgToolBySlug(ctx, slug)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			toolID := id.New("ot")
			var enc []byte
			if tc.Secret != "" && box != nil {
				enc, err = box.Seal([]byte(tc.Secret), OrgToolAAD(toolID))
				if err != nil {
					return fmt.Errorf("encrypt org tool %q secret: %w", slug, err)
				}
			}
			if err := st.CreateOrgTool(ctx, db.CreateOrgToolParams{
				ID:               toolID,
				Slug:             slug,
				Name:             name,
				Description:      strings.TrimSpace(tc.Description),
				Kind:             tc.Kind,
				AuthMode:         authMode,
				ConfigJson:       string(cfgJSON),
				EncryptedSecret:  enc,
				SecretHostsJson:  string(hostsJSON),
				EnabledByDefault: enabledByDefault,
				AllowedRolesJson: string(rolesJSON),
				CreatedBy:        sql.NullString{String: "config", Valid: true},
				CreatedAt:        now,
				UpdatedAt:        now,
			}); err != nil {
				return fmt.Errorf("seed org tool %q: %w", slug, err)
			}
		case err != nil:
			return err
		default:
			enc := existing.EncryptedSecret
			if tc.Secret != "" && box != nil {
				enc, err = box.Seal([]byte(tc.Secret), OrgToolAAD(existing.ID))
				if err != nil {
					return fmt.Errorf("encrypt org tool %q secret: %w", slug, err)
				}
			}
			if err := st.UpdateOrgTool(ctx, db.UpdateOrgToolParams{
				Slug:             slug,
				Name:             name,
				Description:      strings.TrimSpace(tc.Description),
				Kind:             tc.Kind,
				AuthMode:         authMode,
				ConfigJson:       string(cfgJSON),
				EncryptedSecret:  enc,
				SecretHostsJson:  string(hostsJSON),
				EnabledByDefault: enabledByDefault,
				AllowedRolesJson: string(rolesJSON),
				UpdatedAt:        now,
				ID:               existing.ID,
			}); err != nil {
				return fmt.Errorf("update org tool %q: %w", slug, err)
			}
		}
	}

	for _, sc := range cfg.Skills {
		name := strings.TrimSpace(sc.Name)
		enabledByDefault := int64(1)
		if sc.EnabledByDefault != nil && !*sc.EnabledByDefault {
			enabledByDefault = 0
		}
		existing, err := st.GetOrgSkillByName(ctx, name)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := st.CreateOrgSkill(ctx, db.CreateOrgSkillParams{
				ID:               id.New("osk"),
				Name:             name,
				Description:      strings.TrimSpace(sc.Description),
				Content:          sc.Content,
				EnabledByDefault: enabledByDefault,
				CreatedAt:        now,
				UpdatedAt:        now,
			}); err != nil {
				return fmt.Errorf("seed org skill %q: %w", name, err)
			}
		case err != nil:
			return err
		default:
			if err := st.UpdateOrgSkill(ctx, db.UpdateOrgSkillParams{
				Name:             name,
				Description:      strings.TrimSpace(sc.Description),
				Content:          sc.Content,
				EnabledByDefault: enabledByDefault,
				UpdatedAt:        now,
				ID:               existing.ID,
			}); err != nil {
				return fmt.Errorf("update org skill %q: %w", name, err)
			}
		}
	}
	return nil
}

// MergeSkills combines agent-attached user skills and enabled org skills.
// User skills win on name collisions; result is sorted by name.
func MergeSkills(userSkills []db.Skill, orgSkills []db.OrgSkill) []db.Skill {
	seen := make(map[string]bool, len(userSkills))
	out := make([]db.Skill, 0, len(userSkills)+len(orgSkills))
	for _, s := range userSkills {
		seen[s.Name] = true
		out = append(out, s)
	}
	for _, os := range orgSkills {
		if seen[os.Name] {
			continue
		}
		seen[os.Name] = true
		out = append(out, db.Skill{
			ID:          os.ID,
			Name:        os.Name,
			Description: os.Description,
			Content:     os.Content,
			CreatedAt:   os.CreatedAt,
			UpdatedAt:   os.UpdatedAt,
		})
	}
	slices.SortFunc(out, func(a, b db.Skill) int { return strings.Compare(a.Name, b.Name) })
	return out
}
