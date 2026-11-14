package egress

import (
	"net/netip"
	"sync"
	"time"
)

// NameCache remembers which names each agent resolved to which addresses, so
// non-HTTP connections (SSH, ...) can be matched against host-based policies.
type NameCache struct {
	mu sync.Mutex
	m  map[string]map[netip.Addr]nameEntry
}

type nameEntry struct {
	name    string
	expires time.Time
}

// NewNameCache returns an empty cache.
func NewNameCache() *NameCache { return &NameCache{m: map[string]map[netip.Addr]nameEntry{}} }

// Add records that name resolved to ip for agentID.
func (c *NameCache) Add(agentID, name string, ip netip.Addr, ttl time.Duration) {
	ttl = max(ttl, 5*time.Minute) // clients cache longer than they should
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.m[agentID]
	if m == nil {
		m = map[netip.Addr]nameEntry{}
		c.m[agentID] = m
	}
	if len(m) > 50000 {
		clear(m)
	}
	m[ip.Unmap()] = nameEntry{name: name, expires: time.Now().Add(ttl)}
}

// Lookup returns the name agentID used for ip, or "".
func (c *NameCache) Lookup(agentID string, ip netip.Addr) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[agentID][ip.Unmap()]
	if !ok || time.Now().After(e.expires) {
		return ""
	}
	return e.name
}
