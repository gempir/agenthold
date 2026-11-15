package sandboxd

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// Default guest block devices (drive order: base, overlay, home, config).
const (
	DefaultBaseDevice    = "/dev/vda"
	DefaultOverlayDevice = "/dev/vdb"
	DefaultHomeDevice    = "/dev/vdc"
	DefaultConfigDevice  = "/dev/vdd"
)

// Layout names the guest's block devices.
type Layout struct {
	Overlay string // per-agent writable overlay disk (upper layer of /)
	Home    string // per-agent /home/agent disk
	Config  string // read-only boot configuration (GuestBoot)
}

// IPConfig is the kernel's ip= argument
// (ip=<client>:<server>:<gw>:<netmask>:<hostname>:<device>:<autoconf>:<dns0>:<dns1>).
type IPConfig struct {
	Client   net.IP
	Gateway  net.IP
	Netmask  net.IPMask
	Hostname string
	Device   string
	DNS      []net.IP
}

// ParseCmdline splits a kernel command line into key -> value. Flags without
// "=" map to "". Double quotes group words, as the kernel does. Later
// occurrences win.
func ParseCmdline(s string) map[string]string {
	out := map[string]string{}
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		k, v, _ := strings.Cut(cur.String(), "=")
		out[k] = v
		cur.Reset()
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t' || r == '\n') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// LayoutFromCmdline returns the device layout: the defaults, overridden by
// agenthold.overlay=, agenthold.home= and agenthold.config=.
func LayoutFromCmdline(args map[string]string) Layout {
	l := Layout{Overlay: DefaultOverlayDevice, Home: DefaultHomeDevice, Config: DefaultConfigDevice}
	if v := args["agenthold.overlay"]; v != "" {
		l.Overlay = v
	}
	if v := args["agenthold.home"]; v != "" {
		l.Home = v
	}
	if v := args["agenthold.config"]; v != "" {
		l.Config = v
	}
	return l
}

// ParseIP parses the value of the kernel ip= argument. Only static
// configuration with a gateway is supported (that is what the driver passes).
func ParseIP(v string) (IPConfig, error) {
	var c IPConfig
	if v == "" {
		return c, errors.New("no ip= argument")
	}
	f := strings.Split(v, ":")
	for len(f) < 9 {
		f = append(f, "")
	}
	if c.Client = net.ParseIP(f[0]).To4(); c.Client == nil {
		return c, fmt.Errorf("ip=: client address %q is not IPv4", f[0])
	}
	if c.Gateway = net.ParseIP(f[2]).To4(); c.Gateway == nil {
		return c, fmt.Errorf("ip=: gateway %q is not IPv4", f[2])
	}
	if f[3] != "" {
		m := net.ParseIP(f[3]).To4()
		if m == nil {
			return c, fmt.Errorf("ip=: netmask %q is not IPv4", f[3])
		}
		c.Netmask = net.IPMask(m)
	}
	c.Hostname = f[4]
	c.Device = f[5]
	for _, d := range f[7:9] {
		if d == "" {
			continue
		}
		ip := net.ParseIP(d).To4()
		if ip == nil {
			return c, fmt.Errorf("ip=: dns server %q is not IPv4", d)
		}
		c.DNS = append(c.DNS, ip)
	}
	return c, nil
}

// Nameserver is the DNS server the guest uses: the first ip= dns field, or
// the gateway (the node's DNS forwarder listens there).
func (c IPConfig) Nameserver() net.IP {
	if len(c.DNS) > 0 {
		return c.DNS[0]
	}
	return c.Gateway
}
