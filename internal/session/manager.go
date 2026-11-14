package session

import (
	"context"
	"database/sql"
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
)

var errLoginNeeded = errors.New("the harness is not signed in: sign in from the agent's Overview tab, or attach an API key secret and rebuild")

func authRequired(err error) bool {
	var re *acp.RequestError
	return errors.As(err, &re) && re.Code == -32000
}

// HomeDir is the harness's working directory.
const HomeDir = "/home/agent"

// Waker returns the agent's tunnel session, starting the sandbox if needed.
type Waker func(ctx context.Context, agentID string, status func(string)) (*tunnel.Session, error)

// Manager runs harness connections and sessions.
type Manager struct {
	store *store.Store
	wake  Waker
	log   *log
	lg    *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	conns map[string]*harnessConn // by agent id
	turns map[string]*turn        // by session id
	// starting serializes harness startup per agent.
	starting map[string]*sync.Mutex
}

type turn struct {
	sessionID string
	agentID   string
	acpID     string
	conn      *harnessConn
	perms     map[string]chan acp.PermissionOptionId // pending permission requests
}

// NewManager returns a session manager.
func NewManager(st *store.Store, wake Waker, lg *slog.Logger) *Manager {
	if lg == nil {
		lg = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		store: st, wake: wake, log: newLog(st), lg: lg, ctx: ctx, cancel: cancel,
		conns: map[string]*harnessConn{}, turns: map[string]*turn{}, starting: map[string]*sync.Mutex{},
	}
	_ = st.ResetRunningSessions(ctx)
	return m
}

// SetWaker sets how agents are woken (the API provides it).
func (m *Manager) SetWaker(w Waker) { m.wake = w }

// Close stops all harness connections and waits for turns to end.
func (m *Manager) Close() {
	m.mu.Lock()
	conns := make([]*harnessConn, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
	m.cancel()
	m.wg.Wait()
}

// Wait blocks until running turns have finished (tests).
func (m *Manager) Wait() { m.wg.Wait() }

// Create starts a new, empty session.
func (m *Manager) Create(ctx context.Context, agentID, userID, title string) (db.AgentSession, error) {
	now := time.Now().Unix()
	s := db.AgentSession{ID: id.New("ses"), AgentID: agentID, Title: title, Status: "idle", CreatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if s.Title == "" {
		s.Title = "New session"
	}
	err := m.store.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: s.ID, AgentID: agentID, Title: s.Title, CreatedBy: userID, CreatedAt: now, UpdatedAt: now})
	return s, err
}

// Subscribe returns events after `after` and a live subscription.
func (m *Manager) Subscribe(ctx context.Context, sessionID string, after int64) ([]Event, *Subscription, error) {
	return m.log.subscribe(ctx, sessionID, after)
}

// Running reports whether a turn is in progress.
func (m *Manager) Running(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.turns[sessionID]
	return ok
}

