//go:build linux && kvm

package egress

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// Run in an isolated host network and mount namespace. Two nested namespaces
// stand in for malicious guests; no KVM or host firewall changes are needed.
func TestGuestFirewallIsolation(t *testing.T) {
	if os.Getenv("AGENTHOLD_FIREWALL_TEST") != "1" {
		if os.Geteuid() != 0 {
			t.Skip("requires root and Linux network namespaces")
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestGuestFirewallIsolation$", "-test.v")
		cmd.Env = append(os.Environ(), "AGENTHOLD_FIREWALL_TEST=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET | unix.CLONE_NEWNS}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated firewall test: %v\n%s", err, out)
		}
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v\n%s", args, err, out)
		}
	}
	run("link", "set", "lo", "up")
	ns := "ah-fw-" + strconv.Itoa(os.Getpid())
	run("netns", "add", ns)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", ns).Run() })
	run("link", "add", "ahfw0", "type", "veth", "peer", "name", "guest0")
	run("link", "set", "guest0", "netns", ns)
	run("addr", "add", "10.213.0.1/30", "dev", "ahfw0")
	run("addr", "add", "fd00::1/64", "dev", "ahfw0")
	run("link", "set", "ahfw0", "up")
	run("-n", ns, "link", "set", "lo", "up")
	run("-n", ns, "addr", "add", "10.213.0.2/30", "dev", "guest0")
	run("-n", ns, "addr", "add", "10.213.0.6/32", "dev", "guest0")
	run("-n", ns, "addr", "add", "fd00::2/64", "dev", "guest0")
	run("-n", ns, "link", "set", "guest0", "up")
	run("-n", ns, "route", "add", "default", "via", "10.213.0.1")
	listen := func(addr string) {
		t.Helper()
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
	}
	listen(":18090") // tunnel, explicitly allowed
	listen(":18080") // unrelated host service
	listen(":15001") // transparent proxy
	listen(":15053") // DNS
	probe := func(name string, want bool, args ...string) {
		t.Helper()
		cmd := exec.Command("ip", append([]string{"netns", "exec", ns, "nc", "-z", "-w", "1"}, args...)...)
		out, err := cmd.CombinedOutput()
		if (err == nil) != want {
			t.Errorf("%s: want connection=%v; %v %s", name, want, err, out)
		}
	}
	f := &Firewall{Pool: netip.MustParsePrefix("10.213.0.0/16"), ProxyPort: 15001, DNSPort: 15053, OpenPorts: []int{18090}}
	if err := f.Install(); err != nil {
		t.Fatal(err)
	}
	probe("unregistered tap", false, "-4", "10.213.0.1", "18090")
	ip := netip.MustParseAddr("10.213.0.2")
	if err := f.Register(context.Background(), "ahfw0", ip); err != nil {
		t.Fatal(err)
	}
	if err := f.Register(context.Background(), "ahfw0", ip); err != nil {
		t.Fatal(err)
	}
	probe("tunnel", true, "-4", "10.213.0.1", "18090")
	probe("host service", false, "-4", "10.213.0.1", "18080")
	probe("transparent outbound", true, "-4", "198.51.100.1", "443")
	probe("DNS redirection", true, "-4", "198.51.100.1", "53")
	probe("forged sibling source", false, "-4", "-s", "10.213.0.6", "10.213.0.1", "18090")
	probe("IPv6 host service", false, "-6", "fd00::1", "18080")
	if err := f.Unregister(context.Background(), "ahfw0", ip); err != nil {
		t.Fatal(err)
	}
	probe("revoked tap", false, "-4", "10.213.0.1", "18090")
	// Replacing an existing table must also reset registrations atomically.
	if err := f.Install(); err != nil {
		t.Fatal(fmt.Errorf("replace firewall: %w", err))
	}
}
