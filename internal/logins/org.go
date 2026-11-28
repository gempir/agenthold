package logins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store/db"
)

// Org provider login kinds.
const (
	OrgKindAPIKey     = "api_key"
	OrgKindOAuthToken = "oauth_token"
	OrgKindGateway    = "gateway"
)

// SupportedOrgProviders lists the built-in harness providers that support org-level logins.
var SupportedOrgProviders = []string{"claude-code", "codex", "antigravity", "opencode"}

// ValidOrgProvider reports whether provider is supported for org provider logins.
func ValidOrgProvider(provider string, allowTestHarness bool) bool {
	for _, p := range SupportedOrgProviders {
		if p == provider {
			return true
		}
	}
	return allowTestHarness && provider == "fake"
}

// ValidOrgLoginKind reports whether kind is a supported org provider login kind.
func ValidOrgLoginKind(kind string) bool {
	switch kind {
	case OrgKindAPIKey, OrgKindOAuthToken, OrgKindGateway:
		return true
	}
	return false
}

// OrgProviderAAD returns the AEAD associated data for an org provider login row.
func OrgProviderAAD(provider string) []byte {
	return []byte("org-provider:" + provider)
}

// OrgProviderPayload is encrypted and stored in org_provider_logins.encrypted_payload.
type OrgProviderPayload struct {
	Credential string   `json:"credential"`
	BaseURL    string   `json:"baseUrl,omitempty"`
	Header     string   `json:"header,omitempty"`
	Hosts      []string `json:"hosts,omitempty"`
	Seed       string   `json:"seed"`
}

// OrgLoginInput configures an organization-wide provider login.
type OrgLoginInput struct {
	Kind       string
	Label      string
	Credential string
	BaseURL    string
	Header     string
	Hosts      []string
	Enabled    bool
}

// SetOrgLogin creates or updates an org-wide provider login.
func (s *Service) SetOrgLogin(ctx context.Context, actorID, provider string, in OrgLoginInput, allowTestHarness bool) (db.OrgProviderLogin, error) {
	if !ValidOrgProvider(provider, allowTestHarness) {
		return db.OrgProviderLogin{}, ErrUnknownProvider
	}
	if !ValidOrgLoginKind(in.Kind) {
		return db.OrgProviderLogin{}, ErrUnknownKind
	}

	var prevPayload OrgProviderPayload
	existing, err := s.Store.GetOrgProviderLogin(ctx, provider)
	hasExisting := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return db.OrgProviderLogin{}, err
	}
	if hasExisting {
		prevPayload, err = s.openOrg(existing)
		if err != nil {
			return db.OrgProviderLogin{}, err
		}
	}

	cred := strings.TrimSpace(in.Credential)
	if cred == "" {
		if !hasExisting || prevPayload.Credential == "" {
			return db.OrgProviderLogin{}, &InputError{errors.New("credential is required")}
		}
		cred = prevPayload.Credential
	}
	if len(cred) > 64*1024 {
		return db.OrgProviderLogin{}, &InputError{errors.New("credential too long")}
	}
	if in.Kind != OrgKindOAuthToken && strings.ContainsAny(cred, "\r\n") {
		return db.OrgProviderLogin{}, &InputError{errors.New("credential must not contain line breaks")}
	}

	baseURL := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if in.Kind == OrgKindGateway {
		if baseURL == "" {
			return db.OrgProviderLogin{}, &InputError{errors.New("baseUrl is required for gateway logins")}
		}
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return db.OrgProviderLogin{}, &InputError{errors.New("baseUrl must be an http(s) URL")}
		}
	}
	header := strings.TrimSpace(in.Header)
	if header != "" && strings.ContainsAny(header, " :\r\n") {
		return db.OrgProviderLogin{}, &InputError{errors.New("header must be a valid HTTP header name")}
	}

	hosts := make([]string, 0, len(in.Hosts))
	for _, h := range in.Hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 && baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
			hosts = []string{strings.ToLower(u.Hostname())}
		}
	}

	seed := prevPayload.Seed
	if seed == "" {
		seed = id.Random(16)
	}
	payload := OrgProviderPayload{
		Credential: cred,
		BaseURL:    baseURL,
		Header:     header,
		Hosts:      hosts,
		Seed:       seed,
	}
	// Validate that the payload can produce a valid delivery before storing.
	if _, err := deliverOrgProvider(provider, in.Kind, payload); err != nil {
		return db.OrgProviderLogin{}, &InputError{err}
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return db.OrgProviderLogin{}, err
	}
	enc, err := s.Box.Seal(raw, OrgProviderAAD(provider))
	if err != nil {
		return db.OrgProviderLogin{}, err
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		label = defaultOrgLabel(in.Kind, cred, baseURL)
	}
	enabled := int64(0)
	if in.Enabled {
		enabled = 1
	}
	now := s.now().Unix()
	row := db.OrgProviderLogin{
		Provider:         provider,
		Kind:             in.Kind,
		Label:            label,
		EncryptedPayload: enc,
		Enabled:          enabled,
		UpdatedBy:        sql.NullString{String: actorID, Valid: actorID != ""},
		UpdatedAt:        now,
	}
	if err := s.Store.UpsertOrgProviderLogin(ctx, db.UpsertOrgProviderLoginParams(row)); err != nil {
		return db.OrgProviderLogin{}, err
	}
	s.Audit.Record(ctx, audit.Entry{
		UserID: actorID, Action: audit.OrgProviderPut, Target: provider,
		Details: map[string]any{"kind": in.Kind, "enabled": in.Enabled},
	})
	s.orgChanged(ctx, provider)
	return row, nil
}