// Prompt starts a turn. It returns once the turn has started; the turn
// itself runs in the background and streams into the event log.
func (m *Manager) Prompt(ctx context.Context, sessionID, userID, userName, text string, status func(string)) error {
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
	t := &turn{sessionID: sessionID, agentID: sess.AgentID, perms: map[string]chan acp.PermissionOptionId{}}
	m.turns[sessionID] = t
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		delete(m.turns, sessionID)
		m.mu.Unlock()
	}

	if err := m.log.append(ctx, sessionID, EventPrompt, userID, map[string]any{"text": text, "user": map[string]string{"id": userID, "name": userName}}); err != nil {
		release()
		return err
	}
	_ = m.store.SetSessionStatus(ctx, db.SetSessionStatusParams{Status: "running", UpdatedAt: time.Now().Unix(), ID: sessionID})
	if sess.Title == "New session" {
		_ = m.store.RenameAgentSessionTitle(ctx, sessionID, titleFrom(text))
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer release()
		ctx := m.ctx
		defer func() {
			_ = m.store.SetSessionStatus(ctx, db.SetSessionStatusParams{Status: "idle", UpdatedAt: time.Now().Unix(), ID: sessionID})
		}()
		if err := m.run(ctx, t, sess, text, status); err != nil {
			m.lg.Warn("turn failed", "session", sessionID, "err", err)
			_ = m.log.append(ctx, sessionID, EventError, "", map[string]string{"message": err.Error()})
			_ = m.log.append(ctx, sessionID, EventTurnEnd, "", map[string]string{"stopReason": "error"})
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

func (m *Manager) run(ctx context.Context, t *turn, sess db.AgentSession, text string, status func(string)) error {
	notice := func(msg string) {
		_ = m.log.append(ctx, sess.ID, EventNotice, "", map[string]string{"message": msg})
		if status != nil {
			status(msg)
		}
	}
	conn, err := m.connection(ctx, sess.AgentID, notice)
	if err != nil {
		return err
	}
	t.conn = conn
	acpID, err := m.activate(ctx, conn, sess, notice)
	if err != nil {
		return err
	}
	t.acpID = acpID
	conn.bind(acpID, sess.ID)

	res, err := conn.acp.Prompt(ctx, acp.PromptRequest{SessionId: acp.SessionId(acpID), Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
	if err != nil {
		select {
		case <-conn.done:
			return errors.New("the agent's harness stopped during the turn")
		default:
		}
		if authRequired(err) {
			return errLoginNeeded
		}
		return fmt.Errorf("prompt: %w", err)
	}
	err = m.log.append(ctx, sess.ID, EventTurnEnd, "", map[string]any{"stopReason": res.StopReason, "usage": res.Usage})
	return err
}

// activate makes sure the harness knows the session: new, loaded or resumed.
func (m *Manager) activate(ctx context.Context, conn *harnessConn, sess db.AgentSession, notice func(string)) (string, error) {
	sess, err := m.store.GetAgentSession(ctx, sess.ID)
	if err != nil {
		return "", err
	}
	if sess.AcpSessionID != "" && conn.isActive(sess.AcpSessionID) {
		return sess.AcpSessionID, nil
	}
	caps := conn.init.AgentCapabilities
	if sess.AcpSessionID != "" {
		switch {
		case caps.LoadSession:
			conn.setLoading(sess.AcpSessionID, true)
			_, err := conn.acp.LoadSession(ctx, acp.LoadSessionRequest{SessionId: acp.SessionId(sess.AcpSessionID), Cwd: HomeDir, McpServers: []acp.McpServer{}})
			conn.setLoading(sess.AcpSessionID, false)
			if err == nil {
				conn.markActive(sess.AcpSessionID)
				return sess.AcpSessionID, nil
			}
			m.lg.Warn("session/load failed, starting a new session", "session", sess.ID, "err", err)
		case caps.SessionCapabilities.Resume != nil:
			if _, err := conn.acp.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: acp.SessionId(sess.AcpSessionID), Cwd: HomeDir}); err == nil {
				conn.markActive(sess.AcpSessionID)
				return sess.AcpSessionID, nil
			}
		}
	}
	res, err := conn.acp.NewSession(ctx, acp.NewSessionRequest{Cwd: HomeDir, McpServers: []acp.McpServer{}})
	if err != nil {
		if authRequired(err) {
			return "", errLoginNeeded
		}
		return "", fmt.Errorf("new session: %w", err)
	}
	if sess.AcpSessionID != "" {
		notice("The agent restarted and could not restore this conversation; it starts with a fresh context.")
	}
	if err := m.store.SetSessionACPID(ctx, db.SetSessionACPIDParams{AcpSessionID: string(res.SessionId), ID: sess.ID}); err != nil {
		return "", err
	}
	conn.markActive(string(res.SessionId))
	return string(res.SessionId), nil
}

// Cancel asks the harness to stop the running turn.
func (m *Manager) Cancel(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	t, ok := m.turns[sessionID]
	m.mu.Unlock()
	if !ok || t.conn == nil || t.acpID == "" {
		return ErrNoTurn
	}
	t.cancelPermissions()
	return t.conn.acp.Cancel(ctx, acp.CancelNotification{SessionId: acp.SessionId(t.acpID)})
}

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
		c.close()
	}
}

// connection returns the agent's live harness connection, starting it if needed.
func (m *Manager) connection(ctx context.Context, agentID string, notice func(string)) (*harnessConn, error) {
	m.mu.Lock()
	if c := m.conns[agentID]; c != nil && !c.closed() {
		m.mu.Unlock()
		return c, nil
	}
	lock := m.starting[agentID]
	if lock == nil {
		lock = &sync.Mutex{}
		m.starting[agentID] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	m.mu.Lock()
	if c := m.conns[agentID]; c != nil && !c.closed() {
		m.mu.Unlock()
		return c, nil
	}
	m.mu.Unlock()

	agent, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
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
	ts, err := m.wake(ctx, agentID, status)
	if err != nil {
		return nil, err
	}
	hc := &harnessConn{m: m, agentID: agentID, sessions: map[string]string{}, active: map[string]bool{}, loading: map[string]bool{}, done: make(chan struct{})}
	cfg := harness.Config{Model: agent.Model}
	proc, err := ts.StartHarness(m.ctx, ad.Command(cfg), ad.Env(cfg), func(line string) {
		m.lg.Debug("harness stderr", "agent", agentID, "line", line)
		hc.stderr(line)
	})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", ad.Name(), err)
	}
	hc.proc = proc
	hc.acp = acp.NewClientSideConnection(hc, proc, proc.Stdout)
	hc.acp.SetLogger(m.lg.With("agent", agentID, "component", "acp"))
	go func() {
		select {
		case <-proc.Done():
		case <-hc.acp.Done():
		}
		hc.close()
	}()

	ictx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	init, err := hc.acp.Initialize(ictx, acp.InitializeRequest{
		ProtocolVersion:    acp.ProtocolVersionNumber,
		ClientInfo:         &acp.Implementation{Name: "agenthold", Version: version.Version},
		ClientCapabilities: acp.ClientCapabilities{Auth: acp.AuthCapabilities{Terminal: true}},
	})
	if err != nil {
		hc.close()
		if tail := hc.stderrTail(); tail != "" {
			return nil, fmt.Errorf("%s did not start (%w): %s", ad.Name(), err, tail)
		}
		return nil, fmt.Errorf("%s did not start: %w", ad.Name(), err)
	}
	hc.init = init
	m.mu.Lock()
	m.conns[agentID] = hc
	m.mu.Unlock()
	m.lg.Info("harness connected", "agent", agentID, "harness", ad.ID())
	return hc, nil
}

func (m *Manager) dropConn(hc *harnessConn) {
	m.mu.Lock()
	if m.conns[hc.agentID] == hc {
		delete(m.conns, hc.agentID)
	}
	m.mu.Unlock()
}
