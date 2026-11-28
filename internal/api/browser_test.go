package api_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/platform"
	"github.com/gempir/agenthold/internal/sandboxd/browsertest"
	"github.com/gempir/agenthold/internal/tunnel"
)

func browserSites() map[string]browsertest.Site {
	return map[string]browsertest.Site{
		"http://localhost:5173/": {Title: "Dashboard", Text: "Revenue 42", ConsoleErrors: []string{"Failed to load chart.js"}, Elements: []tunnel.BrowserElement{
			{Role: "button", Name: "Save", Tag: "button", Selector: "#save", Rect: tunnel.BrowserRect{X: 20, Y: 40, Width: 80, Height: 24}},
			{Role: "textbox", Name: "Email", Tag: "input", Selector: "#email", Rect: tunnel.BrowserRect{X: 20, Y: 80, Width: 200, Height: 24}},
		}},
	}
}

func TestBrowserAPI(t *testing.T) {
	e := newEnv(t)
	drv := browsertest.New(browserSites())
	e.driver.SetBrowser(drv)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	ag := e.fakeAgent(alice, "")
	base := "/agents/" + ag.Id + "/browser/"

	var st gen.BrowserState
	expect(t, "status", alice.do("GET", base+"status", nil, &st), 200)
	if !st.Available || st.Running || st.Width != 1280 || st.Height != 800 || st.Elements == nil {
		t.Fatalf("status %+v", st)
	}
	expect(t, "bob status", bob.do("GET", base+"status", nil, nil), 404)
	expect(t, "bob navigate", bob.do("POST", base+"navigate", map[string]any{"url": "http://localhost:5173/"}, nil), 404)

	expect(t, "no url", alice.do("POST", base+"navigate", map[string]any{"url": ""}, nil), 400)
	expect(t, "file url", alice.do("POST", base+"navigate", map[string]any{"url": "file:///etc/passwd"}, nil), 400)
	expect(t, "tiny viewport", alice.do("POST", base+"navigate", map[string]any{"url": "http://localhost:5173/", "width": 50}, nil), 400)
	expect(t, "navigate", alice.do("POST", base+"navigate", map[string]any{"url": "localhost:5173/", "width": 768, "height": 1024}, &st), 200)
	if st.Url == nil || *st.Url != "http://localhost:5173/" || *st.Title != "Dashboard" || !st.Running || st.Width != 768 ||
		len(st.Elements) != 2 || st.Elements[0].Selector != "#save" || st.ConsoleErrors != 1 || st.Screenshot == nil || *st.Screenshot == "" {
		t.Fatalf("navigate %+v", st)
	}

	expect(t, "screenshot", alice.do("GET", base+"screenshot?width=390&height=844", nil, &st), 200)
	if st.Width != 390 || st.Height != 844 || st.Screenshot == nil {
		t.Fatalf("screenshot %+v", st)
	}
	expect(t, "bad action", alice.do("POST", base+"interact", map[string]any{"action": "hover"}, nil), 400)
	expect(t, "missing selector", alice.do("POST", base+"interact", map[string]any{"action": "click", "selector": "#nope"}, nil), 400)
	expect(t, "type", alice.do("POST", base+"interact", map[string]any{"action": "type", "selector": "#email", "text": "secret@example.com"}, &st), 200)
	if drv.Page().Value("#email") != "secret@example.com" {
		t.Fatalf("typed %q", drv.Page().Value("#email"))
	}
	expect(t, "click", alice.do("POST", base+"interact", map[string]any{"action": "click", "x": 30, "y": 50}, &st), 200)
	expect(t, "press", alice.do("POST", base+"interact", map[string]any{"action": "press", "key": "Enter"}, &st), 200)
	st = gen.BrowserState{}
	expect(t, "eval", alice.do("POST", base+"interact", map[string]any{"action": "eval", "selector": "#save"}, &st), 200)
	if st.Matches == nil || len(*st.Matches) != 1 || (*st.Matches)[0].Text != "Save" || st.Screenshot != nil {
		t.Fatalf("eval %+v", st)
	}
	st = gen.BrowserState{}
	expect(t, "snapshot", alice.do("GET", base+"snapshot", nil, &st), 200)
	if st.Text == nil || *st.Text != "Revenue 42" || len(st.Elements) != 2 || st.Screenshot != nil {
		t.Fatalf("snapshot %+v", st)
	}
	calls := drv.Page().Calls()
	for _, want := range []string{"navigate http://localhost:5173/", "viewport 768x1024", "viewport 390x844", "focus #email", "click 30 50", "press Enter"} {
		if !slices.Contains(calls, want) {
			t.Fatalf("no %q in %q", want, calls)
		}
	}

	// Navigation, input and close are audited; what was typed is not kept.
	expect(t, "close", alice.do("POST", base+"close", nil, &st), 200)
	if st.Running {
		t.Fatalf("close %+v", st)
	}
	var log []gen.AuditEntry
	expect(t, "audit", alice.do("GET", "/agents/"+ag.Id+"/audit", nil, &log), 200)
	actions := map[string]int{}
	for _, a := range log {
		actions[a.Action]++
		if b, _ := json.Marshal(a.Details); strings.Contains(string(b), "secret@example.com") {
			t.Fatalf("typed text audited: %s", b)
		}
		if a.Action == "browser.interact" && a.Details["action"] == "type" && a.Details["textLength"] != float64(18) {
			t.Fatalf("type audit %+v", a.Details)
		}
	}
	if actions["browser.navigate"] != 1 || actions["browser.interact"] != 3 || actions["browser.close"] != 1 {
		t.Fatalf("audit actions %v", actions)
	}

	// No Chromium: 503; a stopped machine: 409.
	drv.SetUnavailable("Chromium is not installed")
	expect(t, "status without chromium", alice.do("GET", base+"status", nil, &st), 200)
	if st.Available {
		t.Fatalf("status %+v", st)
	}
	expect(t, "navigate without chromium", alice.do("POST", base+"navigate", map[string]any{"url": "http://localhost:5173/"}, nil), 503)
	expect(t, "stop", alice.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "stopped", alice.do("GET", base+"status", nil, nil), 409)
}

