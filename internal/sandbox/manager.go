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

// ErrTemplate is returned when a template would be started, chatted with or
// scheduled. Templates are stopped machines that only exist to be cloned.
var ErrTemplate = errors.New("this machine is a template: it cannot be started, chatted with or scheduled. Clone it to get a machine, or convert it back to an agent")

// ErrCloneFailed is returned when a machine whose clone never finished is started.
var ErrCloneFailed = errors.New("this machine's clone did not finish: delete it and clone again")

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

// Backend is the lifecycle part of the node boundary. All calls carry a
// context and values so a remote node can implement it.
type Backend interface {
	Name(context.Context) (string, error)
	Create(context.Context, Spec) (ID, error)
	Start(context.Context, ID, Boot) error
	Stop(context.Context, ID) error
	Destroy(context.Context, ID, DestroyOpts) error
	Status(context.Context, ID) (Status, error)
	Rebuild(context.Context, ID, Spec) error
	Clone(context.Context, ID, Spec) (ID, error)
	Attach(context.Context, ID) error
	WaitConnected(context.Context, string) error
	Disconnect(context.Context, string) error
	SetTunnelToken(context.Context, string, string) error
}

type driverBackend struct {
	Driver
	tunnel Tunnel
}

func (d driverBackend) SetTunnelToken(context.Context, string, string) error { return nil }
func (d driverBackend) Name(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return d.Driver.Name(), nil
}

func (d driverBackend) Attach(ctx context.Context, id ID) error {
	if a, ok := d.Driver.(Attacher); ok {
		return a.Attach(ctx, id)
	}
	return ctx.Err()
}

func (d driverBackend) WaitConnected(ctx context.Context, id string) error {
	if d.tunnel != nil {
		return d.tunnel.WaitConnected(ctx, id)
	}
	return nil
}

func (d driverBackend) Disconnect(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.tunnel != nil {
		d.tunnel.Disconnect(id)
	}
	return nil
}

// Manager drives agents' sandboxes through their lifecycle. Every operation
// first moves the persisted state with a guarded UPDATE (so concurrent requests
// cannot both win) and then performs the driver call in the background.
type Manager struct {
	store  *store.Store
	driver Backend
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
	return NewManagerWithNode(st, driverBackend{Driver: d, tunnel: opts.Tunnel}, spec, opts, log)
}

