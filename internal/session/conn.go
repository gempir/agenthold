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
	"github.com/gempir/agenthold/internal/store/db"
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
	// home is the agent account's home in the sandbox.
	home  string
	proto protoClient
	init  acp.InitializeResponse
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
	// promptAt is when the last prompt was sent (unix nanoseconds; 0 once
	// its first update arrived): the activity log times the first answer.
	promptAt atomic.Int64
	// probing: a model probe is scheduled or running (see probe.go).
	probing atomic.Bool
	// probeSessions are the ACP sessions of running probes (guarded by mu):
	// what they report describes models, not the harness's current state.
	probeSessions map[string]bool

	mu       sync.Mutex
	lastUsed map[string]time.Time       // native session id -> when a turn last started or ended in it
	closing  map[string]chan struct{}   // native sessions being closed (idle); closed when done
	sessions map[string]string          // native session id -> our session id
	active   map[string]bool            // native sessions known to this process
	config   map[string][]ConfigOption  // native session id -> its current config options
	commands map[string][]SlashCommand  // native session id -> advertised slash commands
	usage    map[string]ContextUsage    // native session id -> latest context usage
	warned   map[string]map[string]bool // native session id -> notices already given
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
		config: map[string][]ConfigOption{}, commands: map[string][]SlashCommand{},
		usage: map[string]ContextUsage{}, warned: map[string]map[string]bool{}, gaveUp: map[string]bool{},
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

