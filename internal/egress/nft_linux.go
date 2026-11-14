//go:build linux

package egress

import (
	"fmt"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const tableName = "agenthold"

// Firewall describes the server-side rules for sandbox traffic.
type Firewall struct {
	// Pool contains every agent network.
	Pool netip.Prefix
	// ProxyPort receives all redirected TCP from sandboxes.
	ProxyPort int
	// DNSPort receives redirected DNS (UDP and TCP port 53).
	DNSPort int
	// OpenPorts on the server that sandboxes may connect to directly (the
	// tunnel and the explicit proxy). Everything else is dropped.
	OpenPorts []int
}

// Install replaces the agenthold nftables table in the current network namespace.
func (f Firewall) Install() error {
	if !f.Pool.Addr().Is4() {
		return fmt.Errorf("pool %s is not IPv4", f.Pool)
	}
	c, err := nftables.New()
	if err != nil {
		return err
	}
	if tables, err := c.ListTablesOfFamily(nftables.TableFamilyIPv4); err == nil {
		for _, t := range tables {
			if t.Name == tableName {
				c.DelTable(t)
			}
		}
		if err := c.Flush(); err != nil {
			return fmt.Errorf("remove old rules: %w", err)
		}
	}
	t := c.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: tableName})

	pre := c.AddChain(&nftables.Chain{
		Name: "prerouting", Table: t, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest,
	})
	for _, proto := range []byte{unix.IPPROTO_UDP, unix.IPPROTO_TCP} {
		c.AddRule(&nftables.Rule{Table: t, Chain: pre, Exprs: concat(
			f.fromPool(), l4proto(proto), dport(53), redirect(f.DNSPort),
		)})
	}
	// All other TCP not addressed to the server itself goes to the proxy.
	c.AddRule(&nftables.Rule{Table: t, Chain: pre, Exprs: concat(
		f.fromPool(), l4proto(unix.IPPROTO_TCP),
		[]expr.Any{
			&expr.Fib{Register: 1, FlagDADDR: true, ResultADDRTYPE: true},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(unix.RTN_LOCAL)},
		},
		redirect(f.ProxyPort),
	)})

	accept := policyAccept()
	input := c.AddChain(&nftables.Chain{
		Name: "input", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityFilter, Policy: &accept,
	})
	c.AddRule(&nftables.Rule{Table: t, Chain: input, Exprs: []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{
			SourceRegister: 1, DestRegister: 1, Len: 4,
			Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
			Xor:  []byte{0, 0, 0, 0},
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}})
	for _, p := range append([]int{f.ProxyPort, f.DNSPort}, f.OpenPorts...) {
		c.AddRule(&nftables.Rule{Table: t, Chain: input, Exprs: concat(
			f.fromPool(), l4proto(unix.IPPROTO_TCP), dport(p), []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}},
		)})
	}
	c.AddRule(&nftables.Rule{Table: t, Chain: input, Exprs: concat(
		f.fromPool(), l4proto(unix.IPPROTO_UDP), dport(f.DNSPort), []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}},
	)})
	c.AddRule(&nftables.Rule{Table: t, Chain: input, Exprs: concat(f.fromPool(), []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}})})

	fwd := c.AddChain(&nftables.Chain{
		Name: "forward", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &accept,
	})
	c.AddRule(&nftables.Rule{Table: t, Chain: fwd, Exprs: concat(f.fromPool(), []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}})})

	if err := c.Flush(); err != nil {
		return fmt.Errorf("install nftables rules (does the server have CAP_NET_ADMIN?): %w", err)
	}
	return nil
}

func policyAccept() nftables.ChainPolicy { return nftables.ChainPolicyAccept }

func concat(parts ...[]expr.Any) []expr.Any {
	var out []expr.Any
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func (f Firewall) fromPool() []expr.Any {
	pfx := f.Pool.Masked()
	mask := make([]byte, 4)
	for i := range f.Pool.Bits() {
		mask[i/8] |= 0x80 >> (i % 8)
	}
	addr := pfx.Addr().As4()
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: mask, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr[:]},
	}
}

func l4proto(p byte) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{p}},
	}
}

func dport(p int) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(p))},
	}
}

func redirect(p int) []expr.Any {
	return []expr.Any{
		&expr.Immediate{Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(p))},
		&expr.Redir{RegisterProtoMin: 1},
	}
}
