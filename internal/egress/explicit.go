package egress

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
)

// ExplicitHandler is the HTTP proxy that HTTPS_PROXY points at. CONNECT
// tunnels go through the same pipeline as transparent connections; plain
// absolute-URI requests are forwarded with injection.
func (p *Proxy) ExplicitHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ap, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "bad remote address", http.StatusBadRequest)
			return
		}
		agent, err := p.Agents.AgentForSource(ctx, ap.Addr().Unmap())
		if err != nil || agent == nil {
			http.Error(w, "unknown sandbox", http.StatusForbidden)
			return
		}
		host, portStr, err := net.SplitHostPort(r.Host)
		if r.Method != http.MethodConnect {
			// Absolute-form request ("GET http://host/path").
			if !r.URL.IsAbs() {
				http.Error(w, "this is a proxy", http.StatusBadRequest)
				return
			}
			host, portStr, err = net.SplitHostPort(r.URL.Host)
			if err != nil {
				host, portStr, err = r.URL.Hostname(), "80", nil
			}
		}
		if err != nil {
			http.Error(w, "bad target", http.StatusBadRequest)
			return
		}
		port, _ := strconv.Atoi(portStr)
		if port < 1 || port > 65535 {
			http.Error(w, "bad port", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodConnect {
			info := &reqInfo{agent: agent, scheme: "http", host: "", port: port}
			p.forwarder().ServeHTTP(w, r.WithContext(withInfo(ctx, info)))
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		c, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		_ = buf.Flush()
		var dst netip.AddrPort
		if ip, err := netip.ParseAddr(host); err == nil {
			dst = netip.AddrPortFrom(ip, uint16(port))
			host = ""
		} else {
			dst = netip.AddrPortFrom(netip.IPv4Unspecified(), uint16(port))
		}
		conn := c
		if buf.Reader.Buffered() > 0 {
			conn = &bufConn{Conn: c, r: buf.Reader}
		}
		p.HandleConn(ctx, conn, agent, dst, host)
	})
}
