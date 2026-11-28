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

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Errors.
var (
	ErrBusy              = errors.New("a turn is already running in this session")
	ErrNotFound          = errors.New("session not found")
	ErrEventNotFound     = errors.New("event not found")
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

// connKey identifies one live harness process inside an agent's sandbox. An
// agent can run more than one harness at once when a thread starts a sub-thread
// on another harness (for example Claude Code starting a Codex sub-thread in
// the same worktree).
type connKey struct {
	agentID string
	harness string
}

// Manager runs harness connections and sessions.
//
// One harness process (a harnessConn) serves all sessions of an agent on that
// harness. A failure of the process ends every turn on it; a harness that stops
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
	closing bool                     // Close was called: no new turns or connections
	conns   map[connKey]*harnessConn // by agent + harness id
	turns   map[string]*turn         // by session id
	// starting serializes harness startup per agent+harness (a size-1 channel,
	// so a waiting turn can be cancelled).
	starting map[connKey]chan struct{}
	warming  map[string]bool
	activity func(agentID string)
	audit    *audit.Log
	tools    Tools
	// workspaces prepares where project threads work; nil: they work where
	// their cwd says.
	workspaces Workspaces
	logins     Logins
	resolver   Resolver
	catMu      sync.Mutex // serializes model catalog updates
	qmu        sync.Mutex // serializes queue enqueue/pop transitions
	probe      bool       // describe unknown models after turns (SetModelProbe)
}

// Resolver resolves built-in and user-configured ACP Store / local ACP
// harnesses, installing Registry agents into the sandbox on demand.
type Resolver interface {
	AdapterFor(ctx context.Context, harnessID string) (*harness.Adapter, bool, error)
	Prepare(ctx context.Context, ad *harness.Adapter, ts *tunnel.Session, home string, cfg harness.Config, status func(string)) ([]string, map[string]string, error)
}

// SetResolver sets the custom harness resolver (ACP Store + local ACP commands).
func (m *Manager) SetResolver(r Resolver) { m.resolver = r }

// Logins supplies the account login of an agent's harness: files are written
// into the sandbox, the returned environment is added to the harness.
type Logins interface {
	Prepare(ctx context.Context, agent db.Agent, ts *tunnel.Session) (map[string]string, error)
}

// SetLogins sets the account logins provider.
func (m *Manager) SetLogins(l Logins) { m.logins = l }

// Restart stops the agent's harnesses so the next turn starts them with current
// settings (an account login changed). Running turns finish first.
func (m *Manager) Restart(agentID string) {
	m.mu.Lock()
	var idle []*harnessConn
	for k, c := range m.conns {
		if k.agentID != agentID {
			continue
		}
		if m.inUse(c) {
			c.stale, c.staleWhy = true, "the harness restarted to apply new settings"
		} else {
			idle = append(idle, c)
		}
	}
	m.mu.Unlock()
	for _, c := range idle {
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
	conn           *harnessConn
	acpID          string
	permissionMode string
	prompting      bool // session/prompt was sent
	cancelled      bool
	steering       bool // cancelled to immediately run a steered queued message
	finished       bool // run returned; nothing may act on the turn anymore
	// touchedPath is the last file a tool call of the turn reported, or the
	// last directory a shell command of it ran in (touchedDir).
	touchedPath string
	touchedDir  bool
	killTimer   *time.Timer                            // armed by Cancel
	perms       map[string]chan acp.PermissionOptionId // pending permission requests
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
		conns: map[connKey]*harnessConn{}, turns: map[string]*turn{}, starting: map[connKey]chan struct{}{},
		warming: map[string]bool{},
	}
	m.endInterruptedTurns(ctx)
	go m.closeIdleSessionsLoop()
	return m
}

// sessionIdleClose is how long an ACP session may go without a turn before
// it is closed in its harness (session/close, where the harness offers it
// and can restore sessions); the next prompt resumes it. Claude Code keeps
// a process of about 220 MiB per session, which also works for about a
// minute after it starts: without this, every chat an agent ever had stays
// in its machine's memory until the harness restarts.
var sessionIdleClose = 10 * time.Minute

// idleCheckEvery is how often idle sessions are looked for.
var idleCheckEvery = time.Minute

func (m *Manager) closeIdleSessionsLoop() {
	t := time.NewTicker(idleCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.closeIdleSessions(time.Now())
		}
	}
}