// start attaches the protocol connection to proc. The connection closes when
// the process ends or the protocol breaks.
func (c *harnessConn) start(proc *tunnel.Process, env map[string]string) {
	c.proc = proc
	switch c.adapter.EffectiveProtocol() {
	case harness.ProtocolCodex:
		c.proto = newCodexProtoClient(c, proc)
	case harness.ProtocolClaude:
		c.proto = newClaudeProtoClient(c, proc, env)
	default:
		c.proto = newACPProtoClient(c, proc)
	}
	go func() {
		select {
		case <-proc.Done():
		case <-c.proto.Done():
			// The process usually ends right after its output: give its exit
			// code a moment to arrive before calling it a protocol failure.
			select {
			case <-proc.Done():
			case <-c.done:
			case <-time.After(exitWait):
				if c.adapter.EffectiveProtocol() == harness.ProtocolACP {
					c.setReason(c.name + " stopped speaking ACP (its output ended or was not valid ACP), so it was stopped; send the message again to retry")
				} else {
					c.setReason(c.name + " stopped speaking its protocol (its output ended or was invalid), so it was stopped; send the message again to retry")
				}
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
// has been handled, if the stream has ended.
func (c *harnessConn) drainUpdates() {
	if c.proto == nil {
		return
	}
	select {
	case <-c.proto.Done():
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
	// ready, when set, is closed before the first read may happen.
	ready <-chan struct{}
	line  []byte
	over  bool // the current line outgrew maxCountedLine: not tracked further
}

// maxCountedLine bounds the bytes kept per line to find its method. A longer
// notification is not counted; the drain then simply does not wait for it.
const maxCountedLine = 1 << 20

func (u *updateCounter) Read(p []byte) (int, error) {
	if u.ready != nil {
		<-u.ready
		u.ready = nil
	}
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

// isProbe reports whether the ACP session belongs to a model probe.
func (c *harnessConn) isProbe(acpID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probeSessions[acpID]
}

func (c *harnessConn) setProbe(acpID string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probeSessions == nil {
		c.probeSessions = map[string]bool{}
	}
	if on {
		c.probeSessions[acpID] = true
	} else {
		delete(c.probeSessions, acpID)
	}
}

// tracks reports whether the connection knows the ACP session: it started,
// loaded or resumed it, and did not forget it since.
func (c *harnessConn) tracks(acpID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.config[acpID]; ok {
		return true
	}
	_, bound := c.sessions[acpID]
	return bound || c.active[acpID]
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
	c.usedLocked(acpID)
}

// used records that a turn ended in the session (it is not idle).
func (c *harnessConn) used(acpID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usedLocked(acpID)
}

func (c *harnessConn) usedLocked(acpID string) {
	if c.lastUsed == nil {
		c.lastUsed = map[string]time.Time{}
	}
	c.lastUsed[acpID] = time.Now()
}

type idleSession struct{ acpID, sessionID string }

// idleSessions are the active, bound sessions (no probes) whose last turn
// ended before since.
func (c *harnessConn) idleSessions(since time.Time) []idleSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []idleSession
	for acpID := range c.active {
		sessionID, bound := c.sessions[acpID]
		if !bound || c.probeSessions[acpID] || c.closing[acpID] != nil || !c.lastUsed[acpID].Before(since) {
			continue
		}
		out = append(out, idleSession{acpID, sessionID})
	}
	return out
}

// beginClose marks the session as being closed and forgets it: the next
// turn restores it, after waitClosed.
func (c *harnessConn) beginClose(acpID string) chan struct{} {
	ch := make(chan struct{})
	c.mu.Lock()
	if c.closing == nil {
		c.closing = map[string]chan struct{}{}
	}
	c.closing[acpID] = ch
	delete(c.lastUsed, acpID)
	c.mu.Unlock()
	c.forget(acpID)
	return ch
}

func (c *harnessConn) endClose(acpID string, ch chan struct{}) {
	c.mu.Lock()
	if c.closing[acpID] == ch {
		delete(c.closing, acpID)
	}
	c.mu.Unlock()
	close(ch)
}

// waitClosed waits until a close of the session in flight is answered.
func (c *harnessConn) waitClosed(ctx context.Context, acpID string) error {
	c.mu.Lock()
	ch := c.closing[acpID]
	c.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// ended reports whether the connection is over. When only the protocol stream
// has ended, it waits for the connection to settle why.
func (c *harnessConn) ended() bool {
	if c.proto == nil {
		return c.closed()
	}
	select {
	case <-c.done:
		return true
	case <-c.proto.Done():
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
		if c.proto != nil {
			c.proto.Close()
		}
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

// rebindNativeID updates an active native session ID when a native CLI (Claude)
// assigns its durable session UUID after the initial temporary ID.
func (c *harnessConn) rebindNativeID(oldID, newID string) {
	if oldID == "" || newID == "" || oldID == newID {
		return
	}
	c.mu.Lock()
	sid, ok := c.sessions[oldID]
	if ok {
		delete(c.sessions, oldID)
		c.sessions[newID] = sid
	}
	if c.active[oldID] {
		delete(c.active, oldID)
		c.active[newID] = true
	}
	if opts, has := c.config[oldID]; has {
		delete(c.config, oldID)
		c.config[newID] = opts
	}
	if cmds, has := c.commands[oldID]; has {
		delete(c.commands, oldID)
		c.commands[newID] = cmds
	}
	if ts, has := c.lastUsed[oldID]; has {
		delete(c.lastUsed, oldID)
		c.lastUsed[newID] = ts
	}
	c.mu.Unlock()
	if ok && sid != "" {
		_ = c.m.store.SetSessionNative(context.WithoutCancel(c.m.ctx), db.SetSessionNativeParams{
			AcpSessionID: newID,
			AcpHarness:   c.adapter.ID,
			ID:           sid,
		})
	}
}

func (c *harnessConn) setCommands(acpID string, cmds []SlashCommand) {
	c.mu.Lock()
	c.commands[acpID] = cmds
	c.mu.Unlock()
}

func (c *harnessConn) noteUsage(acpID string, u ContextUsage) {
	c.mu.Lock()
	c.usage[acpID] = u
	c.mu.Unlock()
}

// emitRawUpdate appends a normalized ACP-shaped SessionUpdate JSON payload from
// a native protocol driver (Codex App Server or Claude stream-json).
func (c *harnessConn) emitRawUpdate(nativeID string, raw json.RawMessage) {
	c.alive()
	sessionID, ok := c.sessionFor(acp.SessionId(nativeID))
	if !ok {
		return
	}
	if at := c.promptAt.Swap(0); at != 0 {
		c.m.lg.Info("harness: first update of the turn", "agent", c.agentID, "session", sessionID, "took", time.Since(time.Unix(0, at)))
	}
	// Track usage & file locations if present.
	var meta struct {
		SessionUpdate string `json:"sessionUpdate"`
		Used          int64  `json:"used"`
		Size          int64  `json:"size"`
		Cost          *struct {
			Amount float64 `json:"amount"`
		} `json:"cost"`
		Locations []acp.ToolCallLocation `json:"locations"`
		RawInput  json.RawMessage        `json:"rawInput"`
	}
	if json.Unmarshal(raw, &meta) == nil {
		if len(meta.Locations) > 0 {
			c.m.noteLocations(sessionID, meta.Locations)
		}
		if strings.HasPrefix(meta.SessionUpdate, "tool_call") {
			c.m.noteToolInput(sessionID, meta.RawInput)
		}
		if meta.SessionUpdate == "usage_update" && meta.Used > 0 {
			u := ContextUsage{UsedTokens: meta.Used, MaxTokens: meta.Size}
			if meta.Cost != nil {
				u.CostUSD = meta.Cost.Amount
			}
			c.noteUsage(nativeID, u)
		}
	}
	_ = c.m.log.append(context.WithoutCancel(c.m.ctx), sessionID, EventUpdate, "", raw)
}

// SessionUpdate implements acp.Client.
func (c *harnessConn) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	defer c.updatesHandled.Add(1)
	c.alive() // also for sessions being loaded: a long replay is progress
	if u := n.Update.ConfigOptionUpdate; u != nil {
		// Harness state, not conversation: recorded, not logged. A session
		// this connection no longer tracks (a finished probe, a forgotten
		// session) is ignored: its late updates must not bring it back.
		if c.tracks(string(n.SessionId)) {
			c.m.observeOptions(c, string(n.SessionId), mergeACPModesAndModels(fromACP(u.ConfigOptions), nil, c.adapter.CustomModelsList), nil, false)
		}
		return nil
	}
	if u := n.Update.AvailableCommandsUpdate; u != nil {
		var cmds []SlashCommand
		for _, cmd := range u.AvailableCommands {
			sc := SlashCommand{Name: cmd.Name, Description: cmd.Description}
			if cmd.Input != nil && cmd.Input.Unstructured != nil {
				sc.InputHint = cmd.Input.Unstructured.Hint
			}
			cmds = append(cmds, sc)
		}
		c.setCommands(string(n.SessionId), cmds)
	}
	if u := n.Update.UsageUpdate; u != nil {
		cu := ContextUsage{UsedTokens: int64(u.Used), MaxTokens: int64(u.Size)}
		if u.Cost != nil {
			cu.CostUSD = u.Cost.Amount
		}
		c.noteUsage(string(n.SessionId), cu)
	}
	sessionID, ok := c.sessionFor(n.SessionId)
	if !ok {
		return nil
	}
	if at := c.promptAt.Swap(0); at != 0 {
		c.m.lg.Info("harness: first update of the turn", "agent", c.agentID, "session", sessionID, "took", time.Since(time.Unix(0, at)))
	}
	if tc := n.Update.ToolCall; tc != nil {
		if len(tc.Locations) > 0 {
			c.m.noteLocations(sessionID, tc.Locations)
		}
		c.m.noteToolInputValue(sessionID, tc.RawInput)
	}
	if tc := n.Update.ToolCallUpdate; tc != nil {
		if len(tc.Locations) > 0 {
			c.m.noteLocations(sessionID, tc.Locations)
		}
		c.m.noteToolInputValue(sessionID, tc.RawInput)
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
// Tool permissions follow the session's per-thread permission mode:
//   - full-access (default): auto-approve immediately inside the Firecracker sandbox.
//   - auto-accept-edits: auto-approve reads and edits; ask the user for execute/other.
//   - supervised / plan: auto-approve read-only; ask the user for mutations/execute.
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
	requestID := id.New("perm")
	lctx := context.WithoutCancel(ctx)
	toolKind := ""
	if p.ToolCall.Kind != nil {
		toolKind = string(*p.ToolCall.Kind)
	}
	// Checked under the lock Cancel takes, so a cancel either sees the turn
	// ending or this request sees the cancel.
	c.m.mu.Lock()
	t := c.m.turns[sessionID]
	if t == nil || t.cancelled || t.finished {
		c.m.mu.Unlock()
		return cancelled, nil
	}
	mode := NormalizePermissionMode(t.permissionMode)
	auto := shouldAutoApprove(mode, toolKind)
	var ch chan acp.PermissionOptionId
	var promptCtx context.Context
	if !auto {
		ch = make(chan acp.PermissionOptionId, 1)
		t.perms[requestID] = ch
		promptCtx = t.prompt
	}
	c.m.mu.Unlock()
	if auto {
		_ = c.m.log.append(lctx, sessionID, EventPermission, "", map[string]any{
			"requestId": requestID, "toolCall": p.ToolCall, "options": p.Options, "policy": PolicyAutoApprove, "permissionMode": mode,
		})
		opt := pickAllow(p.Options)
		_ = c.m.log.append(lctx, sessionID, EventPermissionResolved, "", map[string]string{"requestId": requestID, "optionId": string(opt), "by": "auto"})
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(opt)}, nil
	}
	c.m.notifySession(lctx, sessionID, "Approval needed", false)
	// Pending permission wakes a snoozed thread so the user sees the approval card.
	_, _ = c.m.store.UnsnoozeSession(lctx, sessionID)
	_ = c.m.log.append(lctx, sessionID, EventPermission, "", map[string]any{
		"requestId": requestID, "toolCall": p.ToolCall, "options": p.Options, "policy": PolicyAsk, "permissionMode": mode,
	})
	select {
	case opt, ok := <-ch:
		if !ok {
			return cancelled, nil
		}
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(opt)}, nil
	case <-ctx.Done():
		c.m.clearPendingPermission(sessionID, requestID, ch)
		return cancelled, nil
	case <-promptCtx.Done():
		c.m.clearPendingPermission(sessionID, requestID, ch)
		return cancelled, nil
	}
}

// pickAllow prefers a persistent approval so third-party ACP harnesses that
// still ask inside the sandbox stop prompting on subsequent tool calls.
func pickAllow(opts []acp.PermissionOption) acp.PermissionOptionId {
	for _, kind := range []acp.PermissionOptionKind{acp.PermissionOptionKindAllowAlways, acp.PermissionOptionKindAllowOnce} {
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
