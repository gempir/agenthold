package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store/db"
)

const maxSecretValue = 16 * 1024

var envVarRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// reservedEnv are variables the platform sets; secrets may not override them.
var reservedEnv = map[string]bool{
	"HOME": true, "PATH": true, "USER": true, "SHELL": true, "TERM": true,
}

func init() {
	for k := range egress.SandboxEnv("0") {
		reservedEnv[strings.ToUpper(k)] = true
	}
}

func apiRule(r secrets.Rule) gen.SecretRule {
	return gen.SecretRule{Hosts: append([]string{}, r.Hosts...), Header: r.Header, Template: r.Template}
}

func ruleFrom(r gen.SecretRule) secrets.Rule {
	hosts := make([]string, 0, len(r.Hosts))
	for _, h := range r.Hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	return secrets.Rule{Hosts: hosts, Header: strings.TrimSpace(r.Header), Template: r.Template}
}

func (a *API) apiSecret(ctx context.Context, s db.Secret, canManage bool) (gen.Secret, error) {
	var rule secrets.Rule
	if err := json.Unmarshal([]byte(s.Rule), &rule); err != nil {
		return gen.Secret{}, err
	}
	owner, err := a.Store.GetUser(ctx, s.OwnerID)
	if err != nil {
		return gen.Secret{}, err
	}
	return gen.Secret{
		Id: s.ID, Name: s.Name, Rule: apiRule(rule), Placeholder: s.Placeholder,
		Owner: apiUser(owner), CanManage: canManage, CreatedAt: ts(s.CreatedAt), UpdatedAt: ts(s.UpdatedAt),
	}, nil
}

// canManageSecret: secrets belong to one user; only they manage and attach it.
func canManageSecret(userID string, s db.Secret) bool {
	return userID != "" && s.OwnerID == userID
}

// GetEgressCA implements gen.StrictServerInterface.
func (a *API) GetEgressCA(context.Context, gen.GetEgressCARequestObject) (gen.GetEgressCAResponseObject, error) {
	pem := a.CA.CertPEM()
	return gen.GetEgressCA200ApplicationxPemFileResponse{Body: bytes.NewReader(pem), ContentLength: int64(len(pem))}, nil
}

// ListSecretPresets implements gen.StrictServerInterface.
func (a *API) ListSecretPresets(context.Context, gen.ListSecretPresetsRequestObject) (gen.ListSecretPresetsResponseObject, error) {
	out := gen.ListSecretPresets200JSONResponse{}
	for _, p := range secrets.Presets {
		out = append(out, gen.SecretPreset{Id: p.ID, Name: p.Name, Rule: apiRule(p.Rule), EnvVar: p.EnvVar, Description: p.Description})
	}
	return out, nil
}

// ListSecrets implements gen.StrictServerInterface.
func (a *API) ListSecrets(ctx context.Context, _ gen.ListSecretsRequestObject) (gen.ListSecretsResponseObject, error) {
	rows, err := a.Store.ListUserSecrets(ctx, me(ctx).ID)
	if err != nil {
		return nil, err
	}
	out := gen.ListSecrets200JSONResponse{}
	for _, s := range rows {
		g, err := a.apiSecret(ctx, s, true)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func validSecretName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len(n) > 100 {
		return "", badRequest("name must be 1 to 100 characters")
	}
	return n, nil
}

func validSecretValue(v string) error {
	if v == "" || len(v) > maxSecretValue {
		return badRequest("value must be 1 to 16384 bytes")
	}
	if strings.ContainsAny(v, "\r\n") {
		return badRequest("value must not contain line breaks")
	}
	return nil
}

// CreateSecret implements gen.StrictServerInterface.
func (a *API) CreateSecret(ctx context.Context, req gen.CreateSecretRequestObject) (gen.CreateSecretResponseObject, error) {
	u := me(ctx)
	b := req.Body
	name, err := validSecretName(b.Name)
	if err != nil {
		return nil, err
	}
	if err := validSecretValue(b.Value); err != nil {
		return nil, err
	}
	var rule secrets.Rule
	switch {
	case b.Preset != nil && *b.Preset != "":
		found := false
		for _, p := range secrets.Presets {
			if p.ID == *b.Preset {
				rule, found = p.Rule, true
			}
		}
		if !found {
			return nil, badRequest("unknown preset")
		}
	case b.Rule != nil:
		rule = ruleFrom(*b.Rule)
	default:
		return nil, badRequest("a preset or a rule is required")
	}
	if err := rule.Validate(); err != nil {
		return nil, badRequest(err.Error())
	}
	secretID := id.New("sec")
	ct, err := a.Box.Seal([]byte(b.Value), []byte(secretID))
	if err != nil {
		return nil, err
	}
	ruleJSON, _ := json.Marshal(rule)
	now := a.Now().Unix()
	s := db.Secret{
		ID: secretID, OwnerID: u.ID, Name: name, Ciphertext: ct,
		Rule: string(ruleJSON), Placeholder: "agenthold-placeholder-" + id.Random(16), CreatedAt: now, UpdatedAt: now,
	}
	if err := a.Store.CreateSecret(ctx, db.CreateSecretParams{
		ID: s.ID, OwnerID: s.OwnerID, Name: s.Name, Ciphertext: s.Ciphertext,
		Rule: s.Rule, Placeholder: s.Placeholder, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errStatus(http.StatusConflict, "a secret with this name already exists")
		}
		return nil, err
	}
	g, err := a.apiSecret(ctx, s, true)
	if err != nil {
		return nil, err
	}
	return gen.CreateSecret201JSONResponse(g), nil
}

// loadSecret returns a secret the user may manage.
func (a *API) loadSecret(ctx context.Context, secretID string) (db.Secret, error) {
	u := me(ctx)
	s, err := a.Store.GetSecret(ctx, secretID)
	if errors.Is(err, sql.ErrNoRows) {
		return s, errNotFound
	}
	if err != nil {
		return s, err
	}
	if !canManageSecret(u.ID, s) {
		return s, errNotFound // other users' secrets are invisible
	}
	return s, nil
}

// UpdateSecret implements gen.StrictServerInterface.
func (a *API) UpdateSecret(ctx context.Context, req gen.UpdateSecretRequestObject) (gen.UpdateSecretResponseObject, error) {
	s, err := a.loadSecret(ctx, req.SecretId)
	if err != nil {
		return nil, err
	}
	b := req.Body
	if b.Name != nil {
		if s.Name, err = validSecretName(*b.Name); err != nil {
			return nil, err
		}
	}
	if b.Value != nil && *b.Value != "" {
		if err := validSecretValue(*b.Value); err != nil {
			return nil, err
		}
		if s.Ciphertext, err = a.Box.Seal([]byte(*b.Value), []byte(s.ID)); err != nil {
			return nil, err
		}
	}
	if b.Rule != nil {
		rule := ruleFrom(*b.Rule)
		if err := rule.Validate(); err != nil {
			return nil, badRequest(err.Error())
		}
		j, _ := json.Marshal(rule)
		s.Rule = string(j)
	}
	s.UpdatedAt = a.Now().Unix()
	if err := a.Store.UpdateSecret(ctx, db.UpdateSecretParams{Name: s.Name, Ciphertext: s.Ciphertext, Rule: s.Rule, UpdatedAt: s.UpdatedAt, ID: s.ID}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errStatus(http.StatusConflict, "a secret with this name already exists")
		}
		return nil, err
	}
	a.Egress.Invalidate()
	g, err := a.apiSecret(ctx, s, true)
	if err != nil {
		return nil, err
	}
	return gen.UpdateSecret200JSONResponse(g), nil
}

