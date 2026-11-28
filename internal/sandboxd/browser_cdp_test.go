package sandboxd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeCDP is a DevTools endpoint answering the calls a page makes.
type fakeCDP struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	calls    []cdpMsg
	conn     *websocket.Conn
	ready    int // evaluations of document.readyState answered "loading" first
	evalErr  string
	navError string
}

func newFakeCDP(t *testing.T) *fakeCDP {
	f := &fakeCDP{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = c
		f.mu.Unlock()
		ctx := context.Background()
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m cdpMsg
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			f.mu.Lock()
			f.calls = append(f.calls, m)
			f.mu.Unlock()
			res, errMsg := f.answer(m)
			out := map[string]any{"id": m.ID, "result": res}
			if m.SessionID != "" {
				out["sessionId"] = m.SessionID
			}
			if errMsg != "" {
				out = map[string]any{"id": m.ID, "error": map[string]any{"code": -32000, "message": errMsg}}
			}
			rb, _ := json.Marshal(out)
			if err := c.Write(ctx, websocket.MessageText, rb); err != nil {
				return
			}
			if m.Method == "Browser.close" {
				_ = c.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCDP) url() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/devtools/browser/x"
}

// event sends a DevTools event to the client.
func (f *fakeCDP) event(method string, params any) {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"method": method, "params": params, "sessionId": "S1"})
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		f.t.Error(err)
	}
}

func value(v any) map[string]any {
	return map[string]any{"result": map[string]any{"type": "object", "value": v}}
}

