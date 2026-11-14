//go:build linux

package sandboxd

import (
	"fmt"
	"net"
	"os"

	"github.com/vishvananda/netlink"
)

// NetSetup makes the server the sandbox's default gateway and DNS server. It
// needs CAP_NET_ADMIN and is run by the driver as a privileged exec.
func NetSetup(gateway string) error {
	gw := net.ParseIP(gateway)
	if gw == nil || gw.To4() == nil {
		return fmt.Errorf("gateway %q is not an IPv4 address", gateway)
	}
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}
	var link netlink.Link
	for _, l := range links {
		addrs, err := netlink.AddrList(l, netlink.FAMILY_V4)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if a.IPNet != nil && a.Contains(gw) {
				link = l
			}
		}
	}
	if link == nil {
		return fmt.Errorf("no interface is on the same network as %s", gw)
	}
	_, all, _ := net.ParseCIDR("0.0.0.0/0")
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: all, Gw: gw}); err != nil {
		return fmt.Errorf("set default route: %w", err)
	}
	// Docker bind-mounts resolv.conf; write in place rather than replacing it.
	f, err := os.OpenFile("/etc/resolv.conf", os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("write resolv.conf: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "# managed by agenthold: all DNS goes through the egress proxy\nnameserver %s\noptions ndots:0\n", gw)
	return err
}
