package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// Browser files, relative to the home: the user data directory, and where
// the preview_screenshot tool saves screenshots.
const (
	BrowserProfile = ".agenthold/browser-profile"
	ScreenshotDir  = ".agenthold/screenshots"
)

// Browser limits and defaults.
const (
	browserDefaultWidth  = 1280
	browserDefaultHeight = 800
	browserMinSize       = 200
	browserMaxWidth      = 3840
	browserMaxHeight     = 4320
	browserMaxText       = 20_000
	browserOpTimeout     = 45 * time.Second
)

// Timing the tests shorten.
var (
	// browserSettle is how long an input action waits for what it started
	// (a navigation, an animation) before the page is captured.
	browserSettle = 250 * time.Millisecond
	// browserIdle stops a browser nobody used for this long: it holds
	// memory, and its timers keep the machine from looking idle.
	browserIdle = 15 * time.Minute
)

// BrowserDriver starts the headless browser sandboxd drives. The default
// starts Chromium and talks to it over the Chrome DevTools Protocol; tests
// inject their own.
type BrowserDriver interface {
	// Available reports whether a browser can be started ("" when it can,
	// else why not).
	Available() string
	// Launch starts a browser with one page.
	Launch(ctx context.Context, opts BrowserLaunch) (BrowserPage, error)
}

// BrowserLaunch configures a browser start.
type BrowserLaunch struct {
	// ProfileDir is the user data directory (it exists).
	ProfileDir string
	// Env is the process environment.
	Env []string
	// Width and Height are the first viewport.
	Width, Height int
	// Prepare sets up the process (account, process group) before it starts.
	Prepare func(*exec.Cmd)
	// TrustSPKI are base64 SHA-256 hashes of public keys whose certificates
	// the browser accepts (the egress CA, which intercepts TLS).
	TrustSPKI []string
	Log       *slog.Logger
}

// BrowserInfo is where a page is.
type BrowserInfo struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// BrowserPage is the one page of a running browser. Coordinates are viewport
// CSS pixels. Methods are called one at a time.
type BrowserPage interface {
	SetViewport(ctx context.Context, width, height int) error
	// Navigate loads url and waits for it (like WaitLoad).
	Navigate(ctx context.Context, url string) error
	// History moves delta entries through the history (-1 back, 1 forward).
	History(ctx context.Context, delta int) error
	Reload(ctx context.Context) error
	// WaitLoad waits (a bounded time) until the document has loaded.
	WaitLoad(ctx context.Context) error
	Click(ctx context.Context, x, y float64) error
	InsertText(ctx context.Context, text string) error
	Press(ctx context.Context, key string) error
	Scroll(ctx context.Context, x, y, dx, dy float64) error
	// Screenshot is the viewport as a base64 PNG.
	Screenshot(ctx context.Context) (string, error)
	Info(ctx context.Context) (BrowserInfo, error)
	// Elements lists the interactive elements that are rendered.
	Elements(ctx context.Context) ([]tunnel.BrowserElement, error)
	// Text is the page's visible text, at most max characters (and whether it was cut).
	Text(ctx context.Context, max int) (string, bool, error)
	// Locate scrolls the first element matching selector into view and returns its box.
	Locate(ctx context.Context, selector string) (tunnel.BrowserRect, error)
	// Focus focuses the first element matching selector.
	Focus(ctx context.Context, selector string) error
	// Query describes the elements matching selector (read-only).
	Query(ctx context.Context, selector string) ([]tunnel.BrowserMatch, error)
	// ConsoleErrors counts the errors logged since ResetConsole, with the last one.
	ConsoleErrors() (int, string)
	ResetConsole()
	// Done is closed when the browser has exited.
	Done() <-chan struct{}
	Close() error
}

// errBrowserGone is returned when the browser exited during an operation.
var errBrowserGone = errors.New("the browser exited; try again to start a new one")

// browserCtl owns the sandbox's one browser: it starts it on first use,
// serializes operations and stops it when unused.
type browserCtl struct {
	d      *Daemon
	driver BrowserDriver

	mu     sync.Mutex
	page   BrowserPage
	width  int
	height int
	idle   *time.Timer
}

func newBrowserCtl(d *Daemon, driver BrowserDriver) *browserCtl {
	if driver == nil {
		driver = ChromiumDriver()
	}
	return &browserCtl{d: d, driver: driver, width: browserDefaultWidth, height: browserDefaultHeight}
}

// browser serves an OpBrowser stream.
func (d *Daemon) browser(ctx context.Context, st net.Conn, o tunnel.Open) error {
	if o.Browser == nil {
		return errors.New("browser: no request")
	}
	ctx, cancel := context.WithTimeout(ctx, browserOpTimeout)
	defer cancel()
	reply, err := d.browserCtl.do(ctx, *o.Browser)
	if err != nil {
		return err
	}
	return tunnel.WriteJSONData(st, reply)
}

