package sandbox

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	id2 "github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// ErrConflict is returned when an operation is not allowed in the current phase.
var ErrConflict = errors.New("operation not allowed in the current state")

// SpecFunc builds the sandbox spec for an agent.
type SpecFunc func(a db.Agent) Spec

// Tunnel is what the manager needs from the tunnel hub.
type Tunnel interface {
	// WaitConnected blocks until the agent's sandboxd is connected.
	WaitConnected(ctx context.Context, agentID string) error
	// Disconnect drops the agent's connection.
	Disconnect(agentID string)
}

// Options configures optional manager behavior.
type Options struct {
	// Tunnel, if set, makes "running" mean "sandboxd is connected".
	Tunnel Tunnel
	// TunnelPort is passed to sandboxes in their boot configuration.
	TunnelPort int
	// ConnectTimeout bounds how long a start waits for sandboxd. Default 60s.
	ConnectTimeout time.Duration
	// BootFiles returns extra files written into the sandbox before each start.
	BootFiles func(ctx context.Context, agentID string) (map[string][]byte, error)
}

// Manager drives agents' sandboxes through their lifecycle. Every operation
// first moves the persisted state with a guarded UPDATE (so concurrent requests
// cannot both win) and then performs the driver call in the background.
type Manager struct {
	store  *store.Store
	driver Driver
	spec   SpecFunc
	opts   Options
	log    *slog.Logger
	now    func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	locks  sync.Map // agent id -> *sync.Mutex

	mu        sync.Mutex
	listeners []func(agentID string, phase Phase)
}

// NewManager returns a manager. Call Close to wait for background operations.
func NewManager(st *store.Store, d Driver, spec SpecFunc, log *slog.Logger) *Manager {
	return NewManagerWithOptions(st, d, spec, Options{}, log)
}

// NewManagerWithOptions returns a manager with options.
func NewManagerWithOptions(st *store.Store, d Driver, spec SpecFunc, opts Options, log *slog.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	if log == nil {
		log = slog.Default()
	}
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 60 * time.Second
	}
	return &Manager{store: st, driver: d, spec: spec, opts: opts, log: log, now: time.Now, ctx: ctx, cancel: cancel}
}

