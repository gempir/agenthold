package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/gempir/agenthold/internal/secrets"
)

type staticAgents struct{ a *Agent }

func (s staticAgents) AgentForSource(context.Context, netip.Addr) (*Agent, error) { return s.a, nil }

type events struct {
	mu sync.Mutex
	l  []Event
}

func (e *events) add(ev Event) { e.mu.Lock(); e.l = append(e.l, ev); e.mu.Unlock() }

// find waits briefly for an event for host (spliced connections are logged when they close).
func (e *events) find(host string) (Event, bool) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		e.mu.Lock()
		for i := len(e.l) - 1; i >= 0; i-- {
			if e.l[i].Host == host {
				ev := e.l[i]
				e.mu.Unlock()
				return ev, true
			}
		}
		e.mu.Unlock()
		if time.Now().After(deadline) {
			return Event{}, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type fixture struct {
	t       *testing.T
	ca      *CA
	agent   *Agent
	proxy   *Proxy
	events  *events
	tlsUp   *httptest.Server // upstream TLS server, echoes the Authorization header
	gotAuth chan string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ca: ca, events: &events{}, gotAuth: make(chan string, 10)}
	f.tlsUp = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth <- r.Header.Get("Authorization")
		fmt.Fprintf(w, "host=%s path=%s", r.Host, r.URL.Path)
	}))
	t.Cleanup(f.tlsUp.Close)
	roots := x509.NewCertPool()
	roots.AddCert(f.tlsUp.Certificate())
	f.agent = &Agent{ID: "ag_1", Policy: Policy{Mode: ModeOpen}}
	f.proxy = &Proxy{
		CA: ca, Agents: staticAgents{f.agent}, Names: NewNameCache(), Log: f.events.add,
		// Every name resolves to loopback, which the tests allow.
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		Reachable:     func(a netip.Addr) bool { return a.IsLoopback() },
		UpstreamRoots: roots,
		PeekTimeout:   200 * time.Millisecond,
	}
	return f
}

func (f *fixture) upPort() int {
	u, _ := url.Parse(f.tlsUp.URL)
	p, _ := strconv.Atoi(u.Port())
	return p
}

// explicit starts the explicit proxy and returns a client using it.
func (f *fixture) explicitClient(roots *x509.CertPool) *http.Client {
	srv := httptest.NewServer(f.proxy.ExplicitHandler())
	f.t.Cleanup(srv.Close)
	pu, _ := url.Parse(srv.URL)
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "api.example.com"},
	}, Timeout: 10 * time.Second}
}

func TestInjectionThroughExplicitProxy(t *testing.T) {
	f := newFixture(t)
	f.agent.Injections = []secrets.Injection{{
		SecretID: "sec_1", Value: "real-token", Placeholder: "ph-123",
		Rule: secrets.Rule{Hosts: []string{"api.example.com"}, Header: "Authorization", Template: "Bearer {{.Value}}"},
	}}
	// The sandbox trusts only the agenthold CA: the response must come through interception.
	c := f.explicitClient(f.ca.Pool())
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("https://api.example.com:%d/v1/messages", f.upPort()), nil)
	req.Header.Set("Authorization", "Bearer ph-123")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if got := <-f.gotAuth; got != "Bearer real-token" {
		t.Fatalf("upstream saw Authorization %q", got)
	}
	if !strings.Contains(string(body), "path=/v1/messages") || strings.Contains(string(body), "real-token") {
		t.Fatalf("body %q", body)
	}
	ev, ok := f.events.find("api.example.com")
	if !ok || !ev.Allowed || !ev.Injected || ev.Status != http.StatusOK || ev.Method != http.MethodGet || ev.Kind != "https" {
		t.Fatalf("event %+v", ev)
	}
}

func TestPassthroughWithoutInjection(t *testing.T) {
	f := newFixture(t)
	// No injection rule: TLS is not intercepted, so the client must trust the
	// upstream's own certificate, and the agenthold CA would not work.
	roots := x509.NewCertPool()
	roots.AddCert(f.tlsUp.Certificate())
	c := f.explicitClient(roots)
	c.Transport.(*http.Transport).TLSClientConfig.ServerName = "example.com" // httptest cert name
	res, err := c.Get(fmt.Sprintf("https://example.com:%d/x", f.upPort()))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	c.CloseIdleConnections()
	if got := <-f.gotAuth; got != "" {
		t.Fatalf("unexpected auth %q", got)
	}
	if ev, ok := f.events.find("example.com"); !ok || ev.Kind != "tls" || !ev.Allowed || ev.BytesDown == 0 {
		t.Fatalf("event %+v", ev)
	}
}