func (f *fakeCDP) answer(m cdpMsg) (any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch m.Method {
	case "Target.getTargets":
		return map[string]any{"targetInfos": []any{map[string]any{"targetId": "T0", "type": "browser"}, map[string]any{"targetId": "T1", "type": "page"}}}, ""
	case "Target.attachToTarget":
		return map[string]any{"sessionId": "S1"}, ""
	case "Page.navigate":
		return map[string]any{"frameId": "F", "errorText": f.navError}, ""
	case "Page.getNavigationHistory":
		return map[string]any{"currentIndex": 1, "entries": []any{map[string]any{"id": 7}, map[string]any{"id": 8}}}, ""
	case "Page.captureScreenshot":
		return map[string]any{"data": "iVBORw0KGgo="}, ""
	case "Runtime.evaluate":
		var p struct {
			Expression string `json:"expression"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch {
		case f.evalErr != "":
			return map[string]any{"result": map[string]any{}, "exceptionDetails": map[string]any{
				"text": "Uncaught", "exception": map[string]any{"description": f.evalErr + "\n    at <anonymous>:1:1"},
			}}, ""
		case p.Expression == "document.readyState":
			if f.ready > 0 {
				f.ready--
				return value("loading"), ""
			}
			return value("complete"), ""
		case strings.Contains(p.Expression, "location.href"):
			return value(map[string]any{"url": "http://localhost:3000/", "title": "Todo"}), ""
		case strings.Contains(p.Expression, "implicitRole"):
			return value([]any{map[string]any{
				"role": "button", "name": "Add", "tag": "button", "selector": "#add",
				"rect": map[string]any{"x": 1, "y": 2, "width": 3, "height": 4},
			}}), ""
		case strings.Contains(p.Expression, "scrollIntoView") && strings.Contains(p.Expression, "getBoundingClientRect"):
			if strings.Contains(p.Expression, `"#missing"`) {
				return value(nil), ""
			}
			return value(map[string]any{"x": 10, "y": 20, "width": 30, "height": 40}), ""
		case strings.Contains(p.Expression, "innerText : \"\""):
			return value("hello world"), ""
		}
		return value(nil), ""
	}
	return map[string]any{}, ""
}

func (f *fakeCDP) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Method)
	}
	return out
}

func (f *fakeCDP) params(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.Method == method {
			var p map[string]any
			_ = json.Unmarshal(c.Params, &p)
			out = append(out, p)
		}
	}
	return out
}

// fakeChromium writes a script that records its arguments, announces the
// fake DevTools endpoint on stderr like Chromium, and waits.
func fakeChromium(t *testing.T, wsURL string) (bin, argsFile string) {
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "chromium")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho 'some warning' >&2\necho 'DevTools listening on " + wsURL + "' >&2\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func TestChromiumDriver(t *testing.T) {
	oldPoll, oldGrace := chromiumPoll, chromiumCloseGrace
	chromiumPoll, chromiumCloseGrace = time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { chromiumPoll, chromiumCloseGrace = oldPoll, oldGrace })
	cdp := newFakeCDP(t)
	bin, argsFile := fakeChromium(t, cdp.url())
	drv := chromiumDriver{find: func() string { return bin }}
	if why := drv.Available(); why != "" {
		t.Fatal(why)
	}
	profile := t.TempDir()
	ctx := t.Context()
	page, err := drv.Launch(ctx, BrowserLaunch{ProfileDir: profile, Env: os.Environ(), Width: 800, Height: 600, TrustSPKI: []string{"abc="}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = page.Close() })

	b, _ := os.ReadFile(argsFile)
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	for _, want := range []string{
		"--headless=new", "--remote-debugging-port=0", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--user-data-dir=" + profile, "--window-size=800,600", "--ignore-certificate-errors-spki-list=abc=",
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("no %s in %q", want, args)
		}
	}
	// It attached to the existing page and enabled what it needs.
	if got := cdp.methods(); !reflect.DeepEqual(got, []string{"Target.getTargets", "Target.attachToTarget", "Page.enable", "Runtime.enable", "Log.enable", "Emulation.setDeviceMetricsOverride"}) {
		t.Fatalf("setup calls %q", got)
	}
	if p := cdp.params("Target.attachToTarget")[0]; p["targetId"] != "T1" || p["flatten"] != true {
		t.Fatalf("attach %v", p)
	}

	// Navigating waits for the document to load.
	cdp.mu.Lock()
	cdp.ready = 3
	cdp.mu.Unlock()
	if err := page.Navigate(ctx, "http://localhost:3000/"); err != nil {
		t.Fatal(err)
	}
	if n := len(cdp.params("Runtime.evaluate")); n != 4 {
		t.Fatalf("readyState polled %d times", n)
	}
	info, err := page.Info(ctx)
	if err != nil || info.Title != "Todo" || info.URL != "http://localhost:3000/" {
		t.Fatalf("info %+v %v", info, err)
	}
	els, err := page.Elements(ctx)
	if err != nil || len(els) != 1 || els[0].Selector != "#add" || els[0].Rect.Height != 4 {
		t.Fatalf("elements %+v %v", els, err)
	}
	if shot, err := page.Screenshot(ctx); err != nil || shot != "iVBORw0KGgo=" {
		t.Fatalf("screenshot %q %v", shot, err)
	}
	if text, cut, err := page.Text(ctx, 5); err != nil || text != "hello" || !cut {
		t.Fatalf("text %q %v %v", text, cut, err)
	}
	if r, err := page.Locate(ctx, "#add"); err != nil || r.X != 10 || r.Width != 30 {
		t.Fatalf("locate %+v %v", r, err)
	}
	if _, err := page.Locate(ctx, "#missing"); err == nil || !strings.Contains(err.Error(), "no element matches") {
		t.Fatalf("locate missing: %v", err)
	}

	// Input.
	if err := page.Click(ctx, 15, 25); err != nil {
		t.Fatal(err)
	}
	mouse := cdp.params("Input.dispatchMouseEvent")
	if len(mouse) != 3 || mouse[1]["type"] != "mousePressed" || mouse[1]["x"] != 15.0 || mouse[2]["type"] != "mouseReleased" {
		t.Fatalf("click %v", mouse)
	}
	if err := page.Press(ctx, "Enter"); err != nil {
		t.Fatal(err)
	}
	keys := cdp.params("Input.dispatchKeyEvent")
	if len(keys) != 2 || keys[0]["type"] != "keyDown" || keys[0]["text"] != "\r" || keys[0]["windowsVirtualKeyCode"] != 13.0 || keys[1]["type"] != "keyUp" {
		t.Fatalf("press %v", keys)
	}
	if err := page.Press(ctx, "Hyper+x"); err == nil {
		t.Fatal("unknown modifier")
	}
	if err := page.InsertText(ctx, "hi"); err != nil || cdp.params("Input.insertText")[0]["text"] != "hi" {
		t.Fatalf("insert %v", err)
	}
	if err := page.Scroll(ctx, 1, 2, 0, 100); err != nil {
		t.Fatal(err)
	}
	if err := page.History(ctx, -1); err != nil || cdp.params("Page.navigateToHistoryEntry")[0]["entryId"] != 7.0 {
		t.Fatalf("back %v", err)
	}
	if err := page.History(ctx, 1); err == nil {
		t.Fatal("forward past the end")
	}

	// Console errors and exceptions are counted; dialogs are answered.
	cdp.event("Runtime.consoleAPICalled", map[string]any{"type": "error", "args": []any{map[string]any{"type": "string", "value": "bad thing"}}})
	cdp.event("Runtime.consoleAPICalled", map[string]any{"type": "log", "args": []any{map[string]any{"value": "fine"}}})
	cdp.event("Runtime.exceptionThrown", map[string]any{"exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "TypeError: x is undefined"}}})
	cdp.event("Page.javascriptDialogOpening", map[string]any{"message": "sure?", "type": "confirm"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, last := page.ConsoleErrors()
		if n == 2 && last == "TypeError: x is undefined" && len(cdp.params("Page.handleJavaScriptDialog")) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("console %d %q, dialog %v", n, last, cdp.params("Page.handleJavaScriptDialog"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	page.ResetConsole()
	if n, _ := page.ConsoleErrors(); n != 0 {
		t.Fatal("reset")
	}

	// Script errors come back as their first line; navigation errors too.
	cdp.mu.Lock()
	cdp.evalErr = "SyntaxError: '!!' is not a valid selector."
	cdp.mu.Unlock()
	if _, err := page.Query(ctx, "!!"); err == nil || err.Error() != "SyntaxError: '!!' is not a valid selector." {
		t.Fatalf("query error: %v", err)
	}
	cdp.mu.Lock()
	cdp.evalErr, cdp.navError = "", "net::ERR_CONNECTION_REFUSED"
	cdp.mu.Unlock()
	if err := page.Navigate(ctx, "http://localhost:1/"); err == nil || !strings.Contains(err.Error(), "ERR_CONNECTION_REFUSED") {
		t.Fatalf("nav error: %v", err)
	}

	// Close ends the process.
	if err := page.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-page.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not done after close")
	}
	if err := page.Click(ctx, 1, 1); err == nil {
		t.Fatal("click after close")
	}
}

func TestChromiumExitsEarly(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "chromium")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'cannot open display' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := chromiumDriver{find: func() string { return bin }}.Launch(t.Context(), BrowserLaunch{ProfileDir: dir, Width: 100, Height: 100})
	if err == nil || !strings.Contains(err.Error(), "cannot open display") {
		t.Fatalf("launch: %v", err)
	}
	if why := (chromiumDriver{find: func() string { return "" }}).Available(); !strings.Contains(why, "not installed") {
		t.Fatalf("available: %q", why)
	}
}

func TestKeyEvents(t *testing.T) {
	for spec, want := range map[string][2]any{
		"a":         {"keyDown", "a"},
		"Tab":       {"rawKeyDown", nil},
		"Control+a": {"rawKeyDown", nil},
		"Shift+A":   {"keyDown", "A"},
		"Space":     {"keyDown", " "},
		"F5":        {"rawKeyDown", nil},
	} {
		evs, err := keyEvents(spec)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		if evs[0]["type"] != want[0] || evs[0]["text"] != want[1] || evs[1]["type"] != "keyUp" {
			t.Fatalf("%s: %v", spec, evs)
		}
	}
	if evs, _ := keyEvents("Control+a"); evs[0]["modifiers"] != 2 || evs[0]["code"] != "KeyA" {
		t.Fatalf("modifiers %v", evs[0])
	}
	for _, bad := range []string{"Foo", "F13", "Hyper+a"} {
		if _, err := keyEvents(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestNormalizeBrowserURL(t *testing.T) {
	for in, want := range map[string]string{
		"localhost:3000":           "http://localhost:3000",
		"https://example.com/a":    "https://example.com/a",
		"about:blank":              "about:blank",
		"http://[::1]:8080/x?y=1":  "http://[::1]:8080/x?y=1",
		"127.0.0.1:5173/dashboard": "http://127.0.0.1:5173/dashboard",
	} {
		if got, err := normalizeBrowserURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "file:///etc/passwd", "ftp://x", "data:text/html,hi", "http://"} {
		if _, err := normalizeBrowserURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