// DeleteOrgLogin removes an org-wide provider login.
func (s *Service) DeleteOrgLogin(ctx context.Context, actorID, provider string, allowTestHarness bool) error {
	if !ValidOrgProvider(provider, allowTestHarness) {
		return ErrUnknownProvider
	}
	n, err := s.Store.DeleteOrgProviderLogin(ctx, provider)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotConnected
	}
	s.Audit.Record(ctx, audit.Entry{UserID: actorID, Action: audit.OrgProviderDelete, Target: provider})
	s.orgChanged(ctx, provider)
	return nil
}

func (s *Service) orgChanged(ctx context.Context, provider string) {
	if s.Invalidate != nil {
		s.Invalidate()
	}
	if s.Restart == nil {
		return
	}
	rows, err := s.Store.ListAllMachines(ctx)
	if err != nil {
		return
	}
	for _, r := range rows {
		if provider == "" || r.Agent.Harness == provider {
			s.Restart(r.Agent.ID)
		}
	}
}

func (s *Service) openOrg(l db.OrgProviderLogin) (OrgProviderPayload, error) {
	b, err := s.Box.Open(l.EncryptedPayload, OrgProviderAAD(l.Provider))
	if err != nil {
		return OrgProviderPayload{}, err
	}
	var p OrgProviderPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return OrgProviderPayload{}, fmt.Errorf("decode org provider payload: %w", err)
	}
	if p.Seed == "" {
		p.Seed = id.Random(16)
	}
	return p, nil
}

func defaultOrgLabel(kind, cred, baseURL string) string {
	switch kind {
	case OrgKindGateway:
		if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
			return "Gateway (" + u.Host + ")"
		}
		return "Gateway " + harness.Hint(cred)
	case OrgKindOAuthToken:
		return "OAuth token " + harness.Hint(cred)
	default:
		return "API key " + harness.Hint(cred)
	}
}

