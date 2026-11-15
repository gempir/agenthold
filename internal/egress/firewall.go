package egress

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync"
)

// TapPrefix identifies interfaces owned by the Firecracker driver.
const TapPrefix = "ah"

// Firewall confines guest traffic on the node. Install must succeed before
// starting any guests. An unregistered tap cannot send IP traffic.
type Firewall struct {
	Pool      netip.Prefix
	ProxyPort int
	DNSPort   int
	OpenPorts []int

	mu         sync.Mutex
	registered map[string]netip.Addr
}

func (f *Firewall) validate() error {
	if !f.Pool.IsValid() || !f.Pool.Addr().Is4() {
		return fmt.Errorf("pool %s is not IPv4", f.Pool)
	}
	for _, p := range append([]int{f.ProxyPort, f.DNSPort}, f.OpenPorts...) {
		if p < 1 || p > 65535 {
			return fmt.Errorf("invalid firewall port %d", p)
		}
	}
	return nil
}

// Register authorizes exactly one source IPv4 address on a tap. The driver
// calls this before boot and again when recovering a running VM.
func (f *Firewall) Register(ctx context.Context, tap string, guest netip.Addr) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := guestKey(tap, guest)
	if err != nil {
		return err
	}
	if !f.Pool.Contains(guest) {
		return fmt.Errorf("guest %s is outside %s", guest, f.Pool)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registered == nil {
		return fmt.Errorf("guest firewall is not installed")
	}
	if prev, ok := f.registered[tap]; ok {
		if prev == guest {
			return nil
		}
		return fmt.Errorf("tap %s already belongs to %s", tap, prev)
	}
	if err := updateGuestSet(key, true); err != nil {
		return err
	}
	f.registered[tap] = guest
	return nil
}

// Unregister revokes a tap before its address can be reused.
func (f *Firewall) Unregister(ctx context.Context, tap string, guest netip.Addr) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := guestKey(tap, guest)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	prev, ok := f.registered[tap]
	if !ok {
		return nil
	}
	if prev != guest {
		return fmt.Errorf("tap %s belongs to %s", tap, prev)
	}
	if err := updateGuestSet(key, false); err != nil {
		return err
	}
	delete(f.registered, tap)
	return nil
}

// nftables concatenations align each component to a 32-bit register. IFNAMSIZ
// is 16, followed by the four IPv4 bytes in network byte order.
func guestKey(tap string, guest netip.Addr) ([]byte, error) {
	if !strings.HasPrefix(tap, TapPrefix) || len(tap) <= len(TapPrefix) || len(tap) > 15 || strings.ContainsAny(tap, "\x00/ \t\n") {
		return nil, fmt.Errorf("invalid guest tap %q", tap)
	}
	if !guest.Is4() {
		return nil, fmt.Errorf("guest %s is not IPv4", guest)
	}
	key := make([]byte, 20)
	copy(key, tap)
	ip := guest.As4()
	copy(key[16:], ip[:])
	return key, nil
}
