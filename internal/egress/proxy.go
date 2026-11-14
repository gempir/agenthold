package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gempir/agenthold/internal/secrets"
)

// Agent is what the proxy needs to know about the sandbox behind a connection.
type Agent struct {
	ID         string
	Policy     Policy
	Injections []secrets.Injection
}

// Agents finds the agent behind a source address.
type Agents interface {
	AgentForSource(ctx context.Context, ip netip.Addr) (*Agent, error)
}

// Event is one entry of an agent's request log.
type Event struct {
	AgentID   string
	At        time.Time
	Kind      string // https, http, tls, tcp, dns
	Host      string
	IP        string
	Port      int
	Method    string
	Path      string
	Status    int
	Allowed   bool
	Reason    string
	Injected  bool
	BytesUp   int64
	BytesDown int64
	Duration  time.Duration
}

// Proxy intercepts sandbox traffic.
type Proxy struct {
	CA     *CA
	Agents Agents
	Names  *NameCache
	// Log receives every event. It must not block.
	Log func(Event)
	// Lookup resolves names for upstream connections. Default: net.DefaultResolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial opens upstream connections. Default: net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Reachable decides whether an upstream address may be dialed. Default: PublicAddr.
	Reachable func(netip.Addr) bool
	// UpstreamRoots verifies upstream TLS when intercepting. Default: system roots.
	UpstreamRoots *x509.CertPool
	// OriginalDst returns a redirected connection's original destination.
	OriginalDst func(net.Conn) (netip.AddrPort, error)
	Logger      *slog.Logger
	// PeekTimeout is how long to wait for the client to speak first. Default 1s.
	PeekTimeout time.Duration

	transportOnce sync.Once
	transport     *http.Transport
}

// errBlocked marks destinations that are never reachable (private addresses).
var errBlocked = errors.New("blocked destination")

type blockedError struct{ msg string }

func (e blockedError) Error() string { return e.msg }
func (e blockedError) Unwrap() error { return errBlocked }

func (p *Proxy) log(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if p.Log != nil {
		p.Log(e)
	}
}

func (p *Proxy) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

func (p *Proxy) reachable(ip netip.Addr) bool {
	if p.Reachable != nil {
		return p.Reachable(ip)
	}
	return PublicAddr(ip)
}

