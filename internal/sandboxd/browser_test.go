package sandboxd_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/sandboxd/browsertest"
	"github.com/gempir/agenthold/internal/tunnel"
)

func browserSetup(t *testing.T, drv *browsertest.Driver) (*tunnel.Session, string) {
	t.Helper()
	t.Cleanup(sandboxd.SetBrowserTiming(0, 0))
	return setupWith(t, func(c *sandboxd.Config) { c.Browser = drv })
}

func decodePNG(t *testing.T, b64 string) (int, int) {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img.Bounds().Dx(), img.Bounds().Dy()
}

func TestBrowserLifecycle(t *testing.T) {
	drv := browsertest.New(map[string]browsertest.Site{
		"http://localhost:3000/": {Title: "Todo", Text: "Buy milk", ConsoleErrors: []string{"boom"}, Elements: []tunnel.BrowserElement{
			{Role: "button", Name: "Add", Tag: "button", Selector: "#add", Rect: tunnel.BrowserRect{X: 100, Y: 200, Width: 40, Height: 20}},
			{Role: "textbox", Name: "New todo", Tag: "input", Selector: "#new", Rect: tunnel.BrowserRect{X: 10, Y: 10, Width: 300, Height: 30}},
		}},
	})
	s, home := browserSetup(t, drv)
	ctx := t.Context()

	// Status never starts it.
	st, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserStatus})
	if err != nil || !st.Available || st.Running || st.Width != 1280 || st.Height != 800 || drv.Launches() != 0 {
		t.Fatalf("status %+v %v", st, err)
	}

	// Navigating starts it with the profile in the home; a bare host gets http://.
	r, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserNavigate, URL: "localhost:3000/"})
	if err != nil {
		t.Fatal(err)
	}
	if r.URL != "http://localhost:3000/" || r.Title != "Todo" || !r.Running || len(r.Elements) != 2 || r.ConsoleErrors != 1 || r.LastError != "boom" {
		t.Fatalf("navigate %+v", r)
	}
	if w, h := decodePNG(t, r.Screenshot); w != 1280 || h != 800 {
		t.Fatalf("screenshot %dx%d", w, h)
	}
	l := drv.LastLaunch()
	if l.ProfileDir != filepath.Join(home, ".agenthold/browser-profile") || l.Width != 1280 || len(l.Env) == 0 {
		t.Fatalf("launch %+v", l)
	}
	if fi, err := os.Stat(l.ProfileDir); err != nil || !fi.IsDir() {
		t.Fatalf("profile %v", err)
	}

	// The viewport changes with a screenshot request.
	r, err = s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserScreenshot, Width: 390, Height: 844})
	if err != nil || r.Width != 390 || r.Height != 844 {
		t.Fatalf("resize %+v %v", r, err)
	}
	if w, h := decodePNG(t, r.Screenshot); w != 390 || h != 844 {
		t.Fatalf("resized screenshot %dx%d", w, h)
	}

	// Typing into a selector focuses it; clicking a selector clicks its center.
	if _, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserType, Selector: "#new", Text: "eggs"}); err != nil {
		t.Fatal(err)
	}
	if v := drv.Page().Value("#new"); v != "eggs" {
		t.Fatalf("typed %q", v)
	}
	if _, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserClick, Selector: "#add"}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []tunnel.BrowserRequest{
		{Kind: tunnel.BrowserPress, Key: "Enter"},
		{Kind: tunnel.BrowserScroll, DY: 300},
		{Kind: tunnel.BrowserReload},
	} {
		req.Action = tunnel.BrowserInteract
		if _, err := s.Browser(ctx, req); err != nil {
			t.Fatalf("%s: %v", req.Kind, err)
		}
	}
	calls := drv.Page().Calls()
	for _, want := range []string{"navigate http://localhost:3000/", "viewport 390x844", "focus #new", "type eggs", "click 120 210", "press Enter", "scroll 195 422 0 300", "reload"} {
		if !slices.Contains(calls, want) {
			t.Fatalf("no %q in %q", want, calls)
		}
	}

	// A read-only query, and the snapshot for agents.
	r, err = s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserQuery, Selector: "#add"})
	if err != nil || len(r.Matches) != 1 || r.Matches[0].Text != "Add" || r.Screenshot != "" {
		t.Fatalf("query %+v %v", r, err)
	}
	r, err = s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserSnapshot})
	if err != nil || r.Text != "Buy milk" || len(r.Elements) != 2 || r.Screenshot != "" || r.Title != "Todo" {
		t.Fatalf("snapshot %+v %v", r, err)
	}

	// Close stops it; the next use starts a new one.
	r, err = s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserClose})
	if err != nil || r.Running || !drv.Page().Closed() {
		t.Fatalf("close %+v %v", r, err)
	}
	if _, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserScreenshot}); err != nil || drv.Launches() != 2 {
		t.Fatalf("restart %v launches=%d", err, drv.Launches())
	}
}

