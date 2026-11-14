//go:build !linux

package egress

import (
	"errors"
	"net"
	"net/netip"
)

// OriginalDst is only available on Linux.
func OriginalDst(net.Conn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("transparent interception requires Linux")
}
