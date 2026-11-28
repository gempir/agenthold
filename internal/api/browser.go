package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// The machine's headless browser. Using it (navigate, interact, even a
// screenshot, which can resize the viewport) needs the right to use the
// agent: the browser reaches every port inside the machine and the web as
// the machine. The status only needs read access.

// browserCall runs a browser request on the agent's machine after checking action.
func (a *API) browserCall(ctx context.Context, agentID string, action authz.Action, req tunnel.BrowserRequest) (tunnel.BrowserReply, db.Agent, error) {
	s, ag, err := a.connectedAgent(ctx, agentID, action)
	if err != nil {
		return tunnel.BrowserReply{}, ag, err
	}
	out, err := s.Browser(ctx, req)
	return out, ag, browserError(err)
}

// browserError maps browser errors to API errors.
func browserError(err error) error {
	var re *tunnel.RemoteError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, tunnel.ErrBrowserUnavailable):
		return errStatus(http.StatusServiceUnavailable, err.Error())
	case errors.As(err, &re):
		return badRequest(re.Msg)
	}
	return err
}

func apiBrowserRect(r tunnel.BrowserRect) gen.BrowserRect {
	return gen.BrowserRect{X: float32(r.X), Y: float32(r.Y), Width: float32(r.Width), Height: float32(r.Height)}
}

func apiBrowserState(r tunnel.BrowserReply) gen.BrowserState {
	out := gen.BrowserState{
		Available: r.Available, Running: r.Running, Width: r.Width, Height: r.Height, ConsoleErrors: r.ConsoleErrors,
		Url: opt(r.URL), Title: opt(r.Title), LastError: opt(r.LastError), Screenshot: opt(r.Screenshot), Text: opt(r.Text),
		Elements: make([]gen.BrowserElement, 0, len(r.Elements)),
	}
	if r.Truncated {
		out.Truncated = &r.Truncated
	}
	for _, e := range r.Elements {
		el := gen.BrowserElement{Role: e.Role, Name: e.Name, Tag: e.Tag, Selector: e.Selector, Rect: apiBrowserRect(e.Rect), Href: opt(e.Href), Value: opt(e.Value)}
		if e.Disabled {
			el.Disabled = &e.Disabled
		}
		out.Elements = append(out.Elements, el)
	}
	if r.Matches != nil {
		ms := make([]gen.BrowserMatch, 0, len(r.Matches))
		for _, m := range r.Matches {
			bm := gen.BrowserMatch{Tag: m.Tag, Text: m.Text, Selector: m.Selector, Rect: apiBrowserRect(m.Rect)}
			if len(m.Attributes) > 0 {
				attrs := m.Attributes
				bm.Attributes = &attrs
			}
			ms = append(ms, bm)
		}
		out.Matches = &ms
	}
	return out
}

func derefFloat(p *float32) float64 {
	if p == nil {
		return 0
	}
	return float64(*p)
}

// GetBrowserStatus implements gen.StrictServerInterface.
func (a *API) GetBrowserStatus(ctx context.Context, req gen.GetBrowserStatusRequestObject) (gen.GetBrowserStatusResponseObject, error) {
	out, _, err := a.browserCall(ctx, req.AgentId, authz.AgentRead, tunnel.BrowserRequest{Action: tunnel.BrowserStatus})
	if err != nil {
		return nil, err
	}
	return gen.GetBrowserStatus200JSONResponse(apiBrowserState(out)), nil
}

// BrowserNavigate implements gen.StrictServerInterface.
func (a *API) BrowserNavigate(ctx context.Context, req gen.BrowserNavigateRequestObject) (gen.BrowserNavigateResponseObject, error) {
	b := req.Body
	if b == nil || b.Url == "" {
		return nil, badRequest("url is required")
	}
	out, ag, err := a.browserCall(ctx, req.AgentId, authz.AgentPrompt, tunnel.BrowserRequest{
		Action: tunnel.BrowserNavigate, URL: b.Url, Width: deref(b.Width), Height: deref(b.Height),
	})
	if err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.BrowserNavigate, out.URL, map[string]any{"url": b.Url})
	return gen.BrowserNavigate200JSONResponse(apiBrowserState(out)), nil
}

// BrowserScreenshot implements gen.StrictServerInterface.
func (a *API) BrowserScreenshot(ctx context.Context, req gen.BrowserScreenshotRequestObject) (gen.BrowserScreenshotResponseObject, error) {
	out, _, err := a.browserCall(ctx, req.AgentId, authz.AgentPrompt, tunnel.BrowserRequest{
		Action: tunnel.BrowserScreenshot, Width: deref(req.Params.Width), Height: deref(req.Params.Height),
	})
	if err != nil {
		return nil, err
	}
	return gen.BrowserScreenshot200JSONResponse(apiBrowserState(out)), nil
}

// BrowserInteract implements gen.StrictServerInterface.
func (a *API) BrowserInteract(ctx context.Context, req gen.BrowserInteractRequestObject) (gen.BrowserInteractResponseObject, error) {
	b := req.Body
	if b == nil || !b.Action.Valid() {
		return nil, badRequest("action must be one of click, type, press, scroll, back, forward, reload, eval")
	}
	r := tunnel.BrowserRequest{
		Action: tunnel.BrowserInteract, Kind: string(b.Action), X: derefFloat(b.X), Y: derefFloat(b.Y),
		Selector: deref(b.Selector), Text: deref(b.Text), Key: deref(b.Key), DX: derefFloat(b.Dx), DY: derefFloat(b.Dy),
	}
	out, ag, err := a.browserCall(ctx, req.AgentId, authz.AgentPrompt, r)
	if err != nil {
		return nil, err
	}
	if b.Action != gen.BrowserEval {
		// What was typed can be a password: only its length is kept.
		details := map[string]any{"action": string(b.Action)}
		if r.Selector != "" {
			details["selector"] = r.Selector
		}
		if r.Kind == tunnel.BrowserClick && r.Selector == "" {
			details["x"], details["y"] = r.X, r.Y
		}
		if r.Text != "" {
			details["textLength"] = len([]rune(r.Text))
		}
		if r.Key != "" {
			details["key"] = r.Key
		}
		a.record(ctx, ag, audit.BrowserInteract, out.URL, details)
	}
	return gen.BrowserInteract200JSONResponse(apiBrowserState(out)), nil
}

// BrowserSnapshot implements gen.StrictServerInterface.
func (a *API) BrowserSnapshot(ctx context.Context, req gen.BrowserSnapshotRequestObject) (gen.BrowserSnapshotResponseObject, error) {
	out, _, err := a.browserCall(ctx, req.AgentId, authz.AgentPrompt, tunnel.BrowserRequest{Action: tunnel.BrowserSnapshot})
	if err != nil {
		return nil, err
	}
	return gen.BrowserSnapshot200JSONResponse(apiBrowserState(out)), nil
}

// BrowserClose implements gen.StrictServerInterface.
func (a *API) BrowserClose(ctx context.Context, req gen.BrowserCloseRequestObject) (gen.BrowserCloseResponseObject, error) {
	out, ag, err := a.browserCall(ctx, req.AgentId, authz.AgentPrompt, tunnel.BrowserRequest{Action: tunnel.BrowserClose})
	if err != nil {
		return nil, err
	}
	a.record(ctx, ag, audit.BrowserClose, "", nil)
	return gen.BrowserClose200JSONResponse(apiBrowserState(out)), nil
}
