package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jmespath-community/go-jmespath"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store/db"
)

// Role precedence (docs/auth explains it to operators):
//
//  1. The local admin (auth.local) is always Admin; no override applies.
//  2. An admin's override in the Overview wins over configuration.
//  3. A verified e-mail address in auth.admins gives Admin.
//  4. The provider's own mapping: GitHub github_roles by user id, else
//     github_allowed_ids gives User; OIDC role_attribute_path.
//  5. Nothing matched: a strict provider (role_attribute_strict, or a GitHub
//     provider with github_allowed_ids) refuses sign-in; otherwise the
//     provider's default_role, else auth.default_role (default Disabled).
//
// Steps 3 to 5 run at every sign-in and again for every user at server start
// (configuration only changes with a restart). OIDC claims are not kept, so a
// start re-uses the result step 4 had at the user's last sign-in.

// roleExpr evaluates an OIDC role_attribute_path.
type roleExpr struct {
	src string
	jp  jmespath.JMESPath
}

func compileRoleExpr(src string) (*roleExpr, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	jp, err := jmespath.Compile(src, lenientContains)
	if err != nil {
		return nil, fmt.Errorf("role_attribute_path: %w", err)
	}
	return &roleExpr{src: src, jp: jp}, nil
}

// lenientContains is JMESPath's contains() that answers false for a missing
// claim (null) instead of failing the whole expression, so
// "contains(realm_access.roles[*], 'ops') && 'Admin' || contains(groups[*], 'x') && 'User'"
// still reaches its second branch for a token without realm_access.
var lenientContains = jmespath.FunctionEntry{
	Name:      "contains",
	Arguments: []jmespath.ArgSpec{{Types: []jmespath.JpType{jmespath.JpAny}}, {Types: []jmespath.JpType{jmespath.JpAny}}},
	Handler: func(args []any) (any, error) {
		switch subject := args[0].(type) {
		case string:
			el, ok := args[1].(string)
			return ok && strings.Contains(subject, el), nil
		case []any:
			for _, item := range subject {
				if item == args[1] {
					return true, nil
				}
			}
		}
		return false, nil
	},
}

// eval returns the role the expression yields for claims, or "" when it
// yields nothing or something that is not a role name.
func (e *roleExpr) eval(claims map[string]any) config.Role {
	if e == nil || claims == nil {
		return ""
	}
	out, err := e.jp.Search(claims)
	if err != nil {
		slog.Warn("role_attribute_path failed", "expr", e.src, "err", err)
		return ""
	}
	s, ok := out.(string)
	if !ok {
		return ""
	}
	r, ok := config.ParseRole(s)
	if !ok {
		slog.Warn("role_attribute_path yields an unknown role (want Admin, User or Disabled)", "expr", e.src, "value", s)
		return ""
	}
	return r
}

// claimsMap decodes a JSON claims object for JMESPath (numbers stay
// json.Number-free float64, as JMESPath expects).
func claimsMap(raw []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// githubRole is step 4 for a GitHub account id.
func githubRole(p config.Provider, id string) config.Role {
	for _, role := range []config.Role{config.RoleAdmin, config.RoleUser, config.RoleDisabled} {
		if contains(p.GitHubRoles[role], id) {
			return role
		}
	}
	if contains(p.GitHubAllowedIDs, id) {
		return config.RoleUser
	}
	return ""
}

// strict reports whether a user nothing matched may not sign in at all.
func strict(p config.Provider) bool {
	return p.RoleAttributeStrict || (p.Type == config.ProviderGitHub && len(p.GitHubAllowedIDs) > 0)
}

// configRole applies steps 3 to 5. allowed is false when sign-in is refused.
func (s *Service) configRole(p config.Provider, providerRole config.Role, email string, emailVerified bool) (role config.Role, allowed bool) {
	if emailVerified && s.admins.IsAdminEmail(email) {
		return config.RoleAdmin, true
	}
	if providerRole != "" {
		return providerRole, true
	}
	if strict(p) {
		return config.RoleDisabled, false
	}
	if p.DefaultRole != "" {
		return p.DefaultRole, true
	}
	if s.admins.DefaultRole != "" {
		return s.admins.DefaultRole, true
	}
	return config.RoleDisabled, true
}

// ReconcileRoles re-evaluates every user's configured role against the
// current configuration. The server calls it at start, before serving, so a
// role removed from the config file takes effect with the restart that
// loads it, not at the user's next sign-in.
func (s *Service) ReconcileRoles(ctx context.Context) error {
	rows, err := s.store.ListIdentities(ctx)
	if err != nil {
		return err
	}
	type user struct {
		current string
		role    config.Role
	}
	users := map[string]*user{}
	var order []string
	for _, r := range rows {
		u := users[r.UserID]
		if u == nil {
			u = &user{current: r.UserRole, role: config.RoleDisabled}
			users[r.UserID] = u
			order = append(order, r.UserID)
		}
		role := s.identityRole(r)
		if role.Rank() > u.role.Rank() {
			u.role = role
		}
	}
	changed := 0
	for _, id := range order {
		u := users[id]
		if string(u.role) == u.current {
			continue
		}
		if err := s.store.SetUserRole(ctx, db.SetUserRoleParams{Role: string(u.role), ID: id}); err != nil {
			return err
		}
		changed++
		slog.Info("user role changed by configuration", "user", id, "from", u.current, "to", u.role)
	}
	if changed > 0 {
		slog.Info("re-evaluated user roles", "users", len(order), "changed", changed)
	}
	return nil
}

func (s *Service) identityRole(r db.ListIdentitiesRow) config.Role {
	if r.Provider == config.LocalProviderID {
		if s.local != nil && r.Subject == s.local.username {
			return config.RoleAdmin
		}
		return config.RoleDisabled
	}
	p, ok := s.providers[r.Provider]
	if !ok {
		return config.RoleDisabled // the provider is gone: nobody signs in with it
	}
	info := p.Info()
	var providerRole config.Role
	switch info.Type {
	case config.ProviderGitHub:
		providerRole = githubRole(info, r.Subject)
	default:
		if info.RoleAttributePath != "" {
			providerRole, _ = config.ParseRole(r.ProviderRole)
		}
	}
	role, allowed := s.configRole(info, providerRole, r.UserEmail, r.UserEmailVerified == 1)
	if !allowed {
		return config.RoleDisabled
	}
	return role
}
