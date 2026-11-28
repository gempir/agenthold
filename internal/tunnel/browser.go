package tunnel

import (
	"context"
	"errors"
	"strings"
)

// Browser actions (BrowserRequest.Action).
const (
	BrowserStatus     = "status"     // what runs; never starts the browser
	BrowserNavigate   = "navigate"   // load URL; replies with a screenshot and the elements
	BrowserScreenshot = "screenshot" // the current page as PNG with the elements
	BrowserInteract   = "interact"   // one input action (Kind); replies like screenshot
	BrowserSnapshot   = "snapshot"   // the page's text and elements, no screenshot
	BrowserClose      = "close"      // stop the browser
)

// Interactions (BrowserRequest.Kind).
const (
	BrowserClick   = "click"   // at X/Y (viewport pixels) or the center of Selector
	BrowserType    = "type"    // insert Text, into Selector when set (else the focused element)
	BrowserPress   = "press"   // press Key ("Enter", "Tab", "ArrowDown", "a", ...)
	BrowserScroll  = "scroll"  // wheel by DX/DY at X/Y (default: the viewport's center)
	BrowserBack    = "back"    // history back
	BrowserForward = "forward" // history forward
	BrowserReload  = "reload"
	// BrowserQuery is a read-only DOM query: the elements matching Selector
	// with their text, attributes and boxes (Matches). It runs no script of
	// the caller's.
	BrowserQuery = "eval"
)

// BrowserUnavailable starts the error sandboxd answers with when the sandbox
// has no browser it can start (Chromium is not installed).
const BrowserUnavailable = "browser unavailable: "

// ErrBrowserUnavailable is returned when the sandbox has no browser.
var ErrBrowserUnavailable = errors.New("the sandbox has no headless Chromium (set CHROME_BIN or install chromium)")

// BrowserRequest is the body of an OpBrowser stream. Width and Height (CSS
// pixels, 0: keep the current viewport) resize the viewport first.
type BrowserRequest struct {
	Action   string  `json:"action"`
	URL      string  `json:"url,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Kind     string  `json:"kind,omitempty"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	Selector string  `json:"selector,omitempty"`
	Text     string  `json:"text,omitempty"`
	Key      string  `json:"key,omitempty"`
	DX       float64 `json:"dx,omitempty"`
	DY       float64 `json:"dy,omitempty"`
}

// BrowserRect is a box in viewport CSS pixels.
type BrowserRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// BrowserElement is an interactive element of the page: its ARIA role (or
// tag), accessible name, a CSS selector that finds it, and its box.
type BrowserElement struct {
	Role     string      `json:"role"`
	Name     string      `json:"name"`
	Tag      string      `json:"tag"`
	Selector string      `json:"selector"`
	Rect     BrowserRect `json:"rect"`
	Href     string      `json:"href,omitempty"`
	Value    string      `json:"value,omitempty"`
	Disabled bool        `json:"disabled,omitempty"`
}

// BrowserMatch is one element a BrowserQuery found.
type BrowserMatch struct {
	Tag        string            `json:"tag"`
	Text       string            `json:"text"`
	Selector   string            `json:"selector"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Rect       BrowserRect       `json:"rect"`
}

// BrowserReply answers OpBrowser. Available: a browser can be started;
// Running: one runs. Screenshot is a base64 PNG of the viewport (navigate,
// screenshot, interact); Text the page's visible text (snapshot).
type BrowserReply struct {
	Available     bool             `json:"available"`
	Running       bool             `json:"running"`
	URL           string           `json:"url,omitempty"`
	Title         string           `json:"title,omitempty"`
	Width         int              `json:"width,omitempty"`
	Height        int              `json:"height,omitempty"`
	ConsoleErrors int              `json:"consoleErrors"`
	LastError     string           `json:"lastError,omitempty"`
	Screenshot    string           `json:"screenshot,omitempty"`
	Elements      []BrowserElement `json:"elements,omitempty"`
	Text          string           `json:"text,omitempty"`
	Truncated     bool             `json:"truncated,omitempty"`
	Matches       []BrowserMatch   `json:"matches,omitempty"`
}

// Browser runs a browser action in the sandbox.
func (s *Session) Browser(ctx context.Context, req BrowserRequest) (BrowserReply, error) {
	c, err := s.Open(ctx, Open{Op: OpBrowser, Browser: &req})
	if err != nil {
		return BrowserReply{}, err
	}
	defer c.Close()
	defer stopOnCancel(ctx, c)()
	var out BrowserReply
	if err := ReadJSONData(c, &out, maxDataReply); err != nil {
		var re *RemoteError
		if errors.As(err, &re) && strings.HasPrefix(re.Msg, BrowserUnavailable) {
			return BrowserReply{}, ErrBrowserUnavailable
		}
		return BrowserReply{}, unsupported(err)
	}
	return out, nil
}