// alive reports whether the page's browser still runs (with mu held).
func (b *browserCtl) alive() bool {
	if b.page == nil {
		return false
	}
	select {
	case <-b.page.Done():
		b.page = nil
		return false
	default:
		return true
	}
}

func (b *browserCtl) do(ctx context.Context, req tunnel.BrowserRequest) (tunnel.BrowserReply, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch req.Action {
	case tunnel.BrowserStatus:
		return b.status(ctx), nil
	case tunnel.BrowserClose:
		b.stopLocked()
		return b.status(ctx), nil
	case tunnel.BrowserNavigate, tunnel.BrowserScreenshot, tunnel.BrowserInteract, tunnel.BrowserSnapshot:
	default:
		return tunnel.BrowserReply{}, fmt.Errorf("browser: unknown action %q", req.Action)
	}
	// Check the request before a browser starts for it.
	var target string
	switch req.Action {
	case tunnel.BrowserNavigate:
		u, err := normalizeBrowserURL(req.URL)
		if err != nil {
			return tunnel.BrowserReply{}, err
		}
		target = u
	case tunnel.BrowserInteract:
		if err := checkInteraction(req); err != nil {
			return tunnel.BrowserReply{}, err
		}
	}
	if err := checkViewport(req.Width, req.Height); err != nil {
		return tunnel.BrowserReply{}, err
	}
	if err := b.ensure(ctx); err != nil {
		return tunnel.BrowserReply{}, err
	}
	b.touch()
	reply, err := b.run(ctx, req, target)
	if err != nil && !b.alive() {
		return tunnel.BrowserReply{}, errBrowserGone
	}
	return reply, err
}

func (b *browserCtl) run(ctx context.Context, req tunnel.BrowserRequest, target string) (tunnel.BrowserReply, error) {
	p := b.page
	if w, h := req.Width, req.Height; (w > 0 || h > 0) && (w != b.width || h != b.height) {
		if w == 0 {
			w = b.width
		}
		if h == 0 {
			h = b.height
		}
		if err := p.SetViewport(ctx, w, h); err != nil {
			return tunnel.BrowserReply{}, fmt.Errorf("set the viewport: %w", err)
		}
		b.width, b.height = w, h
	}
	switch req.Action {
	case tunnel.BrowserNavigate:
		p.ResetConsole()
		if err := p.Navigate(ctx, target); err != nil {
			return tunnel.BrowserReply{}, err
		}
	case tunnel.BrowserSnapshot:
		reply, err := b.state(ctx, false)
		if err != nil {
			return reply, err
		}
		if reply.Text, reply.Truncated, err = p.Text(ctx, browserMaxText); err != nil {
			return reply, err
		}
		return reply, nil
	case tunnel.BrowserInteract:
		if req.Kind == tunnel.BrowserQuery {
			reply, err := b.state(ctx, false)
			if err != nil {
				return reply, err
			}
			if reply.Matches, err = p.Query(ctx, req.Selector); err != nil {
				return reply, err
			}
			return reply, nil
		}
		if err := b.interact(ctx, req); err != nil {
			return tunnel.BrowserReply{}, err
		}
	}
	return b.state(ctx, true)
}