// closeIdleSessions closes the sessions without a turn since sessionIdleClose
// before now.
func (m *Manager) closeIdleSessions(now time.Time) {
	type closing struct {
		c    *harnessConn
		s    idleSession
		done chan struct{}
	}
	var todo []closing
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return
	}
	for _, c := range m.conns {
		caps := c.init.AgentCapabilities
		if c.closed() || caps.SessionCapabilities.Close == nil || (caps.SessionCapabilities.Resume == nil && !caps.LoadSession) {
			continue
		}
		for _, s := range c.idleSessions(now.Add(-sessionIdleClose)) {
			if _, busy := m.turns[s.sessionID]; busy {
				continue
			}
			// Under m.mu: a turn that starts after this waits for the close.
			todo = append(todo, closing{c, s, c.beginClose(s.acpID)})
		}
	}
	m.mu.Unlock()
	for _, x := range todo {
		ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
		err := x.c.proto.CloseSession(ctx, x.s.acpID)
		cancel()
		x.c.endClose(x.s.acpID, x.done)
		if err != nil {
			m.lg.Warn("harness: closing an idle session failed", "agent", x.c.agentID, "session", x.s.sessionID, "err", err)
		} else {
			m.lg.Info("harness: closed an idle session; the next message resumes it", "agent", x.c.agentID, "session", x.s.sessionID, "idleFor", sessionIdleClose)
		}
	}
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

// Observe calls fn with every event appended to any session's log, after it
// is stored, in order per session. fn runs while the log is locked: it must
// return at once and never call back into the Manager (hand the event to a
// goroutine of its own).
func (m *Manager) Observe(fn func(sessionID string, ev Event)) {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()
	m.log.observers = append(m.log.observers, fn)
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

// Workspaces prepares the working directories of project threads.
type Workspaces interface {
	// Prepare makes sure the workspace of sess exists on the machine and
	// returns its working directory, relative to the home.
	Prepare(ctx context.Context, ts *tunnel.Session, sess db.AgentSession) (string, error)
}

// SetWorkspaces sets how project threads' workspaces are prepared.
func (m *Manager) SetWorkspaces(w Workspaces) { m.workspaces = w }

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

// PlaceholderTitle is the title of a new thread until its first message
// names it.
const PlaceholderTitle = "New thread"

// IsPlaceholderTitle reports whether t is a placeholder title, including the
// one threads got before they were called threads.
func IsPlaceholderTitle(t string) bool { return t == PlaceholderTitle || t == "New session" }

// Create starts a new, empty session.
func (m *Manager) Create(ctx context.Context, agentID, userID, title string) (db.AgentSession, error) {
	return m.CreateWith(ctx, agentID, userID, title, CreateOpts{})
}

// CreateOpts are optional settings of a new session.
type CreateOpts struct {
	// Cwd is the harness's working directory, relative to the home ('' the
	// home itself).
	Cwd string
	// GitProject, GitBranch and GitRepoPath describe the repository Cwd is in.
	GitProject, GitBranch, GitRepoPath string
	// Harness pins this session to a specific harness (e.g. "codex" for a
	// sub-thread on a Claude Code agent); '' uses the agent's default harness.
	Harness string
	// PermissionMode sets the per-thread permission mode ('' defaults to full-access).
	PermissionMode string
	// ParentKind records how this session relates to its origin session
	// ('subthread', 'side_chat', 'a2a', or '').
	ParentKind string
}

// CreateWith is Create with options.
func (m *Manager) CreateWith(ctx context.Context, agentID, userID, title string, opts CreateOpts) (db.AgentSession, error) {
	if ag, err := m.store.GetAgent(ctx, agentID); err == nil && ag.Kind == "app" {
		return db.AgentSession{}, ErrNoHarness
	}
	now := time.Now().Unix()
	permMode := NormalizePermissionMode(opts.PermissionMode)
	s := db.AgentSession{
		ID: id.New("ses"), AgentID: agentID, Title: title, Status: "idle",
		CreatedBy: userID, CreatedAt: now, UpdatedAt: now,
		PermissionMode: permMode, ParentKind: opts.ParentKind,
	}
	if s.Title == "" {
		s.Title = PlaceholderTitle
	}
	if err := m.store.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: s.ID, AgentID: agentID, Title: s.Title, CreatedBy: userID, CreatedAt: now, UpdatedAt: now}); err != nil {
		return s, err
	}
	if opts.Cwd != "" || opts.GitProject != "" || opts.GitBranch != "" || opts.GitRepoPath != "" {
		if err := m.store.SetSessionGit(ctx, db.SetSessionGitParams{
			Cwd: opts.Cwd, GitProject: opts.GitProject, GitBranch: opts.GitBranch, GitRepoPath: opts.GitRepoPath, ID: s.ID,
		}); err != nil {
			return s, err
		}
		s.Cwd, s.GitProject, s.GitBranch, s.GitRepoPath = opts.Cwd, opts.GitProject, opts.GitBranch, opts.GitRepoPath
	}
	if opts.Harness != "" {
		if err := m.store.SetSessionHarness(ctx, db.SetSessionHarnessParams{Harness: opts.Harness, ID: s.ID}); err != nil {
			return s, err
		}
		s.Harness = opts.Harness
	}
	if permMode != PermissionFullAccess {
		if err := m.store.SetSessionPermissionMode(ctx, db.SetSessionPermissionModeParams{
			PermissionMode: permMode, UpdatedAt: now, ID: s.ID,
		}); err != nil {
			return s, err
		}
	}
	if opts.ParentKind != "" {
		if err := m.store.SetSessionParentKind(ctx, db.SetSessionParentKindParams{
			ParentKind: opts.ParentKind, ID: s.ID,
		}); err != nil {
			return s, err
		}
	}
	return s, nil
}

