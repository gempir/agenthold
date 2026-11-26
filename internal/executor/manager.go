// Package executor manages per-user Executor instances, bridges agent MCP
// sessions over the sandbox tunnel, and proxies the Executor dashboard for
// OAuth2 and account management.
package executor

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/audit"
)

// Approval modes supported by Executor's /mcp endpoint.
const (
	ApprovalModel     = "model"
	ApprovalAutomatic = "automatic"
	ApprovalBrowser   = "browser"
)

// ValidApprovalMode reports whether s is a supported Executor approval mode.
func ValidApprovalMode(s string) bool {
	switch s {
	case ApprovalModel, ApprovalAutomatic, ApprovalBrowser:
		return true
	}
	return false
}

// NormalizeApprovalMode returns a valid approval mode, defaulting to "model".
func NormalizeApprovalMode(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if ValidApprovalMode(s) {
		return s
	}
	return ApprovalModel
}

// Sentinel errors.
var (
	ErrDisabled           = errors.New("executor is disabled on this server")
	ErrUnavailable        = errors.New("executor runtime binary is not available on this server")
	ErrBadUserID          = errors.New("invalid user id")
	ErrCustomAppsDisabled = errors.New("custom MCP/app imports are disabled when executor.allow_private_fetch is false; install an official catalog app or enable executor.allow_private_fetch")
)

// Config configures the Executor manager.
type Config struct {
	Disabled bool
	// Binary is the path to the executor executable. Empty means unavailable.
	Binary string
	// Version is the known Executor runtime version (optional; probed lazily if empty).
	Version string
	// DataDir is Agenthold's data directory; per-user data lives in DataDir/executor/users/<userID>.
	DataDir string
	// MasterKey is Agenthold's 32-byte master key used to derive deterministic per-user
	// EXECUTOR_ENCRYPTION_KEY and EXECUTOR_API_KEY values via HMAC-SHA256.
	MasterKey []byte
	// DashboardOrigin is the dedicated Agenthold-authenticated origin serving Executor's UI
	// and OAuth2 callbacks (for example https://executor-demo.agenthold.dev). Empty when unavailable.
	DashboardOrigin string
	// IdleTimeout stops a user's Executor process after this duration without activity
	// (and with no running agents). 0 disables idle shutdown.
	IdleTimeout time.Duration
	// StartupTimeout bounds how long Ensure waits for `executor serve --stdio` to print its URL.
	// Default 60s.
	StartupTimeout time.Duration
	// AllowPrivateFetch allows Executor apps and custom MCP imports to reach private/loopback networks.
	AllowPrivateFetch bool
	// HasRunningAgents reports whether userID currently has any running agents.
	// When true, idle reaping keeps the user's Executor alive.
	HasRunningAgents func(ctx context.Context, userID string) bool
	Log              *slog.Logger
	Audit            *audit.Log
}

// Status describes the state of a user's Executor runtime.
type Status struct {
	Available    bool
	Enabled      bool
	Running      bool
	Version      string
	DashboardURL string
	OAuthReady   bool
	Reason       string
}

// Instance is one running per-user `executor serve --stdio` process.
type Instance struct {
	ID            string // unique per spawned process
	UserID        string
	URL           string // loopback URL, e.g. http://127.0.0.1:54321
	Host          string // 127.0.0.1:54321
	Port          string
	APIKey        string
	BrowserOrigin string // optional https://... origin passed to Executor

	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	exitErr  error
	lastUsed atomic.Int64 // unix nanos
	active   atomic.Int64 // in-flight operations
	stopping bool         // guarded by Manager.mu
	closeMu  sync.Mutex
	closed   bool
}

func (i *Instance) touch() {
	i.lastUsed.Store(time.Now().UnixNano())
}

func (i *Instance) acquire() func() {
	i.active.Add(1)
	i.touch()
	return func() {
		i.touch()
		i.active.Add(-1)
	}
}

