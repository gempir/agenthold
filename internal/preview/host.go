// Package preview serves ports of machines over HTTP at
// <name><preview.host_suffix> ("exposures"). It owns host matching, exposure
// names, the gate's codes and cookies and the identity tokens handed to apps.
// The HTTP router that uses it lives in package api.
package preview

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/gempir/agenthold/internal/config"
)

// Access is who may open an exposure.
type Access string

// Access modes.
const (
	Private       Access = "private"       // the owner and users the machine is shared with
	Authenticated Access = "authenticated" // any signed-in user
	Public        Access = "public"        // anyone
)

// Valid reports whether a is a known mode.
func (a Access) Valid() bool { return a == Private || a == Authenticated || a == Public }

// Hosts maps exposure names to hosts and URLs.
type Hosts struct {
	// Suffix is preview.host_suffix; empty means the feature is off.
	Suffix string
	// Scheme and Port come from public_url: exposures sit behind the same
	// TLS proxy as the main host.
	Scheme string
	Port   string
}

// NewHosts derives exposure URLs from the suffix and the main public URL.
func NewHosts(suffix, publicURL string) Hosts {
	h := Hosts{Suffix: suffix, Scheme: "http"}
	if u, err := url.Parse(publicURL); err == nil {
		if u.Scheme == "https" {
			h.Scheme = "https"
		}
		h.Port = u.Port()
	}
	return h
}

// Enabled reports whether preview URLs are configured.
func (h Hosts) Enabled() bool { return h.Suffix != "" }

// Secure reports whether exposures are served over https.
func (h Hosts) Secure() bool { return h.Scheme == "https" }

// Classify splits a request Host header: under reports whether it is an
// exposure host (ends with the suffix, ignoring port, case and trailing
// dots); name is the exposure name, valid only if ValidName accepts it.
// Every host under the suffix belongs to the preview router, valid or not.
func (h Hosts) Classify(host string) (name string, under bool) {
	if h.Suffix == "" {
		return "", false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if hn, _, err := net.SplitHostPort(host); err == nil {
		host = hn
	}
	host = strings.TrimRight(host, ".")
	return strings.CutSuffix(host, h.Suffix)
}

// Match returns the exposure name of a request Host header, if it is a host
// of a valid exposure name.
func (h Hosts) Match(host string) (string, bool) {
	name, under := h.Classify(host)
	if !under || ValidName(name) != nil {
		return "", false
	}
	return name, true
}

// Host is the hostname of an exposure (no port).
func (h Hosts) Host(name string) string { return name + h.Suffix }

// Origin is the scheme://host[:port] of an exposure.
func (h Hosts) Origin(name string) string {
	host := h.Host(name)
	if h.Port != "" {
		host = net.JoinHostPort(host, h.Port)
	}
	return h.Scheme + "://" + host
}

// URL is the URL an exposure is opened at.
func (h Hosts) URL(name string) string { return h.Origin(name) + "/" }

// reserved names look like infrastructure; xn-- (punycode) names can
// imitate other names.
var reserved = map[string]bool{
	"www": true, "api": true, "admin": true, "auth": true, "login": true, "app": true, "apps": true,
	"demo": true, "mail": true, "dl": true, "static": true, "assets": true, "agenthold": true,
}

// ValidName checks a new exposure name: a DNS label part of lowercase
// letters, digits and hyphens, 1 to 40 characters, not starting or ending
// with a hyphen, not reserved and not punycode.
func ValidName(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if reserved[name] {
		return Invalid(fmt.Sprintf("the name %q is reserved", name))
	}
	if strings.HasPrefix(name, "xn--") {
		return Invalid("names starting with xn-- are reserved")
	}
	return nil
}

func checkName(name string) error {
	if name == "" || len(name) > config.MaxExposureName {
		return Invalid(fmt.Sprintf("name must be 1 to %d characters", config.MaxExposureName))
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return Invalid("name must not start or end with a hyphen")
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return Invalid("name may only contain lowercase letters, digits and hyphens")
		}
	}
	return nil
}

// DefaultName is <agent-name-slug>-<port>, shortened to fit.
func DefaultName(agentName string, port int) string {
	p := "-" + strconv.Itoa(port)
	// Slug collapses "--", so a default name is never punycode.
	return trimSlug(Slug(agentName), config.MaxExposureName-len(p)) + p
}

// Slug turns an agent name into name characters ("My Agent!" -> "my-agent").
// It returns "app" when nothing usable is left.
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "app"
	}
	return out
}

func trimSlug(s string, n int) string {
	if len(s) > n {
		s = strings.TrimRight(s[:n], "-")
	}
	if s == "" {
		return "app"
	}
	return s
}

// InvalidError is a rejected input (a bad name, port or access mode).
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// Invalid returns an InvalidError.
func Invalid(msg string) error { return &InvalidError{Msg: msg} }
