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

// Install replaces the agenthold nftables table in the current network namespace.
func (f *Firewall) Install() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.validate(); err != nil {
		return err
	}
	c, err := nftables.New()
	if err != nil {
		return err
	}
	tables, err := c.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("list firewall tables: %w", err)
	}
	for _, t := range tables {
		if t.Name == tableName {
			c.DelTable(t)
		}
	}
	// Replacement and creation are one netlink transaction: no unfiltered gap.

	t := c.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: tableName})

	guests := guestSet(t)
	if err := c.AddSet(guests, nil); err != nil {
		return err
	}
	// Validate identity before conntrack/NAT. Drop IPv6 and forged source IPs,
	// even if the guest configures another address or routes packets manually.
	raw := c.AddChain(&nftables.Chain{
		Name: "guest_identity", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityRaw,
	})
	c.AddRule(&nftables.Rule{Table: t, Chain: raw, Exprs: concat(fromTap(), []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}},
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Payload{DestRegister: 2, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Lookup{SourceRegister: 1, SetID: guests.ID, SetName: guests.Name},
		&expr.Verdict{Kind: expr.VerdictAccept},
	})})
	c.AddRule(&nftables.Rule{Table: t, Chain: raw, Exprs: concat(fromTap(), []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}})})

	pre := c.AddChain(&nftables.Chain{
		Name: "prerouting", Table: t, Type: nftables.ChainTypeNAT,
		Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest,
	})
	for _, proto := range []byte{unix.IPPROTO_UDP, unix.IPPROTO_TCP} {
		c.AddRule(&nftables.Rule{Table: t, Chain: pre, Exprs: concat(
			fromTap(), l4proto(proto), dport(53), redirect(f.DNSPort),
		)})
	}
	// All other TCP not addressed to the server itself goes to the proxy.
	c.AddRule(&nftables.Rule{Table: t, Chain: pre, Exprs: concat(
		fromTap(), l4proto(unix.IPPROTO_TCP),
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
			fromTap(), l4proto(unix.IPPROTO_TCP), dport(p), []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}},
		)})
	}
	c.AddRule(&nftables.Rule{Table: t, Chain: input, Exprs: concat(
		fromTap(), l4proto(unix.IPPROTO_UDP), dport(f.DNSPort), []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}},
	)})
	c.AddRule(&nftables.Rule{Table: t, Chain: input, Exprs: concat(fromTap(), []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}})})

	fwd := c.AddChain(&nftables.Chain{
		Name: "forward", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &accept,
	})
	c.AddRule(&nftables.Rule{Table: t, Chain: fwd, Exprs: concat(fromTap(), []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}})})

	if err := c.Flush(); err != nil {
		return fmt.Errorf("install nftables rules (does the server have CAP_NET_ADMIN?): %w", err)
	}
	f.registered = make(map[string]netip.Addr)
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

func fromTap() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(TapPrefix)},
	}
}

func guestSet(t *nftables.Table) *nftables.Set {
	return &nftables.Set{
		Table: t, Name: "guest_sources",
		KeyType:       nftables.MustConcatSetType(nftables.TypeIFName, nftables.TypeIPAddr),
		Concatenation: true,
	}
}

func updateGuestSet(key []byte, add bool) error {
	c, err := nftables.New()
	if err != nil {
		return err
	}
	t := &nftables.Table{Family: nftables.TableFamilyINet, Name: tableName}
	s, err := c.GetSetByName(t, "guest_sources")
	if err != nil {
		return fmt.Errorf("guest firewall set: %w", err)
	}
	elements := []nftables.SetElement{{Key: key}}
	if add {
		err = c.SetAddElements(s, elements)
	} else {
		err = c.SetDeleteElements(s, elements)
	}
	if err != nil {
		return err
	}
	return c.Flush()
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