// AuthenticateTunnel checks a sandboxd's boot token. Only sandboxes that are
// starting or running may connect.
func (m *Manager) AuthenticateTunnel(ctx context.Context, agentID, token string) error {
	sb, err := m.store.GetSandbox(ctx, agentID)
	if err != nil {
		return notFound(err)
	}
	if sb.BootTokenHash == "" || subtle.ConstantTimeCompare([]byte(sb.BootTokenHash), []byte(hashToken(token))) != 1 {
		return errors.New("invalid boot token")
	}
	switch Phase(sb.State) {
	case PhaseStarting, PhaseRunning:
		return nil
	}
	return fmt.Errorf("sandbox is %s", sb.State)
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// boot starts a sandbox with a fresh token and waits for sandboxd.
func (m *Manager) boot(ctx context.Context, agentID string, id ID) error {
	token := id2.Random(32)
	if err := m.store.SetBootToken(ctx, db.SetBootTokenParams{BootTokenHash: hashToken(token), AgentID: agentID}); err != nil {
		return err
	}
	boot := Boot{AgentID: agentID, Token: token, TunnelPort: m.opts.TunnelPort}
	if m.opts.BootFiles != nil {
		files, err := m.opts.BootFiles(ctx, agentID)
		if err != nil {
			return err
		}
		boot.Files = files
	}
	if err := m.driver.Start(ctx, id, boot); err != nil {
		return err
	}
	if st, err := m.driver.Status(ctx, id); err == nil && st.Subnet.IsValid() {
		if err := m.store.SetSandboxSubnet(ctx, db.SetSandboxSubnetParams{Subnet: st.Subnet.String(), AgentID: agentID}); err != nil {
			return err
		}
		m.notify(agentID, PhaseStarting)
	}
	if m.opts.Tunnel != nil {
		wctx, cancel := context.WithTimeout(ctx, m.opts.ConnectTimeout)
		defer cancel()
		if err := m.opts.Tunnel.WaitConnected(wctx, agentID); err != nil {
			st, _ := m.driver.Status(ctx, id)
			detail := ""
			if st.State != StateRunning {
				detail = fmt.Sprintf(" (sandbox %s %s)", st.State, st.Detail)
			}
			return fmt.Errorf("sandboxd did not connect within %s%s", m.opts.ConnectTimeout, detail)
		}
	}
	m.set(agentID, PhaseRunning, "")
	return nil
}

// Driver returns the manager's driver.
func (m *Manager) Driver() Driver { return m.driver }

// OnChange registers a callback for phase changes (used for live UI updates
// and, later, waking agents).
func (m *Manager) OnChange(fn func(agentID string, phase Phase)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listeners = append(m.listeners, fn)
}

func (m *Manager) notify(agentID string, p Phase) {
	m.mu.Lock()
	ls := append([]func(string, Phase){}, m.listeners...)
	m.mu.Unlock()
	for _, fn := range ls {
		fn(agentID, p)
	}
}

// Close cancels nothing that is running but waits for it to finish.
func (m *Manager) Close() {
	m.wg.Wait()
	m.cancel()
}

// Wait blocks until all background operations have finished (tests).
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) lock(agentID string) func() {
	v, _ := m.locks.LoadOrStore(agentID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (m *Manager) transition(ctx context.Context, agentID string, from []Phase, to Phase) error {
	n, err := m.store.TransitionSandbox(ctx, db.TransitionSandboxParams{
		ToState: string(to), Error: "", Now: m.now().Unix(), AgentID: agentID, FromStates: phases(from),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := m.store.GetSandbox(ctx, agentID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrConflict
	}
	m.notify(agentID, to)
	return nil
}

func (m *Manager) set(agentID string, p Phase, errMsg string) {
	if err := m.store.SetSandboxState(m.ctx, db.SetSandboxStateParams{
		State: string(p), Error: errMsg, StateChangedAt: m.now().Unix(), AgentID: agentID,
	}); err != nil {
		m.log.Error("persist sandbox state", "agent", agentID, "state", p, "err", err)
	}
	m.notify(agentID, p)
}

// background runs fn for agentID, serialized with other operations on the same agent.
func (m *Manager) background(agentID, op string, fn func(ctx context.Context) error) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		unlock := m.lock(agentID)
		defer unlock()
		ctx, cancel := context.WithTimeout(m.ctx, 15*time.Minute)
		defer cancel()
		if err := fn(ctx); err != nil {
			m.log.Warn("sandbox operation failed", "agent", agentID, "op", op, "err", err)
			m.set(agentID, PhaseError, fmt.Sprintf("%s failed: %v", op, err))
		}
	}()
}

// SpecJSON is the canonical form of a spec, stored to detect pending rebuilds.
func SpecJSON(s Spec) string {
	b, _ := json.Marshal(struct {
		Image     string
		Resources Resources
		Env       map[string]string `json:",omitempty"`
	}{s.Image, s.Resources, s.Env})
	return string(b)
}

// NeedsRebuild reports whether the agent's settings differ from what its
// sandbox was built with.
func (m *Manager) NeedsRebuild(a db.Agent, sb db.Sandbox) bool {
	return sb.AppliedSpec != "" && sb.AppliedSpec != SpecJSON(m.spec(a))
}

// Provision creates the sandbox row for a new agent (inside the caller's
// transaction q) and returns a function that starts provisioning; call it
// after the transaction commits.
func (m *Manager) Provision(ctx context.Context, q *db.Queries, agentID string) (start func(), err error) {
	if err := q.CreateSandbox(ctx, db.CreateSandboxParams{
		AgentID: agentID, Driver: m.driver.Name(), State: string(PhaseCreating), StateChangedAt: m.now().Unix(),
	}); err != nil {
		return nil, err
	}
	return func() { m.background(agentID, "create", m.create(agentID)) }, nil
}

func (m *Manager) create(agentID string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		a, err := m.store.GetAgent(ctx, agentID)
		if err != nil {
			return err
		}
		spec := m.spec(a)
		id, err := m.driver.Create(ctx, spec)
		if err != nil {
			return err
		}
		if err := m.store.SetSandboxBuilt(ctx, db.SetSandboxBuiltParams{DriverID: string(id), AppliedSpec: SpecJSON(spec), AgentID: agentID}); err != nil {
			return err
		}
		m.set(agentID, PhaseStarting, "")
		return m.boot(ctx, agentID, id)
	}
}

func (m *Manager) driverID(ctx context.Context, agentID string) (ID, error) {
	sb, err := m.store.GetSandbox(ctx, agentID)
	if err != nil {
		return "", err
	}
	if sb.DriverID == "" {
		return "", errors.New("sandbox was never created")
	}
	return ID(sb.DriverID), nil
}

// Start wakes a sleeping sandbox (or retries one in error).
func (m *Manager) Start(ctx context.Context, agentID string) error {
	sb, err := m.store.GetSandbox(ctx, agentID)
	if err != nil {
		return notFound(err)
	}
	if sb.State == string(PhaseError) && sb.DriverID == "" {
		// Creation failed before the driver produced a sandbox: retry it.
		if err := m.transition(ctx, agentID, []Phase{PhaseError}, PhaseCreating); err != nil {
			return err
		}
		m.background(agentID, "create", m.create(agentID))
		return nil
	}
	if err := m.transition(ctx, agentID, startFrom, PhaseStarting); err != nil {
		return err
	}
	m.background(agentID, "start", func(ctx context.Context) error {
		id, err := m.driverID(ctx, agentID)
		if err != nil {
			return err
		}
		return m.boot(ctx, agentID, id)
	})
	return nil
}

// Stop puts a sandbox to sleep; its disk is kept.
func (m *Manager) Stop(ctx context.Context, agentID string) error {
	if err := m.transition(ctx, agentID, stopFrom, PhaseStopping); err != nil {
		return err
	}
	m.background(agentID, "stop", func(ctx context.Context) error {
		id, err := m.driverID(ctx, agentID)
		if err != nil {
			return err
		}
		if err := m.driver.Stop(ctx, id); err != nil {
			return err
		}
		m.disconnect(agentID)
		m.set(agentID, PhaseSleeping, "")
		return nil
	})
	return nil
}

// Rebuild recreates the sandbox from the agent's current settings, keeping the
// home volume. A running sandbox is started again afterwards.
func (m *Manager) Rebuild(ctx context.Context, agentID string) error {
	sb, err := m.store.GetSandbox(ctx, agentID)
	if err != nil {
		return notFound(err)
	}
	wasRunning := sb.State == string(PhaseRunning)
	if err := m.transition(ctx, agentID, rebuildFrom, PhaseRebuilding); err != nil {
		return err
	}
	m.background(agentID, "rebuild", func(ctx context.Context) error {
		a, err := m.store.GetAgent(ctx, agentID)
		if err != nil {
			return err
		}
		spec := m.spec(a)
		sb, err := m.store.GetSandbox(ctx, agentID)
		if err != nil {
			return err
		}
		id := ID(sb.DriverID)
		if id == "" {
			if id, err = m.driver.Create(ctx, spec); err != nil {
				return err
			}
		} else if err := m.driver.Rebuild(ctx, id, spec); err != nil {
			return err
		}
		if err := m.store.SetSandboxBuilt(ctx, db.SetSandboxBuiltParams{DriverID: string(id), AppliedSpec: SpecJSON(spec), AgentID: agentID}); err != nil {
			return err
		}
		if !wasRunning {
			m.set(agentID, PhaseSleeping, "")
			return nil
		}
		m.set(agentID, PhaseStarting, "")
		return m.boot(ctx, agentID, id)
	})
	return nil
}

func (m *Manager) disconnect(agentID string) {
	if m.opts.Tunnel != nil {
		m.opts.Tunnel.Disconnect(agentID)
	}
}

// Destroy removes the sandbox, its home volume and finally the agent row.
func (m *Manager) Destroy(ctx context.Context, agentID string) error {
	if err := m.transition(ctx, agentID, destroyFrom, PhaseDestroying); err != nil {
		return err
	}
	m.background(agentID, "destroy", m.destroy(agentID))
	return nil
}

func (m *Manager) destroy(agentID string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sb, err := m.store.GetSandbox(ctx, agentID)
		if err != nil {
			return err
		}
		if sb.DriverID != "" {
			if err := m.driver.Destroy(ctx, ID(sb.DriverID), DestroyOpts{}); err != nil {
				return err
			}
		}
		m.disconnect(agentID)
		if err := m.store.DeleteAgent(ctx, agentID); err != nil {
			return err
		}
		m.locks.Delete(agentID)
		m.notify(agentID, PhaseDestroying)
		return nil
	}
}

