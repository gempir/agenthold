package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/project"
	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// maxListedElements bounds the elements a tool result lists.
const maxListedElements = 120

var viewportArgs = map[string]any{
	"width":  map[string]any{"type": "integer", "description": "Viewport width in CSS pixels (200-3840); default: keep the current one (1280 at first)."},
	"height": map[string]any{"type": "integer", "description": "Viewport height in CSS pixels (200-4320); default: keep the current one (800 at first)."},
}

// browserTools drive the headless Chromium in the calling agent's own machine.
var browserTools = []map[string]any{
	{
		"name": "preview_navigate",
		"description": "Open a URL in the headless Chromium of your machine (started on first use) and wait for it to load. " +
			"Use http://localhost:<port> for servers you run (they need not be exposed); external sites work too. " +
			"Returns the page's URL, title, console error count and its interactive elements (role, name, CSS selector, " +
			"box) for preview_interact. Your owner sees the same browser in the Preview panel.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"url"},
			"properties": mergeProps(map[string]any{
				"url": map[string]any{"type": "string", "description": "http(s) URL; a bare host like localhost:3000 gets http://."},
			}, viewportArgs),
		},
	},
	{
		"name": "preview_snapshot",
		"description": "Describe the page open in your machine's headless browser: URL, title, console errors, its visible " +
			"text and its interactive elements with CSS selectors and boxes. Cheaper than a screenshot.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "preview_interact",
		"description": "Act on the page in your machine's headless browser: click (selector, or x/y in viewport pixels), " +
			"type (text, into selector when given, else the focused element), press (key: Enter, Tab, Escape, ArrowDown, " +
			"a, Control+a, ...), scroll (dx/dy pixels), back, forward, reload, or eval: a read-only query returning the " +
			"elements matching the CSS selector with their text and attributes. Returns the page afterwards.",
		"inputSchema": map[string]any{
			"type": "object", "required": []string{"action"},
			"properties": map[string]any{
				"action":   map[string]any{"type": "string", "enum": []string{"click", "type", "press", "scroll", "back", "forward", "reload", "eval"}},
				"selector": map[string]any{"type": "string", "description": "CSS selector (see the elements preview_navigate and preview_snapshot list)."},
				"text":     map[string]any{"type": "string"},
				"key":      map[string]any{"type": "string"},
				"x":        map[string]any{"type": "number"},
				"y":        map[string]any{"type": "number"},
				"dx":       map[string]any{"type": "number"},
				"dy":       map[string]any{"type": "number"},
			},
		},
	},
	{
		"name": "preview_screenshot",
		"description": "Take a PNG screenshot of the page in your machine's headless browser. It is returned as an image " +
			"and saved to ~/" + sandboxd.ScreenshotDir + "/<time>.png, so you can open it again or show it to your owner.",
		"inputSchema": map[string]any{"type": "object", "properties": viewportArgs},
	},
}

func mergeProps(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func isBrowserTool(tool string) bool { return strings.HasPrefix(tool, "preview_") }

// browserArgs are the arguments of the browser tools.
type browserArgs struct {
	URL      string  `json:"url"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Action   string  `json:"action"`
	Selector string  `json:"selector"`
	Text     string  `json:"text"`
	Key      string  `json:"key"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	DX       float64 `json:"dx"`
	DY       float64 `json:"dy"`
}

// machine returns the function reaching agents' machines (nil: none).
func (p *Service) machine() project.MachineFunc {
	if p.Machine != nil {
		return p.Machine
	}
	if p.Projects != nil {
		return p.Projects.Machine
	}
	return nil
}

// MCPContent is a tool result with MCP content blocks instead of text. An
// image block may name a File (absolute, below the screenshots directory of
// the agent's home) instead of Data: the MCP server in the sandbox reads it,
// so the image never has to fit in a platform reply.
type MCPContent struct {
	Content []MCPBlock `json:"mcpContent"`
}

// MCPBlock is one MCP content block.
type MCPBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	File     string `json:"file,omitempty"`
}