func (i *Instance) alive() bool {
	select {
	case <-i.done:
		return false
	default:
		return true
	}
}

// stop asks the `executor serve --stdio` child to exit by closing stdin, then
// escalates to SIGTERM / SIGKILL if it does not stop within grace.
func (i *Instance) stop(grace time.Duration) error {
	i.closeMu.Lock()
	if i.closed {
		i.closeMu.Unlock()
		<-i.done
		return nil
	}
	i.closed = true
	stdin := i.stdin
	i.closeMu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if grace <= 0 {
		grace = 5 * time.Second
	}
	select {
	case <-i.done:
		return nil
	case <-time.After(grace):
	}
	if i.cmd != nil && i.cmd.Process != nil {
		_ = i.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-i.done:
			return nil
		case <-time.After(2 * time.Second):
		}
		_ = i.cmd.Process.Kill()
	}
	<-i.done
	return nil
}

type activeStream struct {
	userID  string
	agentID string
	cancel  context.CancelFunc
}

type pairedEntry struct {
	userID  string
	instID  string
	cookies []*http.Cookie
}

// Manager supervises per-user Executor instances.
type Manager struct {
	cfg            Config
	log            *slog.Logger
	client         *http.Client
	proxyTransport *http.Transport
	procCtx        context.Context
	procCancel     context.CancelFunc

	mu          sync.Mutex
	instances   map[string]*Instance
	userLocks   map[string]*sync.Mutex
	closed      bool
	egressGuard *egressGuard

	streamMu      sync.Mutex
	nextStreamID  uint64
	activeStreams map[uint64]activeStream

	pairMu sync.Mutex
	paired map[string]pairedEntry // keyed by gateSessionKey

	versionOnce sync.Once
	version     string
}

// New creates an Executor manager.
func New(cfg Config) *Manager {
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 60 * time.Second
	}
	cfg.DashboardOrigin = strings.TrimRight(strings.TrimSpace(cfg.DashboardOrigin), "/")
	lg := cfg.Log
	if lg == nil {
		lg = slog.Default()
	}
	pctx, pcancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:        cfg,
		log:        lg.With("component", "executor"),
		procCtx:    pctx,
		procCancel: pcancel,
		client:     &http.Client{Timeout: 60 * time.Second},
		proxyTransport: &http.Transport{
			ResponseHeaderTimeout: 5 * time.Minute,
			IdleConnTimeout:       60 * time.Second,
			MaxIdleConnsPerHost:   16,
			DisableCompression:    true,
		},
		instances:     map[string]*Instance{},
		userLocks:     map[string]*sync.Mutex{},
		activeStreams: map[uint64]activeStream{},
		paired:        map[string]pairedEntry{},
		version:       cfg.Version,
	}
}

func (m *Manager) userOpLock(userID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lk := m.userLocks[userID]
	if lk == nil {
		lk = &sync.Mutex{}
		m.userLocks[userID] = lk
	}
	return lk
}

func (m *Manager) ensureEgressGuard() (string, error) {
	if m.cfg.AllowPrivateFetch {
		return "", nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errors.New("executor manager is shutting down")
	}
	if m.egressGuard == nil {
		g, err := startEgressGuard(m.log)
		if err != nil {
			return "", fmt.Errorf("start executor egress guard: %w", err)
		}
		m.egressGuard = g
	}
	return m.egressGuard.url, nil
}

// DiscoverBinary locates an Executor executable from explicit config, bundled
// release extraction, platform-specific or legacy local dev checkout
// (build/runtime/executor-<os>-<arch>/executor), or PATH.
func DiscoverBinary(explicit, bundledBinary, bundledVersion string) (binary, version string) {
	if p := strings.TrimSpace(explicit); p != "" {
		return p, ""
	}
	if bundledBinary != "" {
		return bundledBinary, bundledVersion
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	candidates := []string{
		filepath.Join("build", "runtime", fmt.Sprintf("executor-%s-%s", runtime.GOOS, arch), "executor"),
		filepath.Join("build", "runtime", "executor", "executor"),
	}
	for _, candidate := range candidates {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			if abs, err := filepath.Abs(candidate); err == nil {
				return abs, ""
			}
			return candidate, ""
		}
	}
	if p, err := exec.LookPath("executor"); err == nil {
		return p, ""
	}
	return "", ""
}

