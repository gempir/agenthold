package harness

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Account logins.
//
// A user connects a provider login (Claude Code, Codex) once on their
// account. The server stores it encrypted and delivers it to every agent that
// uses that user's logins when the harness starts. Adapters decide what can be
// connected and how it reaches the harness; the platform keeps the values out
// of the sandbox wherever the harness allows it: the sandbox only sees
// placeholders, and the egress proxy swaps them for the real values.

// PlaceholderMarker is part of every placeholder an adapter builds, so files
// written for an account login can be recognized later.
const PlaceholderMarker = "agenthold-placeholder-"

// LoginKind is one way to connect a provider login.
type LoginKind struct {
	ID          string
	Name        string
	Description string
	// Command is what the user runs on their own computer to obtain the
	// credential (shown in the UI), if any.
	Command string
	// InputLabel names what the user pastes.
	InputLabel string
	// Multiline means the input is a file's contents rather than one token.
	Multiline bool
}

// Credential is a stored account login. Secret is encrypted at rest and
// never leaves the server; the other fields are shown to the user.
type Credential struct {
	Kind   string
	Secret []byte
	// Label describes the login without revealing it (a key's last
	// characters, an e-mail address, a plan).
	Label string
	// ExpiresAt is when the stored credential stops working (zero: unknown).
	ExpiresAt time.Time
}

// Injection is one value the egress proxy puts into requests: it replaces
// Placeholder with Value inside Header, and only there, on requests to Hosts
// (and, if set, to paths starting with one of PathPrefixes).
type Injection struct {
	Hosts        []string
	Header       string
	PathPrefixes []string
	Placeholder  string
	Value        string
}

// Intercept is a request the platform answers itself instead of letting it
// reach the provider: the harness's own token refresh, which must never use
// the account's refresh token from inside a sandbox.
type Intercept struct {
	Host, Method, Path string
	// Placeholder must appear in the request body; other requests pass.
	Placeholder string
}

// Delivery is how a credential reaches a harness. It holds placeholders
// only, except for Injection values, which stay in the egress proxy.
type Delivery struct {
	// Env is added to the harness's environment.
	Env map[string]string
	// Files are written (relative to the agent's home) before the harness starts.
	Files      map[string][]byte
	Injections []Injection
	Intercepts []Intercept
	// InSandbox is set when the real credential has to live inside the
	// sandbox (the agent can read it).
	InSandbox bool
}

// AccountLoginer is implemented by adapters whose provider login can live on
// the user's account instead of inside each sandbox.
type AccountLoginer interface {
	LoginKinds() []LoginKind
	// ParseLogin validates what the user provided for kind. It may call the
	// provider through client (to verify the login or take it over).
	ParseLogin(ctx context.Context, client *http.Client, kind, input string) (Credential, error)
	// Deliver returns how c reaches the harness. seed is a random string
	// that stays the same for the stored login; placeholders derive from it.
	Deliver(c Credential, seed string) (Delivery, error)
	// ManagedFiles are files Deliver may write. When an agent no longer uses
	// an account login, these are removed if they hold placeholders.
	ManagedFiles() []string
}

// Refresher is implemented by adapters whose stored credentials expire and
// can be renewed. The server is the only place that refreshes them, so many
// sandboxes can share one login with a rotating refresh token.
type Refresher interface {
	// RefreshDue reports whether c should be refreshed now.
	RefreshDue(c Credential, now time.Time) bool
	// Refresh renews c with the provider. Errors wrapping ErrLoginRevoked
	// mean the user has to connect again.
	Refresh(ctx context.Context, client *http.Client, c Credential) (Credential, error)
	// InterceptResponse is the body answering an Intercept, after the
	// server refreshed (placeholders only).
	InterceptResponse(c Credential, seed string) ([]byte, error)
}

// ErrLoginRevoked marks a stored login the provider no longer accepts.
var ErrLoginRevoked = errors.New("the provider no longer accepts this login; connect it again")

// FindKind returns the kind with id.
func FindKind(a AccountLoginer, id string) (LoginKind, bool) {
	for _, k := range a.LoginKinds() {
		if k.ID == id {
			return k, true
		}
	}
	return LoginKind{}, false
}

// Hint returns a non-revealing label for a token: its last four characters.
func Hint(token string) string {
	token = strings.TrimSpace(token)
	if len(token) < 12 {
		return "…"
	}
	return "…" + token[len(token)-4:]
}

// Placeholder builds a placeholder: prefix (to look like the real token where
// a harness checks its shape), the marker and the login's seed.
func Placeholder(prefix, seed string) string {
	return prefix + PlaceholderMarker + seed
}