func (p *Proxy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if p.Lookup != nil {
		return p.Lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// resolve picks the upstream address for host (or ip when host is empty),
// refusing private destinations. Names are resolved here, not taken from the
// client, so a forged SNI or Host header cannot redirect to another address.
func (p *Proxy) resolve(ctx context.Context, host string, ip netip.Addr) (netip.Addr, error) {
	if host == "" {
		if !p.reachable(ip) {
			return netip.Addr{}, blockedError{fmt.Sprintf("%s is not a public address", ip)}
		}
		return ip, nil
	}
	if lit, err := netip.ParseAddr(host); err == nil {
		return p.resolve(ctx, "", lit)
	}
	ips, err := p.lookup(ctx, host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, a := range ips {
		if a.Unmap().Is4() && p.reachable(a) {
			return a.Unmap(), nil
		}
	}
	for _, a := range ips {
		if p.reachable(a) {
			return a, nil
		}
	}
	return netip.Addr{}, blockedError{fmt.Sprintf("%s does not resolve to a public address", host)}
}

func (p *Proxy) dialUpstream(ctx context.Context, host string, ip netip.Addr, port int) (net.Conn, netip.Addr, error) {
	addr, err := p.resolve(ctx, host, ip)
	if err != nil {
		return nil, addr, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c, err := p.dial(ctx, "tcp", netip.AddrPortFrom(addr, uint16(port)).String())
	return c, addr, err
}

// ServeTransparent accepts redirected connections.
func (p *Proxy) ServeTransparent(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go p.handleTransparent(c)
	}
}

func (p *Proxy) handleTransparent(c net.Conn) {
	defer c.Close()
	ctx := context.Background()
	if p.OriginalDst == nil {
		return
	}
	dst, err := p.OriginalDst(c)
	if err != nil {
		p.logger().Warn("egress: original destination", "err", err)
		return
	}
	agent := p.agentFor(ctx, c.RemoteAddr())
	if agent == nil {
		return
	}
	p.HandleConn(ctx, c, agent, dst, "")
}

func (p *Proxy) agentFor(ctx context.Context, remote net.Addr) *Agent {
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return nil
	}
	a, err := p.Agents.AgentForSource(ctx, ap.Addr().Unmap())
	if err != nil || a == nil {
		p.logger().Warn("egress: connection from unknown source", "src", remote.String())
		return nil
	}
	return a
}

// HandleConn handles one intercepted connection to dst. knownHost is the name
// the client asked for (explicit proxy), if any.
func (p *Proxy) HandleConn(ctx context.Context, c net.Conn, agent *Agent, dst netip.AddrPort, knownHost string) {
	timeout := p.PeekTimeout
	if timeout == 0 {
		timeout = time.Second
	}
	br := bufio.NewReaderSize(c, 16*1024)
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	first, _ := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	conn := &bufConn{Conn: c, r: br}
	port := int(dst.Port())

	switch {
	case len(first) == 1 && first[0] == 0x16:
		hello, err := readClientHello(br)
		if err == nil && hello.ServerName != "" {
			p.handleTLS(ctx, conn, agent, dst, hello.ServerName)
			return
		}
		p.handleTCP(ctx, conn, agent, dst, knownHost, "tls")
	case len(first) == 1 && looksLikeHTTP(br):
		p.serveHTTP(ctx, conn, agent, "http", knownHost, dst.Addr(), port)
	default:
		p.handleTCP(ctx, conn, agent, dst, knownHost, "tcp")
	}
}

func looksLikeHTTP(br *bufio.Reader) bool {
	b, _ := br.Peek(8)
	for _, m := range []string{"GET ", "POST ", "PUT ", "HEAD ", "DELETE ", "OPTIONS ", "PATCH ", "TRACE "} {
		if bytes.HasPrefix(b, []byte(m)) || (len(b) < len(m) && bytes.HasPrefix([]byte(m), b) && len(b) >= 3) {
			return true
		}
	}
	return false
}

// handleTCP splices a connection that is not HTTP or has no SNI. The name is
// taken from the explicit proxy request or the agent's own DNS answers.
func (p *Proxy) handleTCP(ctx context.Context, c net.Conn, agent *Agent, dst netip.AddrPort, host, kind string) {
	if host == "" && p.Names != nil {
		host = p.Names.Lookup(agent.ID, dst.Addr())
	}
	ev := Event{AgentID: agent.ID, Kind: kind, Host: host, IP: dst.Addr().String(), Port: int(dst.Port())}
	if !agent.Policy.Allows(host, dst.Addr(), int(dst.Port())) {
		ev.Reason = "not in allowlist"
		p.log(ev)
		return
	}
	// With a name, connect to what the name resolves to; without one, to the
	// address the client used.
	up, addr, err := p.dialUpstream(ctx, hostIfName(host), dst.Addr(), int(dst.Port()))
	if err != nil {
		ev.Reason = err.Error()
		p.log(ev)
		return
	}
	ev.IP = addr.String()
	ev.Allowed = true
	p.splice(c, up, &ev)
}

func hostIfName(h string) string {
	if _, err := netip.ParseAddr(h); err == nil {
		return ""
	}
	return h
}

func (p *Proxy) splice(c, up net.Conn, ev *Event) {
	start := time.Now()
	defer up.Close()
	var upN, downN atomic.Int64
	done := make(chan struct{})
	go func() {
		n, _ := io.Copy(up, c)
		upN.Store(n)
		closeWrite(up)
		close(done)
	}()
	n, _ := io.Copy(c, up)
	downN.Store(n)
	closeWrite(c)
	<-done
	ev.BytesUp, ev.BytesDown, ev.Duration = upN.Load(), downN.Load(), time.Since(start)
	p.log(*ev)
}

func closeWrite(c net.Conn) {
	type cw interface{ CloseWrite() error }
	if x, ok := c.(cw); ok {
		_ = x.CloseWrite()
		return
	}
	if b, ok := c.(*bufConn); ok {
		closeWrite(b.Conn)
		return
	}
	_ = c.Close()
}

func (p *Proxy) injectionsFor(agent *Agent, host string) []secrets.Injection {
	var out []secrets.Injection
	for _, inj := range agent.Injections {
		if inj.Rule.MatchHost(host) {
			out = append(out, inj)
		}
	}
	return out
}

func (p *Proxy) handleTLS(ctx context.Context, c net.Conn, agent *Agent, dst netip.AddrPort, sni string) {
	sni = strings.ToLower(strings.TrimSuffix(sni, "."))
	port := int(dst.Port())
	if !agent.Policy.Allows(sni, netip.Addr{}, port) {
		p.log(Event{AgentID: agent.ID, Kind: "tls", Host: sni, IP: dst.Addr().String(), Port: port, Reason: "not in allowlist"})
		return
	}
	if len(p.injectionsFor(agent, sni)) == 0 || p.CA == nil {
		// Nothing to inject: pass the TLS session through untouched.
		up, addr, err := p.dialUpstream(ctx, sni, netip.Addr{}, port)
		ev := Event{AgentID: agent.ID, Kind: "tls", Host: sni, IP: addr.String(), Port: port}
		if err != nil {
			ev.Reason = err.Error()
			p.log(ev)
			return
		}
		ev.Allowed = true
		p.splice(c, up, &ev)
		return
	}
	cert, err := p.CA.Leaf(sni)
	if err != nil {
		p.logger().Error("egress: leaf certificate", "host", sni, "err", err)
		return
	}
	tc := tls.Server(c, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = tc.HandshakeContext(hctx)
	cancel()
	if err != nil {
		p.log(Event{AgentID: agent.ID, Kind: "https", Host: sni, Port: port, Reason: "client TLS handshake failed (CA not trusted?): " + err.Error()})
		return
	}
	p.serveHTTP(ctx, tc, agent, "https", sni, netip.Addr{}, port)
}

type ctxKey int

const reqInfoKey ctxKey = 0

type reqInfo struct {
	agent  *Agent
	scheme string
	host   string // fixed host (TLS SNI or explicit proxy); empty: use Host header
	ip     netip.Addr
	port   int
	ev     *Event
}

// serveHTTP serves HTTP/1.1 requests on c and forwards them upstream.
func (p *Proxy) serveHTTP(ctx context.Context, c net.Conn, agent *Agent, scheme, host string, ip netip.Addr, port int) {
	srv := &http.Server{
		Handler:           p.forwarder(),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          nil,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return withInfo(ctx, &reqInfo{agent: agent, scheme: scheme, host: host, ip: ip, port: port})
		},
	}
	ln := newOneConnListener(c)
	_ = srv.Serve(ln)
	<-ln.closed
	_ = ctx
}

func (p *Proxy) upstreamTransport() *http.Transport {
	p.transportOnce.Do(func() {
		p.transport = &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				host, portStr, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				port, _ := strconv.Atoi(portStr)
				c, _, err := p.dialUpstream(ctx, host, netip.Addr{}, port)
				return c, err
			},
			TLSClientConfig:       &tls.Config{RootCAs: p.UpstreamRoots, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2:     true,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 10 * time.Minute,
		}
	})
	return p.transport
}

func (p *Proxy) forwarder() http.Handler {
	rp := &httputil.ReverseProxy{
		Transport:     p.upstreamTransport(),
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			info := pr.In.Context().Value(reqInfoKey).(*reqInfo)
			ev := info.ev
			pr.Out.URL.Scheme = info.scheme
			pr.Out.URL.Host = net.JoinHostPort(ev.Host, strconv.Itoa(ev.Port))
			pr.Out.Host = pr.In.Host
			if info.host != "" {
				pr.Out.Host = hostWithPort(info.host, ev.Port, info.scheme)
			}
			// Secrets only travel encrypted: never inject into plain HTTP.
			if info.scheme != "https" {
				return
			}
			for _, inj := range p.injectionsFor(info.agent, ev.Host) {
				if ok, err := inj.Apply(pr.Out); err != nil {
					p.logger().Error("egress: inject", "secret", inj.SecretID, "err", err)
				} else if ok {
					ev.Injected = true
				}
			}
		},
		ModifyResponse: func(res *http.Response) error {
			info := res.Request.Context().Value(reqInfoKey).(*reqInfo)
			info.ev.Status = res.StatusCode
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			info := r.Context().Value(reqInfoKey).(*reqInfo)
			status := http.StatusBadGateway
			if errors.Is(err, errBlocked) {
				status = http.StatusForbidden
				info.ev.Allowed = false
				var be blockedError
				if errors.As(err, &be) {
					err = be
				}
			}
			info.ev.Status = status
			info.ev.Reason = err.Error()
			http.Error(w, "agenthold egress: "+err.Error(), status)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := r.Context().Value(reqInfoKey).(*reqInfo)
		host := base.host
		if host == "" {
			host = r.Host
			if r.URL.IsAbs() {
				host = r.URL.Host
			}
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		ev := &Event{AgentID: base.agent.ID, Kind: base.scheme, Host: host, Port: base.port, Method: r.Method, Path: r.URL.Path, At: time.Now()}
		start := time.Now()
		defer func() {
			ev.Duration = time.Since(start)
			p.log(*ev)
		}()
		if host == "" {
			ev.Reason = "no host"
			http.Error(w, "agenthold egress: request has no host", http.StatusBadRequest)
			return
		}
		if !base.agent.Policy.Allows(host, base.ip, base.port) {
			ev.Reason = "not in allowlist"
			ev.Status = http.StatusForbidden
			http.Error(w, fmt.Sprintf("agenthold egress: %s:%d is not allowed by this agent's network policy", host, base.port), http.StatusForbidden)
			return
		}
		ev.Allowed = true
		info := *base
		info.ev = ev
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqInfoKey, &info)))
	})
}