// Enabled reports whether Executor is enabled in configuration.
func (m *Manager) Enabled() bool { return !m.cfg.Disabled }

// Available reports whether Executor is enabled and has a runnable binary.
func (m *Manager) Available() bool { return !m.cfg.Disabled && m.cfg.Binary != "" }

// AllowPrivateFetch reports whether private/loopback network access is allowed.
func (m *Manager) AllowPrivateFetch() bool { return m.cfg.AllowPrivateFetch }

// DashboardOrigin returns the dedicated Agenthold-authenticated origin for Executor's UI, if any.
func (m *Manager) DashboardOrigin() string { return m.cfg.DashboardOrigin }

// OAuthReady reports whether an HTTPS dedicated origin is configured for OAuth2 callbacks.
func (m *Manager) OAuthReady() bool { return strings.HasPrefix(m.cfg.DashboardOrigin, "https://") }

// Version returns the Executor runtime version (probing `executor --version` once if unknown).
func (m *Manager) Version() string {
	if m.version != "" || !m.Available() {
		return m.version
	}
	m.versionOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, m.cfg.Binary, "--version").Output()
		if err != nil {
			return
		}
		v := strings.TrimSpace(string(out))
		v = strings.TrimPrefix(v, "executor ")
		v = strings.TrimPrefix(v, "v")
		m.version = v
	})
	return m.version
}

// Status returns the current Executor status for userID without starting a process.
func (m *Manager) Status(userID string) Status {
	s := Status{
		Available:    m.Available(),
		Enabled:      m.Enabled(),
		Version:      m.Version(),
		DashboardURL: m.cfg.DashboardOrigin,
		OAuthReady:   m.OAuthReady(),
	}
	switch {
	case m.cfg.Disabled:
		s.Reason = "Executor is disabled in server configuration (executor.disabled)."
	case m.cfg.Binary == "":
		s.Reason = "Executor runtime binary not found. Use a release build or run scripts/guest/fetch-executor.sh."
	}
	if userID != "" {
		m.mu.Lock()
		if inst, ok := m.instances[userID]; ok && !inst.stopping && inst.alive() {
			s.Running = true
		}
		m.mu.Unlock()
	}
	return s
}

// Ensure returns a running Executor instance for userID, starting one if needed.
func (m *Manager) Ensure(ctx context.Context, userID string) (*Instance, error) {
	inst, release, err := m.acquireInstance(ctx, userID)
	if err != nil {
		return nil, err
	}
	release()
	return inst, nil
}

// acquireInstance returns a running Executor instance with its active refcount
// already incremented under m.mu so idle reaping cannot race to stop it before
// the caller starts using it.
func (m *Manager) acquireInstance(ctx context.Context, userID string) (*Instance, func(), error) {
	if m.cfg.Disabled {
		return nil, nil, ErrDisabled
	}
	if m.cfg.Binary == "" {
		return nil, nil, ErrUnavailable
	}
	if !validUserID(userID) {
		return nil, nil, ErrBadUserID
	}

	// Fast path when already alive and not stopping.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, errors.New("executor manager is shutting down")
	}
	if inst, ok := m.instances[userID]; ok && !inst.stopping && inst.alive() {
		release := inst.acquire()
		m.mu.Unlock()
		return inst, release, nil
	}
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	opMu := m.userOpLock(userID)
	opMu.Lock()
	defer opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, errors.New("executor manager is shutting down")
	}
	if inst, ok := m.instances[userID]; ok && !inst.stopping && inst.alive() {
		release := inst.acquire()
		m.mu.Unlock()
		return inst, release, nil
	}
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	inst, err := m.spawn(userID)
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = inst.stop(3 * time.Second)
		return nil, nil, errors.New("executor manager is shutting down")
	}
	m.instances[userID] = inst
	release := inst.acquire()
	m.mu.Unlock()
	return inst, release, nil
}

