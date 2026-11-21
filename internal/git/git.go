// Package git keeps users' git settings: the commit identity their agents
// use and HTTPS credentials for git hosts.
//
// Credentials never enter a sandbox. They are sealed with the master key and
// the egress proxy adds them (as HTTP basic auth) to the requests an agent
// makes to the credential's host, so git over HTTPS just works inside. The
// commit identity is written into the guest at every boot.
package git

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// GuestGitConfigFile is the commit identity in the guest; the base image's
// system gitconfig includes it.
const GuestGitConfigFile = "/etc/agenthold/gitconfig"

// Limits.
const (
	maxAuthorLen   = 256
	maxUsernameLen = 256
	maxLabelLen    = 100
	maxTokenLen    = 16384
	maxHostLen     = 253
)

// Errors.
var (
	ErrNotFound = errors.New("git credential not found")
	// ErrInvalid is wrapped by every validation error; their messages are
	// meant for the user.
	ErrInvalid = errors.New("invalid git settings")
)

type invalidError string

func (e invalidError) Error() string        { return string(e) }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, args ...any) error {
	return invalidError(fmt.Sprintf(format, args...))
}

// Service manages git settings.
type Service struct {
	Store *store.Store
	Box   *secrets.Box
	Audit *audit.Log
	// Invalidate is called after credentials change (the egress directory's
	// cache and the controller configuration).
	Invalidate func()
	Now        func() time.Time
}

