//go:build linux

package firecracker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/gempir/agenthold/internal/sandbox"
)

func allocate(pool netip.Prefix, used []netip.Prefix) (netip.Prefix, uint32, error) {
	pool = pool.Masked()
	b := pool.Addr().As4()
	start := binary.BigEndian.Uint32(b[:])
	count := uint32(1) << (30 - pool.Bits())
	for i := uint32(0); i < count; i++ {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], start+i*4)
		p := netip.PrefixFrom(netip.AddrFrom4(buf), 30)
		free := true
		for _, u := range used {
			if p.Overlaps(u) {
				free = false
				break
			}
		}
		if free {
			return p, i, nil
		}
	}
	return netip.Prefix{}, 0, fmt.Errorf("network pool %s is exhausted", pool)
}

func linkHandle() (*netlink.Handle, error) {
	h, err := netlink.NewHandle()
	if err != nil {
		return nil, err
	}
	if err = h.SetSocketTimeout(2 * time.Second); err != nil {
		h.Close()
		return nil, err
	}
	return h, nil
}

func (d *Driver) ensureTap(ctx context.Context, id sandbox.ID, m *manifest) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	h, err := linkHandle()
	if err != nil {
		return err
	}
	defer h.Close()
	name := TapName(id)
	alias := "agenthold:" + string(id)
	link, err := h.LinkByName(name)
	if err == nil {
		if link.Type() != "tuntap" || link.Attrs().Alias != alias {
			return fmt.Errorf("tap name %s belongs to another interface", name)
		}
	} else {
		var missing netlink.LinkNotFoundError
		if !errors.As(err, &missing) {
			return err
		}
		// Reject host interface address overlaps instead of silently stealing a
		// connected route; dedicated /30s are never bridged with each other.
		addrs, e := h.AddrList(nil, netlink.FAMILY_V4)
		if e != nil {
			return e
		}
		for _, a := range addrs {
			p, e := netip.ParsePrefix(a.IPNet.String())
			if e == nil && p.Overlaps(m.Subnet) {
				return fmt.Errorf("sandbox network %s overlaps host address %s", m.Subnet, p)
			}
		}
		tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: name, Alias: alias}, Mode: netlink.TUNTAP_MODE_TAP, Flags: netlink.TUNTAP_DEFAULTS, Owner: m.UID, Group: m.GID}
		if err = h.LinkAdd(tap); err != nil {
			return fmt.Errorf("create tap: %w", err)
		}
		// Some kernels do not set IFLA_IFALIAS through the tuntap creation path.
		link, err = h.LinkByName(name)
		if err != nil {
			return err
		}
		if err = h.LinkSetAlias(link, alias); err != nil {
			_ = h.LinkDel(link)
			return err
		}
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: net.ParseIP(m.Subnet.Addr().Next().String()).To4(), Mask: net.CIDRMask(30, 32)}}
	addrs, err := h.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	found := false
	for _, a := range addrs {
		if a.IPNet.String() == addr.IPNet.String() {
			found = true
		} else {
			return fmt.Errorf("tap %s has unexpected address %s", name, a.IPNet)
		}
	}
	if !found {
		if err = h.AddrAdd(link, addr); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return h.LinkSetUp(link)
}

func (d *Driver) deleteTap(id sandbox.ID) error {
	h, err := linkHandle()
	if err != nil {
		return err
	}
	defer h.Close()
	link, err := h.LinkByName(TapName(id))
	if err != nil {
		var missing netlink.LinkNotFoundError
		if errors.As(err, &missing) {
			return nil
		}
		return err
	}
	if link.Type() != "tuntap" || link.Attrs().Alias != "agenthold:"+string(id) {
		return fmt.Errorf("refusing to delete foreign interface %s", TapName(id))
	}
	return h.LinkDel(link)
}
