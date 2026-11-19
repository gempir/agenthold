package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
	"github.com/gempir/agenthold/internal/version"
)

// Errors.
var (
	ErrBusy              = errors.New("a turn is already running in this session")
	ErrNotFound          = errors.New("session not found")
	ErrNoTurn            = errors.New("no turn is running")
	ErrUnknownPermission = errors.New("permission request not found or already answered")
	// ErrNoHarness: the machine is an app, which runs no harness.
	ErrNoHarness    = errors.New("this machine is an app: it has no harness to talk to")
	errShuttingDown = errors.New("the server is shutting down")
)

var errLoginNeeded = errors.New("the harness is not signed in: connect it under Settings → Provider logins and let the agent use your logins (Overview tab), or attach an API key secret and rebuild")

func authRequired(err error) bool {
	var re *acp.RequestError
	return errors.As(err, &re) && re.Code == -32000
}

// Timeouts of the harness protocol. A turn itself has none: it may run for
// hours, and the user can always cancel it.
var (
	// wakeTimeout bounds waking the sandbox (a cold boot under nested
	// virtualization is slow).
	wakeTimeout = 10 * time.Minute
	// loginsTimeout bounds preparing the account login.
	loginsTimeout = 2 * time.Minute
	// openTimeout bounds opening the harness stream to sandboxd.
	openTimeout = 2 * time.Minute
	// setupIdle is how long a setup request (initialize, authenticate,
	// session/new, load or resume) may go without the harness showing life
	// (an ACP message or a stderr line); setupCap bounds it in any case.
	setupIdle = 2 * time.Minute
	setupCap  = 15 * time.Minute
	// cancelGrace is how long a harness may take to end a turn after
	// session/cancel before the turn is ended without it.
	cancelGrace = 15 * time.Second
)

// HomeDir is the harness's working directory.
const HomeDir = "/home/agent"

// Waker returns the agent's tunnel session, starting the sandbox if needed.
type Waker func(ctx context.Context, agentID string, status func(string)) (*tunnel.Session, error)

// Manager runs harness connections and sessions.
//
// One harness process (a harnessConn) serves all sessions of an agent. A
// failure of the process ends every turn on it; a harness that stops
// answering one turn's request ends only that turn while other turns use it,
// and is restarted once none does.
type Manager struct {
	store *store.Store
	wake  Waker
	log   *log
	lg    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closing bool                    // Close was called: no new turns or connections
	conns   map[string]*harnessConn // by agent id
	turns   map[string]*turn        // by session id
	// starting serializes harness startup per agent (a size-1 channel, so a
	// waiting turn can be cancelled).
	starting map[string]chan struct{}
	activity func(agentID string)
	tools    Tools
	logins   Logins
	catMu    sync.Mutex // serializes model catalog updates
	probe    bool       // describe unknown models after turns (SetModelProbe)
}

// Logins supplies the account login of an agent's harness: files are written
// into the sandbox, the returned environment is added to the harness.
type Logins interface {
	Prepare(ctx context.Context, agent db.Agent, ts *tunnel.Session) (map[string]string, error)
}

// SetLogins sets the account logins provider.
func (m *Manager) SetLogins(l Logins) { m.logins = l }

// Restart stops the agent's harness so the next turn starts it with current
// settings (an account login changed). Running turns finish first.
func (m *Manager) Restart(agentID string) {
	m.mu.Lock()
	c := m.conns[agentID]
	busy := c != nil && m.inUse(c)
	if busy {
		c.stale, c.staleWhy = true, "the harness restarted to apply new settings"
	}
	m.mu.Unlock()
	if c != nil && !busy {
		c.close("the harness restarted to apply new settings")
	}
}

type turn struct {
	sessionID string
	agentID   string
	depth     int
	// setup is the context of everything before the prompt is sent (waking,
	// starting the harness, activating the session); Cancel ends it.
	setup     context.Context
	stopSetup context.CancelFunc
	// prompt is the context of session/prompt. It ends only when the turn is
	// given up without the harness (its cause says why).
	prompt     context.Context
	stopPrompt context.CancelCauseFunc

	// Guarded by Manager.mu.
	conn      *harnessConn
	acpID     string
	prompting bool // session/prompt was sent
	cancelled bool
	finished  bool                                   // run returned; nothing may act on the turn anymore
	killTimer *time.Timer                            // armed by Cancel
	perms     map[string]chan acp.PermissionOptionId // pending permission requests
}

