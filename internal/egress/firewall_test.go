package egress

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestGuestIdentityKey(t *testing.T) {
	ip := netip.MustParseAddr("10.213.0.2")
	key, err := guestKey("ah012345", ip)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 20 || string(key[:8]) != "ah012345" || key[16] != 10 || key[19] != 2 {
		t.Fatalf("unexpected nftables concatenated key: %v", key)
	}
	for _, tap := range []string{"eth0", "ah", "ah\x00x", "ah0123456789012345", "ah/x"} {
		if _, err := guestKey(tap, ip); err == nil {
			t.Errorf("accepted invalid tap %q", tap)
		}
	}
	if _, err := guestKey("ah0", netip.MustParseAddr("::1")); err == nil {
		t.Fatal("accepted IPv6 identity")
	}
}

func TestFirewallRegistrationFailsClosed(t *testing.T) {
	f := &Firewall{Pool: netip.MustParsePrefix("10.213.0.0/16"), ProxyPort: 15001, DNSPort: 15053}
	if err := f.Register(context.Background(), "ah0", netip.MustParseAddr("10.213.0.2")); err == nil {
		t.Fatal("registered before firewall installation")
	}
	if err := f.Register(context.Background(), "ah0", netip.MustParseAddr("10.214.0.2")); err == nil {
		t.Fatal("registered address outside pool")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Register(ctx, "ah0", netip.MustParseAddr("10.213.0.2")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled registration: %v", err)
	}
}

func TestProxyRejectsPublicGuestPool(t *testing.T) {
	p := &Proxy{GuestPool: netip.MustParsePrefix("203.0.113.0/24")}
	if !PublicAddr(netip.MustParseAddr("203.0.113.2")) {
		t.Fatal("test requires an address the ordinary public-address check allows")
	}
	if p.reachable(netip.MustParseAddr("203.0.113.2")) {
		t.Fatal("proxy allowed a sibling guest in a public network pool")
	}
	if !p.reachable(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("proxy blocked unrelated public destination")
	}
}
