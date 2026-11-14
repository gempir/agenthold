package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Permission policies.
const (
	PolicyAutoApprove = "auto-approve"
	PolicyAsk         = "ask"
)

// harnessConn is one running harness and the ACP connection to it. It
// implements acp.Client: the harness calls back into it.
type harnessConn struct {
	m       *Manager
	agentID string
	proc    *tunnel.Process
	acp     *acp.ClientSideConnection
	init    acp.InitializeResponse

	mu       sync.Mutex
	sessions map[string]string // ACP session id -> our session id
	active   map[string]bool   // ACP sessions known to this process
	loading  map[string]bool   // replays from session/load are not stored again
	errLines []string
	done     chan struct{}
	once     sync.Once
}

var _ acp.Client = (*harnessConn)(nil)

func (c *harnessConn) bind(acpID, sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[acpID] = sessionID
}

func (c *harnessConn) markActive(acpID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active[acpID] = true
}

func (c *harnessConn) isActive(acpID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active[acpID]
}

func (c *harnessConn) setLoading(acpID string, v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v {
		c.loading[acpID] = true
	} else {
		delete(c.loading, acpID)
	}
}

func (c *harnessConn) stderr(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errLines = append(c.errLines, line)
	if len(c.errLines) > 20 {
		c.errLines = c.errLines[len(c.errLines)-20:]
	}
}

func (c *harnessConn) stderrTail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.errLines, "\n")
}

func (c *harnessConn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *harnessConn) close() {
	c.once.Do(func() {
		close(c.done)
		if c.proc != nil {
			_ = c.proc.Close()
		}
		c.m.dropConn(c)
	})
}

func (c *harnessConn) sessionFor(acpID acp.SessionId) (string, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[string(acpID)], c.loading[string(acpID)], c.sessions[string(acpID)] != ""
}

// SessionUpdate implements acp.Client.
func (c *harnessConn) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	sessionID, loading, ok := c.sessionFor(n.SessionId)
	if !ok || loading {
		return nil
	}
	raw, err := json.Marshal(n.Update)
	if err != nil {
		return err
	}
	err = c.m.log.append(context.WithoutCancel(ctx), sessionID, EventUpdate, "", json.RawMessage(raw))
	return err
}

// RequestPermission implements acp.Client.
func (c *harnessConn) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	sessionID, _, ok := c.sessionFor(p.SessionId)
	cancelled := acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	if !ok || len(p.Options) == 0 {
		return cancelled, nil
	}
	agent, err := c.m.store.GetAgent(ctx, c.agentID)
	if err != nil {
		return cancelled, nil
	}
	requestID := id.New("perm")
	lctx := context.WithoutCancel(ctx)
	_ = c.m.log.append(lctx, sessionID, EventPermission, "", map[string]any{
		"requestId": requestID, "toolCall": p.ToolCall, "options": p.Options, "policy": agent.PermissionPolicy,
	})
	if agent.PermissionPolicy != PolicyAsk {
		// The sandbox is the safety boundary: approve.
		opt := pickAllow(p.Options)
		_ = c.m.log.append(lctx, sessionID, EventPermissionResolved, "", map[string]string{"requestId": requestID, "optionId": string(opt), "by": "auto"})
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(opt)}, nil
	}

	ch := make(chan acp.PermissionOptionId, 1)
	c.m.mu.Lock()
	t := c.m.turns[sessionID]
	if t == nil {
		c.m.mu.Unlock()
		return cancelled, nil
	}
	t.perms[requestID] = ch
	c.m.mu.Unlock()

	select {
	case opt, ok := <-ch:
		if !ok || opt == "" {
			return cancelled, nil
		}
		for _, o := range p.Options {
			if o.OptionId == opt {
				return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(opt)}, nil
			}
		}
		return cancelled, nil
	case <-ctx.Done():
		return cancelled, nil
	case <-c.done:
		return cancelled, nil
	}
}

// pickAllow prefers a one-time approval (approvals stay visible per call).
func pickAllow(opts []acp.PermissionOption) acp.PermissionOptionId {
	for _, kind := range []acp.PermissionOptionKind{acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways} {
		for _, o := range opts {
			if o.Kind == kind {
				return o.OptionId
			}
		}
	}
	return opts[0].OptionId
}

// The client file system and terminal capabilities are not advertised: the
// harness uses its own tools inside the sandbox.
var errUnsupported = errors.New("not supported by this client")

// ReadTextFile implements acp.Client.
func (c *harnessConn) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errUnsupported
}

// WriteTextFile implements acp.Client.
func (c *harnessConn) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errUnsupported
}

// CreateTerminal implements acp.Client.
func (c *harnessConn) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errUnsupported
}

// KillTerminal implements acp.Client.
func (c *harnessConn) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errUnsupported
}

// TerminalOutput implements acp.Client.
func (c *harnessConn) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errUnsupported
}

// ReleaseTerminal implements acp.Client.
func (c *harnessConn) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errUnsupported
}

// WaitForTerminalExit implements acp.Client.
func (c *harnessConn) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errUnsupported
}