func TestBrowserPlatformTools(t *testing.T) {
	e := newEnv(t)
	drv := browsertest.New(browserSites())
	e.driver.SetBrowser(drv)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	ctx := context.Background()
	call := func(tool string, args map[string]any) (any, error) {
		b, _ := json.Marshal(args)
		return e.platform.Call(ctx, ag.Id, "", tool, b)
	}

	tools, _ := e.platform.Call(ctx, ag.Id, "", "_list_tools", nil)
	b, _ := json.Marshal(tools)
	for _, name := range []string{"preview_navigate", "preview_snapshot", "preview_interact", "preview_screenshot"} {
		if !strings.Contains(string(b), `"`+name+`"`) {
			t.Fatalf("%s not listed", name)
		}
	}

	if _, err := call("preview_navigate", nil); err == nil {
		t.Fatal("navigate without url")
	}
	out, err := call("preview_navigate", map[string]any{"url": "http://localhost:5173/"})
	if s, _ := out.(string); err != nil || !strings.Contains(s, "Title: Dashboard") || !strings.Contains(s, `button "Save" #save @ 20,40 80x24`) ||
		!strings.Contains(s, "Console errors: 1 (last: Failed to load chart.js)") {
		t.Fatalf("navigate %q %v", out, err)
	}
	out, err = call("preview_snapshot", nil)
	if s, _ := out.(string); err != nil || !strings.Contains(s, "Revenue 42") || !strings.Contains(s, "#email") {
		t.Fatalf("snapshot %q %v", out, err)
	}
	out, err = call("preview_interact", map[string]any{"action": "click", "selector": "#save"})
	if s, _ := out.(string); err != nil || !strings.Contains(s, "URL: http://localhost:5173/") {
		t.Fatalf("click %q %v", out, err)
	}
	out, err = call("preview_interact", map[string]any{"action": "eval", "selector": "#save"})
	if s, _ := out.(string); err != nil || !strings.Contains(s, "1 element(s) match #save") {
		t.Fatalf("eval %q %v", out, err)
	}
	if _, err := call("preview_interact", map[string]any{}); err == nil {
		t.Fatal("interact without action")
	}

	// A screenshot is saved in the machine and returned as an image block
	// naming that file.
	out, err = call("preview_screenshot", map[string]any{"width": 640, "height": 480})
	if err != nil {
		t.Fatal(err)
	}
	mc, ok := out.(platform.MCPContent)
	if !ok || len(mc.Content) != 2 || mc.Content[1].Type != "image" || mc.Content[1].MimeType != "image/png" {
		t.Fatalf("screenshot %#v", out)
	}
	file := mc.Content[1].File
	home := e.home(ag.Id)
	if !strings.HasPrefix(file, filepath.Join(home, ".agenthold/screenshots")+"/") || !strings.Contains(mc.Content[0].Text, file) {
		t.Fatalf("screenshot file %q (home %s): %q", file, home, mc.Content[0].Text)
	}
	png, err := os.ReadFile(file)
	if err != nil || !strings.HasPrefix(string(png), "\x89PNG") {
		t.Fatalf("saved %v", err)
	}

	var log []gen.AuditEntry
	expect(t, "audit", alice.do("GET", "/agents/"+ag.Id+"/audit", nil, &log), 200)
	found := false
	for _, a := range log {
		if a.Action == "browser.navigate" && a.Details["fromAgentId"] == ag.Id {
			found = true
		}
	}
	if !found {
		t.Fatalf("no agent browser.navigate audit in %+v", log)
	}
}
