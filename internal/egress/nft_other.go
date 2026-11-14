//go:build !linux

package egress

import (
	"errors"
	"net/netip"
)

// Firewall describes the server-side rules for sandbox traffic (Linux only).
type Firewall struct {
	Pool      netip.Prefix
	ProxyPort int
	DNSPort   int
	OpenPorts []int
}

// Install is unsupported outside Linux.
func (Firewall) Install() error { return errors.New("the egress firewall requires Linux") }