// NewManager returns a session manager. Turns that were running when the
// server stopped are ended in their logs.
func NewManager(st *store.Store, wake Waker, lg *slog.Logger) *Manager {
	if lg == nil {
		lg = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		store: st, wake: wake, log: newLog(st), lg: lg, ctx: ctx, cancel: cancel,
		conns: map[string]*harnessConn{}, turns: map[string]*turn{}, starting: map[string]chan struct{}{},
	}
	m.endInterruptedTurns(ctx)
	return m
}

// endInterruptedTurns closes turns that a previous server process left
// open: their harness died with the tunnel, so they can never finish. A turn
// is open when its session's last prompt has no turn_end after it, which
// also covers a crash between the status update and turn_end; once closed
// it is not found again, so repeated restarts add nothing.
func (m *Manager) endInterruptedTurns(ctx context.Context) {
	ids, err := m.store.ListUnfinishedSessions(ctx)
	if err != nil {
		m.lg.Warn("list interrupted turns", "err", err)
	}
	for _, sid := range ids {
		if err := m.log.append(ctx, sid, EventError, "", map[string]string{"message": "The server restarted during this turn, which stopped it. Send the message again to retry."}); err != nil {
			m.lg.Warn("end interrupted turn", "session", sid, "err", err)
			continue
		}
		_ = m.log.append(ctx, sid, EventTurnEnd, "", map[string]any{"stopReason": "error"})
	}
	if err := m.store.ResetRunningSessions(ctx); err != nil {
		m.lg.Warn("reset running sessions", "err", err)
	}
}

// OnActivity is called with an agent id whenever a session is used.
func (m *Manager) OnActivity(fn func(agentID string)) { m.activity = fn }

func (m *Manager) touch(agentID string) {
	if m.activity != nil {
		m.activity(agentID)
	}
}

// AgentBusy reports whether any session of the agent has a running turn.
func (m *Manager) AgentBusy(agentID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.turns {
		if t.agentID == agentID {
			return true
		}
	}
	return false
}

// inUse reports whether a turn uses c (m.mu held).
func (m *Manager) inUse(c *harnessConn) bool {
	for _, t := range m.turns {
		if t.conn == c {
			return true
		}
	}
	return false
}

// othersOn reports whether a live turn other than t uses c (m.mu held).
func (m *Manager) othersOn(c *harnessConn, t *turn) bool {
	for _, o := range m.turns {
		if o != t && o.conn == c && !o.finished {
			return true
		}
	}
	return false
}

// turnLive reports whether the session has a turn that is neither cancelled
// nor finished.
func (m *Manager) turnLive(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.turns[sessionID]
	return t != nil && !t.cancelled && !t.finished
}

// Tools supplies what a session gets: MCP servers, and files to install
// (skills) before the harness sees the session.
type Tools interface {
	MCPServers(ctx context.Context, agentID, sessionID string, caps acp.McpCapabilities) ([]acp.McpServer, error)
	Prepare(ctx context.Context, agentID string, ts *tunnel.Session) error
}

// SetTools sets the tools provider.
func (m *Manager) SetTools(t Tools) { m.tools = t }

// SetWaker sets how agents are woken (the API provides it).
func (m *Manager) SetWaker(w Waker) { m.wake = w }

// Close stops all harness connections and waits for turns to end. Running
// turns end with an error in their logs.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closing = true // no turn or connection is added after this
	conns := make([]*harnessConn, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.mu.Unlock()
	for _, c := range conns {
		c.close("the server shut down during the turn; send the message again to retry")
	}
	m.cancel()
	m.wg.Wait()
}

// Wait blocks until running turns have finished (tests).
func (m *Manager) Wait() { m.wg.Wait() }

// Create starts a new, empty session.
func (m *Manager) Create(ctx context.Context, agentID, userID, title string) (db.AgentSession, error) {
	if ag, err := m.store.GetAgent(ctx, agentID); err == nil && ag.Kind == "app" {
		return db.AgentSession{}, ErrNoHarness
	}
	now := time.Now().Unix()
	s := db.AgentSession{ID: id.New("ses"), AgentID: agentID, Title: title, Status: "idle", CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if s.Title == "" {
		s.Title = "New session"
	}
	err := m.store.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: s.ID, AgentID: agentID, Title: s.Title, CreatedBy: userID, CreatedAt: now, UpdatedAt: now})
	return s, err
}

// Subscribe returns events after `after` and a live subscription. A non-nil
// viewer is shown to everyone else watching the session.
func (m *Manager) Subscribe(ctx context.Context, sessionID string, after int64, viewer *Viewer) ([]Event, *Subscription, error) {
	return m.log.subscribe(ctx, sessionID, after, viewer)
}

