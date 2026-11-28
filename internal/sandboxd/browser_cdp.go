package sandboxd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/gempir/agenthold/internal/tunnel"
)

// Chromium timing.
var (
	chromiumStartTimeout = 20 * time.Second
	chromiumLoadTimeout  = 15 * time.Second
	chromiumPoll         = 100 * time.Millisecond
	chromiumCloseGrace   = 3 * time.Second
)

// chromiumNames are the binaries tried after $CHROME_BIN, in order.
var chromiumNames = []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "headless_shell", "chrome"}

// chromiumDriver starts Chromium headless and drives it over the Chrome
// DevTools Protocol on a WebSocket.
type chromiumDriver struct {
	// find returns the binary ("" when there is none).
	find func() string
}

// ChromiumDriver returns the default BrowserDriver: headless Chromium, found
// at $CHROME_BIN or on the PATH.
func ChromiumDriver() BrowserDriver { return chromiumDriver{find: findChromium} }

func findChromium() string {
	if p := os.Getenv("CHROME_BIN"); p != "" {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	for _, n := range chromiumNames {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	if runtime.GOOS == "darwin" { // development on a Mac with the fake driver
		for _, p := range []string{
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func (c chromiumDriver) Available() string {
	if c.find() == "" {
		return "Chromium is not installed in this machine (set CHROME_BIN or install chromium)"
	}
	return ""
}

var devtoolsLine = regexp.MustCompile(`DevTools listening on (ws://\S+)`)

// chromiumArgs are the flags a headless Chromium starts with.
func chromiumArgs(o BrowserLaunch) []string {
	args := []string{
		"--headless=new",
		"--remote-debugging-port=0",
		"--remote-debugging-address=127.0.0.1",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--metrics-recording-only",
		"--mute-audio",
		"--hide-scrollbars",
		"--user-data-dir=" + o.ProfileDir,
		fmt.Sprintf("--window-size=%d,%d", o.Width, o.Height),
	}
	if len(o.TrustSPKI) > 0 {
		args = append(args, "--ignore-certificate-errors-spki-list="+strings.Join(o.TrustSPKI, ","))
	}
	return append(args, "about:blank")
}

func (c chromiumDriver) Launch(ctx context.Context, o BrowserLaunch) (BrowserPage, error) {
	bin := c.find()
	if bin == "" {
		return nil, errors.New(tunnel.BrowserUnavailable + c.Available())
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	cmd := exec.Command(bin, chromiumArgs(o)...) //nolint:noctx // outlives the request; killed by Close
	cmd.Env = o.Env
	if o.Prepare != nil {
		o.Prepare(cmd)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &chromiumPage{cmd: cmd, exited: make(chan struct{}), done: make(chan struct{}), log: log}
	// The first line naming the DevTools endpoint, then the rest of stderr
	// (kept short for errors).
	wsURL := make(chan string, 1)
	var tailMu sync.Mutex
	var tail []string
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sent := false
		for sc.Scan() {
			line := sc.Text()
			if !sent {
				if m := devtoolsLine.FindStringSubmatch(line); m != nil {
					wsURL <- m[1]
					sent = true
					continue
				}
				tailMu.Lock()
				tail = append(tail, line)
				if len(tail) > 8 {
					tail = tail[1:]
				}
				tailMu.Unlock()
			}
		}
		_, _ = io.Copy(io.Discard, stderr)
	}()
	go func() {
		_ = cmd.Wait()
		close(p.exited)
		p.markDone()
	}()
	fail := func(err error) error {
		_ = p.Close()
		return err
	}
	var u string
	t := time.NewTimer(chromiumStartTimeout)
	defer t.Stop()
	select {
	case u = <-wsURL:
	case <-p.exited:
		tailMu.Lock()
		msg := strings.Join(tail, "\n")
		tailMu.Unlock()
		return nil, fail(fmt.Errorf("chromium exited: %s", strings.TrimSpace(msg)))
	case <-t.C:
		return nil, fail(errors.New("chromium did not start in time"))
	case <-ctx.Done():
		return nil, fail(ctx.Err())
	}
	conn, err := dialCDP(ctx, u, p.onEvent)
	if err != nil {
		return nil, fail(err)
	}
	p.conn = conn
	go func() {
		<-conn.done
		p.markDone()
	}()
	if err := p.attach(ctx, o.Width, o.Height); err != nil {
		return nil, fail(err)
	}
	return p, nil
}

// caSPKI hashes the public key of the CA certificate in file for
// --ignore-certificate-errors-spki-list (nil without one).
func caSPKI(file string) []string {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out []string
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return out
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
		out = append(out, base64.StdEncoding.EncodeToString(sum[:]))
	}
}

// cdpMsg is one DevTools protocol message: a call, its answer or an event.
type cdpMsg struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// cdpConn is a DevTools protocol connection (flat sessions).
type cdpConn struct {
	ws      *websocket.Conn
	onEvent func(cdpMsg)

	mu      sync.Mutex
	next    int64
	pending map[int64]chan cdpMsg
	done    chan struct{}
	err     error
}

func dialCDP(ctx context.Context, u string, onEvent func(cdpMsg)) (*cdpConn, error) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, res, err := websocket.Dial(dctx, u, nil)
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("connect to chromium: %w", err)
	}
	ws.SetReadLimit(64 << 20) // screenshots of large viewports
	c := &cdpConn{ws: ws, onEvent: onEvent, pending: map[int64]chan cdpMsg{}, done: make(chan struct{})}
	go c.read()
	return c, nil
}

func (c *cdpConn) read() {
	defer close(c.done)
	for {
		_, b, err := c.ws.Read(context.Background())
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var m cdpMsg
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method != "" && c.onEvent != nil {
			c.onEvent(m)
		}
	}
}

// call sends method to session ("" for the browser) and decodes its result into out.
func (c *cdpConn) call(ctx context.Context, session, method string, params, out any) error {
	if params == nil {
		params = struct{}{}
	}
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan cdpMsg, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return errBrowserGone
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(cdpMsg{ID: id, SessionID: session, Method: method, Params: p})
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return errBrowserGone
		}
		if m.Error != nil {
			return fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

func (c *cdpConn) close() { _ = c.ws.Close(websocket.StatusNormalClosure, "") }

// chromiumPage is a running Chromium and the page it is attached to.
type chromiumPage struct {
	cmd    *exec.Cmd
	conn   *cdpConn
	log    *slog.Logger
	exited chan struct{}

	sessMu   sync.Mutex
	session  string
	doneOnce sync.Once
	done     chan struct{}

	consoleMu sync.Mutex
	errCount  int
	lastErr   string
}

var _ BrowserPage = (*chromiumPage)(nil)

func (p *chromiumPage) markDone() { p.doneOnce.Do(func() { close(p.done) }) }

func (p *chromiumPage) Done() <-chan struct{} { return p.done }

// attach finds the browser's page (or opens one), attaches to it and enables
// the domains the page needs.
func (p *chromiumPage) attach(ctx context.Context, width, height int) error {
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := p.conn.call(ctx, "", "Target.getTargets", nil, &targets); err != nil {
		return err
	}
	target := ""
	for _, t := range targets.TargetInfos {
		if t.Type == "page" {
			target = t.TargetID
			break
		}
	}
	if target == "" {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := p.conn.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			return err
		}
		target = created.TargetID
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := p.conn.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, &attached); err != nil {
		return err
	}
	p.sessMu.Lock()
	p.session = attached.SessionID
	p.sessMu.Unlock()
	for _, m := range []string{"Page.enable", "Runtime.enable", "Log.enable"} {
		if err := p.call(ctx, m, nil, nil); err != nil {
			return err
		}
	}
	return p.SetViewport(ctx, width, height)
}

func (p *chromiumPage) call(ctx context.Context, method string, params, out any) error {
	return p.conn.call(ctx, p.sid(), method, params, out)
}

func (p *chromiumPage) sid() string {
	p.sessMu.Lock()
	defer p.sessMu.Unlock()
	return p.session
}

func (p *chromiumPage) onEvent(m cdpMsg) {
	switch m.Method {
	case "Runtime.consoleAPICalled":
		var e struct {
			Type string `json:"type"`
			Args []struct {
				Value       any    `json:"value"`
				Description string `json:"description"`
			} `json:"args"`
		}
		if json.Unmarshal(m.Params, &e) == nil && (e.Type == "error" || e.Type == "assert") {
			var parts []string
			for _, a := range e.Args {
				if a.Description != "" {
					parts = append(parts, a.Description)
				} else if a.Value != nil {
					parts = append(parts, fmt.Sprint(a.Value))
				}
			}
			p.consoleError(strings.Join(parts, " "))
		}
	case "Runtime.exceptionThrown":
		var e struct {
			ExceptionDetails struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if json.Unmarshal(m.Params, &e) == nil {
			msg := e.ExceptionDetails.Exception.Description
			if msg == "" {
				msg = e.ExceptionDetails.Text
			}
			p.consoleError(msg)
		}
	case "Log.entryAdded":
		var e struct {
			Entry struct {
				Level string `json:"level"`
				Text  string `json:"text"`
				URL   string `json:"url"`
			} `json:"entry"`
		}
		if json.Unmarshal(m.Params, &e) == nil && e.Entry.Level == "error" {
			msg := e.Entry.Text
			if e.Entry.URL != "" {
				msg += " (" + e.Entry.URL + ")"
			}
			p.consoleError(msg)
		}
	case "Page.javascriptDialogOpening":
		// A dialog blocks the page until answered: accept it.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = p.call(ctx, "Page.handleJavaScriptDialog", map[string]any{"accept": true}, nil)
		}()
	case "Inspector.detached", "Target.targetCrashed":
		p.markDone()
	case "Target.detachedFromTarget":
		var e struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(m.Params, &e) == nil && e.SessionID == p.sid() {
			p.markDone()
		}
	}
}

func (p *chromiumPage) consoleError(msg string) {
	p.consoleMu.Lock()
	defer p.consoleMu.Unlock()
	p.errCount++
	p.lastErr = truncateRunes(msg, 500)
}

func (p *chromiumPage) ConsoleErrors() (int, string) {
	p.consoleMu.Lock()
	defer p.consoleMu.Unlock()
	return p.errCount, p.lastErr
}

func (p *chromiumPage) ResetConsole() {
	p.consoleMu.Lock()
	defer p.consoleMu.Unlock()
	p.errCount, p.lastErr = 0, ""
}

func (p *chromiumPage) SetViewport(ctx context.Context, width, height int) error {
	return p.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": width, "height": height, "deviceScaleFactor": 1, "mobile": width <= 480,
	}, nil)
}

func (p *chromiumPage) Navigate(ctx context.Context, u string) error {
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := p.call(ctx, "Page.navigate", map[string]any{"url": u}, &res); err != nil {
		return err
	}
	if res.ErrorText != "" {
		return fmt.Errorf("could not open %s: %s", u, res.ErrorText)
	}
	return p.WaitLoad(ctx)
}

func (p *chromiumPage) History(ctx context.Context, delta int) error {
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int64 `json:"id"`
		} `json:"entries"`
	}
	if err := p.call(ctx, "Page.getNavigationHistory", nil, &h); err != nil {
		return err
	}
	i := h.CurrentIndex + delta
	if i < 0 || i >= len(h.Entries) {
		if delta < 0 {
			return errors.New("there is no page to go back to")
		}
		return errors.New("there is no page to go forward to")
	}
	return p.call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil)
}