// deliverOrgProvider turns a decrypted org provider login into placeholder env
// vars and egress proxy injections (real credentials never enter the sandbox).
func deliverOrgProvider(provider, kind string, p OrgProviderPayload) (harness.Delivery, error) {
	switch provider {
	case "claude-code":
		switch kind {
		case OrgKindAPIKey:
			ph := harness.Placeholder("sk-ant-api03-", p.Seed)
			return harness.Delivery{
				Env: map[string]string{"ANTHROPIC_API_KEY": ph},
				Injections: []harness.Injection{{
					Hosts: []string{"api.anthropic.com"}, Header: "X-Api-Key", Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		case OrgKindOAuthToken:
			ph := harness.Placeholder("sk-ant-oat01-", p.Seed)
			return harness.Delivery{
				Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": ph},
				Injections: []harness.Injection{{
					Hosts: []string{"api.anthropic.com"}, Header: "Authorization", Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		case OrgKindGateway:
			if len(p.Hosts) == 0 {
				return harness.Delivery{}, errors.New("gateway requires baseUrl or hosts")
			}
			ph := harness.Placeholder("sk-ant-api03-", p.Seed)
			hdr := p.Header
			if hdr == "" {
				hdr = "X-Api-Key"
			}
			injs := []harness.Injection{{
				Hosts: p.Hosts, Header: hdr, Placeholder: ph, Value: p.Credential,
			}}
			if !strings.EqualFold(hdr, "Authorization") {
				injs = append(injs, harness.Injection{
					Hosts: p.Hosts, Header: "Authorization", Placeholder: ph, Value: p.Credential,
				})
			}
			return harness.Delivery{
				Env: map[string]string{
					"ANTHROPIC_API_KEY":  ph,
					"ANTHROPIC_BASE_URL": p.BaseURL,
				},
				Injections: injs,
			}, nil
		}
	case "codex":
		switch kind {
		case OrgKindAPIKey:
			ph := harness.Placeholder("sk-", p.Seed)
			return harness.Delivery{
				Env: map[string]string{"OPENAI_API_KEY": ph},
				Injections: []harness.Injection{{
					Hosts: []string{"api.openai.com"}, Header: "Authorization", Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		case OrgKindOAuthToken:
			if al, ok := Adapter("codex"); ok {
				return al.Deliver(harness.Credential{Kind: "access-token", Secret: []byte(p.Credential)}, p.Seed)
			}
			return harness.Delivery{}, errors.New("codex adapter unavailable")
		case OrgKindGateway:
			if len(p.Hosts) == 0 {
				return harness.Delivery{}, errors.New("gateway requires baseUrl or hosts")
			}
			ph := harness.Placeholder("sk-", p.Seed)
			hdr := p.Header
			if hdr == "" {
				hdr = "Authorization"
			}
			return harness.Delivery{
				Env: map[string]string{
					"OPENAI_API_KEY":  ph,
					"OPENAI_BASE_URL": p.BaseURL,
				},
				Injections: []harness.Injection{{
					Hosts: p.Hosts, Header: hdr, Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		}
	case "antigravity":
		switch kind {
		case OrgKindAPIKey:
			ph := harness.Placeholder("AIza", p.Seed)
			return harness.Delivery{
				Env: map[string]string{"GEMINI_API_KEY": ph},
				Injections: []harness.Injection{{
					Hosts: []string{"generativelanguage.googleapis.com"}, Header: "X-Goog-Api-Key", Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		case OrgKindOAuthToken:
			ph := harness.Placeholder("AIza", p.Seed)
			return harness.Delivery{
				Env: map[string]string{"GOOGLE_API_KEY": ph},
				Injections: []harness.Injection{{
					Hosts:  []string{"aiplatform.googleapis.com", "*.aiplatform.googleapis.com", "generativelanguage.googleapis.com"},
					Header: "X-Goog-Api-Key", Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		case OrgKindGateway:
			if len(p.Hosts) == 0 {
				return harness.Delivery{}, errors.New("gateway requires baseUrl or hosts")
			}
			ph := harness.Placeholder("AIza", p.Seed)
			hdr := p.Header
			if hdr == "" {
				hdr = "X-Goog-Api-Key"
			}
			env := map[string]string{"GEMINI_API_KEY": ph}
			if p.BaseURL != "" {
				env["GOOGLE_GEMINI_BASE_URL"] = p.BaseURL
			}
			return harness.Delivery{
				Env: env,
				Injections: []harness.Injection{{
					Hosts: p.Hosts, Header: hdr, Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		}
	case "opencode":
		if kind == OrgKindGateway {
			if len(p.Hosts) == 0 {
				return harness.Delivery{}, errors.New("gateway requires baseUrl or hosts")
			}
			ph := harness.Placeholder("sk-", p.Seed)
			hdr := p.Header
			if hdr == "" {
				hdr = "Authorization"
			}
			return harness.Delivery{
				Env: map[string]string{
					"OPENAI_API_KEY":  ph,
					"OPENAI_BASE_URL": p.BaseURL,
				},
				Injections: []harness.Injection{{
					Hosts: p.Hosts, Header: hdr, Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		}
		switch {
		case strings.HasPrefix(p.Credential, "sk-ant-"):
			ph := harness.Placeholder("sk-ant-api03-", p.Seed)
			return harness.Delivery{
				Env: map[string]string{"ANTHROPIC_API_KEY": ph},
				Injections: []harness.Injection{{
					Hosts: []string{"api.anthropic.com"}, Header: "X-Api-Key", Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		case strings.HasPrefix(p.Credential, "sk-or-"):
			ph := harness.Placeholder("sk-or-", p.Seed)
			return harness.Delivery{
				Env: map[string]string{"OPENROUTER_API_KEY": ph},
				Injections: []harness.Injection{{
					Hosts: []string{"openrouter.ai"}, Header: "Authorization", Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		default:
			ph := harness.Placeholder("sk-", p.Seed)
			return harness.Delivery{
				Env: map[string]string{"OPENAI_API_KEY": ph},
				Injections: []harness.Injection{{
					Hosts: []string{"api.openai.com"}, Header: "Authorization", Placeholder: ph, Value: p.Credential,
				}},
			}, nil
		}
	case "fake":
		if al, ok := Adapter("fake"); ok {
			return al.Deliver(harness.Credential{Kind: "token", Secret: []byte(p.Credential)}, p.Seed)
		}
	}
	return harness.Delivery{}, fmt.Errorf("unsupported org login combination: %s / %s", provider, kind)
}