// NewManagerWithNode constructs a controller lifecycle manager backed by a node.
func NewManagerWithNode(st *store.Store, d Backend, spec SpecFunc, opts Options, log *slog.Logger) *Manager {
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
	if err := m.driver.SetTunnelToken(ctx, agentID, hashToken(token)); err != nil {
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
	{
		wctx, cancel := context.WithTimeout(ctx, m.opts.ConnectTimeout)
		defer cancel()
		if err := m.driver.WaitConnected(wctx, agentID); err != nil {
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
	if p == PhaseError {
		_ = m.driver.SetTunnelToken(m.ctx, agentID, "")
	}
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
			if errors.Is(err, ErrUnreachable) {
				// Not the sandbox's fault: keep the transient phase, which
				// ReconcileNode resolves when the node reconnects.
				m.log.Warn("sandbox operation interrupted: node unreachable", "agent", agentID, "op", op, "err", err)
				return
			}
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
	var place Placement
	if p, ok := m.driver.(Placer); ok {
		if place, err = p.Place(ctx); err != nil {
			return nil, err
		}
	} else if place.Driver, err = m.driver.Name(ctx); err != nil {
		return nil, err
	}
	if err := q.CreateSandbox(ctx, db.CreateSandboxParams{
		AgentID: agentID, NodeID: place.NodeID, Driver: place.Driver, State: string(PhaseCreating), StateChangedAt: m.now().Unix(),
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

// ProvisionClone creates the sandbox row of a clone of srcID (inside the
// caller's transaction q, which also inserts the clone's agent row with
// cloned_from = srcID) and returns a function that starts the clone; call it
// after the transaction commits.
//
// The source is moved to stopping, which keeps every other operation off it
// while its disks are copied, and remembers whether to boot it again. A clone
// lives on the node of its source: disks never leave their node.
func (m *Manager) ProvisionClone(ctx context.Context, q *db.Queries, srcID, dstID string) (start func(), err error) {
	if err := m.Reachable(ctx, srcID); err != nil {
		return nil, err
	}
	src, err := q.GetSandbox(ctx, srcID)
	if err != nil {
		return nil, notFound(err)
	}
	if src.DriverID == "" {
		return nil, ErrConflict
	}
	// Each guarded update is atomic, so the phase we left is the one we saw.
	moved, wasRunning := false, false
	for _, from := range []Phase{PhaseRunning, PhaseSleeping} {
		n, err := q.TransitionSandbox(ctx, db.TransitionSandboxParams{
			ToState: string(PhaseStopping), Now: m.now().Unix(), AgentID: srcID, FromStates: phases([]Phase{from}),
		})
		if err != nil {
			return nil, err
		}
		if n > 0 {
			moved, wasRunning = true, from == PhaseRunning
			break
		}
	}
	if !moved {
		return nil, ErrConflict
	}
	resume := int64(0)
	if wasRunning {
		resume = 1
	}
	if err := q.SetSandboxResume(ctx, db.SetSandboxResumeParams{Resume: resume, AgentID: srcID}); err != nil {
		return nil, err
	}
	if err := q.CreateSandbox(ctx, db.CreateSandboxParams{
		AgentID: dstID, NodeID: src.NodeID, Driver: src.Driver, State: string(PhaseCreating), StateChangedAt: m.now().Unix(),
	}); err != nil {
		return nil, err
	}
	return func() {
		m.notify(srcID, PhaseStopping)
		m.background(dstID, "clone", m.clone(dstID))
	}, nil
}

// clone copies the disks of the clone's source into the new sandbox. It is
// resumable: after a restart of the server Reconcile runs it again, from the
// source being stopped and the destination having no disks.
func (m *Manager) clone(dstID string) func(ctx context.Context) error {
	return func(ctx context.Context) (err error) {
		dst, err := m.store.GetAgent(ctx, dstID)
		if err != nil {
			return err
		}
		srcID := dst.ClonedFrom
		unlock := m.lock(srcID)
		defer unlock()
		src, err := m.store.GetSandbox(ctx, srcID)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("the source machine was deleted")
		} else if err != nil {
			return err
		}
		stopped := false
		defer func() {
			if errors.Is(err, ErrUnreachable) {
				return // the node returns, and Reconcile runs the clone again
			}
			m.releaseSource(srcID, ID(src.DriverID), stopped)
		}()
		if src.DriverID == "" {
			return errors.New("the source machine has no sandbox to clone")
		}
		// Normally stopped already; not when the server restarted mid-clone.
		if err = m.driver.Stop(ctx, ID(src.DriverID)); err != nil {
			return fmt.Errorf("stop the source: %w", err)
		}
		stopped = true
		m.disconnect(srcID)
		spec := m.spec(dst)
		id, err := m.driver.Clone(ctx, ID(src.DriverID), spec)
		if err != nil {
			return err
		}
		if err = m.store.SetSandboxBuilt(ctx, db.SetSandboxBuiltParams{DriverID: string(id), AppliedSpec: SpecJSON(spec), AgentID: dstID}); err != nil {
			return err
		}
		// Not m.set: a destroy that arrived meanwhile wins.
		if err = m.transition(ctx, dstID, []Phase{PhaseCreating}, PhaseSleeping); errors.Is(err, ErrConflict) {
			err = nil
		}
		return err
	}
}

// releaseSource ends the hold a clone has on its source: it is booted again if
// it was running before, otherwise it goes back to sleep. A source that could
// not be stopped is an error, not asleep.
func (m *Manager) releaseSource(srcID string, driverID ID, stopped bool) {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Minute)
	defer cancel()
	sb, err := m.store.GetSandbox(ctx, srcID)
	if err != nil || Phase(sb.State) != PhaseStopping {
		return // deleted, or resolved by someone else
	}
	if err := m.store.SetSandboxResume(ctx, db.SetSandboxResumeParams{AgentID: srcID}); err != nil {
		m.log.Error("clear resume", "agent", srcID, "err", err)
	}
	switch {
	case !stopped:
		m.set(srcID, PhaseError, "could not stop the machine to clone it")
	case sb.Resume == 0:
		_ = m.transition(ctx, srcID, []Phase{PhaseStopping}, PhaseSleeping)
	default:
		if err := m.transition(ctx, srcID, []Phase{PhaseStopping}, PhaseStarting); err != nil {
			return
		}
		if err := m.boot(ctx, srcID, driverID); err != nil && !errors.Is(err, ErrUnreachable) {
			m.log.Warn("start after clone failed", "agent", srcID, "err", err)
			m.set(srcID, PhaseError, fmt.Sprintf("start failed after cloning: %v", err))
		}
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

// Reachable returns an error wrapping ErrUnreachable when the agent's node is
// disconnected. Lifecycle operations check it first so they fail fast.
func (m *Manager) Reachable(ctx context.Context, agentID string) error {
	if r, ok := m.driver.(Reachability); ok {
		return r.Reachable(ctx, agentID)
	}
	return nil
}

// Start wakes a sleeping sandbox (or retries one in error).
func (m *Manager) Start(ctx context.Context, agentID string) error {
	sb, err := m.store.GetSandbox(ctx, agentID)
	if err != nil {
		return notFound(err)
	}
	if err := m.Reachable(ctx, agentID); err != nil {
		return err
	}
	a, err := m.store.GetAgent(ctx, agentID)
	if err != nil {
		return notFound(err)
	}
	if a.IsTemplate == 1 {
		return ErrTemplate
	}
	if sb.State == string(PhaseError) && sb.DriverID == "" && a.ClonedFrom != "" {
		return ErrCloneFailed
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
	if err := m.Reachable(ctx, agentID); err != nil {
		return err
	}
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
	if err := m.Reachable(ctx, agentID); err != nil {
		return err
	}
	if err := m.transition(ctx, agentID, rebuildFrom, PhaseRebuilding); err != nil {
		return err
	}
	m.background(agentID, "reset system", func(ctx context.Context) error {
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
	_ = m.driver.SetTunnelToken(m.ctx, agentID, "")
	_ = m.driver.Disconnect(m.ctx, agentID)
}

// Destroy removes the sandbox, its home volume and finally the agent row.
func (m *Manager) Destroy(ctx context.Context, agentID string) error {
	if err := m.Reachable(ctx, agentID); err != nil {
		return err
	}
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
	return m.reconcile(ctx, sbs)
}

// ReconcileNode aligns the sandboxes of one node after it (re)connected.
func (m *Manager) ReconcileNode(ctx context.Context, nodeID string) error {
	sbs, err := m.store.ListNodeSandboxes(ctx, nodeID)
	if err != nil {
		return err
	}
	return m.reconcile(ctx, sbs)
}

// pendingClones finds the clones a restart interrupted before their disks
// existed, and the sources they hold (by id).
func (m *Manager) pendingClones(ctx context.Context, sbs []db.Sandbox) (clones, held map[string]bool, err error) {
	clones, held = map[string]bool{}, map[string]bool{}
	for _, sb := range sbs {
		if Phase(sb.State) != PhaseCreating || sb.DriverID != "" {
			continue
		}
		a, err := m.store.GetAgent(ctx, sb.AgentID)
		if err != nil {
			return nil, nil, err
		}
		if a.ClonedFrom != "" {
			clones[sb.AgentID], held[a.ClonedFrom] = true, true
		}
	}
	return clones, held, nil
}

func (m *Manager) reconcile(ctx context.Context, sbs []db.Sandbox) error {
	clones, held, err := m.pendingClones(ctx, sbs)
	if err != nil {
		return err
	}
	for _, sb := range sbs {
		if err := m.Reachable(ctx, sb.AgentID); err != nil {
			// Reconciled by ReconcileNode once the node connects.
			continue
		}
		if held[sb.AgentID] {
			continue // the clone job started below releases its source
		}
		phase := Phase(sb.State)
		if phase == PhaseRunning || phase == PhaseStarting {
			if err := m.driver.SetTunnelToken(ctx, sb.AgentID, sb.BootTokenHash); err != nil {
				return err
			}
		}
		switch {
		case phase == PhaseDestroying:
			m.background(sb.AgentID, "destroy", m.destroy(sb.AgentID))
			continue
		case sb.DriverID == "":
			switch {
			case clones[sb.AgentID]:
				m.background(sb.AgentID, "clone", m.clone(sb.AgentID))
			case phase == PhaseCreating:
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
			if err := m.driver.Attach(ctx, ID(sb.DriverID)); err != nil {
				m.log.Warn("reconcile: reattach", "agent", sb.AgentID, "err", err)
			}
		case StateCreated, StateStopped:
			actual = PhaseSleeping
		case StateMissing:
			actual, msg = PhaseError, "sandbox no longer exists on the node; reset the system to recreate it"
		}
		if actual == phase && (phase != PhaseError || sb.Error != "") {
			continue
		}
		if phase == PhaseError && actual != PhaseError {
			continue // keep the error visible until someone acts on it
		}
		m.log.Info("reconcile", "agent", sb.AgentID, "persisted", phase, "actual", actual)
		m.set(sb.AgentID, actual, msg)
		if sb.Resume == 1 && actual == PhaseSleeping {
			// It was stopped for a clone that has finished (or is gone): boot it again.
			if err := m.store.SetSandboxResume(ctx, db.SetSandboxResumeParams{AgentID: sb.AgentID}); err != nil {
				return err
			}
			if err := m.Start(ctx, sb.AgentID); err != nil {
				m.log.Warn("reconcile: start after clone", "agent", sb.AgentID, "err", err)
			}
		}
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