func hostWithPort(host string, port int, scheme string) string {
	if (scheme == "https" && port == 443) || (scheme == "http" && port == 80) {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// readClientHello parses the TLS ClientHello without consuming it.
func readClientHello(br *bufio.Reader) (*tls.ClientHelloInfo, error) {
	hdr, err := br.Peek(5)
	if err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[3:5]))
	data, err := br.Peek(5 + n)
	if err != nil {
		return nil, err
	}
	var hello *tls.ClientHelloInfo
	errDone := errors.New("done")
	_ = tls.Server(readOnlyConn{r: bytes.NewReader(data)}, &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			cp := *h
			hello = &cp
			return nil, errDone
		},
	}).HandshakeContext(context.Background())
	if hello == nil {
		return nil, errors.New("no client hello")
	}
	return hello, nil
}

type readOnlyConn struct{ r io.Reader }

func (c readOnlyConn) Read(p []byte) (int, error)     { return c.r.Read(p) }
func (readOnlyConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (readOnlyConn) Close() error                     { return nil }
func (readOnlyConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (readOnlyConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (readOnlyConn) SetDeadline(time.Time) error      { return nil }
func (readOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (readOnlyConn) SetWriteDeadline(time.Time) error { return nil }

// bufConn is a conn whose first bytes were peeked into r.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// oneConnListener serves exactly one connection.
type oneConnListener struct {
	c      net.Conn
	once   sync.Once
	closed chan struct{}
}

func newOneConnListener(c net.Conn) *oneConnListener {
	return &oneConnListener{c: &notifyConn{Conn: c}, closed: make(chan struct{})}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() {
		nc := l.c.(*notifyConn)
		nc.onClose = func() { close(l.closed) }
		c = nc
	})
	if c != nil {
		return c, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.c.LocalAddr() }

type notifyConn struct {
	net.Conn
	once    sync.Once
	onClose func()
}

func (c *notifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return err
}

func withInfo(ctx context.Context, info *reqInfo) context.Context {
	return context.WithValue(ctx, reqInfoKey, info)
}