// Stop shuts down userID's Executor instance if running and clears cached proxy pairings.
func (m *Manager) Stop(userID string) error {
	if !validUserID(userID) {
		return nil
	}
	opMu := m.userOpLock(userID)
	opMu.Lock()
	defer opMu.Unlock()
	return m.stopLocked(userID)
}

func (m *Manager) stopLocked(userID string) error {
	m.mu.Lock()
	inst := m.instances[userID]
	if inst != nil {
		inst.stopping = true
	}
	m.mu.Unlock()
	if inst == nil {
		m.ClearUserPairings(userID)
		return nil
	}
	err := inst.stop(5 * time.Second)
	m.mu.Lock()
	if m.instances[userID] == inst {
		delete(m.instances, userID)
	}
	m.mu.Unlock()
	m.ClearUserPairings(userID)
	return err
}

// Restart stops userID's Executor instance (waiting for full exit) and starts a fresh one.
func (m *Manager) Restart(ctx context.Context, userID string) (Status, error) {
	if m.cfg.Disabled {
		return m.Status(userID), ErrDisabled
	}
	if m.cfg.Binary == "" {
		return m.Status(userID), ErrUnavailable
	}
	if !validUserID(userID) {
		return m.Status(userID), ErrBadUserID
	}
	opMu := m.userOpLock(userID)
	opMu.Lock()
	defer opMu.Unlock()

	if err := m.stopLocked(userID); err != nil {
		return m.Status(userID), err
	}
	if err := ctx.Err(); err != nil {
		return m.Status(userID), err
	}
	inst, err := m.spawn(userID)
	if err != nil {
		return m.Status(userID), err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = inst.stop(3 * time.Second)
		return m.Status(userID), errors.New("executor manager is shutting down")
	}
	m.instances[userID] = inst
	m.mu.Unlock()
	return m.Status(userID), nil
}

// Run periodically stops idle user Executor instances until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	if m.cfg.IdleTimeout <= 0 {
		<-ctx.Done()
		return
	}
	interval := min(m.cfg.IdleTimeout/4, time.Minute)
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.reapIdle(ctx)
		}
	}
}

func (m *Manager) reapIdle(ctx context.Context) {
	cutoff := time.Now().Add(-m.cfg.IdleTimeout).UnixNano()
	m.mu.Lock()
	var userIDs []string
	for uid, inst := range m.instances {
		if inst.active.Load() == 0 && inst.lastUsed.Load() < cutoff {
			userIDs = append(userIDs, uid)
		}
	}
	m.mu.Unlock()
	for _, uid := range userIDs {
		if m.cfg.HasRunningAgents != nil && m.cfg.HasRunningAgents(ctx, uid) {
			m.mu.Lock()
			if inst := m.instances[uid]; inst != nil {
				inst.touch()
			}
			m.mu.Unlock()
			continue
		}
		opMu := m.userOpLock(uid)
		opMu.Lock()
		m.mu.Lock()
		inst := m.instances[uid]
		shouldStop := inst != nil && !inst.stopping && inst.active.Load() == 0 && inst.lastUsed.Load() < cutoff
		if shouldStop {
			inst.stopping = true
		}
		m.mu.Unlock()
		if shouldStop {
			m.log.Info("stopping idle executor instance", "user", uid)
			_ = m.stopLocked(uid)
		}
		opMu.Unlock()
	}
}

func (m *Manager) registerStream(userID, agentID string, cancel context.CancelFunc) func() {
	m.streamMu.Lock()
	m.nextStreamID++
	sid := m.nextStreamID
	m.activeStreams[sid] = activeStream{userID: userID, agentID: agentID, cancel: cancel}
	m.streamMu.Unlock()
	return func() {
		m.streamMu.Lock()
		delete(m.activeStreams, sid)
		m.streamMu.Unlock()
	}
}

