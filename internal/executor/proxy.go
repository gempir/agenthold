package executor

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// ProxyRequest forwards an already Agenthold-authenticated & CSRF-validated
// HTTP/SSE request to userID's isolated Executor instance.
//
// Upstream Executor session cookies are paired and cached server-side per
// (gateSessionKey, userID, inst.ID), so switching accounts or restarting
// Executor never strands a browser with a stale cookie from another user or
// instance, and no Agenthold credentials ever reach Executor.
func (m *Manager) ProxyRequest(w http.ResponseWriter, r *http.Request, userID, gateSessionKey string, stripCookieNames []string) {
	ctx := r.Context()
	inst, release, err := m.acquireInstance(ctx, userID)
	if err != nil {
		http.Error(w, "Executor unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer release()

	// When allow_private_fetch is false, restrict the Executor dashboard to
	// official catalog apps: block custom app imports, code deploys, and git pushes
	// because Executor's embedded workerd allows private-network fetches for app code.
	if !m.cfg.AllowPrivateFetch && isCustomAppMutation(r) {
		http.Error(w, ErrCustomAppsDisabled.Error(), http.StatusForbidden)
		return
	}

	var pairedCookies []*http.Cookie
	if !strings.HasPrefix(r.URL.Path, "/auth/") {
		cookies, err := m.ensurePairedCookies(ctx, gateSessionKey, userID, inst)
		if err != nil {
			m.log.WarnContext(ctx, "auto-pair executor dashboard", "user", userID, "err", err)
		} else {
			pairedCookies = cookies
		}
	}

	target, err := url.Parse(inst.URL)
	if err != nil {
		http.Error(w, "Executor proxy target error", http.StatusBadGateway)
		return
	}
	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Transport:     m.proxyTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme = target.Scheme
			out.URL.Host = target.Host
			// Never forward Agenthold bearer tokens or auth headers upstream.
			out.Header.Del("Authorization")

			// Executor's localRequest validates Host and Origin against either
			// EXECUTOR_BROWSER_ORIGIN (HTTPS) or 127.0.0.1:<port>.
			// ExecutorHostHandler already verified same-origin CSRF before calling us.
			if inst.BrowserOrigin != "" {
				if bu, err := url.Parse(inst.BrowserOrigin); err == nil && bu.Host != "" {
					out.Host = bu.Host
					if out.Header.Get("Origin") != "" {
						out.Header.Set("Origin", inst.BrowserOrigin)
					}
				}
			} else {
				out.Host = inst.Host
				if out.Header.Get("Origin") != "" {
					out.Header.Set("Origin", inst.URL)
				}
			}

			// Strip Agenthold session cookies AND any browser-supplied executor_session_*
			// cookies so stale cookies from a previous user/instance can never collide.
			stripRequestCookies(out.Header, stripCookieNames, true)
			for _, c := range pairedCookies {
				out.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
			}
		},
		ModifyResponse: func(res *http.Response) error {
			// If upstream rejected our cached paired session, drop it so the next load re-pairs.
			if res.StatusCode == http.StatusUnauthorized && gateSessionKey != "" {
				m.ClearSessionPairing(gateSessionKey)
			}
			// Keep non-session cookies (e.g., OAuth state cookies) usable across OAuth redirects
			// while keeping executor_session_* server-managed in m.paired.
			if lines := res.Header.Values("Set-Cookie"); len(lines) > 0 {
				res.Header.Del("Set-Cookie")
				for _, line := range lines {
					if c, err := http.ParseSetCookie(line); err == nil {
						if strings.HasPrefix(strings.ToLower(c.Name), "executor_session_") {
							m.updatePairedCookie(gateSessionKey, userID, inst.ID, c)
							continue
						}
						c.Domain = ""
						if strings.HasPrefix(m.cfg.DashboardOrigin, "https://") {
							c.Secure = true
						}
						if c.SameSite == http.SameSiteStrictMode {
							c.SameSite = http.SameSiteLaxMode
						}
						res.Header.Add("Set-Cookie", c.String())
					} else {
						res.Header.Add("Set-Cookie", line)
					}
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if ctx.Err() != nil {
				return
			}
			slog.WarnContext(r.Context(), "executor dashboard proxy", "user", userID, "path", r.URL.Path, "err", err)
			http.Error(w, "Executor did not respond", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

func (m *Manager) ensurePairedCookies(ctx context.Context, gateSessionKey, userID string, inst *Instance) ([]*http.Cookie, error) {
	if gateSessionKey != "" {
		m.pairMu.Lock()
		entry, ok := m.paired[gateSessionKey]
		m.pairMu.Unlock()
		if ok && entry.userID == userID && entry.instID == inst.ID && len(entry.cookies) > 0 {
			return entry.cookies, nil
		}
	}
	cookies, err := m.PairBrowser(ctx, inst)
	if err != nil {
		return nil, err
	}
	if gateSessionKey != "" && len(cookies) > 0 {
		m.pairMu.Lock()
		m.paired[gateSessionKey] = pairedEntry{userID: userID, instID: inst.ID, cookies: cookies}
		m.pairMu.Unlock()
	}
	return cookies, nil
}

func (m *Manager) updatePairedCookie(gateSessionKey, userID, instID string, c *http.Cookie) {
	if gateSessionKey == "" {
		return
	}
	m.pairMu.Lock()
	defer m.pairMu.Unlock()
	if c.MaxAge < 0 || c.Value == "" {
		delete(m.paired, gateSessionKey)
		return
	}
	m.paired[gateSessionKey] = pairedEntry{userID: userID, instID: instID, cookies: []*http.Cookie{c}}
}

func isCustomAppMutation(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return false
	}
	p := strings.ToLower(r.URL.Path)
	// Allow official catalog installs (/dashboard/api/catalog/import).
	if strings.Contains(p, "/catalog/import") {
		return false
	}
	for _, blocked := range []string{
		"/apps/import",
		"/apps/deploy",
		"/apps/create",
		"/apps/push",
		"/apps/publish",
		"/git/",
		"git-receive-pack",
	} {
		if strings.Contains(p, blocked) {
			return true
		}
	}
	return false
}

func stripRequestCookies(h http.Header, names []string, stripExecutorSession bool) {
	lines := h.Values("Cookie")
	if len(lines) == 0 {
		return
	}
	var kept []string
	for _, line := range lines {
		for part := range strings.SplitSeq(line, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			cleanName := strings.TrimSpace(name)
			drop := stripExecutorSession && strings.HasPrefix(strings.ToLower(cleanName), "executor_session_")
			if !drop {
				for _, n := range names {
					if strings.EqualFold(cleanName, n) {
						drop = true
						break
					}
				}
			}
			if !drop {
				kept = append(kept, part)
			}
		}
	}
	h.Del("Cookie")
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}