func TestAllowlist(t *testing.T) {
	f := newFixture(t)
	f.agent.Policy = Policy{Mode: ModeAllowlist, Allow: []string{fmt.Sprintf("example.com:%d", f.upPort())}}
	roots := x509.NewCertPool()
	roots.AddCert(f.tlsUp.Certificate())
	c := f.explicitClient(roots)
	c.Transport.(*http.Transport).TLSClientConfig.ServerName = "example.com"
	if res, err := c.Get(fmt.Sprintf("https://example.com:%d/", f.upPort())); err != nil {
		t.Fatalf("allowed host: %v", err)
	} else {
		res.Body.Close()
	}
	c.Transport.(*http.Transport).TLSClientConfig.ServerName = "other.example.org"
	if _, err := c.Get(fmt.Sprintf("https://other.example.org:%d/", f.upPort())); err == nil {
		t.Fatal("denied host reachable")
	}
	if ev, ok := f.events.find("other.example.org"); !ok || ev.Allowed || ev.Reason != "not in allowlist" {
		t.Fatalf("event %+v", ev)
	}
}

// transparent runs HandleConn on one side of a TCP pair, as if the connection
// had been redirected from dst.
func (f *fixture) transparent(dst netip.AddrPort) net.Conn {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	go func() {
		c, err := ln.Accept()
		ln.Close()
		if err == nil {
			f.proxy.HandleConn(context.Background(), c, f.agent, dst, "")
			c.Close()
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Close() })
	return c
}

func TestTransparentSNIAndPlainHTTP(t *testing.T) {
	f := newFixture(t)
	f.agent.Injections = []secrets.Injection{{
		Value: "v", Rule: secrets.Rule{Hosts: []string{"api.example.com"}, Header: "Authorization", Template: "Token {{.Value}}"},
	}}
	// The client connected to some address; the proxy goes by SNI, not by that address.
	dst := netip.AddrPortFrom(netip.MustParseAddr("203.0.113.9"), uint16(f.upPort()))
	tc := tls.Client(f.transparent(dst), &tls.Config{ServerName: "api.example.com", RootCAs: f.ca.Pool()})
	fmt.Fprintf(tc, "GET /t HTTP/1.1\r\nHost: api.example.com\r\nConnection: close\r\n\r\n")
	res, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := <-f.gotAuth; got != "Token v" || res.StatusCode != http.StatusOK {
		t.Fatalf("status %d auth %q", res.StatusCode, got)
	}

	// Plain HTTP: the Host header names the destination.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "plain-ok") }))
	defer plain.Close()
	pu, _ := url.Parse(plain.URL)
	pp, _ := strconv.Atoi(pu.Port())
	c := f.transparent(netip.AddrPortFrom(netip.MustParseAddr("203.0.113.9"), uint16(pp)))
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: plain.example.com\r\nConnection: close\r\n\r\n")
	res, err = http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if string(b) != "plain-ok" {
		t.Fatalf("plain body %q", b)
	}
	if ev, _ := f.events.find("plain.example.com"); ev.Kind != "http" || !ev.Allowed {
		t.Fatalf("plain event %+v", ev)
	}
}

func TestRawTCPUsesDNSNames(t *testing.T) {
	f := newFixture(t)
	// An "SSH server" that speaks first, like SSH does.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			fmt.Fprint(c, "SSH-2.0-test\r\n")
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ip := netip.MustParseAddr("127.0.0.1")
	f.proxy.Names.Add("ag_1", "github.com", ip, time.Minute)
	f.agent.Policy = Policy{Mode: ModeAllowlist, Allow: []string{fmt.Sprintf("github.com:%d", port)}}

	c := f.transparent(netip.AddrPortFrom(ip, uint16(port)))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "SSH-2.0") {
		t.Fatalf("banner %q %v", line, err)
	}
	c.Close()
	if ev, ok := f.events.find("github.com"); !ok || ev.Kind != "tcp" || !ev.Allowed {
		t.Fatalf("event %+v", ev)
	}

	// Not allowed: the same server under a name that is not on the list.
	f.proxy.Names.Add("ag_1", "gitlab.com", ip, time.Minute)
	f.proxy.Names = NewNameCache()
	f.proxy.Names.Add("ag_1", "gitlab.com", ip, time.Minute)
	c = f.transparent(netip.AddrPortFrom(ip, uint16(port)))
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, _ := c.Read(make([]byte, 10)); n != 0 {
		t.Fatal("denied connection got data")
	}
	if ev, ok := f.events.find("gitlab.com"); !ok || ev.Allowed {
		t.Fatalf("event %+v", ev)
	}
}

