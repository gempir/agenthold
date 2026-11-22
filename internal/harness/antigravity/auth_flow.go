package antigravity

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// CodeRedirectURI is where `agy acp --login` redirects Google sign-in: the
// page displays a code that the user pastes back.
const CodeRedirectURI = "https://antigravity.google/oauth-callback"

// AuthURLPrefix is printed by `agy acp --login` before the Google sign-in link.
const AuthURLPrefix = "Open the following link to authenticate the ACP server: "

const maxAuthURLLen = 16384

// AuthorizationURL is a validated Google sign-in URL emitted by `agy acp --login`.
type AuthorizationURL struct {
	URL         string
	RedirectURI string
	State       string
}

var (
	codeResponsePattern = regexp.MustCompile(`^[\w./~-]+$`)
	urlFinderPattern    = regexp.MustCompile(`https://accounts\.google\.com/o/oauth2/[^\s"']+`)
)

// ExtractAuthorizationURL scans a line of `agy acp --login` output for a valid
// Google OAuth URL.
func ExtractAuthorizationURL(line string) (AuthorizationURL, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return AuthorizationURL{}, false
	}
	candidate := ""
	if idx := strings.Index(line, AuthURLPrefix); idx >= 0 {
		candidate = strings.TrimSpace(line[idx+len(AuthURLPrefix):])
	} else if m := urlFinderPattern.FindString(line); m != "" {
		candidate = m
	} else {
		return AuthorizationURL{}, false
	}
	parsed, err := ParseAuthorizationURL(candidate)
	if err != nil {
		return AuthorizationURL{}, false
	}
	return parsed, true
}

// ParseAuthorizationURL validates a Google OAuth URL emitted by `agy acp --login`,
// matching T3 Code's antigravityAuthSupport.ts validation.
func ParseAuthorizationURL(raw string) (AuthorizationURL, error) {
	if len(raw) == 0 || len(raw) > maxAuthURLLen || strings.ContainsAny(raw, " \t\r\n") {
		return AuthorizationURL{}, errors.New("invalid Google sign-in URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return AuthorizationURL{}, errors.New("invalid Google sign-in URL")
	}
	if u.Scheme != "https" || u.Host != "accounts.google.com" ||
		(u.Path != "/o/oauth2/auth" && u.Path != "/o/oauth2/v2/auth") ||
		u.User != nil || u.Fragment != "" {
		return AuthorizationURL{}, errors.New("unexpected Google sign-in URL origin or path")
	}
	q := u.Query()
	if len(q["state"]) != 1 || len(q["redirect_uri"]) != 1 || len(q["response_type"]) != 1 || q.Get("response_type") != "code" {
		return AuthorizationURL{}, errors.New("incomplete Google sign-in URL parameters")
	}
	state := q.Get("state")
	if state == "" || len(state) > 512 || strings.ContainsAny(state, " \t\r\n") {
		return AuthorizationURL{}, errors.New("invalid Google sign-in state")
	}
	redirectURI := q.Get("redirect_uri")
	if redirectURI != CodeRedirectURI {
		return AuthorizationURL{}, errors.New("unexpected Google sign-in redirect URI")
	}
	return AuthorizationURL{URL: raw, RedirectURI: redirectURI, State: state}, nil
}

// ValidateLoginResponse validates what the user pasted back for `agy acp --login`:
// either the raw authorization code shown on the Antigravity page, or the full
// callback URL whose state matches the pending sign-in flow.
func ValidateLoginResponse(pending AuthorizationURL, response string) (string, error) {
	v := strings.TrimSpace(response)
	if v == "" {
		return "", errors.New("paste the authorization code or callback URL from the Google sign-in page")
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", errors.New("paste the code or URL on a single line")
	}
	if len(v) <= 2048 && codeResponsePattern.MatchString(v) {
		return v, nil
	}
	if len(v) > maxAuthURLLen || strings.ContainsAny(v, " \t") {
		return "", errors.New("that does not look like an Antigravity authorization code or callback URL")
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", errors.New("that does not look like an Antigravity authorization code or callback URL")
	}
	originPath := u.Scheme + "://" + u.Host + u.Path
	q := u.Query()
	if originPath != pending.RedirectURI || u.User != nil ||
		len(q["state"]) != 1 || q.Get("state") != pending.State ||
		len(q["code"]) != 1 || q.Get("code") == "" {
		return "", errors.New("callback URL does not match this sign-in session")
	}
	return v, nil
}
