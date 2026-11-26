package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrPrivateURL is returned when a URL targets a private, loopback, or
// link-local address while executor.allow_private_fetch is false.
var ErrPrivateURL = errors.New("private or local network URLs are blocked (enable executor.allow_private_fetch to allow)")

// ValidatePublicURL verifies that rawURL is an http(s) URL whose host does not
// name or resolve to a private, loopback, link-local, or otherwise non-public IP.
func ValidatePublicURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an http(s) URL")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || isBlockedHostname(host) {
		return ErrPrivateURL
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(addr) {
			return ErrPrivateURL
		}
		return nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q did not resolve to any IP address", host)
	}
	for _, addr := range ips {
		if !isPublicAddr(addr) {
			return ErrPrivateURL
		}
	}
	return nil
}

func isBlockedHostname(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	return false
}

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

func isPublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() {
		return false
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	if cgnatPrefix.Contains(addr) {
		return false
	}
	return true
}

// publicDialContext dials host:port only after verifying every resolved IP is public.
func publicDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	cleanHost := strings.TrimSuffix(strings.ToLower(host), ".")
	if cleanHost == "" || isBlockedHostname(cleanHost) {
		return nil, ErrPrivateURL
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(cleanHost); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", cleanHost)
		if err != nil {
			return nil, err
		}
		addrs = ips
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses for %q", host)
	}
	for _, ip := range addrs {
		if !isPublicAddr(ip) {
			return nil, ErrPrivateURL
		}
	}
	var d net.Dialer
	d.Timeout = 15 * time.Second
	var lastErr error
	for _, ip := range addrs {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// egressGuard is a local HTTP/CONNECT proxy that enforces public-IP-only egress
// for Executor child processes when executor.allow_private_fetch is false.
type egressGuard struct {
	srv *http.Server
	url string
}

func startEgressGuard(log *slog.Logger) (*egressGuard, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DialContext:           publicDialContext,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			handleGuardConnect(w, r)
			return
		}
		handleGuardHTTP(w, r, transport)
	})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && log != nil {
			log.Warn("executor egress guard stopped", "err", err)
		}
	}()
	return &egressGuard{srv: srv, url: "http://" + ln.Addr().String()}, nil
}

func (g *egressGuard) Close() error {
	if g == nil || g.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return g.srv.Shutdown(ctx)
}

func handleGuardConnect(w http.ResponseWriter, r *http.Request) {
	upstream, err := publicDialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrPrivateURL) {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "hijacking unavailable", http.StatusInternalServerError)
		return
	}
	clientConn, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	defer clientConn.Close()
	defer upstream.Close()
	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if buf.Reader.Buffered() > 0 {
			_, _ = io.CopyN(upstream, buf, int64(buf.Reader.Buffered()))
		}
		_, _ = io.Copy(upstream, clientConn)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, upstream)
		if cw, ok := clientConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	wg.Wait()
}

func handleGuardHTTP(w http.ResponseWriter, r *http.Request, transport *http.Transport) {
	if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
		http.Error(w, "unsupported proxy scheme", http.StatusBadRequest)
		return
	}
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Del("Proxy-Authorization")
	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrPrivateURL) {
			status = http.StatusForbidden
		}
		http.Error(w, err.Error(), status)
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
