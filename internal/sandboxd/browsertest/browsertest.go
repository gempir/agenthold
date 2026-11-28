// Package browsertest is an in-memory sandboxd.BrowserDriver for tests: a
// browser whose pages are described up front, with a history, input that
// clicks links and fills fields, and real (single-colour) PNG screenshots.
package browsertest

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Site is a page the fake browser can show.
type Site struct {
	Title    string
	Text     string
	Elements []tunnel.BrowserElement
	// ConsoleErrors are logged when the page loads.
	ConsoleErrors []string
	// Fail makes loading it fail with this error ("net::ERR_CONNECTION_REFUSED").
	Fail string
}

// Driver is a fake browser driver. Pages not in Sites load as a page titled
// after their URL with a "Go" button (#go) and a link (#next) to <url>/next.
type Driver struct {
	mu          sync.Mutex
	Unavailable string
	Sites       map[string]Site
	launches    int
	last        sandboxd.BrowserLaunch
	page        *Page
}

var _ sandboxd.BrowserDriver = (*Driver)(nil)

// New returns a driver with these sites.
func New(sites map[string]Site) *Driver {
	if sites == nil {
		sites = map[string]Site{}
	}
	return &Driver{Sites: sites}
}

// SetUnavailable makes Available report why (empty: available).
func (d *Driver) SetUnavailable(why string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Unavailable = why
}

// Available implements sandboxd.BrowserDriver.
func (d *Driver) Available() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Unavailable
}

// Launch implements sandboxd.BrowserDriver.
func (d *Driver) Launch(_ context.Context, o sandboxd.BrowserLaunch) (sandboxd.BrowserPage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Unavailable != "" {
		return nil, errors.New(d.Unavailable)
	}
	d.launches++
	d.last = o
	d.page = &Page{d: d, width: o.Width, height: o.Height, done: make(chan struct{}), history: []string{"about:blank"}}
	return d.page, nil
}

// Launches counts the browsers started.
func (d *Driver) Launches() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.launches
}

// LastLaunch is the configuration of the last start.
func (d *Driver) LastLaunch() sandboxd.BrowserLaunch {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// Page is the current page (nil before the first start).
func (d *Driver) Page() *Page {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.page
}

func (d *Driver) site(u string) Site {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.Sites[u]; ok {
		return s
	}
	if u == "about:blank" {
		return Site{}
	}
	return Site{
		Title: "Page " + u,
		Text:  "Welcome to " + u,
		Elements: []tunnel.BrowserElement{
			{Role: "button", Name: "Go", Tag: "button", Selector: "#go", Rect: tunnel.BrowserRect{X: 10, Y: 10, Width: 100, Height: 30}},
			{Role: "link", Name: "Next", Tag: "a", Selector: "#next", Href: strings.TrimSuffix(u, "/") + "/next", Rect: tunnel.BrowserRect{X: 10, Y: 60, Width: 80, Height: 20}},
			{Role: "textbox", Name: "Search", Tag: "input", Selector: "#q", Rect: tunnel.BrowserRect{X: 10, Y: 100, Width: 200, Height: 30}},
		},
	}
}

// Page is a fake page.
type Page struct {
	d *Driver

	mu       sync.Mutex
	width    int
	height   int
	history  []string
	index    int
	focused  string
	values   map[string]string
	errs     []string
	calls    []string
	closed   bool
	done     chan struct{}
	scrolled [2]float64
}

var _ sandboxd.BrowserPage = (*Page)(nil)

func (p *Page) record(format string, args ...any) {
	p.calls = append(p.calls, fmt.Sprintf(format, args...))
}

// Calls lists what was done to the page ("navigate http://x", "click 10 20", ...).
func (p *Page) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// Value is what was typed into the element with this selector.
func (p *Page) Value(selector string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.values[selector]
}

// Crash ends the browser as if it had exited.
func (p *Page) Crash() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
}

// Closed reports whether Close was called (or it crashed).
func (p *Page) Closed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *Page) check() error {
	if p.closed {
		return errors.New("browser closed")
	}
	return nil
}

func (p *Page) current() string { return p.history[p.index] }

func (p *Page) load(u string) error {
	s := p.d.site(u)
	if s.Fail != "" {
		return fmt.Errorf("could not open %s: %s", u, s.Fail)
	}
	p.history = append(p.history[:p.index+1], u)
	p.index = len(p.history) - 1
	p.errs = append(p.errs, s.ConsoleErrors...)
	return nil
}

// SetViewport implements sandboxd.BrowserPage.
func (p *Page) SetViewport(_ context.Context, w, h int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("viewport %dx%d", w, h)
	p.width, p.height = w, h
	return p.check()
}

// Navigate implements sandboxd.BrowserPage.
func (p *Page) Navigate(_ context.Context, u string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return err
	}
	p.record("navigate %s", u)
	return p.load(u)
}

// History implements sandboxd.BrowserPage.
func (p *Page) History(_ context.Context, delta int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return err
	}
	p.record("history %d", delta)
	i := p.index + delta
	if i < 0 || i >= len(p.history) {
		return errors.New("no history entry")
	}
	p.index = i
	return nil
}

// Reload implements sandboxd.BrowserPage.
func (p *Page) Reload(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("reload")
	return p.check()
}

