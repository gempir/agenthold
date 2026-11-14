//go:build linux

package egress

import (
	"errors"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

const soOriginalDst = 80 // SO_ORIGINAL_DST from linux/netfilter_ipv4.h

// OriginalDst returns the destination of a connection redirected by netfilter.
func OriginalDst(c net.Conn) (netip.AddrPort, error) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return netip.AddrPort{}, errors.New("not a TCP connection")
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var out netip.AddrPort
	var serr error
	err = raw.Control(func(fd uintptr) {
		// The kernel returns a sockaddr_in; IPv6Mreq is a 20-byte buffer that fits it.
		mreq, err := unix.GetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IP, soOriginalDst)
		if err != nil {
			serr = err
			return
		}
		b := mreq.Multiaddr
		port := uint16(b[2])<<8 | uint16(b[3])
		out = netip.AddrPortFrom(netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]}), port)
	})
	if err != nil {
		return out, err
	}
	return out, serr
}
