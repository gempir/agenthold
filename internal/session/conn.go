package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
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
	// stale: restart once no turn uses it (guarded by Manager.mu). staleWhy
	// says why.
	stale    bool
	staleWhy string
	// stopStream ends the context the harness stream was opened with.
	stopStream context.CancelFunc
	// lastActive is when the harness last showed life (unix nanoseconds): an
	// ACP message or a stderr line.
	lastActive atomic.Int64
	// updatesSeen counts the session/update notifications read off the
	// harness's stdout, updatesHandled those SessionUpdate has finished.
	updatesSeen, updatesHandled atomic.Int64

	mu       sync.Mutex
	sessions map[string]string          // ACP session id -> our session id
	active   map[string]bool            // ACP sessions known to this process
	config   map[string][]ConfigOption  // ACP session id -> its current config options
	warned   map[string]map[string]bool // ACP session id -> notices already given
	defaults map[string]string          // config option values of the first fresh session (nil: none yet)
	gaveUp   map[string]bool            // "option=value" the harness refused or did not answer
	// startModel is the model the harness got in its environment ('' none).
	startModel string
	errLines   []string
	reason     string // why the connection was closed, if the server closed it
	done       chan struct{}
	once       sync.Once
}

var _ acp.Client = (*harnessConn)(nil)

func newHarnessConn(m *Manager, agentID string, ad *harness.Adapter, ts *tunnel.Session) *harnessConn {
	c := &harnessConn{
		m: m, agentID: agentID, adapter: ad, name: ad.Name, ts: ts,
		sessions: map[string]string{}, active: map[string]bool{}, done: make(chan struct{}),
		config: map[string][]ConfigOption{}, warned: map[string]map[string]bool{}, gaveUp: map[string]bool{},
	}
	c.alive()
	return c
}

// alive records that the harness showed life.
func (c *harnessConn) alive() { c.lastActive.Store(time.Now().UnixNano()) }

// idleSince is how long the harness has been silent, counting from no
// earlier than since.
func (c *harnessConn) idleSince(since time.Time) time.Duration {
	last := time.Unix(0, c.lastActive.Load())
	if last.Before(since) {
		last = since
	}
	return time.Since(last)
}

// start attaches the ACP connection to proc. The connection closes when the
// process ends or the protocol breaks (output that is not ACP, a line over
// the SDK's limit).
func (c *harnessConn) start(proc *tunnel.Process) {
	c.proc = proc
	c.acp = acp.NewClientSideConnection(c, proc, &updateCounter{r: proc.Stdout, seen: &c.updatesSeen})
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

// drainWait bounds how long a turn waits for the updates already read off a
// dead connection to be handled.
var drainWait = 500 * time.Millisecond

// drainUpdates waits (bounded) until every session/update read off the wire
// has been handled, if the ACP stream has ended. The SDK fails a pending
// request as soon as the stream ends, while notifications it already read
// are still queued for SessionUpdate: ending the turn first would persist
// the harness's last output after the turn's error and end.
func (c *harnessConn) drainUpdates() {
	select {
	case <-c.acp.Done():
	default:
		return
	}
	deadline := time.Now().Add(drainWait)
	for c.updatesHandled.Load() < c.updatesSeen.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

// updateCounter counts the session/update notifications in the line-delimited
// stream passing through it (the stream's reader is a single goroutine).
type updateCounter struct {
	r    io.Reader
	seen *atomic.Int64
	line []byte
	over bool // the current line outgrew maxCountedLine: not tracked further
}

// maxCountedLine bounds the bytes kept per line to find its method. A longer
// notification is not counted; the drain then simply does not wait for it.
const maxCountedLine = 1 << 20

func (u *updateCounter) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	for rest := p[:n]; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		seg := rest
		if i >= 0 {
			seg = rest[:i]
		}
		if !u.over {
			if len(u.line)+len(seg) > maxCountedLine {
				u.over = true
			} else {
				u.line = append(u.line, seg...)
			}
		}
		if i < 0 {
			break
		}
		u.endLine()
		rest = rest[i+1:]
	}
	return n, err
}

func (u *updateCounter) endLine() {
	line, over := u.line, u.over
	u.line, u.over = u.line[:0], false
	if over || !bytes.Contains(line, []byte("session/update")) {
		return
	}
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(line, &m) == nil && m.Method == "session/update" && len(m.ID) == 0 {
		u.seen.Add(1)
	}
}

// exitWait is how long a connection whose ACP stream ended waits for the
// process's exit code.
var exitWait = 5 * time.Second

func (c *harnessConn) bind(acpID, sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[acpID] = sessionID
}

// forget drops what this process knows about a session, so the next turn
// loads it again (or starts a new one).
func (c *harnessConn) forget(acpID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, acpID)
	delete(c.active, acpID)
	delete(c.config, acpID)
	delete(c.warned, acpID)
}