// Running reports whether a turn is in progress.
func (m *Manager) Running(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.turns[sessionID]
	return ok
}

// PromptOpts are optional settings of a turn.
type PromptOpts struct {
	// Depth counts how many agents are between a human (or trigger) and this
	// turn: 0 for people, n+1 for a turn another agent's depth-n turn started.
	Depth int
	// Origin describes the agent that started the turn, shown in the log.
	Origin map[string]string
}

// AgentDepth is the depth of the deepest turn the agent is running (0 if none).
func (m *Manager) AgentDepth(agentID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := 0
	for _, t := range m.turns {
		if t.agentID == agentID && t.depth > d {
			d = t.depth
		}
	}
	return d
}

// Prompt starts a turn. It returns once the turn has started; the turn
// itself runs in the background and streams into the event log.
func (m *Manager) Prompt(ctx context.Context, sessionID, userID, userName, text string, status func(string)) error {
	return m.PromptWith(ctx, sessionID, userID, userName, text, status, PromptOpts{})
}

// PromptWith is Prompt with options.
//
// Every turn ends with exactly one turn_end event (stopReason "error" after
// an error event, "cancelled" after Cancel), and the session is idle again
// before turn_end is visible.
func (m *Manager) PromptWith(ctx context.Context, sessionID, userID, userName, text string, status func(string), opts PromptOpts) error {
	sess, err := m.store.GetAgentSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	if _, busy := m.turns[sessionID]; busy {
		m.mu.Unlock()
		return ErrBusy
	}
	if m.closing {
		m.mu.Unlock()
		return errShuttingDown
	}
	setup, stopSetup := context.WithCancel(m.ctx)
	prompt, stopPrompt := context.WithCancelCause(m.ctx)
	t := &turn{
		sessionID: sessionID, agentID: sess.AgentID, depth: opts.Depth,
		setup: setup, stopSetup: stopSetup, prompt: prompt, stopPrompt: stopPrompt,
		perms: map[string]chan acp.PermissionOptionId{},
	}
	m.turns[sessionID] = t
	m.wg.Add(1) // under m.mu with !closing: never after Close's Wait started
	m.mu.Unlock()
	m.touch(sess.AgentID)
	// The log and the session row outlive the request and the turn's
	// contexts: whatever happens, the turn's end is recorded.
	lctx := context.WithoutCancel(ctx)
	release := func() {
		stopSetup()
		stopPrompt(nil)
		m.mu.Lock()
		delete(m.turns, sessionID)
		var retire *harnessConn
		why := ""
		if c := t.conn; c != nil && c.stale && !m.inUse(c) {
			retire, why = c, c.staleWhy
		}
		m.mu.Unlock()
		if retire != nil {
			retire.close(why)
		}
		m.touch(sess.AgentID)
	}

	promptData := map[string]any{"text": text, "user": map[string]string{"id": userID, "name": userName}}
	if opts.Origin != nil {
		promptData["origin"] = opts.Origin
		promptData["depth"] = opts.Depth
	}
	if err := m.log.append(lctx, sessionID, EventPrompt, userID, promptData); err != nil {
		release()
		m.wg.Done()
		return err
	}
	if err := m.store.SetSessionStatus(lctx, db.SetSessionStatusParams{Status: "running", UpdatedAt: time.Now().Unix(), ID: sessionID}); err != nil {
		_ = m.log.append(lctx, sessionID, EventError, "", map[string]string{"message": "Could not start the turn: " + err.Error()})
		release()
		_ = m.log.append(lctx, sessionID, EventTurnEnd, "", map[string]any{"stopReason": "error"})
		m.wg.Done()
		return err
	}
	if sess.Title == "New session" {
		if err := m.store.RenameAgentSessionTitle(lctx, sessionID, titleFrom(text)); err != nil {
			m.lg.Warn("title session", "session", sessionID, "err", err)
		}
	}

	go func() {
		defer m.wg.Done()
		end, err := m.run(t, sess, text, status)
		m.mu.Lock()
		t.finished = true
		if t.killTimer != nil {
			t.killTimer.Stop()
		}
		t.cancelPermissions()
		cancelled := t.cancelled
		m.mu.Unlock()
		switch {
		case err != nil && cancelled:
			// What failed after a cancel is the cancel taking effect, unless
			// the harness had to be given up for it.
			var se *stoppedError
			if errors.As(err, &se) {
				_ = m.log.append(lctx, sessionID, EventNotice, "", map[string]string{"message": err.Error()})
			}
			end = map[string]any{"stopReason": string(acp.StopReasonCancelled)}
		case err != nil:
			m.lg.Warn("turn failed", "session", sessionID, "err", err)
			_ = m.log.append(lctx, sessionID, EventError, "", map[string]string{"message": err.Error()})
			end = map[string]any{"stopReason": "error"}
		}
		// The turn is over for new prompts before anyone can see turn_end:
		// a client that reacts to turn_end by prompting again must not get a
		// "turn already running" conflict.
		if err := m.store.SetSessionStatus(lctx, db.SetSessionStatusParams{Status: "idle", UpdatedAt: time.Now().Unix(), ID: sessionID}); err != nil {
			m.lg.Warn("session status", "session", sessionID, "err", err)
		}
		release()
		_ = m.log.append(lctx, sessionID, EventTurnEnd, "", end)
		if err == nil && t.conn != nil {
			m.probeLater(t.conn)
		}
	}()
	return nil
}

