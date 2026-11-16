// Package fake is an in-process sandbox driver for tests and for UI work
// without Docker. Each sandbox's home is a real directory, so persistence
// across stop, start and rebuild can be tested.
package fake

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gempir/agenthold/internal/sandbox"
)

// Sandbox is the fake's record of one sandbox.
type Sandbox struct {
	Spec    sandbox.Spec
	State   sandbox.State
	HomeDir string
	Starts  int
	Boot    sandbox.Boot
	// ClonedFrom is the sandbox this one was cloned from, if any.
	ClonedFrom sandbox.ID
}

// Driver implements sandbox.Driver in memory.
type Driver struct {
	dir string

	mu        sync.Mutex
	sandboxes map[sandbox.ID]*Sandbox
	// FailNext, if set, makes the next call of that operation fail ("create", "start", ...).
	failNext map[string]error
	// OnStart is called after a sandbox starts.
	OnStart func(id sandbox.ID, sb Sandbox, boot sandbox.Boot)
	// OnStop is called before a sandbox stops.
	OnStop func(id sandbox.ID)
	// OnClone is called while a clone copies, before it is registered. It may
	// block (to hold a clone in flight) and fail the clone.
	OnClone func(ctx context.Context, src sandbox.ID) error
}

var _ sandbox.Driver = (*Driver)(nil)

// New returns a fake driver keeping home directories under dir.
func New(dir string) *Driver {
	return &Driver{dir: dir, sandboxes: map[sandbox.ID]*Sandbox{}, failNext: map[string]error{}}
}

// Name implements sandbox.Driver.
func (d *Driver) Name() string { return "fake" }

// FailNext makes the next call of op return err.
func (d *Driver) FailNext(op string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failNext[op] = err
}

func (d *Driver) fail(op string) error {
	if err, ok := d.failNext[op]; ok {
		delete(d.failNext, op)
		return err
	}
	return nil
}

// Get returns a copy of a sandbox's record.
func (d *Driver) Get(id sandbox.ID) (Sandbox, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sb, ok := d.sandboxes[id]
	if !ok {
		return Sandbox{}, false
	}
	return *sb, true
}

// Forget drops a sandbox as if it disappeared outside the platform.
func (d *Driver) Forget(id sandbox.ID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sandboxes, id)
}

// Create implements sandbox.Driver.
func (d *Driver) Create(_ context.Context, spec sandbox.Spec) (sandbox.ID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.fail("create"); err != nil {
		return "", err
	}
	id := sandbox.ID("fake-" + spec.AgentID)
	if _, ok := d.sandboxes[id]; ok {
		return "", fmt.Errorf("sandbox %s already exists", id)
	}
	home := filepath.Join(d.dir, spec.AgentID, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	d.sandboxes[id] = &Sandbox{Spec: spec, State: sandbox.StateCreated, HomeDir: home}
	return id, nil
}

// Start implements sandbox.Driver.
func (d *Driver) Start(_ context.Context, id sandbox.ID, boot sandbox.Boot) error {
	d.mu.Lock()
	if err := d.fail("start"); err != nil {
		d.mu.Unlock()
		return err
	}
	sb, ok := d.sandboxes[id]
	if !ok {
		d.mu.Unlock()
		return sandbox.ErrNotFound
	}
	if sb.State == sandbox.StateRunning {
		d.mu.Unlock()
		return nil
	}
	sb.State = sandbox.StateRunning
	sb.Starts++
	sb.Boot = boot
	hook, copySB := d.OnStart, *sb
	d.mu.Unlock()
	if hook != nil {
		hook(id, copySB, boot)
	}
	return nil
}

// Stop implements sandbox.Driver.
func (d *Driver) Stop(_ context.Context, id sandbox.ID) error {
	d.mu.Lock()
	if err := d.fail("stop"); err != nil {
		d.mu.Unlock()
		return err
	}
	sb, ok := d.sandboxes[id]
	if !ok {
		d.mu.Unlock()
		return sandbox.ErrNotFound
	}
	wasRunning := sb.State == sandbox.StateRunning
	sb.State = sandbox.StateStopped
	hook := d.OnStop
	d.mu.Unlock()
	if wasRunning && hook != nil {
		hook(id)
	}
	return nil
}

// Destroy implements sandbox.Driver.
func (d *Driver) Destroy(ctx context.Context, id sandbox.ID, opts sandbox.DestroyOpts) error {
	sb, ok := d.Get(id)
	if !ok {
		return nil
	}
	if sb.State == sandbox.StateRunning {
		if err := d.Stop(ctx, id); err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.fail("destroy"); err != nil {
		return err
	}
	delete(d.sandboxes, id)
	if !opts.KeepHome {
		return os.RemoveAll(filepath.Dir(sb.HomeDir))
	}
	return nil
}

// Status implements sandbox.Driver.
func (d *Driver) Status(_ context.Context, id sandbox.ID) (sandbox.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sb, ok := d.sandboxes[id]
	if !ok {
		return sandbox.Status{State: sandbox.StateMissing}, nil
	}
	return sandbox.Status{State: sb.State, GatewayIP: "127.0.0.1"}, nil
}

// Rebuild implements sandbox.Driver.
func (d *Driver) Rebuild(ctx context.Context, id sandbox.ID, spec sandbox.Spec) error {
	if sb, ok := d.Get(id); ok && sb.State == sandbox.StateRunning {
		if err := d.Stop(ctx, id); err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.fail("rebuild"); err != nil {
		return err
	}
	home := filepath.Join(d.dir, spec.AgentID, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	prev := d.sandboxes[id]
	starts := 0
	if prev != nil {
		starts = prev.Starts
	}
	d.sandboxes[id] = &Sandbox{Spec: spec, State: sandbox.StateStopped, HomeDir: home, Starts: starts}
	return nil
}

// Clone implements sandbox.Driver: the clone's home is a copy of the source's.
func (d *Driver) Clone(ctx context.Context, src sandbox.ID, spec sandbox.Spec) (sandbox.ID, error) {
	d.mu.Lock()
	hook := d.OnClone
	d.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, src); err != nil {
			return "", err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.fail("clone"); err != nil {
		return "", err
	}
	from, ok := d.sandboxes[src]
	if !ok {
		return "", sandbox.ErrNotFound
	}
	if from.State == sandbox.StateRunning {
		return "", fmt.Errorf("%w: %s", sandbox.ErrRunning, src)
	}
	id := sandbox.ID("fake-" + spec.AgentID)
	if _, ok := d.sandboxes[id]; ok {
		return id, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	home := filepath.Join(d.dir, spec.AgentID, "home")
	if err := os.RemoveAll(home); err != nil {
		return "", err
	}
	if err := os.CopyFS(home, os.DirFS(from.HomeDir)); err != nil {
		_ = os.RemoveAll(filepath.Dir(home))
		return "", err
	}
	d.sandboxes[id] = &Sandbox{Spec: spec, State: sandbox.StateCreated, HomeDir: home, ClonedFrom: src}
	return id, nil
}