func TestPrivateAddressesBlockedEvenWhenOpen(t *testing.T) {
	f := newFixture(t)
	f.proxy.Reachable = nil // production rule: only public addresses
	f.proxy.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}
	c := f.explicitClient(f.ca.Pool())
	res, err := c.Get("http://metadata.example.com/latest/meta-data")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("metadata: %d", res.StatusCode)
	}
	if ev, ok := f.events.find("metadata.example.com"); !ok || ev.Allowed || !strings.Contains(ev.Reason, "not resolve to a public address") {
		t.Fatalf("event %+v", ev)
	}
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.17.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1", "fd00::1"} {
		if PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s considered public", s)
		}
	}
	if !PublicAddr(netip.MustParseAddr("1.1.1.1")) {
		t.Error("1.1.1.1 not public")
	}
}

func TestPolicy(t *testing.T) {
	p := Policy{Mode: ModeAllowlist, Allow: []string{"github.com:22", "*.npmjs.org", "registry.example.com:*", "1.2.3.0/24:443"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	ip := netip.Addr{}
	tests := []struct {
		host string
		ip   netip.Addr
		port int
		want bool
	}{
		{"github.com", ip, 22, true},
		{"github.com", ip, 443, false},
		{"registry.npmjs.org", ip, 443, true},
		{"npmjs.org", ip, 443, false},
		{"registry.example.com", ip, 5000, true},
		{"", netip.MustParseAddr("1.2.3.4"), 443, true},
		{"", netip.MustParseAddr("1.2.4.4"), 443, false},
		{"evil.com", ip, 80, false},
	}
	for _, tt := range tests {
		if got := p.Allows(tt.host, tt.ip, tt.port); got != tt.want {
			t.Errorf("Allows(%q, %v, %d) = %v", tt.host, tt.ip, tt.port, got)
		}
	}
	if !p.AllowsName("github.com") || p.AllowsName("evil.com") {
		t.Error("AllowsName")
	}
	for _, bad := range []Policy{{Mode: "x"}, {Mode: ModeAllowlist, Allow: []string{"*"}}, {Mode: ModeAllowlist, Allow: []string{"a.com:99999"}}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestDNS(t *testing.T) {
	// Upstream answers every A query with 93.184.216.34.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			h, _ := p.Start(buf[:n])
			q, _ := p.Question()
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true})
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{93, 184, 216, 34}})
			out, _ := b.Finish()
			_, _ = pc.WriteTo(out, addr)
		}
	}()
	agent := &Agent{ID: "ag_1", Policy: Policy{Mode: ModeAllowlist, Allow: []string{"github.com:22"}}}
	names := NewNameCache()
	var evs events
	d := &DNS{Agents: staticAgents{agent}, Names: names, Upstream: pc.LocalAddr().String(), Log: evs.add}

	query := func(name string) dnsmessage.RCode {
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 7, RecursionDesired: true})
		_ = b.StartQuestions()
		_ = b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
		q, _ := b.Finish()
		res := d.Handle(t.Context(), q, netip.MustParseAddr("10.0.0.9"), "udp")
		var p dnsmessage.Parser
		h, err := p.Start(res)
		if err != nil || h.ID != 7 {
			t.Fatalf("bad response %v", err)
		}
		return h.RCode
	}
	if rc := query("github.com."); rc != dnsmessage.RCodeSuccess {
		t.Fatalf("allowed name: %v", rc)
	}
	if got := names.Lookup("ag_1", netip.MustParseAddr("93.184.216.34")); got != "github.com" {
		t.Fatalf("name cache = %q", got)
	}
	if rc := query("data-to-exfiltrate.evil.com."); rc != dnsmessage.RCodeNameError {
		t.Fatalf("denied name: %v", rc)
	}
	if ev, ok := evs.find("data-to-exfiltrate.evil.com"); !ok || ev.Kind != "dns" || ev.Allowed {
		t.Fatalf("event %+v", ev)
	}
}