func (p *chromiumPage) Reload(ctx context.Context) error {
	return p.call(ctx, "Page.reload", nil, nil)
}

func (p *chromiumPage) WaitLoad(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, chromiumLoadTimeout)
	defer cancel()
	for {
		var state string
		if err := p.eval(ctx, "document.readyState", &state); err == nil && state == "complete" {
			return nil
		} else if errors.Is(err, errBrowserGone) {
			return err
		}
		select {
		case <-ctx.Done():
			// A page still loading (a long poll, a slow asset) is shown as it is.
			return nil
		case <-time.After(chromiumPoll):
		}
	}
}

func (p *chromiumPage) Click(ctx context.Context, x, y float64) error {
	for _, ev := range []map[string]any{
		{"type": "mouseMoved", "x": x, "y": y},
		{"type": "mousePressed", "x": x, "y": y, "button": "left", "buttons": 1, "clickCount": 1},
		{"type": "mouseReleased", "x": x, "y": y, "button": "left", "buttons": 0, "clickCount": 1},
	} {
		if err := p.call(ctx, "Input.dispatchMouseEvent", ev, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *chromiumPage) InsertText(ctx context.Context, text string) error {
	return p.call(ctx, "Input.insertText", map[string]any{"text": text}, nil)
}

func (p *chromiumPage) Scroll(ctx context.Context, x, y, dx, dy float64) error {
	return p.call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseWheel", "x": x, "y": y, "deltaX": dx, "deltaY": dy,
	}, nil)
}