func titleFrom(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(t) > 60 {
		t = string([]rune(t)[:60]) + "…"
	}
	if t == "" {
		t = "Session"
	}
	return t
}

// run executes one turn and returns the data of its turn_end event, which the
// caller appends once the turn has been released.
func (m *Manager) run(t *turn, sess db.AgentSession, text string, status func(string)) (map[string]any, error) {
	ctx := t.setup
	notice := func(msg string) {
		_ = m.log.append(context.WithoutCancel(ctx), sess.ID, EventNotice, "", map[string]string{"message": msg})
		if status != nil {
			status(msg)
		}
	}
	conn, err := m.connection(ctx, sess.AgentID, notice)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	t.conn = conn
	m.mu.Unlock()
	acpID, err := m.activate(ctx, t, conn, sess, notice)
	if err != nil {
		return nil, err
	}
	conn.bind(acpID, sess.ID)
	if err := m.applyChoice(ctx, conn, sess.ID, acpID, notice); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if t.cancelled {
		m.mu.Unlock()
		return nil, context.Canceled
	}
	t.acpID, t.prompting = acpID, true
	m.mu.Unlock()

	// Not the setup context: a cancel is session/cancel, answered by the
	// harness with stopReason "cancelled".
	res, err := conn.acp.Prompt(t.prompt, acp.PromptRequest{SessionId: acp.SessionId(acpID), Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
	if err != nil {
		conn.drainUpdates()
		var se *stoppedError
		if cause := context.Cause(t.prompt); t.prompt.Err() != nil && errors.As(cause, &se) {
			conn.forget(acpID) // the harness may still be busy with it
			return nil, cause
		}
		if conn.ended() {
			return nil, conn.failure()
		}
		if m.shuttingDown() {
			return nil, errors.New("the server shut down during the turn; send the message again to retry")
		}
		if authRequired(err) {
			return nil, errLoginNeeded
		}
		return nil, &harnessError{msg: fmt.Sprintf("%s failed the prompt: %s", conn.name, refusal(err)), err: err}
	}
	return map[string]any{"stopReason": res.StopReason, "usage": res.Usage}, nil
}

func (m *Manager) shuttingDown() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closing
}

// stepTimeout is a setup request the harness did not answer in time.
type stepTimeout struct{ msg string }

func (e *stepTimeout) Error() string { return e.msg }

// step runs one setup request of turn t (nil: no turn, e.g. a UI connect).
// The request may take as long as the harness keeps showing life, up to
// setupCap, and setupIdle without any. On a timeout the request is
// cancelled and a *stepTimeout returned; if the harness ended meanwhile, its
// failure is returned instead (exit status and protocol breaks always win).
func (m *Manager) step(ctx context.Context, conn *harnessConn, method string, fn func(context.Context) error) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- fn(sctx) }()
	timer := time.NewTimer(setupIdle)
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			if err != nil && conn.ended() {
				return conn.failure()
			}
			return err
		case <-timer.C:
			idle, total := conn.idleSince(start), time.Since(start)
			if idle < setupIdle && total < setupCap {
				timer.Reset(min(setupIdle-idle, setupCap-total))
				continue
			}
			cancel()
			<-done
			if conn.ended() {
				return conn.failure()
			}
			if idle >= setupIdle {
				return &stepTimeout{fmt.Sprintf("%s did not answer %s and showed no activity for %s", conn.name, method, setupIdle)}
			}
			return &stepTimeout{fmt.Sprintf("%s was still working on %s after %s, the limit", conn.name, method, setupCap)}
		}
	}
}