// RecordSubthread appends a subthread status event to parentSessionID's log so
// viewers see the sub-thread immediately and can jump into it while it runs.
func (m *Manager) RecordSubthread(ctx context.Context, parentSessionID string, data map[string]any) error {
	if parentSessionID == "" {
		return nil
	}
	return m.log.append(ctx, parentSessionID, EventSubthread, "", data)
}

// Subscribe returns events after `after` and a live subscription. A non-nil
// viewer is shown to everyone else watching the session.
func (m *Manager) Subscribe(ctx context.Context, sessionID string, after int64, viewer *Viewer) ([]Event, *Subscription, error) {
	return m.log.subscribe(ctx, sessionID, after, viewer)
}

// Busy reports whether a turn of the agent is in progress.
func (m *Manager) Busy(agentID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.turns {
		if t.agentID == agentID {
			return true
		}
	}
	return false
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
	// Images are attached to the prompt (validated by the caller).
	Images []PromptImage
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

// SessionDepth is the depth of sessionID's running turn, falling back to
// AgentDepth when sessionID is empty or not currently running.
func (m *Manager) SessionDepth(agentID, sessionID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sessionID != "" {
		if t, ok := m.turns[sessionID]; ok {
			return t.depth
		}
	}
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
		permissionMode: NormalizePermissionMode(sess.PermissionMode),
		setup:          setup, stopSetup: stopSetup, prompt: prompt, stopPrompt: stopPrompt,
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
	if len(opts.Images) > 0 {
		promptData["images"] = opts.Images
	}
	if opts.Origin != nil {
		promptData["origin"] = opts.Origin
		promptData["depth"] = opts.Depth
	}
	promptSeq, err := m.log.appendSeq(lctx, sessionID, EventPrompt, userID, promptData)
	if err != nil {
		release()
		m.wg.Done()
		return err
	}
	// A new turn is activity: it unsettles a settled session.
	if err := m.store.MarkSessionRunning(lctx, db.MarkSessionRunningParams{UpdatedAt: time.Now().Unix(), ID: sessionID}); err != nil {
		_ = m.log.append(lctx, sessionID, EventError, "", map[string]string{"message": "Could not start the turn: " + err.Error()})
		release()
		_ = m.log.append(lctx, sessionID, EventTurnEnd, "", map[string]any{"stopReason": "error"})
		m.wg.Done()
		return err
	}
	if sess.SettledAt.Valid {
		m.recordUnsettledByActivity(lctx, sess, userID, opts)
	}
	if IsPlaceholderTitle(sess.Title) {
		if err := m.store.RenameAgentSessionTitle(lctx, sessionID, titleFrom(text)); err != nil {
			m.lg.Warn("title session", "session", sessionID, "err", err)
		}
	}

	go func() {
		defer m.wg.Done()
		end, err := m.run(t, sess, text, opts.Images, promptSeq, status)
		m.mu.Lock()
		t.finished = true
		if t.killTimer != nil {
			t.killTimer.Stop()
		}
		t.cancelPermissions()
		cancelled := t.cancelled
		steering := t.steering
		touched := touchedPlace{path: t.touchedPath, dir: t.touchedDir}
		m.mu.Unlock()
		if c := t.conn; c != nil && !c.closed() {
			m.refreshSessionGit(lctx, c, sess, touched)
		}
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
			// Turn errors auto-wake a snoozed thread so the user sees the failure.
			_, _ = m.store.UnsnoozeSession(lctx, sessionID)
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
		// Auto-drain the persistent follow-up queue after normal completion, or
		// immediately after a steer-initiated interruption. A manual Stop/cancel
		// (cancelled && !steering) pauses the queue until the user sends or resumes.
		if (err == nil && !cancelled) || (cancelled && steering) {
			m.drainQueue(lctx, sessionID)
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
func (m *Manager) run(t *turn, sess db.AgentSession, text string, images []PromptImage, promptSeq int64, status func(string)) (map[string]any, error) {
	ctx := t.setup
	notice := func(msg string) {
		_ = m.log.append(context.WithoutCancel(ctx), sess.ID, EventNotice, "", map[string]string{"message": msg})
		if status != nil {
			status(msg)
		}
	}
	conn, err := m.connectionFor(ctx, sess.AgentID, sess.Harness, notice)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	t.conn = conn
	m.mu.Unlock()
	acpID, inlineCtx, handoffToSeq, err := m.activate(ctx, t, conn, sess, text, promptSeq, notice)
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
	m.lg.Info("harness: prompt sent", "agent", sess.AgentID, "session", sess.ID)
	conn.promptAt.Store(time.Now().UnixNano())

	promptText := WithInlineContext(inlineCtx, text)
	if NormalizePermissionMode(sess.PermissionMode) == PermissionPlan && !IsSlashCommand(text) && conn.adapter.EffectiveProtocol() != harness.ProtocolClaude {
		promptText = planModeInstruction + promptText
	}
	// Not the setup context: a cancel is session/cancel, answered by the
	// harness with stopReason "cancelled".
	res, err := conn.proto.Prompt(t.prompt, acpID, promptText, images)
	conn.used(acpID)
	if err == nil && handoffToSeq > 0 {
		_ = m.store.SetSessionHandoffSeq(context.WithoutCancel(ctx), db.SetSessionHandoffSeqParams{
			HandoffSeq: handoffToSeq,
			ID:         sess.ID,
		})
	}
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
			if err != nil {
				m.lg.Info("harness: "+method+" failed", "agent", conn.agentID, "took", time.Since(start), "err", err)
			} else {
				m.lg.Info("harness: "+method+" answered", "agent", conn.agentID, "took", time.Since(start))
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
			m.lg.Warn("harness: "+method+" timed out", "agent", conn.agentID, "took", total, "idle", idle)
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
// When the agent switched harnesses or restore fails on a thread with prior
// history, it performs T3-style budgeted Context Handoff (natively via Codex
// `thread/inject_items` when supported, or inline with slash-command deferral).
func (m *Manager) activate(ctx context.Context, t *turn, conn *harnessConn, sess db.AgentSession, userText string, promptSeq int64, notice func(string)) (string, string, int64, error) {
	sess, err := m.store.GetAgentSession(ctx, sess.ID)
	if err != nil {
		return "", "", 0, err
	}
	if err := conn.waitClosed(ctx, sess.AcpSessionID); err != nil {
		return "", "", 0, err
	}

	harnessSwitched := sess.AcpSessionID != "" && sess.AcpHarness != "" && sess.AcpHarness != conn.adapter.ID
	if !harnessSwitched && sess.AcpSessionID != "" && conn.isActive(sess.AcpSessionID) {
		// If a previous turn deferred inline handoff because of a slash command,
		// deliver the pending handoff now if this turn is not also a slash command.
		if sess.HandoffSeq < 0 && !IsSlashCommand(userText) {
			if sess.ParentKind == ParentKindSideChat && sess.OriginSessionID != "" {
				inlineCtx, toSeq := m.deliverSideChatHandoff(ctx, conn, sess, sess.AcpSessionID, userText, notice)
				return sess.AcpSessionID, inlineCtx, toSeq, nil
			}
			inlineCtx, toSeq := m.deliverHandoff(ctx, conn, sess, sess.AcpSessionID, userText, promptSeq, sess.AcpHarness, notice)
			return sess.AcpSessionID, inlineCtx, toSeq, nil
		}
		return sess.AcpSessionID, "", 0, nil
	}

	// Sub-threads share their parent's worktree (cwd) directly and never
	// create a new project workspace of their own.
	if sess.ProjectID != "" && m.workspaces != nil && (sess.OriginSessionID == "" || sess.Cwd == "") {
		dir, err := m.workspaces.Prepare(ctx, conn.ts, sess)
		if err != nil {
			return "", "", 0, fmt.Errorf("prepare the project workspace: %w", err)
		}
		if dir != sess.Cwd {
			if err := m.store.SetSessionGit(ctx, db.SetSessionGitParams{Cwd: dir, ID: sess.ID}); err != nil {
				return "", "", 0, err
			}
			sess.Cwd, sess.GitProject, sess.GitBranch, sess.GitRepoPath = dir, "", "", ""
		}
	}
	cwd := sessionCwd(conn.home, sess.Cwd)
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

	// Only attempt native resume/load when the saved session belongs to this harness.
	if !harnessSwitched && sess.AcpSessionID != "" {
		sid := sess.AcpSessionID
		resume := func(ctx context.Context) ([]ConfigOption, error) {
			return conn.proto.ResumeSession(ctx, sid, cwd, mcp)
		}
		load := func(ctx context.Context) ([]ConfigOption, error) {
			return conn.proto.LoadSession(ctx, sid, cwd, mcp)
		}
		type restore struct {
			method string
			fn     func(context.Context) ([]ConfigOption, error)
		}
		var ways []restore
		if caps.SessionCapabilities.Resume != nil {
			ways = append(ways, restore{"session/resume", resume})
		}
		if caps.LoadSession {
			ways = append(ways, restore{"session/load", load})
		}
		err := errors.New("the harness cannot restore sessions")
		for i, w := range ways {
			var config []ConfigOption
			err = m.step(ctx, conn, w.method, func(ctx context.Context) error {
				var err error
				config, err = w.fn(ctx)
				return err
			})
			if err == nil {
				m.observeOptions(conn, sess.AcpSessionID, config, nil, false)
				conn.markActive(sess.AcpSessionID)
				if sess.AcpHarness == "" {
					_ = m.store.SetSessionNative(context.WithoutCancel(ctx), db.SetSessionNativeParams{
						AcpSessionID: sess.AcpSessionID,
						AcpHarness:   conn.adapter.ID,
						ID:           sess.ID,
					})
				}
				return sess.AcpSessionID, "", 0, nil
			}
			if conn.closed() || ctx.Err() != nil {
				return "", "", 0, err
			}
			if i < len(ways)-1 {
				m.lg.Warn("restoring the session failed, trying another way", "session", sess.ID, "method", w.method, "err", err)
			}
		}
		// Failed or timed out: start a fresh session and hand off context below.
		m.lg.Warn("restoring the session failed, starting a new one with context handoff", "session", sess.ID, "err", err)
	}

	if conn.adapter.AuthenticateEagerly {
		if _, aerr := m.autoAuthenticate(ctx, t, conn); aerr != nil {
			return "", "", 0, aerr
		}
	}

	newSession := func() (string, error) {
		var sid string
		var opts []ConfigOption
		err := m.step(ctx, conn, "session/new", func(ctx context.Context) error {
			var err error
			sid, opts, err = conn.proto.NewSession(ctx, cwd, mcp)
			return err
		})
		if err == nil {
			m.observeOptions(conn, sid, opts, nil, true)
		}
		return sid, err
	}
	acpID, err := newSession()
	if err != nil && authRequired(err) {
		ok, aerr := m.autoAuthenticate(ctx, t, conn)
		if aerr != nil {
			return "", "", 0, aerr
		}
		if ok {
			acpID, err = newSession()
		}
	}
	if err != nil {
		var st *stepTimeout
		switch {
		case errors.As(err, &st):
			return "", "", 0, m.giveUp(t, conn, st.msg)
		case authRequired(err):
			return "", "", 0, errLoginNeeded
		case conn.closed() || ctx.Err() != nil:
			return "", "", 0, err
		}
		return "", "", 0, fmt.Errorf("%s could not start a session: %w", conn.name, err)
	}

	prevHarness := sess.AcpHarness
	hadPriorSession := sess.AcpSessionID != ""
	if err := m.store.SetSessionNative(context.WithoutCancel(ctx), db.SetSessionNativeParams{
		AcpSessionID: acpID,
		AcpHarness:   conn.adapter.ID,
		ID:           sess.ID,
	}); err != nil {
		return "", "", 0, err
	}
	conn.markActive(acpID)

	if hadPriorSession || sess.HandoffSeq < 0 {
		inlineCtx, toSeq := m.deliverHandoff(ctx, conn, sess, acpID, userText, promptSeq, prevHarness, notice)
		return acpID, inlineCtx, toSeq, nil
	}
	if sess.ParentKind == ParentKindSideChat && sess.OriginSessionID != "" && sess.HandoffSeq == 0 {
		inlineCtx, toSeq := m.deliverSideChatHandoff(ctx, conn, sess, acpID, userText, notice)
		return acpID, inlineCtx, toSeq, nil
	}
	return acpID, "", 0, nil
}

// deliverHandoff builds a budgeted portable handoff from stored thread events
// before promptSeq, attempts native injection (Codex thread/inject_items), and
// falls back to inline context (with slash-command deferral).
func (m *Manager) deliverHandoff(ctx context.Context, conn *harnessConn, sess db.AgentSession, acpID, userText string, promptSeq int64, prevHarness string, notice func(string)) (string, int64) {
	msgs, err := ReconstructHistory(ctx, m.store, sess.ID, 0, promptSeq)
	if err != nil {
		m.lg.Warn("handoff: reconstruct history failed", "session", sess.ID, "err", err)
		notice("The agent restarted and could not restore this conversation; it starts with a fresh context.")
		return "", 0
	}
	if len(msgs) == 0 {
		if prevHarness != "" && prevHarness != conn.adapter.ID {
			notice(fmt.Sprintf("Switched harness to %s.", conn.name))
		} else {
			notice("The agent restarted and could not restore this conversation; it starts with a fresh context.")
		}
		return "", 0
	}

	budget := HandoffBudget(HandoffTokenCap(), userText, DefaultContextWindow)
	sel := SelectHistory(sess.ID, prevHarness, conn.adapter.ID, msgs, budget)
	if len(sel.Messages) == 0 {
		notice("The conversation history exceeded the handoff budget; starting with a fresh context (earlier messages are retrievable with read_thread).")
		return "", 0
	}

	// Try native historical-item injection first (e.g. Codex thread/inject_items).
	injected, ierr := conn.proto.InjectHistory(ctx, acpID, sess.ID, sel)
	if ierr != nil {
		m.lg.Warn("handoff: native inject failed, falling back to inline", "session", sess.ID, "err", ierr)
	}
	if injected {
		_ = m.store.SetSessionHandoffSeq(context.WithoutCancel(ctx), db.SetSessionHandoffSeqParams{
			HandoffSeq: sel.CoveredToSeq,
			ID:         sess.ID,
		})
		notice(formatHandoffNotice(prevHarness, conn.name, sel, true))
		return "", 0
	}

	// Defer inline handoff when the user's message is a slash command so the
	// harness still executes the command natively (mark handoff_seq = -1).
	if IsSlashCommand(userText) {
		_ = m.store.SetSessionHandoffSeq(context.WithoutCancel(ctx), db.SetSessionHandoffSeqParams{
			HandoffSeq: -1,
			ID:         sess.ID,
		})
		return "", 0
	}

	notice(formatHandoffNotice(prevHarness, conn.name, sel, false))
	return RenderHistory(sess.ID, sel), sel.CoveredToSeq
}

func formatHandoffNotice(fromHarness, toName string, sel HandoffSelection, native bool) string {
	omitted := ""
	if sel.OmittedItems > 0 {
		omitted = fmt.Sprintf(" (%d omitted; retrievable with read_thread)", sel.OmittedItems)
	}
	mode := "context handoff"
	if native {
		mode = "native context handoff"
	}
	if fromHarness != "" && fromHarness != toName {
		fromLabel := fromHarness
		if ad, ok := harness.Get(fromHarness); ok {
			fromLabel = ad.Name
		}
		if fromLabel != toName {
			return fmt.Sprintf("Switched from %s to %s with %s: transferred %d conversation items%s.", fromLabel, toName, mode, len(sel.Messages), omitted)
		}
	}
	return fmt.Sprintf("Restored conversation on %s via %s: transferred %d conversation items%s.", toName, mode, len(sel.Messages), omitted)
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
			return conn.proto.Authenticate(ctx, id)
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

// WaitTurn waits for sessionID's current turn (if any) to end and returns the
// final answer of its latest turn. If the session is already idle, it returns
// the final answer of the most recent completed turn immediately.
func (m *Manager) WaitTurn(ctx context.Context, sessionID string) (string, error) {
	backlog, sub, err := m.log.subscribe(ctx, sessionID, 0, nil)
	if err != nil {
		return "", err
	}
	defer sub.Close()
	var answer strings.Builder
	var lastErr string
	lastPromptSeq := int64(0)
	lastEndSeq := int64(0)
	var lastStop string
	fold := func(ev Event) {
		switch ev.Type {
		case EventPrompt:
			lastPromptSeq = ev.Seq
			answer.Reset()
			lastErr = ""
			lastStop = ""
		case EventUpdate:
			var u struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(ev.Data, &u) != nil {
				return
			}
			switch u.Kind {
			case "agent_message_chunk":
				answer.WriteString(u.Content.Text)
			case "tool_call":
				answer.Reset()
			}
		case EventError:
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(ev.Data, &e)
			lastErr = e.Message
		case EventTurnEnd:
			var e struct {
				StopReason string `json:"stopReason"`
			}
			_ = json.Unmarshal(ev.Data, &e)
			lastEndSeq = ev.Seq
			lastStop = e.StopReason
		}
	}
	for _, ev := range backlog {
		fold(ev)
	}
	done := func() (string, bool, error) {
		if lastPromptSeq == 0 {
			return "", true, nil
		}
		if lastEndSeq > lastPromptSeq {
			if lastErr != "" {
				return strings.TrimSpace(answer.String()), true, errors.New(lastErr)
			}
			if lastStop != "end_turn" && lastStop != "" {
				return strings.TrimSpace(answer.String()), true, fmt.Errorf("the turn ended: %s", lastStop)
			}
			return strings.TrimSpace(answer.String()), true, nil
		}
		return "", false, nil
	}
	if ans, ok, err := done(); ok || !m.Running(sessionID) {
		if !ok && lastErr != "" {
			return strings.TrimSpace(answer.String()), errors.New(lastErr)
		}
		return ans, err
	}
	for {
		select {
		case ev := <-sub.C:
			fold(ev)
			if ans, ok, err := done(); ok {
				return ans, err
			}
		case <-sub.Lost():
			return "", errors.New("lost the sub-thread's events")
		case <-ctx.Done():
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
	return conn.proto.Cancel(ctx, acpID)
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

// Harness returns the agent's primary harness state, connecting if connect is set.
func (m *Manager) Harness(ctx context.Context, agentID string, connect bool) (HarnessInfo, error) {
	ag, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return HarnessInfo{}, err
	}
	m.mu.Lock()
	c := m.conns[connKey{agentID: agentID, harness: ag.Harness}]
	m.mu.Unlock()
	if c == nil && connect {
		if c, err = m.connectionFor(ctx, agentID, ag.Harness, nil); err != nil {
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

// Disconnect stops all of an agent's harnesses (after settings changes, or when the sandbox sleeps).
func (m *Manager) Disconnect(agentID string) {
	m.mu.Lock()
	var cs []*harnessConn
	for k, c := range m.conns {
		if k.agentID == agentID {
			cs = append(cs, c)
		}
	}
	m.mu.Unlock()
	for _, c := range cs {
		c.close("the harness was stopped because the agent's sandbox is stopping or its settings changed")
	}
}

// warmTimeout bounds a background harness pre-warm.
var warmTimeout = time.Minute

// Warm starts an already-running agent's harness connection in the background
// so the process and ACP initialize (and any background binary warm-up) are
// already done before the first prompt arrives. It never wakes a sleeping
// sandbox and does not count as user activity.
func (m *Manager) Warm(agentID string) {
	m.mu.Lock()
	if m.closing || m.wake == nil || m.warming[agentID] {
		m.mu.Unlock()
		return
	}
	m.warming[agentID] = true
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.warming, agentID)
			m.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(m.ctx, warmTimeout)
		defer cancel()
		ag, err := m.store.GetAgent(ctx, agentID)
		if err != nil || ag.Kind == "app" {
			return
		}
		m.mu.Lock()
		if c := m.conns[connKey{agentID: agentID, harness: ag.Harness}]; c != nil && !c.closed() && !m.restartable(c) {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		sb, err := m.store.GetSandbox(ctx, agentID)
		if err != nil || sb.State != "running" {
			return
		}
		if _, err := m.connectionFor(ctx, agentID, ag.Harness, nil); err != nil && m.ctx.Err() == nil {
			m.lg.Debug("harness: background warm-up skipped", "agent", agentID, "err", err)
		}
	}()
}

// connectionFor returns the live connection for agentID running harnessID
// (empty = the agent's default harness), starting it if needed.
func (m *Manager) connectionFor(ctx context.Context, agentID, harnessID string, notice func(string)) (*harnessConn, error) {
	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent.Kind == "app" {
		return nil, ErrNoHarness
	}
	if harnessID == "" {
		harnessID = agent.Harness
	}
	key := connKey{agentID: agentID, harness: harnessID}

	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, errShuttingDown
	}
	if c := m.conns[key]; c != nil && !c.closed() && !m.restartable(c) {
		m.mu.Unlock()
		return c, nil
	}
	lock := m.starting[key]
	if lock == nil {
		lock = make(chan struct{}, 1)
		m.starting[key] = lock
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
	if c := m.conns[key]; c != nil && !c.closed() {
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

	// Reload agent in case settings changed while waiting for the startup lock.
	agent, err = m.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if agent.Kind == "app" {
		return nil, ErrNoHarness
	}
	var ad *harness.Adapter
	var ok bool
	if m.resolver != nil {
		ad, ok, err = m.resolver.AdapterFor(ctx, harnessID)
		if err != nil {
			return nil, err
		}
	} else {
		ad, ok = harness.Get(harnessID)
	}
	if !ok {
		return nil, fmt.Errorf("unknown harness %q", harnessID)
	}
	status := func(msg string) {
		if notice != nil {
			notice(msg)
		}
	}
	began := time.Now()
	m.lg.Info("harness: starting "+ad.Name+"; waiting for the machine", "agent", agentID, "harness", ad.ID)
	wctx, wcancel := context.WithTimeout(ctx, wakeTimeout)
	ts, err := m.wake(wctx, agentID, status)
	wcancel()
	if err != nil {
		m.lg.Warn("harness: the machine is not reachable", "agent", agentID, "took", time.Since(began), "err", err)
		if ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("the agent's sandbox did not start within %s: %w", wakeTimeout, err)
		}
		return nil, err
	}
	// The default model goes into the environment only for the agent's own
	// harness, and only while the harness is not known to switch models
	// through its session config options.
	var cfg harness.Config
	if harnessID == agent.Harness {
		cfg.Model = agent.Model
		if c, ok, _ := m.Catalog(ctx, agentID); ok && c.Harness == ad.ID {
			if _, switches := c.ModelOption(); switches {
				cfg.Model = ""
			}
		}
	}
	home := sandboxHome(ctx, ts)
	cmd := ad.Command
	env := ad.Environment(cfg)
	if m.resolver != nil && (ad.Source == "registry" || ad.Source == "local") {
		cmd, env, err = m.resolver.Prepare(ctx, ad, ts, home, cfg, status)
		if err != nil {
			return nil, fmt.Errorf("prepare %s: %w", ad.Name, err)
		}
	}
	m.lg.Info("harness: machine reachable", "agent", agentID, "took", time.Since(began))
	if m.logins != nil {
		lbegan := time.Now()
		lctx, lcancel := context.WithTimeout(ctx, loginsTimeout)
		loginAgent := agent
		loginAgent.Harness = harnessID
		extra, err := m.logins.Prepare(lctx, loginAgent, ts)
		lcancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			m.lg.Warn("account login unavailable", "agent", agentID, "harness", harnessID, "err", err)
			status("The account login could not be prepared: " + err.Error())
		}
		for k, v := range extra {
			env[k] = v
		}
		m.lg.Info("harness: account login prepared", "agent", agentID, "harness", harnessID, "took", time.Since(lbegan))
	}

	hc := newHarnessConn(m, agentID, ad, ts)
	hc.startModel = cfg.Model
	hc.home = home
	spawned := time.Now()
	proc, err := m.open(ctx, hc, ts, cmd, env)
	if err != nil {
		m.lg.Warn("harness: could not start the process", "agent", agentID, "harness", ad.ID, "took", time.Since(spawned), "err", err)
		return nil, err
	}
	m.lg.Info("harness: process started", "agent", agentID, "harness", ad.ID, "command", strings.Join(cmd, " "), "took", time.Since(spawned))
	hc.start(proc, env)
	var init acp.InitializeResponse
	var initOpts []ConfigOption
	var initByModel map[string][]ConfigOption
	err = m.step(ctx, hc, "initialize", func(ctx context.Context) error {
		var err error
		init, initOpts, initByModel, err = hc.proto.Initialize(ctx)
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
	if len(initOpts) > 0 {
		m.observeOptions(hc, "", initOpts, initByModel, true)
	}
	m.mu.Lock()
	if m.closing || hc.closed() {
		m.mu.Unlock()
		hc.close("the server shut down while the harness started")
		return nil, hc.failure()
	}
	m.conns[key] = hc
	m.mu.Unlock()
	m.lg.Info("harness connected", "agent", agentID, "harness", ad.ID, "took", time.Since(began))
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
	key := connKey{agentID: hc.agentID, harness: hc.adapter.ID}
	m.mu.Lock()
	if m.conns[key] == hc {
		delete(m.conns, key)
	}
	m.mu.Unlock()
}
