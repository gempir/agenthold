package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"github.com/jmespath-community/go-jmespath"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store/db"
)

// Role precedence (docs/auth explains it to operators):
//
//  1. The local admin (auth.local) is always Admin; no override applies.
//  2. An admin's override in the Overview wins over configuration.
//  3. The provider's gate: a strict provider (role_attribute_strict, or a
//     GitHub provider with github_allowed_ids) refuses a user its mapping
//     gives no role.
//  4. A verified e-mail address in auth.admins gives Admin, to a user the
//     gate let through and the mapping did not explicitly make Disabled.
//  5. The provider's own mapping: GitHub github_roles by user id, else
//     github_allowed_ids gives User; OIDC role_attribute_path.
//  6. Otherwise the provider's default_role, else auth.default_role
//     (default Disabled).
//
// Steps 3 to 6 run at every sign-in and again for every user at server start
// (configuration only changes with a restart). OIDC claims are not kept, so a
// start re-uses the result the mapping had at the user's last sign-in, as
// long as the mapping itself (its hash in identities.role_config) is the same.

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
// still reaches its second branch for a token without realm_access. Items
// are compared with reflect.DeepEqual: == on two maps or slices panics.
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
				if reflect.DeepEqual(item, args[1]) {
					return true, nil
				}
			}
		}
		return false, nil
	},
}

// errRoleEval is a role_attribute_path that could not be evaluated. The
// sign-in is refused: an expression that denies (… && 'Disabled' || 'User')
// must not fail open.
var errRoleEval = errors.New("role_attribute_path could not be evaluated")

// eval returns the role the expression yields for claims, or "" when it
// yields nothing or something that is not a role name.
func (e *roleExpr) eval(claims map[string]any) (role config.Role, err error) {
	if e == nil {
		return "", nil
	}
	defer func() {
		if r := recover(); r != nil {
			role, err = "", fmt.Errorf("%w: %v", errRoleEval, r)
		}
	}()
	out, err := e.jp.Search(claims)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errRoleEval, err)
	}
	s, ok := out.(string)
	if !ok {
		return "", nil
	}
	r, ok := config.ParseRole(s)
	if !ok {
		slog.Warn("role_attribute_path yields an unknown role (want Admin, User or Disabled)", "expr", e.src, "value", s)
		return "", nil
	}
	return r, nil
}

// mergeClaims overlays the ID token's claims on the userinfo response's,
// like Grafana: both are searched together, and the ID token wins a conflict.
func mergeClaims(idToken, userinfo map[string]any) map[string]any {
	out := make(map[string]any, len(idToken)+len(userinfo))
	for k, v := range userinfo {
		out[k] = v
	}
	for k, v := range idToken {
		out[k] = v
	}
	return out
}

// claimsMap decodes a JSON claims object for JMESPath.
func claimsMap(raw []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// roleConfigHash identifies the role mapping a stored provider_role came
// from: "" when the provider has none (so rows from before the hash existed
// match an unchanged empty configuration).
func roleConfigHash(p config.Provider) string {
	if p.Type != config.ProviderOIDC || strings.TrimSpace(p.RoleAttributePath) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(p.RoleAttributePath))
	return hex.EncodeToString(sum[:8])
}

// githubRole is the mapping for a GitHub account id.
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

// configRole applies steps 3 to 6. allowed is false when sign-in is refused.
func (s *Service) configRole(p config.Provider, providerRole config.Role, email string, emailVerified bool) (role config.Role, allowed bool) {
	// The provider's gate comes first: auth.admins does not open it.
	if providerRole == "" && strict(p) {
		return config.RoleDisabled, false
	}
	if emailVerified && s.admins.IsAdminEmail(email) && providerRole != config.RoleDisabled {
		return config.RoleAdmin, true
	}
	if providerRole != "" {
		return providerRole, true
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
// loads it, not at the user's next sign-in. An OIDC identity whose
// role_attribute_path changed since its last sign-in loses its stored
// mapping result and its sessions: the next sign-in evaluates the new one.
func (s *Service) ReconcileRoles(ctx context.Context) error {
	rows, err := s.store.ListIdentities(ctx)
	if err != nil {
		return err
	}
	type user struct {
		current string
		role    config.Role
		signOut bool
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
		if p, ok := s.providers[r.Provider]; ok && p.Info().Type == config.ProviderOIDC {
			if h := roleConfigHash(p.Info()); h != r.RoleConfig {
				if err := s.store.SetIdentityProviderRole(ctx, db.SetIdentityProviderRoleParams{
					ProviderRole: "", RoleConfig: h, Provider: r.Provider, Subject: r.Subject,
				}); err != nil {
					return err
				}
				r.ProviderRole, r.RoleConfig = "", h
				u.signOut = true
			}
		}
		role := s.identityRole(r)
		if role.Rank() > u.role.Rank() {
			u.role = role
		}
	}
	changed, signedOut := 0, 0
	for _, id := range order {
		u := users[id]
		if u.signOut {
			if err := s.store.DeleteUserSessions(ctx, id); err != nil {
				return err
			}
			signedOut++
		}
		if string(u.role) == u.current {
			continue
		}
		if err := s.store.SetUserRole(ctx, db.SetUserRoleParams{Role: string(u.role), ID: id}); err != nil {
			return err
		}
		changed++
		slog.Info("user role changed by configuration", "user", id, "from", u.current, "to", u.role)
	}
	if changed > 0 || signedOut > 0 {
		slog.Info("re-evaluated user roles", "users", len(order), "changed", changed, "signedOutForNewRoleMapping", signedOut)
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