// giveUp handles a harness that did not answer turn t's request. If no
// other turn uses the connection it is stopped. Otherwise it keeps serving
// them and is restarted once none does; only t ends now.
func (m *Manager) giveUp(t *turn, conn *harnessConn, why string) error {
	m.mu.Lock()
	others := m.othersOn(conn, t)
	if others {
		conn.stale = true
		conn.staleWhy = conn.name + " stopped responding in a conversation and was restarted once the others were done"
	}
	m.mu.Unlock()
	if others {
		return &stoppedError{msg: why + "; this turn was stopped, and the harness restarts once the agent's other conversations are done"}
	}
	conn.close(why + ", so it was stopped; send the message again to retry")
	return conn.failure()
}

// activate makes sure the harness knows the session: new, loaded or resumed.
func (m *Manager) activate(ctx context.Context, t *turn, conn *harnessConn, sess db.AgentSession, notice func(string)) (string, error) {
	sess, err := m.store.GetAgentSession(ctx, sess.ID)
	if err != nil {
		return "", err
	}
	if sess.AcpSessionID != "" && conn.isActive(sess.AcpSessionID) {
		return sess.AcpSessionID, nil
	}
	caps := conn.init.AgentCapabilities
	mcp := []acp.McpServer{}
	if m.tools != nil {
		if err := m.tools.Prepare(ctx, sess.AgentID, conn.ts); err != nil {
			notice("Could not install the agent's skills: " + err.Error())
		}
		servers, err := m.tools.MCPServers(ctx, sess.AgentID, sess.ID, caps.McpCapabilities)
		if err != nil {
			notice("MCP servers are not available in this session: " + err.Error())
		} else {
			mcp = servers
		}
	}
	if sess.AcpSessionID != "" {
		sid := acp.SessionId(sess.AcpSessionID)
		var err error
		var config []acp.SessionConfigOption
		switch {
		case caps.SessionCapabilities.Resume != nil:
			// Preferred: the log already has the conversation, and a load
			// would have the harness read and send all of it again (on a
			// woken agent, before the first prompt can start).
			var res acp.ResumeSessionResponse
			err = m.step(ctx, conn, "session/resume", func(ctx context.Context) error {
				var err error
				res, err = conn.acp.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: sid, Cwd: HomeDir, McpServers: mcp})
				return err
			})
			config = res.ConfigOptions
		case caps.LoadSession:
			// The replay is not stored again: the session is bound to this
			// connection only after it is active.
			var res acp.LoadSessionResponse
			err = m.step(ctx, conn, "session/load", func(ctx context.Context) error {
				var err error
				res, err = conn.acp.LoadSession(ctx, acp.LoadSessionRequest{SessionId: sid, Cwd: HomeDir, McpServers: mcp})
				return err
			})
			config = res.ConfigOptions
		default:
			err = errors.New("the harness cannot restore sessions")
		}
		if err == nil {
			m.observe(conn, sess.AcpSessionID, config, false)
			conn.markActive(sess.AcpSessionID)
			return sess.AcpSessionID, nil
		}
		if conn.closed() || ctx.Err() != nil {
			return "", err
		}
		// Failed or timed out: a fresh session, with a notice below.
		m.lg.Warn("restoring the session failed, starting a new one", "session", sess.ID, "err", err)
	}
	newSession := func() (acp.SessionId, error) {
		var res acp.NewSessionResponse
		err := m.step(ctx, conn, "session/new", func(ctx context.Context) error {
			var err error
			res, err = conn.acp.NewSession(ctx, acp.NewSessionRequest{Cwd: HomeDir, McpServers: mcp})
			return err
		})
		if err == nil {
			m.observe(conn, string(res.SessionId), res.ConfigOptions, true)
		}
		return res.SessionId, err
	}
	acpID, err := newSession()
	if err != nil && authRequired(err) {
		ok, aerr := m.autoAuthenticate(ctx, t, conn)
		if aerr != nil {
			return "", aerr
		}
		if ok {
			acpID, err = newSession()
		}
	}
	if err != nil {
		var st *stepTimeout
		switch {
		case errors.As(err, &st):
			return "", m.giveUp(t, conn, st.msg)
		case authRequired(err):
			return "", errLoginNeeded
		case conn.closed() || ctx.Err() != nil:
			return "", err
		}
		return "", fmt.Errorf("%s could not start a session: %w", conn.name, err)
	}
	if sess.AcpSessionID != "" {
		notice("The agent restarted and could not restore this conversation; it starts with a fresh context.")
	}
	if err := m.store.SetSessionACPID(context.WithoutCancel(ctx), db.SetSessionACPIDParams{AcpSessionID: string(acpID), ID: sess.ID}); err != nil {
		return "", err
	}
	conn.markActive(string(acpID))
	return string(acpID), nil
}