// Reconcile aligns persisted state with the driver after a (re)start of the
// server: interrupted operations are resolved from what actually exists.
func (m *Manager) Reconcile(ctx context.Context) error {
	sbs, err := m.store.ListSandboxes(ctx)
	if err != nil {
		return err
	}
	for _, sb := range sbs {
		phase := Phase(sb.State)
		switch {
		case phase == PhaseDestroying:
			m.background(sb.AgentID, "destroy", m.destroy(sb.AgentID))
			continue
		case sb.DriverID == "":
			if phase == PhaseCreating {
				m.background(sb.AgentID, "create", m.create(sb.AgentID))
			}
			continue
		}
		st, err := m.driver.Status(ctx, ID(sb.DriverID))
		if err != nil {
			m.log.Warn("reconcile: status", "agent", sb.AgentID, "err", err)
			continue
		}
		var actual Phase
		msg := ""
		switch st.State {
		case StateRunning:
			actual = PhaseRunning
			// A recreated server container is not on the agent's network yet.
			if at, ok := m.driver.(Attacher); ok {
				if err := at.Attach(ctx, ID(sb.DriverID)); err != nil {
					m.log.Warn("reconcile: reattach", "agent", sb.AgentID, "err", err)
				}
			}
		case StateCreated, StateStopped:
			actual = PhaseSleeping
		case StateMissing:
			actual, msg = PhaseError, "sandbox no longer exists in the driver; rebuild to recreate it"
		}
		if actual == phase && (phase != PhaseError || sb.Error != "") {
			continue
		}
		if phase == PhaseError && actual != PhaseError {
			continue // keep the error visible until someone acts on it
		}
		m.log.Info("reconcile", "agent", sb.AgentID, "persisted", phase, "actual", actual)
		m.set(sb.AgentID, actual, msg)
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