// WaitLoad implements sandboxd.BrowserPage.
func (p *Page) WaitLoad(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.check()
}

func inside(r tunnel.BrowserRect, x, y float64) bool {
	return x >= r.X && x <= r.X+r.Width && y >= r.Y && y <= r.Y+r.Height
}

// Click implements sandboxd.BrowserPage: a click on a link follows it, on a
// text box focuses it.
func (p *Page) Click(_ context.Context, x, y float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return err
	}
	p.record("click %g %g", x, y)
	for _, e := range p.d.site(p.current()).Elements {
		if !inside(e.Rect, x, y) {
			continue
		}
		if e.Href != "" {
			return p.load(e.Href)
		}
		p.focused = e.Selector
		return nil
	}
	return nil
}

// InsertText implements sandboxd.BrowserPage.
func (p *Page) InsertText(_ context.Context, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return err
	}
	p.record("type %s", text)
	if p.values == nil {
		p.values = map[string]string{}
	}
	p.values[p.focused] += text
	return nil
}

// Press implements sandboxd.BrowserPage.
func (p *Page) Press(_ context.Context, key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return err
	}
	p.record("press %s", key)
	return nil
}

// Scroll implements sandboxd.BrowserPage.
func (p *Page) Scroll(_ context.Context, x, y, dx, dy float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return err
	}
	p.record("scroll %g %g %g %g", x, y, dx, dy)
	p.scrolled[0] += dx
	p.scrolled[1] += dy
	return nil
}

// Screenshot implements sandboxd.BrowserPage: a PNG of the viewport's size.
func (p *Page) Screenshot(context.Context) (string, error) {
	p.mu.Lock()
	w, h := p.width, p.height
	err := p.check()
	p.mu.Unlock()
	if err != nil {
		return "", err
	}
	img := image.NewUniform(color.RGBA{R: 30, G: 60, B: 90, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, &sized{img, image.Rect(0, 0, w, h)}); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

type sized struct {
	*image.Uniform
	r image.Rectangle
}

func (s *sized) Bounds() image.Rectangle { return s.r }

// Info implements sandboxd.BrowserPage.
func (p *Page) Info(context.Context) (sandboxd.BrowserInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return sandboxd.BrowserInfo{}, err
	}
	u := p.current()
	return sandboxd.BrowserInfo{URL: u, Title: p.d.site(u).Title}, nil
}

// Elements implements sandboxd.BrowserPage.
func (p *Page) Elements(context.Context) ([]tunnel.BrowserElement, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return nil, err
	}
	els := append([]tunnel.BrowserElement(nil), p.d.site(p.current()).Elements...)
	for i := range els {
		els[i].Value = p.values[els[i].Selector]
	}
	return els, nil
}

// Text implements sandboxd.BrowserPage.
func (p *Page) Text(_ context.Context, max int) (string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return "", false, err
	}
	t := p.d.site(p.current()).Text
	if len(t) > max {
		return t[:max], true, nil
	}
	return t, false, nil
}

func (p *Page) find(selector string) (tunnel.BrowserElement, error) {
	if strings.ContainsAny(selector, "!@") {
		return tunnel.BrowserElement{}, fmt.Errorf("SyntaxError: '%s' is not a valid selector", selector)
	}
	for _, e := range p.d.site(p.current()).Elements {
		if e.Selector == selector {
			return e, nil
		}
	}
	return tunnel.BrowserElement{}, fmt.Errorf("no element matches %s", selector)
}

// Locate implements sandboxd.BrowserPage.
func (p *Page) Locate(_ context.Context, selector string) (tunnel.BrowserRect, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return tunnel.BrowserRect{}, err
	}
	e, err := p.find(selector)
	return e.Rect, err
}

// Focus implements sandboxd.BrowserPage.
func (p *Page) Focus(_ context.Context, selector string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return err
	}
	if _, err := p.find(selector); err != nil {
		return err
	}
	p.record("focus %s", selector)
	p.focused = selector
	return nil
}

// Query implements sandboxd.BrowserPage.
func (p *Page) Query(_ context.Context, selector string) ([]tunnel.BrowserMatch, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.check(); err != nil {
		return nil, err
	}
	e, err := p.find(selector)
	if err != nil {
		if strings.HasPrefix(err.Error(), "no element") {
			return []tunnel.BrowserMatch{}, nil
		}
		return nil, err
	}
	attrs := map[string]string{}
	if e.Href != "" {
		attrs["href"] = e.Href
	}
	return []tunnel.BrowserMatch{{Tag: e.Tag, Text: e.Name, Selector: e.Selector, Attributes: attrs, Rect: e.Rect}}, nil
}

// ConsoleErrors implements sandboxd.BrowserPage.
func (p *Page) ConsoleErrors() (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.errs) == 0 {
		return 0, ""
	}
	return len(p.errs), p.errs[len(p.errs)-1]
}

// ResetConsole implements sandboxd.BrowserPage.
func (p *Page) ResetConsole() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errs = nil
}

// Done implements sandboxd.BrowserPage.
func (p *Page) Done() <-chan struct{} { return p.done }

// Close implements sandboxd.BrowserPage.
func (p *Page) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("close")
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	return nil
}