// autoAuthenticate tries the adapter's non-interactive login methods that
// the harness advertises. An error means the connection cannot be used.
func (m *Manager) autoAuthenticate(ctx context.Context, t *turn, conn *harnessConn) (bool, error) {
	advertised := map[string]bool{}
	for _, am := range conn.init.AuthMethods {
		if am.Agent != nil {
			advertised[am.Agent.Id] = true
		}
	}
	for _, id := range conn.adapter.AutoAuthenticate {
		if !advertised[id] {
			continue
		}
		err := m.step(ctx, conn, "authenticate", func(ctx context.Context) error {
			_, err := conn.acp.Authenticate(ctx, acp.AuthenticateRequest{MethodId: id})
			return err
		})
		if err == nil {
			return true, nil
		}
		var st *stepTimeout
		if errors.As(err, &st) {
			return false, m.giveUp(t, conn, st.msg)
		}
		if conn.closed() || ctx.Err() != nil {
			return false, err
		}
		m.lg.Warn("automatic authentication failed", "agent", conn.agentID, "method", id, "err", err)
	}
	return false, nil
}

// Ask prompts a session and waits for the turn to end. It returns the
// agent's final message (the text after its last tool call).
func (m *Manager) Ask(ctx context.Context, sessionID, userID, userName, text string, opts PromptOpts) (string, error) {
	seq, err := m.store.MaxSessionSeq(ctx, sessionID)
	if err != nil {
		return "", err
	}
	_, sub, err := m.log.subscribe(ctx, sessionID, seq, nil)
	if err != nil {
		return "", err
	}
	defer sub.Close()
	if err := m.PromptWith(ctx, sessionID, userID, userName, text, nil, opts); err != nil {
		return "", err
	}
	var answer strings.Builder
	for {
		select {
		case ev := <-sub.C:
			switch ev.Type {
			case EventUpdate:
				var u struct {
					Kind    string `json:"sessionUpdate"`
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				if json.Unmarshal(ev.Data, &u) != nil {
					continue
				}
				switch u.Kind {
				case "agent_message_chunk":
					answer.WriteString(u.Content.Text)
				case "tool_call":
					answer.Reset() // keep the final answer, not the narration before tools
				}
			case EventError:
				var e struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(ev.Data, &e)
				return answer.String(), errors.New(e.Message)
			case EventTurnEnd:
				var e struct {
					StopReason string `json:"stopReason"`
				}
				_ = json.Unmarshal(ev.Data, &e)
				if e.StopReason != "end_turn" && e.StopReason != "" {
					return answer.String(), fmt.Errorf("the turn ended: %s", e.StopReason)
				}
				return strings.TrimSpace(answer.String()), nil
			}
		case <-sub.Lost():
			return "", errors.New("lost the target session's events")
		case <-ctx.Done():
			_ = m.Cancel(context.WithoutCancel(ctx), sessionID)
			return "", ctx.Err()
		}
	}
}

// Cancel stops the running turn. Before the prompt reached the harness the
// turn simply ends; afterwards the harness is asked to stop (session/cancel),
// and if it has not ended the turn after cancelGrace, the turn ends without
// it (see cancelExpired).
func (m *Manager) Cancel(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	t, ok := m.turns[sessionID]
	if !ok || t.finished {
		m.mu.Unlock()
		return ErrNoTurn
	}
	again := t.cancelled
	t.cancelled = true
	t.cancelPermissions()
	conn, acpID, prompting := t.conn, t.acpID, t.prompting
	if prompting && !again {
		t.killTimer = time.AfterFunc(cancelGrace, func() { m.cancelExpired(t) })
	}
	m.mu.Unlock()
	if !prompting {
		t.stopSetup()
		return nil
	}
	return conn.acp.Cancel(ctx, acp.CancelNotification{SessionId: acp.SessionId(acpID)})
}

// cancelExpired ends a cancelled turn whose harness did not stop it. The
// harness is stopped if no other turn uses it, and restarted once none does
// otherwise.
func (m *Manager) cancelExpired(t *turn) {
	m.mu.Lock()
	if t.finished {
		m.mu.Unlock()
		return
	}
	conn := t.conn
	others := m.othersOn(conn, t)
	if others {
		conn.stale = true
		conn.staleWhy = conn.name + " did not stop a cancelled turn and was restarted once no conversation used it"
	}
	m.mu.Unlock()
	why := fmt.Sprintf("%s did not stop within %s after the turn was cancelled", conn.name, cancelGrace)
	if others {
		t.stopPrompt(&stoppedError{msg: why + "; the turn was ended, and the harness restarts once the agent's other conversations are done"})
		return
	}
	conn.close(why + ", so it was stopped")
}

// cancelPermissions answers pending permission requests with "cancelled"
// (Manager.mu held).
func (t *turn) cancelPermissions() {
	for id, ch := range t.perms {
		close(ch)
		delete(t.perms, id)
	}
}

// AnswerPermission resolves a pending permission request.
func (m *Manager) AnswerPermission(ctx context.Context, sessionID, requestID, optionID, userID string) error {
	m.mu.Lock()
	t, ok := m.turns[sessionID]
	var ch chan acp.PermissionOptionId
	if ok {
		ch = t.perms[requestID]
		delete(t.perms, requestID)
	}
	m.mu.Unlock()
	if ch == nil {
		return ErrUnknownPermission
	}
	ch <- acp.PermissionOptionId(optionID)
	close(ch)
	err := m.log.append(ctx, sessionID, EventPermissionResolved, userID, map[string]string{"requestId": requestID, "optionId": optionID, "by": userID})
	return err
}

// Delete removes a session and its log.
func (m *Manager) Delete(ctx context.Context, sessionID string) error {
	if m.Running(sessionID) {
		return ErrBusy
	}
	return m.store.DeleteAgentSession(ctx, sessionID)
}

// HarnessInfo is what the UI shows about an agent's harness.
type HarnessInfo struct {
	Connected   bool
	AgentName   string
	AgentVer    string
	LoadSession bool
	AuthMethods []acp.AuthMethod
}

// Harness returns the agent's harness state, connecting if connect is set.
func (m *Manager) Harness(ctx context.Context, agentID string, connect bool) (HarnessInfo, error) {
	m.mu.Lock()
	c := m.conns[agentID]
	m.mu.Unlock()
	if c == nil && connect {
		var err error
		if c, err = m.connection(ctx, agentID, nil); err != nil {
			return HarnessInfo{}, err
		}
	}
	if c == nil {
		return HarnessInfo{}, nil
	}
	info := HarnessInfo{Connected: true, LoadSession: c.init.AgentCapabilities.LoadSession, AuthMethods: c.init.AuthMethods}
	if c.init.AgentInfo != nil {
		info.AgentName, info.AgentVer = c.init.AgentInfo.Name, c.init.AgentInfo.Version
	}
	return info, nil
}

// Disconnect stops an agent's harness (after settings changes, or when the sandbox sleeps).
func (m *Manager) Disconnect(agentID string) {
	m.mu.Lock()
	c := m.conns[agentID]
	m.mu.Unlock()
	if c != nil {
		c.close("the harness was stopped because the agent's sandbox is stopping or its settings changed")
	}
}

// connection returns the agent's live harness connection, starting it if needed.
func (m *Manager) connection(ctx context.Context, agentID string, notice func(string)) (*harnessConn, error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, errShuttingDown
	}
	if c := m.conns[agentID]; c != nil && !c.closed() && !m.restartable(c) {
		m.mu.Unlock()
		return c, nil
	}
	lock := m.starting[agentID]
	if lock == nil {
		lock = make(chan struct{}, 1)
		m.starting[agentID] = lock
	}
	m.mu.Unlock()
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-lock }()
	m.mu.Lock()
	var stale *harnessConn
	why := ""
	if c := m.conns[agentID]; c != nil && !c.closed() {
		if !m.restartable(c) {
			m.mu.Unlock()
			return c, nil
		}
		stale, why = c, c.staleWhy
	}
	m.mu.Unlock()
	if stale != nil {
		stale.close(why)
	}

	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent.Kind == "app" {
		return nil, ErrNoHarness
	}
	ad, ok := harness.Get(agent.Harness)
	if !ok {
		return nil, fmt.Errorf("unknown harness %q", agent.Harness)
	}
	status := func(msg string) {
		if notice != nil {
			notice(msg)
		}
	}
	wctx, wcancel := context.WithTimeout(ctx, wakeTimeout)
	ts, err := m.wake(wctx, agentID, status)
	wcancel()
	if err != nil {
		if ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("the agent's sandbox did not start within %s: %w", wakeTimeout, err)
		}
		return nil, err
	}
	// The default model goes into the environment only while the harness is
	// not known to switch models through its session config options: then
	// sessions start on the harness's own default and the turn applies it.
	cfg := harness.Config{Model: agent.Model}
	if c, ok, _ := m.Catalog(ctx, agentID); ok && c.Harness == ad.ID {
		if _, switches := c.ModelOption(); switches {
			cfg.Model = ""
		}
	}
	env := ad.Environment(cfg)
	if m.logins != nil {
		lctx, lcancel := context.WithTimeout(ctx, loginsTimeout)
		extra, err := m.logins.Prepare(lctx, agent, ts)
		lcancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			m.lg.Warn("account login unavailable", "agent", agentID, "err", err)
			status("The account login could not be prepared: " + err.Error())
		}
		for k, v := range extra {
			env[k] = v
		}
	}

	hc := newHarnessConn(m, agentID, ad, ts)
	hc.startModel = cfg.Model
	proc, err := m.open(ctx, hc, ts, ad.Command, env)
	if err != nil {
		return nil, err
	}
	hc.start(proc)
	var init acp.InitializeResponse
	err = m.step(ctx, hc, "initialize", func(ctx context.Context) error {
		var err error
		init, err = hc.acp.Initialize(ctx, acp.InitializeRequest{
			ProtocolVersion:    acp.ProtocolVersionNumber,
			ClientInfo:         &acp.Implementation{Name: "agenthold", Version: version.Version},
			ClientCapabilities: acp.ClientCapabilities{Auth: acp.AuthCapabilities{Terminal: true}},
		})
		return err
	})
	if err != nil {
		var se *stoppedError
		var st *stepTimeout
		var re *acp.RequestError
		switch {
		case errors.As(err, &se):
			return nil, err // the harness ended; step waited for how
		case ctx.Err() != nil:
			hc.close("the turn was cancelled while the harness started")
			return nil, ctx.Err()
		case errors.As(err, &st):
			return nil, m.giveUp(nil, hc, st.msg)
		case errors.As(err, &re):
			hc.close(fmt.Sprintf("%s refused initialize: %v", ad.Name, err))
		default:
			hc.close(fmt.Sprintf("%s did not start: %v", ad.Name, err))
		}
		return nil, hc.failure()
	}
	hc.init = init
	m.mu.Lock()
	if m.closing || hc.closed() {
		m.mu.Unlock()
		hc.close("the server shut down while the harness started")
		return nil, hc.failure()
	}
	m.conns[agentID] = hc
	m.mu.Unlock()
	m.lg.Info("harness connected", "agent", agentID, "harness", ad.ID)
	return hc, nil
}

