package egress

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/gempir/agenthold/internal/secrets"
)

// Policy modes.
const (
	ModeOpen      = "open"
	ModeAllowlist = "allowlist"
)

// Policy is an agent's network policy.
type Policy struct {
	Mode string `json:"mode"`
	// Allow entries: "host", "*.host", "host:port", "host:*", or an IP/CIDR with
	// optional port. Entries without a port allow 80 and 443.
	Allow []string `json:"allow"`
}

// DefaultPolicy is used for agents without a stored policy.
func DefaultPolicy() Policy { return Policy{Mode: ModeOpen} }

type allowEntry struct {
	host   string // pattern, empty for CIDR entries
	prefix netip.Prefix
	ports  []int // nil means any
}

// Validate checks a policy.
func (p Policy) Validate() error {
	if p.Mode != ModeOpen && p.Mode != ModeAllowlist {
		return fmt.Errorf("mode must be %q or %q", ModeOpen, ModeAllowlist)
	}
	if len(p.Allow) > 500 {
		return errors.New("at most 500 allow entries")
	}
	for _, a := range p.Allow {
		if _, err := parseEntry(a); err != nil {
			return fmt.Errorf("allow entry %q: %w", a, err)
		}
	}
	return nil
}

func parseEntry(s string) (allowEntry, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return allowEntry{}, errors.New("empty")
	}
	hostPart, portPart := s, ""
	if strings.HasPrefix(s, "[") { // [v6]:port
		end := strings.Index(s, "]")
		if end < 0 {
			return allowEntry{}, errors.New("bad IPv6 literal")
		}
		hostPart, portPart = s[1:end], strings.TrimPrefix(s[end+1:], ":")
	} else if i := strings.LastIndex(s, ":"); i >= 0 && strings.Count(s, ":") == 1 {
		hostPart, portPart = s[:i], s[i+1:]
	}
	e := allowEntry{}
	switch portPart {
	case "":
		e.ports = []int{80, 443}
	case "*":
		e.ports = nil
	default:
		p, err := strconv.Atoi(portPart)
		if err != nil || p < 1 || p > 65535 {
			return e, errors.New("bad port")
		}
		e.ports = []int{p}
	}
	if pfx, err := netip.ParsePrefix(hostPart); err == nil {
		e.prefix = pfx.Masked()
		return e, nil
	}
	if ip, err := netip.ParseAddr(hostPart); err == nil {
		e.prefix = netip.PrefixFrom(ip, ip.BitLen())
		return e, nil
	}
	h := strings.TrimPrefix(hostPart, "*.")
	if h == "" || strings.ContainsAny(h, "*/@ ") || !strings.Contains(h, ".") && h != "localhost" {
		return e, errors.New("bad host")
	}
	e.host = hostPart
	return e, nil
}

func (e allowEntry) port(p int) bool {
	if e.ports == nil {
		return true
	}
	for _, x := range e.ports {
		if x == p {
			return true
		}
	}
	return false
}

// Allows decides a connection to host (a name, may be empty) or ip at port.
func (p Policy) Allows(host string, ip netip.Addr, port int) bool {
	if p.Mode == ModeOpen {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range p.Allow {
		e, err := parseEntry(a)
		if err != nil || !e.port(port) {
			continue
		}
		if e.host != "" && host != "" && secrets.MatchPattern(e.host, host) {
			return true
		}
		if e.prefix.IsValid() && ip.IsValid() && e.prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// AllowsName reports whether any port of host is allowed (used for DNS).
func (p Policy) AllowsName(host string) bool {
	if p.Mode == ModeOpen {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range p.Allow {
		if e, err := parseEntry(a); err == nil && e.host != "" && secrets.MatchPattern(e.host, host) {
			return true
		}
	}
	return false
}

// blockedNets are never reachable from sandboxes, whatever the policy: the
// server's own and other private networks, loopback, link-local (including
// cloud metadata at 169.254.169.254), CGNAT, multicast and reserved ranges.
var blockedNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "64:ff9b::/96",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PublicAddr reports whether ip is a destination sandboxes may reach.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return false
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}