// CancelAgentStreams immediately terminates all open MCP tunnel streams for agentID.
func (m *Manager) CancelAgentStreams(agentID string) {
	m.streamMu.Lock()
	var cancels []context.CancelFunc
	for _, s := range m.activeStreams {
		if s.agentID == agentID {
			cancels = append(cancels, s.cancel)
		}
	}
	m.streamMu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// CancelUserStreams immediately terminates all open MCP tunnel streams for userID.
func (m *Manager) CancelUserStreams(userID string) {
	m.streamMu.Lock()
	var cancels []context.CancelFunc
	for _, s := range m.activeStreams {
		if s.userID == userID {
			cancels = append(cancels, s.cancel)
		}
	}
	m.streamMu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// ClearSessionPairing drops any cached upstream Executor cookie for gateSessionKey.
func (m *Manager) ClearSessionPairing(gateSessionKey string) {
	if gateSessionKey == "" {
		return
	}
	m.pairMu.Lock()
	delete(m.paired, gateSessionKey)
	m.pairMu.Unlock()
}

// ClearUserPairings drops all cached upstream Executor cookies for userID.
func (m *Manager) ClearUserPairings(userID string) {
	m.pairMu.Lock()
	for k, v := range m.paired {
		if v.userID == userID {
			delete(m.paired, k)
		}
	}
	m.pairMu.Unlock()
}

// Close shuts down all running Executor instances, active streams, and the egress guard.
func (m *Manager) Close() error {
	m.streamMu.Lock()
	for _, s := range m.activeStreams {
		s.cancel()
	}
	m.streamMu.Unlock()

	m.mu.Lock()
	m.closed = true
	uids := make([]string, 0, len(m.instances))
	for uid := range m.instances {
		uids = append(uids, uid)
	}
	guard := m.egressGuard
	m.egressGuard = nil
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, uid := range uids {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			_ = m.Stop(u)
		}(uid)
	}
	wg.Wait()
	m.procCancel()
	if guard != nil {
		_ = guard.Close()
	}
	return nil
}

func (m *Manager) spawn(userID string) (*Instance, error) {
	proxyURL, err := m.ensureEgressGuard()
	if err != nil {
		return nil, err
	}
	dataDir := filepath.Join(m.cfg.DataDir, "executor", "users", userID)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("executor data dir: %w", err)
	}
	encKey := deriveHexKey(m.cfg.MasterKey, "agenthold-executor-encryption-v1:", userID)
	apiKey := deriveHexKey(m.cfg.MasterKey, "agenthold-executor-apikey-v1:", userID)
	svcPW := randomHex(32)

	cmd := exec.CommandContext(m.procCtx, m.cfg.Binary, "serve", "--stdio")
	env := filteredEnv(os.Environ())
	env = append(env,
		"EXECUTOR_DATA_DIR="+dataDir,
		"EXECUTOR_PORT=0",
		"EXECUTOR_API_KEY="+apiKey,
		"EXECUTOR_ENCRYPTION_KEY="+encKey,
		"EXECUTOR_SERVICE_PASSWORD="+svcPW,
		"EXECUTOR_NO_UPDATE_CHECK=1",
		"DO_NOT_TRACK=1",
		"EXECUTOR_DISABLE_ANALYTICS=1",
	)
	browserOrigin := ""
	if strings.HasPrefix(m.cfg.DashboardOrigin, "https://") {
		browserOrigin = m.cfg.DashboardOrigin
		env = append(env, "EXECUTOR_BROWSER_ORIGIN="+browserOrigin)
	}
	if m.cfg.AllowPrivateFetch {
		env = append(env, "EXECUTOR_APPS_ALLOW_PRIVATE_FETCH=true")
	} else if proxyURL != "" {
		env = append(env,
			"HTTP_PROXY="+proxyURL,
			"HTTPS_PROXY="+proxyURL,
			"http_proxy="+proxyURL,
			"https_proxy="+proxyURL,
			"NO_PROXY=127.0.0.1,localhost,::1",
			"no_proxy=127.0.0.1,localhost,::1",
		)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start executor: %w", err)
	}

	var stderrBuf strings.Builder
	var stderrMu sync.Mutex
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 4096), 256*1024)
		for sc.Scan() {
			line := sc.Text()
			stderrMu.Lock()
			if stderrBuf.Len() < 16*1024 {
				stderrBuf.WriteString(line)
				stderrBuf.WriteByte('\n')
			}
			stderrMu.Unlock()
			m.log.Debug("executor stderr", "user", userID, "line", line)
		}
	}()

	type readyMsg struct {
		url string
		err error
	}
	readyCh := make(chan readyMsg, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 4096), 256*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "{") {
				continue
			}
			var payload struct {
				URL string `json:"url"`
			}
			if json.Unmarshal([]byte(line), &payload) == nil && payload.URL != "" {
				readyCh <- readyMsg{url: payload.URL}
				_, _ = io.Copy(io.Discard, stdout)
				return
			}
		}
		if err := sc.Err(); err != nil {
			readyCh <- readyMsg{err: err}
			return
		}
		readyCh <- readyMsg{err: io.EOF}
	}()

	done := make(chan struct{})
	inst := &Instance{
		ID:            randomHex(16),
		UserID:        userID,
		APIKey:        apiKey,
		BrowserOrigin: browserOrigin,
		cmd:           cmd,
		stdin:         stdin,
		done:          done,
	}
	inst.touch()

	go func() {
		inst.exitErr = cmd.Wait()
		close(done)
		m.mu.Lock()
		if cur, ok := m.instances[userID]; ok && cur == inst {
			delete(m.instances, userID)
		}
		m.mu.Unlock()
		m.log.Info("executor instance exited", "user", userID, "err", inst.exitErr)
	}()

	timer := time.NewTimer(m.cfg.StartupTimeout)
	defer timer.Stop()
	select {
	case msg := <-readyCh:
		if msg.err != nil {
			_ = inst.stop(2 * time.Second)
			stderrMu.Lock()
			tail := strings.TrimSpace(stderrBuf.String())
			stderrMu.Unlock()
			if tail != "" {
				return nil, fmt.Errorf("executor failed to start: %w (%s)", msg.err, tail)
			}
			return nil, fmt.Errorf("executor failed to start: %w", msg.err)
		}
		u, err := url.Parse(msg.url)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			_ = inst.stop(2 * time.Second)
			return nil, fmt.Errorf("executor printed invalid URL %q", msg.url)
		}
		inst.URL = strings.TrimRight(msg.url, "/")
		inst.Host = u.Host
		inst.Port = u.Port()
		m.log.Info("executor instance ready", "user", userID, "url", inst.URL)
		return inst, nil
	case <-done:
		stderrMu.Lock()
		tail := strings.TrimSpace(stderrBuf.String())
		stderrMu.Unlock()
		if tail != "" {
			return nil, fmt.Errorf("executor exited before becoming ready: %w (%s)", inst.exitErr, tail)
		}
		return nil, fmt.Errorf("executor exited before becoming ready: %w", inst.exitErr)
	case <-timer.C:
		_ = inst.stop(2 * time.Second)
		return nil, fmt.Errorf("executor did not become ready within %s", m.cfg.StartupTimeout)
	}
}

func validUserID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func deriveHexKey(master []byte, domain, userID string) string {
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte(domain))
	_, _ = mac.Write([]byte(userID))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func filteredEnv(base []string) []string {
	out := make([]string, 0, len(base)+12)
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "EXECUTOR_") || strings.HasPrefix(upper, "AGENTHOLD_") ||
			upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "NO_PROXY" {
			continue
		}
		out = append(out, kv)
	}
	return out
}