// keyDef describes a named key for Input.dispatchKeyEvent.
type keyDef struct {
	code string
	vk   int
	text string
}

var namedKeys = map[string]keyDef{
	"Enter":      {"Enter", 13, "\r"},
	"Tab":        {"Tab", 9, ""},
	"Backspace":  {"Backspace", 8, ""},
	"Delete":     {"Delete", 46, ""},
	"Escape":     {"Escape", 27, ""},
	" ":          {"Space", 32, " "},
	"Space":      {"Space", 32, " "},
	"ArrowLeft":  {"ArrowLeft", 37, ""},
	"ArrowUp":    {"ArrowUp", 38, ""},
	"ArrowRight": {"ArrowRight", 39, ""},
	"ArrowDown":  {"ArrowDown", 40, ""},
	"Home":       {"Home", 36, ""},
	"End":        {"End", 35, ""},
	"PageUp":     {"PageUp", 33, ""},
	"PageDown":   {"PageDown", 34, ""},
}

var modifierBits = map[string]int{"Alt": 1, "Control": 2, "Ctrl": 2, "Meta": 4, "Cmd": 4, "Shift": 8}

// keyEvents builds the key down and up events of a key like "Enter", "a" or
// "Control+a".
func keyEvents(spec string) ([]map[string]any, error) {
	mods := 0
	key := spec
	if spec != "+" && strings.Contains(spec, "+") {
		parts := strings.Split(spec, "+")
		key = parts[len(parts)-1]
		for _, m := range parts[:len(parts)-1] {
			bit, ok := modifierBits[m]
			if !ok {
				return nil, fmt.Errorf("unknown modifier %q", m)
			}
			mods |= bit
		}
	}
	if key == "Space" {
		key = " "
	}
	var def keyDef
	if d, ok := namedKeys[key]; ok {
		def = d
	} else if r := []rune(key); len(r) == 1 {
		def = keyDef{text: key}
		switch c := r[0]; {
		case c >= 'a' && c <= 'z':
			def.code, def.vk = "Key"+strings.ToUpper(key), int(c-'a'+'A')
		case c >= 'A' && c <= 'Z':
			def.code, def.vk = "Key"+key, int(c)
		case c >= '0' && c <= '9':
			def.code, def.vk = "Digit"+key, int(c)
		}
	} else if strings.HasPrefix(key, "F") {
		n, err := strconv.Atoi(key[1:])
		if err != nil || n < 1 || n > 12 {
			return nil, fmt.Errorf("unknown key %q", key)
		}
		def = keyDef{code: key, vk: 111 + n}
	} else {
		return nil, fmt.Errorf("unknown key %q", key)
	}
	if mods&^8 != 0 {
		def.text = "" // a shortcut types nothing
	}
	down := map[string]any{"type": "rawKeyDown", "key": key, "code": def.code, "windowsVirtualKeyCode": def.vk, "modifiers": mods}
	if def.text != "" {
		down["type"], down["text"] = "keyDown", def.text
	}
	up := map[string]any{"type": "keyUp", "key": key, "code": def.code, "windowsVirtualKeyCode": def.vk, "modifiers": mods}
	return []map[string]any{down, up}, nil
}