func TestBrowserHistoryAndCrash(t *testing.T) {
	drv := browsertest.New(nil)
	s, _ := browserSetup(t, drv)
	ctx := t.Context()
	if _, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserNavigate, URL: "http://example.test"}); err != nil {
		t.Fatal(err)
	}
	// The fake page's "Next" link sits at (10,60)-(90,80).
	r, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserClick, X: 20, Y: 70})
	if err != nil || r.URL != "http://example.test/next" {
		t.Fatalf("click link %+v %v", r, err)
	}
	if r, err = s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserBack}); err != nil || r.URL != "http://example.test" {
		t.Fatalf("back %+v %v", r, err)
	}
	if r, err = s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserForward}); err != nil || r.URL != "http://example.test/next" {
		t.Fatalf("forward %+v %v", r, err)
	}

	// A browser that died is noticed and replaced on the next use.
	drv.Page().Crash()
	if st, _ := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserStatus}); st.Running {
		t.Fatal("crashed browser reported running")
	}
	if r, err = s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserScreenshot}); err != nil || drv.Launches() != 2 || r.URL != "about:blank" {
		t.Fatalf("after crash %+v %v", r, err)
	}
}

func TestBrowserRejects(t *testing.T) {
	drv := browsertest.New(map[string]browsertest.Site{"http://down.test/": {Fail: "net::ERR_CONNECTION_REFUSED"}})
	s, _ := browserSetup(t, drv)
	ctx := t.Context()
	for name, req := range map[string]tunnel.BrowserRequest{
		"no url":        {Action: tunnel.BrowserNavigate},
		"file url":      {Action: tunnel.BrowserNavigate, URL: "file:///etc/passwd"},
		"javascript":    {Action: tunnel.BrowserNavigate, URL: "javascript:alert(1)"},
		"chrome":        {Action: tunnel.BrowserNavigate, URL: "chrome://settings"},
		"tiny viewport": {Action: tunnel.BrowserScreenshot, Width: 10},
		"huge viewport": {Action: tunnel.BrowserScreenshot, Height: 100000},
		"no kind":       {Action: tunnel.BrowserInteract},
		"bad kind":      {Action: tunnel.BrowserInteract, Kind: "hover"},
		"type nothing":  {Action: tunnel.BrowserInteract, Kind: tunnel.BrowserType},
		"press nothing": {Action: tunnel.BrowserInteract, Kind: tunnel.BrowserPress, Key: " "},
		"eval no sel":   {Action: tunnel.BrowserInteract, Kind: tunnel.BrowserQuery},
		"bad action":    {Action: "hover"},
	} {
		if _, err := s.Browser(ctx, req); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if drv.Launches() != 0 {
		t.Fatalf("a rejected request started the browser (%d)", drv.Launches())
	}
	if _, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserNavigate, URL: "http://down.test/"}); err == nil || !strings.Contains(err.Error(), "ERR_CONNECTION_REFUSED") {
		t.Fatalf("refused: %v", err)
	}
	if _, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserClick, X: 5000, Y: 1}); err == nil {
		t.Fatal("click outside the viewport")
	}
	if _, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserClick, Selector: "#nope"}); err == nil || !strings.Contains(err.Error(), "no element matches") {
		t.Fatalf("missing selector: %v", err)
	}
	if _, err := s.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserInteract, Kind: tunnel.BrowserBack}); err == nil {
		t.Fatal("back without history")
	}
}

func TestBrowserUnavailable(t *testing.T) {
	drv := browsertest.New(nil)
	drv.SetUnavailable("Chromium is not installed")
	s, _ := browserSetup(t, drv)
	st, err := s.Browser(t.Context(), tunnel.BrowserRequest{Action: tunnel.BrowserStatus})
	if err != nil || st.Available || st.Running {
		t.Fatalf("status %+v %v", st, err)
	}
	if _, err := s.Browser(t.Context(), tunnel.BrowserRequest{Action: tunnel.BrowserNavigate, URL: "http://x.test"}); !errors.Is(err, tunnel.ErrBrowserUnavailable) {
		t.Fatalf("navigate: %v", err)
	}
}

func TestBrowserStopsWhenUnused(t *testing.T) {
	drv := browsertest.New(nil)
	t.Cleanup(sandboxd.SetBrowserTiming(0, 50*time.Millisecond))
	s, _ := setupWith(t, func(c *sandboxd.Config) { c.Browser = drv })
	if _, err := s.Browser(t.Context(), tunnel.BrowserRequest{Action: tunnel.BrowserScreenshot}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !drv.Page().Closed() {
		if time.Now().After(deadline) {
			t.Fatal("unused browser still runs")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