// DeleteSecret implements gen.StrictServerInterface.
func (a *API) DeleteSecret(ctx context.Context, req gen.DeleteSecretRequestObject) (gen.DeleteSecretResponseObject, error) {
	s, err := a.loadSecret(ctx, req.SecretId)
	if err != nil {
		return nil, err
	}
	if err := a.Store.DeleteSecret(ctx, s.ID); err != nil {
		return nil, err
	}
	a.Egress.Invalidate()
	return gen.DeleteSecret204Response{}, nil
}

// ListAgentSecrets implements gen.StrictServerInterface.
func (a *API) ListAgentSecrets(ctx context.Context, req gen.ListAgentSecretsRequestObject) (gen.ListAgentSecretsResponseObject, error) {
	if _, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentRead); err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAgentSecrets(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	out := gen.ListAgentSecrets200JSONResponse{}
	for _, r := range rows {
		g, err := a.apiSecret(ctx, r.Secret, canManageSecret(me(ctx).ID, r.Secret))
		if err != nil {
			return nil, err
		}
		out = append(out, gen.AttachedSecret{Secret: g, EnvVar: r.EnvVar})
	}
	return out, nil
}

// AttachSecret implements gen.StrictServerInterface.
func (a *API) AttachSecret(ctx context.Context, req gen.AttachSecretRequestObject) (gen.AttachSecretResponseObject, error) {
	u := me(ctx)
	acc, err := a.authz.Agent(ctx, u.ID, req.AgentId, authz.AgentSecrets)
	if err != nil {
		return nil, err
	}
	// Only your own secrets, on agents you own.
	s, err := a.Store.GetSecret(ctx, req.SecretId)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !canManageSecret(u.ID, s)) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}

	envVar := ""
	if req.Body != nil && req.Body.EnvVar != nil {
		envVar = strings.TrimSpace(*req.Body.EnvVar)
	}
	if envVar != "" && (!envVarRe.MatchString(envVar) || reservedEnv[strings.ToUpper(envVar)]) {
		return nil, badRequest("envVar must be a valid, non-reserved environment variable name")
	}
	if err := a.Store.AttachSecret(ctx, db.AttachSecretParams{AgentID: req.AgentId, SecretID: s.ID, EnvVar: envVar, CreatedAt: a.Now().Unix()}); err != nil {
		return nil, err
	}
	a.Egress.Invalidate()
	a.record(ctx, acc.Row.Agent, audit.SecretAttach, s.Name, map[string]any{"secretId": s.ID, "envVar": envVar})
	g, err := a.apiSecret(ctx, s, true)
	if err != nil {
		return nil, err
	}
	return gen.AttachSecret200JSONResponse{Secret: g, EnvVar: envVar}, nil
}

// DetachSecret implements gen.StrictServerInterface.
func (a *API) DetachSecret(ctx context.Context, req gen.DetachSecretRequestObject) (gen.DetachSecretResponseObject, error) {
	acc, err := a.authz.Agent(ctx, me(ctx).ID, req.AgentId, authz.AgentSecrets)
	if err != nil {
		return nil, err
	}
	n, err := a.Store.DetachSecret(ctx, db.DetachSecretParams{AgentID: req.AgentId, SecretID: req.SecretId})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	a.Egress.Invalidate()
	a.record(ctx, acc.Row.Agent, audit.SecretDetach, req.SecretId, nil)
	return gen.DetachSecret204Response{}, nil
}