func (p *chromiumPage) Press(ctx context.Context, key string) error {
	evs, err := keyEvents(key)
	if err != nil {
		return err
	}
	for _, ev := range evs {
		if err := p.call(ctx, "Input.dispatchKeyEvent", ev, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *chromiumPage) Screenshot(ctx context.Context) (string, error) {
	var res struct {
		Data string `json:"data"`
	}
	if err := p.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"}, &res); err != nil {
		return "", err
	}
	return res.Data, nil
}

// eval evaluates a script in the page and decodes its JSON value into out.
func (p *chromiumPage) eval(ctx context.Context, expr string, out any) error {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := p.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expr, "returnByValue": true, "awaitPromise": true,
	}, &res); err != nil {
		return err
	}
	if e := res.ExceptionDetails; e != nil {
		msg := e.Exception.Description
		if msg == "" {
			msg = e.Text
		}
		// The first line ("SyntaxError: ... is not a valid selector.").
		msg, _, _ = strings.Cut(msg, "\n")
		return errors.New(msg)
	}
	if out == nil || len(res.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(res.Result.Value, out)
}

func (p *chromiumPage) Info(ctx context.Context) (BrowserInfo, error) {
	var out BrowserInfo
	err := p.eval(ctx, `({url: location.href, title: document.title})`, &out)
	return out, err
}