// open opens the harness stream. The stream lives as long as the harness,
// so it is opened with a context of its own, but ctx and openTimeout bound
// the opening (a remote node that hangs must not hold the agent's startup
// lock forever).
func (m *Manager) open(ctx context.Context, hc *harnessConn, ts *tunnel.Session, cmd []string, env map[string]string) (*tunnel.Process, error) {
	sctx, scancel := context.WithCancel(m.ctx)
	octx, ocancel := context.WithTimeout(ctx, openTimeout)
	defer ocancel()
	detach := context.AfterFunc(octx, scancel)
	proc, err := ts.StartHarness(sctx, cmd, env, func(line string) {
		m.lg.Debug("harness stderr", "agent", hc.agentID, "line", line)
		hc.stderr(line)
	})
	detached := detach()
	if err == nil && !detached {
		_ = proc.Close() // opened just as the deadline or the cancel hit
		err = octx.Err()
	}
	if err != nil {
		scancel()
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(octx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("could not start %s: the sandbox did not open the harness within %s", hc.name, openTimeout)
		}
		return nil, fmt.Errorf("could not start %s: %w", hc.name, err)
	}
	hc.stopStream = scancel
	return proc, nil
}

// restartable reports whether c is stale and no turn uses it (m.mu held).
func (m *Manager) restartable(c *harnessConn) bool {
	return c.stale && !m.inUse(c)
}

func (m *Manager) dropConn(hc *harnessConn) {
	m.mu.Lock()
	if m.conns[hc.agentID] == hc {
		delete(m.conns, hc.agentID)
	}
	m.mu.Unlock()
}