func (b *browserCtl) interact(ctx context.Context, req tunnel.BrowserRequest) error {
	p := b.page
	switch req.Kind {
	case tunnel.BrowserClick:
		x, y := req.X, req.Y
		if req.Selector != "" {
			r, err := p.Locate(ctx, req.Selector)
			if err != nil {
				return err
			}
			x, y = r.X+r.Width/2, r.Y+r.Height/2
		} else if x < 0 || y < 0 || x > float64(b.width) || y > float64(b.height) {
			return fmt.Errorf("click: (%g, %g) is outside the %dx%d viewport", x, y, b.width, b.height)
		}
		if err := p.Click(ctx, x, y); err != nil {
			return err
		}
	case tunnel.BrowserType:
		if req.Selector != "" {
			if err := p.Focus(ctx, req.Selector); err != nil {
				return err
			}
		}
		if err := p.InsertText(ctx, req.Text); err != nil {
			return err
		}
	case tunnel.BrowserPress:
		if err := p.Press(ctx, req.Key); err != nil {
			return err
		}
	case tunnel.BrowserScroll:
		x, y := req.X, req.Y
		if x <= 0 && y <= 0 {
			x, y = float64(b.width)/2, float64(b.height)/2
		}
		if err := p.Scroll(ctx, x, y, req.DX, req.DY); err != nil {
			return err
		}
	case tunnel.BrowserBack, tunnel.BrowserForward:
		delta := -1
		if req.Kind == tunnel.BrowserForward {
			delta = 1
		}
		if err := p.History(ctx, delta); err != nil {
			return err
		}
	case tunnel.BrowserReload:
		p.ResetConsole()
		if err := p.Reload(ctx); err != nil {
			return err
		}
	}
	// Give what the input started (a navigation, a re-render) a moment.
	if browserSettle > 0 {
		t := time.NewTimer(browserSettle)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return p.WaitLoad(ctx)
}

// state describes the page; with capture also as a screenshot with its elements.
func (b *browserCtl) state(ctx context.Context, capture bool) (tunnel.BrowserReply, error) {
	p := b.page
	reply := tunnel.BrowserReply{Available: true, Running: true, Width: b.width, Height: b.height}
	info, err := p.Info(ctx)
	if err != nil {
		return reply, err
	}
	reply.URL, reply.Title = info.URL, info.Title
	reply.ConsoleErrors, reply.LastError = p.ConsoleErrors()
	if reply.Elements, err = p.Elements(ctx); err != nil {
		return reply, err
	}
	if capture {
		if reply.Screenshot, err = p.Screenshot(ctx); err != nil {
			return reply, fmt.Errorf("screenshot: %w", err)
		}
	}
	return reply, nil
}

// status describes the browser without starting it (with mu held).
func (b *browserCtl) status(ctx context.Context) tunnel.BrowserReply {
	reply := tunnel.BrowserReply{Available: b.driver.Available() == "", Width: b.width, Height: b.height}
	if !b.alive() {
		return reply
	}
	reply.Available, reply.Running = true, true
	ictx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if info, err := b.page.Info(ictx); err == nil {
		reply.URL, reply.Title = info.URL, info.Title
	}
	reply.ConsoleErrors, reply.LastError = b.page.ConsoleErrors()
	return reply
}

// ensure starts the browser unless it runs (with mu held).
func (b *browserCtl) ensure(ctx context.Context) error {
	if b.alive() {
		return nil
	}
	if why := b.driver.Available(); why != "" {
		return errors.New(tunnel.BrowserUnavailable + why)
	}
	d := b.d
	profile := filepath.Join(d.acct.home, BrowserProfile)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return fmt.Errorf("browser profile: %w", err)
	}
	d.own(filepath.Dir(profile))
	d.own(profile)
	// A browser that did not exit cleanly (or a machine cloned or resumed
	// under another hostname) leaves its profile locked.
	for _, f := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		_ = os.Remove(filepath.Join(profile, f))
	}
	page, err := b.driver.Launch(ctx, BrowserLaunch{
		ProfileDir: profile,
		Env:        d.env(nil, d.shell()[0]),
		Width:      b.width, Height: b.height,
		Prepare: func(cmd *exec.Cmd) {
			cmd.Dir = d.acct.home
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if d.acct.switchTo {
				cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(d.acct.uid), Gid: uint32(d.acct.gid)}
			}
		},
		TrustSPKI: caSPKI(CAFile),
		Log:       d.log,
	})
	if err != nil {
		return fmt.Errorf("start the browser: %w", err)
	}
	b.page = page
	d.log.Info("browser started")
	return nil
}

// touch restarts the idle timer (with mu held).
func (b *browserCtl) touch() {
	if browserIdle <= 0 {
		return
	}
	if b.idle != nil {
		b.idle.Stop()
	}
	page := b.page
	b.idle = time.AfterFunc(browserIdle, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.page == page {
			b.d.log.Info("browser stopped: unused")
			b.stopLocked()
		}
	})
}

func (b *browserCtl) stopLocked() {
	if b.idle != nil {
		b.idle.Stop()
		b.idle = nil
	}
	if b.page != nil {
		_ = b.page.Close()
		b.page = nil
	}
}

// close stops the browser (the daemon ends).
func (b *browserCtl) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
}

// normalizeBrowserURL accepts http(s) URLs and about:blank; a bare host
// ("localhost:3000") gets http://.
func normalizeBrowserURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("url is required")
	}
	if s == "about:blank" {
		return s, nil
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http and https URLs can be opened, not %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("invalid url: no host")
	}
	return u.String(), nil
}

func checkViewport(w, h int) error {
	if w != 0 && (w < browserMinSize || w > browserMaxWidth) {
		return fmt.Errorf("width must be between %d and %d", browserMinSize, browserMaxWidth)
	}
	if h != 0 && (h < browserMinSize || h > browserMaxHeight) {
		return fmt.Errorf("height must be between %d and %d", browserMinSize, browserMaxHeight)
	}
	return nil
}

func checkInteraction(req tunnel.BrowserRequest) error {
	switch req.Kind {
	case tunnel.BrowserClick, tunnel.BrowserScroll, tunnel.BrowserBack, tunnel.BrowserForward, tunnel.BrowserReload:
	case tunnel.BrowserType:
		if req.Text == "" {
			return errors.New("type: text is required")
		}
	case tunnel.BrowserPress:
		if strings.TrimSpace(req.Key) == "" {
			return errors.New("press: key is required")
		}
	case tunnel.BrowserQuery:
		if strings.TrimSpace(req.Selector) == "" {
			return errors.New("eval: selector is required (a CSS selector to query)")
		}
	case "":
		return errors.New("action is required")
	default:
		return fmt.Errorf("unknown action %q", req.Kind)
	}
	return nil
}