func (p *chromiumPage) Elements(ctx context.Context) ([]tunnel.BrowserElement, error) {
	var out []tunnel.BrowserElement
	err := p.eval(ctx, elementsScript, &out)
	return out, err
}

func (p *chromiumPage) Text(ctx context.Context, max int) (string, bool, error) {
	var text string
	if err := p.eval(ctx, `document.body ? document.body.innerText : ""`, &text); err != nil {
		return "", false, err
	}
	if r := []rune(text); len(r) > max {
		return string(r[:max]), true, nil
	}
	return text, false, nil
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (p *chromiumPage) Locate(ctx context.Context, selector string) (tunnel.BrowserRect, error) {
	var r *tunnel.BrowserRect
	err := p.eval(ctx, `(() => {
  const el = document.querySelector(`+jsString(selector)+`);
  if (!el) return null;
  el.scrollIntoView({block: "center", inline: "center"});
  const r = el.getBoundingClientRect();
  return {x: r.x, y: r.y, width: r.width, height: r.height};
})()`, &r)
	if err != nil {
		return tunnel.BrowserRect{}, err
	}
	if r == nil {
		return tunnel.BrowserRect{}, fmt.Errorf("no element matches %s", selector)
	}
	return *r, nil
}

func (p *chromiumPage) Focus(ctx context.Context, selector string) error {
	var ok bool
	if err := p.eval(ctx, `(() => {
  const el = document.querySelector(`+jsString(selector)+`);
  if (!el) return false;
  el.scrollIntoView({block: "center"});
  el.focus();
  return true;
})()`, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no element matches %s", selector)
	}
	return nil
}

func (p *chromiumPage) Query(ctx context.Context, selector string) ([]tunnel.BrowserMatch, error) {
	var out []tunnel.BrowserMatch
	err := p.eval(ctx, `(() => {`+selectorPathJS+`
  const out = [];
  for (const el of document.querySelectorAll(`+jsString(selector)+`)) {
    if (out.length >= 50) break;
    const r = el.getBoundingClientRect();
    const attributes = {};
    for (const a of el.attributes) attributes[a.name] = a.value.slice(0, 200);
    out.push({tag: el.tagName.toLowerCase(), text: (el.innerText || el.textContent || "").replace(/\s+/g, " ").trim().slice(0, 500),
      selector: selectorPath(el), attributes, rect: {x: r.x, y: r.y, width: r.width, height: r.height}});
  }
  return out;
})()`, &out)
	return out, err
}

// Close stops the browser: politely, then its whole process group.
func (p *chromiumPage) Close() error {
	if p.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.conn.call(ctx, "", "Browser.close", nil, nil)
		cancel()
		p.conn.close()
	}
	select {
	case <-p.exited:
	case <-time.After(chromiumCloseGrace):
	}
	if pid := p.cmd.Process.Pid; pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = p.cmd.Process.Kill()
	}
	<-p.exited
	p.markDone()
	return nil
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// selectorPathJS defines selectorPath(el): a CSS selector that finds el (its
// id when unique, else a tag:nth-of-type chain from the nearest unique id).
const selectorPathJS = `
  const selectorPath = (el) => {
    const uniqueID = (e) => e.id && document.querySelectorAll("#" + CSS.escape(e.id)).length === 1;
    const parts = [];
    for (let e = el; e && e.nodeType === 1; e = e.parentElement) {
      if (uniqueID(e)) { parts.unshift("#" + CSS.escape(e.id)); break; }
      let part = e.tagName.toLowerCase();
      if (e === document.documentElement) { parts.unshift(part); break; }
      const parent = e.parentElement;
      if (parent) {
        const same = Array.from(parent.children).filter((c) => c.tagName === e.tagName);
        if (same.length > 1) part += ":nth-of-type(" + (same.indexOf(e) + 1) + ")";
      }
      parts.unshift(part);
    }
    return parts.join(" > ");
  };`