func (p *Service) browserCall(ctx context.Context, caller db.Agent, tool string, raw json.RawMessage) (any, error) {
	var in browserArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, errors.New("invalid arguments")
		}
	}
	machine := p.machine()
	if machine == nil {
		return nil, fmt.Errorf("unknown tool %q", tool)
	}
	ts, err := machine(ctx, caller.ID)
	if err != nil {
		return nil, err
	}
	switch tool {
	case "preview_navigate":
		if strings.TrimSpace(in.URL) == "" {
			return nil, errors.New("url is required")
		}
		r, err := ts.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserNavigate, URL: in.URL, Width: in.Width, Height: in.Height})
		if err != nil {
			return nil, err
		}
		p.agentAudit(ctx, caller, caller.ID, audit.BrowserNavigate, r.URL, map[string]any{"url": in.URL})
		return describePage(r, false), nil
	case "preview_snapshot":
		r, err := ts.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserSnapshot})
		if err != nil {
			return nil, err
		}
		return describePage(r, true), nil
	case "preview_interact":
		if in.Action == "" {
			return nil, errors.New("action is required")
		}
		r, err := ts.Browser(ctx, tunnel.BrowserRequest{
			Action: tunnel.BrowserInteract, Kind: in.Action, Selector: in.Selector, Text: in.Text, Key: in.Key,
			X: in.X, Y: in.Y, DX: in.DX, DY: in.DY,
		})
		if err != nil {
			return nil, err
		}
		if in.Action == tunnel.BrowserQuery {
			return describeMatches(r, in.Selector), nil
		}
		return describePage(r, false), nil
	case "preview_screenshot":
		return p.screenshot(ctx, ts, in)
	}
	return nil, fmt.Errorf("unknown tool %q", tool)
}

// screenshot captures the page, saves it in the machine and returns it as an image.
func (p *Service) screenshot(ctx context.Context, ts *tunnel.Session, in browserArgs) (any, error) {
	r, err := ts.Browser(ctx, tunnel.BrowserRequest{Action: tunnel.BrowserScreenshot, Width: in.Width, Height: in.Height})
	if err != nil {
		return nil, err
	}
	png, err := base64.StdEncoding.DecodeString(r.Screenshot)
	if err != nil || len(png) == 0 {
		return nil, errors.New("the browser returned no screenshot")
	}
	rel := path.Join(sandboxd.ScreenshotDir, time.Now().UTC().Format("20060102-150405.000")+".png")
	if err := ts.WriteFile(ctx, rel, 0o644, bytes.NewReader(png)); err != nil {
		return nil, fmt.Errorf("save the screenshot: %w", err)
	}
	abs := "~/" + rel
	if h, err := ts.HelloInfo(ctx); err == nil && h.Home != "" {
		abs = path.Join(h.Home, rel)
	}
	text := fmt.Sprintf("Screenshot of %s (%q, %dx%d) saved to %s.", r.URL, r.Title, r.Width, r.Height, abs)
	if r.ConsoleErrors > 0 {
		text += fmt.Sprintf(" Console errors: %d (last: %s).", r.ConsoleErrors, r.LastError)
	}
	return MCPContent{Content: []MCPBlock{
		{Type: "text", Text: text},
		{Type: "image", MimeType: "image/png", File: abs},
	}}, nil
}

func describeHeader(b *strings.Builder, r tunnel.BrowserReply) {
	fmt.Fprintf(b, "URL: %s\nTitle: %s\nViewport: %dx%d\n", r.URL, r.Title, r.Width, r.Height)
	if r.ConsoleErrors > 0 {
		fmt.Fprintf(b, "Console errors: %d (last: %s)\n", r.ConsoleErrors, r.LastError)
	} else {
		b.WriteString("Console errors: 0\n")
	}
}

// describePage is a tool result for a page: where it is, optionally its
// text, and its interactive elements.
func describePage(r tunnel.BrowserReply, withText bool) string {
	var b strings.Builder
	describeHeader(&b, r)
	if withText {
		b.WriteString("\nText:\n")
		b.WriteString(strings.TrimSpace(r.Text))
		if r.Truncated {
			b.WriteString("\n[text cut]")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nInteractive elements (%d):\n", len(r.Elements))
	for i, e := range r.Elements {
		if i == maxListedElements {
			fmt.Fprintf(&b, "... and %d more\n", len(r.Elements)-i)
			break
		}
		fmt.Fprintf(&b, "- %s %q %s @ %.0f,%.0f %.0fx%.0f", e.Role, e.Name, e.Selector, e.Rect.X, e.Rect.Y, e.Rect.Width, e.Rect.Height)
		if e.Href != "" {
			fmt.Fprintf(&b, " href=%s", e.Href)
		}
		if e.Value != "" {
			fmt.Fprintf(&b, " value=%q", e.Value)
		}
		if e.Disabled {
			b.WriteString(" disabled")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func describeMatches(r tunnel.BrowserReply, selector string) string {
	var b strings.Builder
	describeHeader(&b, r)
	if len(r.Matches) == 0 {
		fmt.Fprintf(&b, "\nNo element matches %s.\n", selector)
		return b.String()
	}
	out, _ := json.MarshalIndent(r.Matches, "", "  ")
	fmt.Fprintf(&b, "\n%d element(s) match %s:\n%s\n", len(r.Matches), selector, out)
	return b.String()
}
