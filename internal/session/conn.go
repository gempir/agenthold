package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/harness"
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
	adapter *harness.Adapter
	name    string // the adapter's name, for messages
	proc    *tunnel.Process
	ts      *tunnel.Session
	acp     *acp.ClientSideConnection
	init    acp.InitializeResponse
	// stale: restart once no turn uses it (guarded by Manager.mu).
	stale bool

	mu       sync.Mutex
	sessions map[string]string // ACP session id -> our session id
	active   map[string]bool   // ACP sessions known to this process
	errLines []string
	reason   string // why the connection was closed, if the server closed it
	done     chan struct{}
	once     sync.Once
}

var _ acp.Client = (*harnessConn)(nil)

func newHarnessConn(m *Manager, agentID string, ad *harness.Adapter, ts *tunnel.Session) *harnessConn {
	return &harnessConn{
		m: m, agentID: agentID, adapter: ad, name: ad.Name, ts: ts,
		sessions: map[string]string{}, active: map[string]bool{}, done: make(chan struct{}),
	}
}

// start attaches the ACP connection to proc. The connection closes when the
// process ends or the protocol breaks (output that is not ACP, a line over
// the SDK's limit).
func (c *harnessConn) start(proc *tunnel.Process) {
	c.proc = proc
	c.acp = acp.NewClientSideConnection(c, proc, proc.Stdout)
	c.acp.SetLogger(c.m.lg.With("agent", c.agentID, "component", "acp"))
	go func() {
		select {
		case <-proc.Done():
		case <-c.acp.Done():
			// The process usually ends right after its output: give its exit
			// code a moment to arrive before calling it a protocol failure.
			select {
			case <-proc.Done():
			case <-c.done:
			case <-time.After(exitWait):
				c.setReason(c.name + " stopped speaking ACP (its output ended or was not valid ACP), so it was stopped; send the message again to retry")
			}
		case <-c.done:
		}
		c.close("")
	}()
}

// exitWait is how long a connection whose ACP stream ended waits for the
// process's exit code.
var exitWait = 5 * time.Second

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

// stderrKeep bounds what is kept of the harness's stderr for error messages.
const (
	stderrKeepLines = 20
	stderrShowBytes = 2000
)

func (c *harnessConn) stderr(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errLines = append(c.errLines, line)
	if len(c.errLines) > stderrKeepLines {
		c.errLines = c.errLines[len(c.errLines)-stderrKeepLines:]
	}
}

// stderrTail is the end of the harness's stderr, at most stderrShowBytes.
func (c *harnessConn) stderrTail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	tail := strings.TrimSpace(strings.Join(c.errLines, "\n"))
	if len(tail) > stderrShowBytes {
		tail = "…" + strings.ToValidUTF8(tail[len(tail)-stderrShowBytes:], "")
	}
	return tail
}

func (c *harnessConn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// ended reports whether the connection is over. When only the ACP stream
// has ended, it waits for the connection to settle why.
func (c *harnessConn) ended() bool {
	select {
	case <-c.done:
		return true
	case <-c.acp.Done():
		<-c.done // start's watcher closes it within exitWait
		return true
	default:
		return false
	}
}

func (c *harnessConn) setReason(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reason == "" {
		c.reason = reason
	}
}

// close stops the harness. reason (if not empty) is what turns using it
// report; otherwise failure describes how the process ended.
func (c *harnessConn) close(reason string) {
	c.once.Do(func() {
		if reason != "" {
			c.setReason(reason)
		}
		close(c.done)
		if c.proc != nil {
			_ = c.proc.Close()
		}
		c.m.dropConn(c)
	})
}

// stoppedError is the error of a turn whose harness stopped.
type stoppedError struct{ msg string }

func (e *stoppedError) Error() string { return e.msg }

// failure describes why the closed connection ended, for the session log.
func (c *harnessConn) failure() error {
	c.mu.Lock()
	reason := c.reason
	c.mu.Unlock()
	msg := reason
	if msg == "" {
		msg = c.describeExit()
	}
	if tail := c.stderrTail(); tail != "" {
		msg += "\n\n" + tail
	}
	return &stoppedError{msg: msg}
}

// describeExit says how the harness process ended.
func (c *harnessConn) describeExit() string {
	if c.proc == nil {
		return c.name + " did not start"
	}
	select {
	case <-c.proc.Done():
	case <-time.After(exitWait):
		return c.name + " stopped"
	}
	code, err := c.proc.ExitCode()
	var re *tunnel.RemoteError
	switch {
	case errors.As(err, &re):
		return "could not run " + c.name + ": " + re.Msg
	case err != nil:
		return "lost the connection to the agent's sandbox while " + c.name + " was running; send the message again to retry"
	case code == 128+9:
		return c.name + " was killed (signal 9; it may have run out of memory); send the message again to retry"
	case code > 128:
		return fmt.Sprintf("%s was killed by signal %d; send the message again to retry", c.name, code-128)
	case code == 0:
		return c.name + " exited unexpectedly; send the message again to retry"
	default:
		return fmt.Sprintf("%s exited with code %d; send the message again to retry", c.name, code)
	}
}

func (c *harnessConn) sessionFor(acpID acp.SessionId) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.sessions[string(acpID)]
	return id, ok
}

// SessionUpdate implements acp.Client.
func (c *harnessConn) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	sessionID, ok := c.sessionFor(n.SessionId)
	if !ok {
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
	sessionID, ok := c.sessionFor(p.SessionId)
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