// elementsScript lists the page's rendered interactive elements.
const elementsScript = `(() => {` + selectorPathJS + `
  const query = 'a[href],button,input:not([type=hidden]),select,textarea,summary,[contenteditable=""],[contenteditable=true],' +
    '[role=button],[role=link],[role=checkbox],[role=radio],[role=tab],[role=menuitem],[role=switch],[role=option],' +
    '[role=textbox],[role=combobox],[role=searchbox],[role=slider],[onclick],[tabindex]:not([tabindex="-1"])';
  const implicitRole = (el) => {
    const tag = el.tagName.toLowerCase();
    if (tag === "a") return "link";
    if (tag === "button" || tag === "summary") return "button";
    if (tag === "select") return "combobox";
    if (tag === "textarea") return "textbox";
    if (tag === "input") {
      const t = (el.getAttribute("type") || "text").toLowerCase();
      return {checkbox: "checkbox", radio: "radio", range: "slider", button: "button", submit: "button", reset: "button",
        image: "button", search: "searchbox"}[t] || "textbox";
    }
    if (el.isContentEditable) return "textbox";
    return tag;
  };
  const clean = (s) => (s || "").replace(/\s+/g, " ").trim().slice(0, 120);
  const nameOf = (el) => {
    const labelled = el.getAttribute("aria-labelledby");
    if (labelled) {
      const t = labelled.split(/\s+/).map((id) => document.getElementById(id)).filter(Boolean).map((e) => e.innerText).join(" ");
      if (clean(t)) return clean(t);
    }
    const label = el.getAttribute("aria-label") || (el.labels && el.labels[0] && el.labels[0].innerText);
    if (clean(label)) return clean(label);
    const text = el.innerText;
    if (clean(text)) return clean(text);
    const img = el.querySelector && el.querySelector("img[alt]");
    return clean(el.getAttribute("placeholder") || el.getAttribute("title") || el.getAttribute("alt") || (img && img.alt) ||
      (el.type !== "password" && el.tagName === "INPUT" && el.type !== "text" ? el.value : ""));
  };
  const out = [];
  for (const el of document.querySelectorAll(query)) {
    if (out.length >= 300) break;
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) continue;
    const cs = getComputedStyle(el);
    if (cs.visibility === "hidden" || cs.display === "none" || Number(cs.opacity) === 0) continue;
    const tag = el.tagName.toLowerCase();
    const editable = tag === "input" || tag === "textarea" || tag === "select";
    out.push({
      role: el.getAttribute("role") || implicitRole(el), name: nameOf(el), tag, selector: selectorPath(el),
      rect: {x: r.x, y: r.y, width: r.width, height: r.height},
      href: tag === "a" ? el.href : "",
      value: editable && el.type !== "password" ? String(el.value || "").slice(0, 200) : "",
      disabled: !!el.disabled,
    });
  }
  return out;
})()`