// configFor is the session's current config options (nil: none reported).
func (c *harnessConn) configFor(acpID string) []ConfigOption {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config[acpID]
}

// defaultValues are the option values of the first fresh session (nil: none).
func (c *harnessConn) defaultValues() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.defaults
}

// setCurrent records a value the harness accepted without reporting options.
func (c *harnessConn) setCurrent(acpID, id, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	opts := append([]ConfigOption(nil), c.config[acpID]...)
	for i := range opts {
		if opts[i].ID == id {
			opts[i].Current = value
		}
	}
	c.config[acpID] = opts
}

func (c *harnessConn) gaveUpOn(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gaveUp[key]
}

func (c *harnessConn) giveUpOn(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gaveUp[key] = true
}

// warnOnce reports whether the notice key was not given yet for the session
// in this process, and marks it given.
func (c *harnessConn) warnOnce(acpID, key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.warned[acpID]
	if w == nil {
		w = map[string]bool{}
		c.warned[acpID] = w
	}
	if w[key] {
		return false
	}
	w[key] = true
	return true
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
	c.alive()
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
		if c.stopStream != nil {
			c.stopStream()
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
	case c.proc.StoppedBySandbox():
		// The stderr tail carries sandboxd's reason.
		return c.name + " was stopped by the sandbox; send the message again to retry"
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
	defer c.updatesHandled.Add(1)
	c.alive() // also for sessions being loaded: a long replay is progress
	if u := n.Update.ConfigOptionUpdate; u != nil {
		// Harness state, not conversation: recorded, not logged.
		c.m.observe(c, string(n.SessionId), u.ConfigOptions, false)
		return nil
	}
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
//
// ACP requires answering with "cancelled" once the turn was cancelled, also
// for requests that cross the cancel on the wire.
func (c *harnessConn) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.alive()
	sessionID, ok := c.sessionFor(p.SessionId)
	cancelled := acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}
	if !ok || len(p.Options) == 0 || !c.m.turnLive(sessionID) {
		return cancelled, nil
	}
	agent, err := c.m.store.GetAgent(ctx, c.agentID)
	if err != nil {
		return cancelled, nil
	}
	requestID := id.New("perm")
	lctx := context.WithoutCancel(ctx)
	ch := make(chan acp.PermissionOptionId, 1)
	// Registered (or refused) under the lock Cancel takes, so a cancel
	// either sees this request or this request sees the cancel.
	c.m.mu.Lock()
	t := c.m.turns[sessionID]
	if t == nil || t.cancelled || t.finished {
		c.m.mu.Unlock()
		return cancelled, nil
	}
	ask := agent.PermissionPolicy == PolicyAsk
	if ask {
		t.perms[requestID] = ch
	}
	c.m.mu.Unlock()
	_ = c.m.log.append(lctx, sessionID, EventPermission, "", map[string]any{
		"requestId": requestID, "toolCall": p.ToolCall, "options": p.Options, "policy": agent.PermissionPolicy,
	})
	if !ask {
		// The sandbox is the safety boundary: approve.
		opt := pickAllow(p.Options)
		_ = c.m.log.append(lctx, sessionID, EventPermissionResolved, "", map[string]string{"requestId": requestID, "optionId": string(opt), "by": "auto"})
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(opt)}, nil
	}

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
func (c *harnessConn) ReadTextFile(_ context.Context, _ acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	c.alive()
	return acp.ReadTextFileResponse{}, errUnsupported
}

// WriteTextFile implements acp.Client.
func (c *harnessConn) WriteTextFile(_ context.Context, _ acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	c.alive()
	return acp.WriteTextFileResponse{}, errUnsupported
}

// CreateTerminal implements acp.Client.
func (c *harnessConn) CreateTerminal(_ context.Context, _ acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	c.alive()
	return acp.CreateTerminalResponse{}, errUnsupported
}

// KillTerminal implements acp.Client.
func (c *harnessConn) KillTerminal(_ context.Context, _ acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	c.alive()
	return acp.KillTerminalResponse{}, errUnsupported
}

// TerminalOutput implements acp.Client.
func (c *harnessConn) TerminalOutput(_ context.Context, _ acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	c.alive()
	return acp.TerminalOutputResponse{}, errUnsupported
}

// ReleaseTerminal implements acp.Client.
func (c *harnessConn) ReleaseTerminal(_ context.Context, _ acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	c.alive()
	return acp.ReleaseTerminalResponse{}, errUnsupported
}

// WaitForTerminalExit implements acp.Client.
func (c *harnessConn) WaitForTerminalExit(_ context.Context, _ acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	c.alive()
	return acp.WaitForTerminalExitResponse{}, errUnsupported
}