// Credential describes a stored credential; the token is never returned.
type Credential struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Username  string `json:"username"`
	Label     string `json:"label"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Settings are a user's git settings. AuthorName and AuthorEmail are what
// the user set (empty: the default, their profile's name and email).
type Settings struct {
	AuthorName         string       `json:"authorName"`
	AuthorEmail        string       `json:"authorEmail"`
	DefaultAuthorName  string       `json:"defaultAuthorName"`
	DefaultAuthorEmail string       `json:"defaultAuthorEmail"`
	Credentials        []Credential `json:"credentials"`
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) invalidate() {
	if s.Invalidate != nil {
		s.Invalidate()
	}
}

func toCredential(c db.GitCredential) Credential {
	return Credential{ID: c.ID, Host: c.Host, Username: c.Username, Label: c.Label, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// GetSettings returns user's git settings.
func (s *Service) GetSettings(ctx context.Context, user db.User) (Settings, error) {
	out := Settings{DefaultAuthorName: user.Name, DefaultAuthorEmail: user.Email, Credentials: []Credential{}}
	cfg, err := s.Store.GetUserGitConfig(ctx, user.ID)
	switch {
	case err == nil:
		out.AuthorName, out.AuthorEmail = cfg.AuthorName, cfg.AuthorEmail
	case !errors.Is(err, sql.ErrNoRows):
		return Settings{}, err
	}
	creds, err := s.Store.ListGitCredentials(ctx, user.ID)
	if err != nil {
		return Settings{}, err
	}
	for _, c := range creds {
		out.Credentials = append(out.Credentials, toCredential(c))
	}
	return out, nil
}

// singleLine rejects values that would break a config file or a header.
func singleLine(field, v string, maxLen int) error {
	if len(v) > maxLen {
		return invalid("%s is too long (at most %d bytes)", field, maxLen)
	}
	if strings.ContainsAny(v, "\r\n\x00") {
		return invalid("%s cannot contain line breaks", field)
	}
	return nil
}

// UpdateConfig sets the user's commit identity (empty fields: the default).
func (s *Service) UpdateConfig(ctx context.Context, userID, authorName, authorEmail string) error {
	authorName, authorEmail = strings.TrimSpace(authorName), strings.TrimSpace(authorEmail)
	if err := singleLine("author name", authorName, maxAuthorLen); err != nil {
		return err
	}
	if err := singleLine("author email", authorEmail, maxAuthorLen); err != nil {
		return err
	}
	if strings.ContainsAny(authorEmail, "<> ") {
		return invalid("author email %q is not a valid address", authorEmail)
	}
	if err := s.Store.UpsertUserGitConfig(ctx, db.UpsertUserGitConfigParams{
		UserID: userID, AuthorName: authorName, AuthorEmail: authorEmail, UpdatedAt: s.now().Unix(),
	}); err != nil {
		return err
	}
	s.Audit.Record(ctx, audit.Entry{
		UserID: userID, Action: audit.GitConfigUpdate, Target: userID,
		Details: map[string]any{"authorName": authorName, "authorEmail": authorEmail},
	})
	return nil
}

// NormalizeHost turns what a user entered ("github.com", "https://GitHub.com/")
// into a host name, or fails. IP addresses, ports, wildcards and paths are
// not accepted.
func NormalizeHost(raw string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(raw))
	for _, p := range []string{"https://", "http://"} {
		h = strings.TrimPrefix(h, p)
	}
	h = strings.TrimSuffix(h, "/")
	h = strings.TrimSuffix(h, ".")
	bad := invalid("%q is not a valid host name (like github.com)", raw)
	if h == "" || len(h) > maxHostLen {
		return "", bad
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "", bad
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", bad
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", bad
			}
		}
	}
	// A numeric top-level label is an IP address, not a host name.
	tld := labels[len(labels)-1]
	if strings.Trim(tld, "0123456789") == "" {
		return "", bad
	}
	return h, nil
}

// DefaultUsername is the basic-auth user a host expects with a token.
func DefaultUsername(host string) string {
	switch host {
	case "github.com":
		return "x-access-token"
	case "gitlab.com":
		return "oauth2"
	}
	return "git"
}

// UpsertCredential stores the token for host (replacing the user's existing
// one for that host).
func (s *Service) UpsertCredential(ctx context.Context, userID, host, username, token, label string) (Credential, error) {
	host, err := NormalizeHost(host)
	if err != nil {
		return Credential{}, err
	}
	username, label = strings.TrimSpace(username), strings.TrimSpace(label)
	if username == "" {
		username = DefaultUsername(host)
	}
	if err := singleLine("username", username, maxUsernameLen); err != nil {
		return Credential{}, err
	}
	if strings.ContainsAny(username, ": \t") {
		return Credential{}, invalid("username cannot contain colons or spaces")
	}
	if err := singleLine("label", label, maxLabelLen); err != nil {
		return Credential{}, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return Credential{}, invalid("token is required")
	}
	if err := singleLine("token", token, maxTokenLen); err != nil {
		return Credential{}, err
	}

	now := s.now().Unix()
	existing, err := s.Store.GetGitCredentialByHost(ctx, db.GetGitCredentialByHostParams{UserID: userID, Host: host})
	switch {
	case err == nil:
		ct, err := s.Box.Seal([]byte(token), []byte(existing.ID))
		if err != nil {
			return Credential{}, err
		}
		if err := s.Store.UpdateGitCredential(ctx, db.UpdateGitCredentialParams{
			Username: username, Ciphertext: ct, Label: label, UpdatedAt: now, ID: existing.ID,
		}); err != nil {
			return Credential{}, err
		}
		existing.Username, existing.Ciphertext, existing.Label, existing.UpdatedAt = username, ct, label, now
	case errors.Is(err, sql.ErrNoRows):
		credID := id.New("gcr")
		ct, err := s.Box.Seal([]byte(token), []byte(credID))
		if err != nil {
			return Credential{}, err
		}
		existing = db.GitCredential{
			ID: credID, UserID: userID, Host: host, Username: username, Ciphertext: ct,
			Label: label, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.Store.CreateGitCredential(ctx, db.CreateGitCredentialParams{
			ID: credID, UserID: userID, Host: host, Username: username, Ciphertext: ct, Label: label,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return Credential{}, err
		}
	default:
		return Credential{}, err
	}
	s.invalidate()
	s.Audit.Record(ctx, audit.Entry{
		UserID: userID, Action: audit.GitCredentialPut, Target: existing.ID,
		Details: map[string]any{"host": host, "username": username, "label": label},
	})
	return toCredential(existing), nil
}

// DeleteCredential removes one of the user's credentials.
func (s *Service) DeleteCredential(ctx context.Context, userID, credID string) error {
	n, err := s.Store.DeleteGitCredential(ctx, db.DeleteGitCredentialParams{ID: credID, UserID: userID})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.invalidate()
	s.Audit.Record(ctx, audit.Entry{UserID: userID, Action: audit.GitCredentialDelete, Target: credID})
	return nil
}

// credentialHosts are the hosts a credential for host is sent to.
func credentialHosts(host string) []string {
	if host == "github.com" {
		// The GitHub API takes the same tokens (gh, API calls from scripts).
		return []string{host, "api.github.com"}
	}
	return []string{host}
}

// Egress returns the injections of the agent owner's git credentials: HTTP
// basic auth for each credential's host. Credentials that cannot be opened
// are left out and reported in the error.
func (s *Service) Egress(ctx context.Context, ag db.Agent) ([]secrets.Injection, error) {
	creds, err := s.Store.ListGitCredentials(ctx, ag.OwnerID)
	if err != nil {
		return nil, err
	}
	var out []secrets.Injection
	var errs []error
	for _, c := range creds {
		token, err := s.Box.Open(c.Ciphertext, []byte(c.ID))
		if err != nil {
			errs = append(errs, fmt.Errorf("git credential %s: %w", c.ID, err))
			continue
		}
		out = append(out, secrets.Injection{
			SecretID: "git:" + c.ID,
			Rule:     secrets.Rule{Hosts: credentialHosts(c.Host), Header: "Authorization", Template: "Basic {{.Value}}"},
			Value:    base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + string(token))),
		})
	}
	return out, errors.Join(errs...)
}

// Identity returns the commit identity of the agent's owner: their git
// settings, falling back field by field to their profile.
func (s *Service) Identity(ctx context.Context, agentID string) (name, email string, err error) {
	ag, err := s.Store.GetAgent(ctx, agentID)
	if err != nil {
		return "", "", err
	}
	owner, err := s.Store.GetUser(ctx, ag.OwnerID)
	if err != nil {
		return "", "", err
	}
	name, email = owner.Name, owner.Email
	cfg, err := s.Store.GetUserGitConfig(ctx, owner.ID)
	switch {
	case err == nil:
		if cfg.AuthorName != "" {
			name = cfg.AuthorName
		}
		if cfg.AuthorEmail != "" {
			email = cfg.AuthorEmail
		}
	case !errors.Is(err, sql.ErrNoRows):
		return "", "", err
	}
	return name, email, nil
}

// BootFiles returns the guest's gitconfig with the owner's commit identity.
// It is always written (empty without an identity), so a reboot clears a
// stale one.
func (s *Service) BootFiles(ctx context.Context, agentID string) (map[string][]byte, error) {
	name, email, err := s.Identity(ctx, agentID)
	if err != nil {
		return nil, err
	}
	cfg, err := RenderConfig(name, email)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{GuestGitConfigFile: cfg}, nil
}

// RenderConfig renders a gitconfig with a [user] section for the non-empty
// values (nothing when both are empty).
func RenderConfig(name, email string) ([]byte, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" && email == "" {
		return []byte{}, nil
	}
	var b strings.Builder
	b.WriteString("# Written by agenthold at every boot; set it in your account's git settings.\n[user]\n")
	for _, kv := range [][2]string{{"name", name}, {"email", email}} {
		if kv[1] == "" {
			continue
		}
		v, err := quoteValue(kv[1])
		if err != nil {
			return nil, err
		}
		b.WriteString("\t" + kv[0] + " = " + v + "\n")
	}
	return []byte(b.String()), nil
}

// quoteValue quotes a gitconfig value: in double quotes, backslashes and
// quotes escaped, line breaks refused.
func quoteValue(v string) (string, error) {
	if strings.ContainsAny(v, "\r\n\x00") {
		return "", invalid("git config values cannot contain line breaks")
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`, nil
}
