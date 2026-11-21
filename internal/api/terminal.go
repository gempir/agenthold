package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

const wakeTimeout = 2 * time.Minute

// Wake returns the agent's tunnel session, starting the sandbox if it is
// asleep. status is called with progress messages.
func (a *API) Wake(ctx context.Context, agentID string, status func(string)) (*tunnel.Session, error) {
	row, err := a.Store.GetSandbox(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if err := a.Sandboxes.Reachable(ctx, agentID); err != nil {
		return nil, err
	}
	// Its VM may still be connected while it is being stopped (also for a copy
	// of its disks): work started now would be killed.
	if sandbox.Phase(row.State) == sandbox.PhaseStopping {
		return nil, errStatus(http.StatusConflict, "the agent is being stopped or copied; try again in a moment")
	}
	if ok, err := a.Node.Connected(ctx, agentID); err != nil {
		return nil, err
	} else if ok {
		return node.Client(a.Node, agentID), nil
	}
	switch sandbox.Phase(row.State) {
	case sandbox.PhaseSleeping, sandbox.PhaseError:
		if err := a.checkMemory(ctx, agentID); err != nil {
			return nil, err
		}
		status("Starting the sandbox…")
		if err := a.Sandboxes.Start(ctx, agentID); err != nil && !errors.Is(err, sandbox.ErrConflict) {
			return nil, err
		}
	case sandbox.PhaseDestroying:
		return nil, errStatus(http.StatusConflict, "the agent is being deleted")
	default:
		status("Waiting for the sandbox…")
	}
	wctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	type res struct {
		s   *tunnel.Session
		err error
	}
	ch := make(chan res, 1)
	go func() {
		err := a.Node.WaitConnected(wctx, agentID)
		ch <- res{node.Client(a.Node, agentID), err}
	}()
	for {
		select {
		case r := <-ch:
			if r.err != nil {
				if sb, err := a.Store.GetSandbox(ctx, agentID); err == nil && sb.Error != "" {
					return nil, errStatus(http.StatusConflict, sb.Error)
				}
				return nil, r.err
			}
			return r.s, nil
		case <-time.After(time.Second):
			// Fail fast if the start failed instead of waiting for the timeout.
			if sb, err := a.Store.GetSandbox(ctx, agentID); err == nil && sb.State == string(sandbox.PhaseError) {
				return nil, errStatus(http.StatusConflict, sb.Error)
			}
		}
	}
}

type termMsg struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Code    int    `json:"code,omitempty"`
	Cols    uint16 `json:"cols,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
}

// terminal bridges a browser WebSocket to a pty in the sandbox.
func (a *API) terminal(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		writeError(w, r, errUnauthorized)
		return
	}
	agentID := chi.URLParam(r, "agentId")
	authMethod := r.URL.Query().Get("auth")
	action := authz.AgentTerminal
	if authMethod != "" {
		action = authz.AgentAuthLogin
	}
	acc, err := a.authz.Agent(r.Context(), u.ID, agentID, action)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// Accept verifies that Origin matches Host, which protects the
	// cookie-authenticated socket against cross-site use.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ctx := r.Context()
	defer a.guardSocket(ctx, u.ID, ws)()

	status := func(m string) { _ = wsjson.Write(ctx, ws, termMsg{Type: "status", Message: m}) }
	fail := func(err error) {
		_, msg := toHTTP(err)
		_ = wsjson.Write(ctx, ws, termMsg{Type: "error", Message: msg})
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}

	s, err := a.Wake(ctx, agentID, status)
	if err != nil {
		fail(err)
		return
	}
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	open := tunnel.Open{Cols: uint16(max(cols, 0)), Rows: uint16(max(rows, 0))}
	if authMethod != "" {
		status("Starting the harness login…")
		cmd, env, err := a.loginCommand(auth.WithUser(ctx, u), acc.Row.Agent, authMethod)
		if err != nil {
			fail(err)
			return
		}
		open.Cmd, open.Env = cmd, env
	}
	if a.Idle != nil {
		a.Idle.Tracker.TerminalOpened(agentID)
		defer a.Idle.Tracker.TerminalClosed(agentID)
	}
	if authMethod != "" {
		a.record(auth.WithUser(ctx, u), acc.Row.Agent, audit.HarnessLogin, authMethod, nil)
	} else {
		a.record(auth.WithUser(ctx, u), acc.Row.Agent, audit.TerminalOpen, "", nil)
	}
	term, err := s.PTY(ctx, open)
	if err != nil {
		fail(err)
		return
	}
	defer term.Close()

	// Sandbox -> browser.
	done := make(chan struct{})
	go func() {
		defer close(done)
		code, err := term.ReadOutput(wsWriter{ctx: ctx, ws: ws})
		if err == nil && code == 0 && authMethod != "" {
			// The harness stored a login on the agent's disk.
			_ = a.Store.MarkCredentialsInSandbox(context.WithoutCancel(ctx), agentID)
		}
		if err != nil && ctx.Err() == nil {
			_ = wsjson.Write(ctx, ws, termMsg{Type: "error", Message: "the terminal connection was lost"})
		} else {
			_ = wsjson.Write(ctx, ws, termMsg{Type: "exit", Code: code})
		}
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}()

	// Browser -> sandbox.
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			break
		}
		if typ == websocket.MessageBinary {
			if _, err := term.Write(data); err != nil {
				break
			}
			continue
		}
		var m termMsg
		if err := jsonUnmarshal(data, &m); err == nil && m.Type == "resize" && m.Cols > 0 && m.Rows > 0 {
			_ = term.Resize(m.Cols, m.Rows)
		}
	}
	_ = term.Close()
	<-done
}

type wsWriter struct {
	ctx context.Context
	ws  *websocket.Conn
}

func (w wsWriter) Write(p []byte) (int, error) {
	if err := w.ws.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
