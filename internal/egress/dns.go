package egress

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DNS answers sandboxes' DNS queries. Names the agent's policy does not
// allow get NXDOMAIN (so DNS cannot be used to smuggle data out); others are
// forwarded upstream, and the answers are recorded for host-based TCP policies.
type DNS struct {
	Agents   Agents
	Names    *NameCache
	Upstream string // host:port
	Log      func(Event)
}

// UpstreamFromResolvConf returns the first nameserver of /etc/resolv.conf.
func UpstreamFromResolvConf() string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return "1.1.1.1:53"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "nameserver" {
			return net.JoinHostPort(fields[1], "53")
		}
	}
	return "1.1.1.1:53"
}

// ServeUDP answers queries on pc.
func (d *DNS) ServeUDP(pc net.PacketConn) error {
	buf := make([]byte, 4096)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		q := append([]byte(nil), buf[:n]...)
		go func() {
			src, ok := addrOf(addr)
			if !ok {
				return
			}
			if res := d.Handle(context.Background(), q, src, "udp"); res != nil {
				_, _ = pc.WriteTo(res, addr)
			}
		}()
	}
}

// ServeTCP answers queries over TCP.
func (d *DNS) ServeTCP(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			src, ok := addrOf(c.RemoteAddr())
			if !ok {
				return
			}
			for {
				_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
				var l [2]byte
				if _, err := io.ReadFull(c, l[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(l[:]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				res := d.Handle(context.Background(), q, src, "tcp")
				if res == nil {
					return
				}
				binary.BigEndian.PutUint16(l[:], uint16(len(res)))
				if _, err := c.Write(append(l[:], res...)); err != nil {
					return
				}
			}
		}()
	}
}

func addrOf(a net.Addr) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

// Handle answers one query from src.
func (d *DNS) Handle(ctx context.Context, q []byte, src netip.Addr, network string) []byte {
	var p dnsmessage.Parser
	hdr, err := p.Start(q)
	if err != nil {
		return nil
	}
	question, err := p.Question()
	if err != nil {
		return reply(hdr, nil, dnsmessage.RCodeFormatError)
	}
	agent, err := d.Agents.AgentForSource(ctx, src)
	if err != nil || agent == nil {
		return reply(hdr, &question, dnsmessage.RCodeRefused)
	}
	name := strings.ToLower(strings.TrimSuffix(question.Name.String(), "."))
	if !agent.Policy.AllowsName(name) {
		if d.Log != nil {
			d.Log(Event{AgentID: agent.ID, At: time.Now(), Kind: "dns", Host: name, Port: 53, Reason: "not in allowlist"})
		}
		return reply(hdr, &question, dnsmessage.RCodeNameError)
	}
	res, err := d.forward(ctx, q, network)
	if err != nil {
		return reply(hdr, &question, dnsmessage.RCodeServerFailure)
	}
	d.record(agent.ID, res)
	return res
}

func (d *DNS) forward(ctx context.Context, q []byte, network string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var dialer net.Dialer
	c, err := dialer.DialContext(ctx, network, d.Upstream)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	if network == "tcp" {
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(q)))
		if _, err := c.Write(append(l[:], q...)); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return nil, err
		}
		res := make([]byte, binary.BigEndian.Uint16(l[:]))
		_, err = io.ReadFull(c, res)
		return res, err
	}
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func (d *DNS) record(agentID string, res []byte) {
	if d.Names == nil {
		return
	}
	var p dnsmessage.Parser
	if _, err := p.Start(res); err != nil {
		return
	}
	q, err := p.Question()
	if err != nil {
		return
	}
	_ = p.SkipAllQuestions()
	name := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
	for {
		h, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) || err != nil {
			return
		}
		ttl := time.Duration(h.TTL) * time.Second
		switch h.Type {
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err == nil {
				d.Names.Add(agentID, name, netip.AddrFrom4(r.A), ttl)
			}
		case dnsmessage.TypeAAAA:
			r, err := p.AAAAResource()
			if err == nil {
				d.Names.Add(agentID, name, netip.AddrFrom16(r.AAAA), ttl)
			}
		default:
			_ = p.SkipAnswer()
		}
	}
}

func reply(q dnsmessage.Header, question *dnsmessage.Question, rcode dnsmessage.RCode) []byte {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: q.ID, Response: true, RecursionDesired: q.RecursionDesired, RecursionAvailable: true, RCode: rcode})
	if question != nil {
		_ = b.StartQuestions()
		_ = b.Question(*question)
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}
