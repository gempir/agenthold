package sandbox_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

type fixture struct {
	t      *testing.T
	st     *store.Store
	driver *fake.Driver
	m      *sandbox.Manager
}

func specFor(a db.Agent) sandbox.Spec {
	return sandbox.Spec{AgentID: a.ID, Image: a.Image, Resources: sandbox.Resources{CPUs: a.Cpus, MemoryMB: a.MemoryMb}}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	must(t, st.CreateUser(ctx, db.CreateUserParams{ID: "usr_1", CreatedAt: 1, UpdatedAt: 1}))
	f := &fixture{t: t, st: st, driver: fake.New(t.TempDir())}
	f.m = sandbox.NewManager(st, f.driver, specFor, nil)
	t.Cleanup(f.m.Close)
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) createAgent(id, image string) {
	f.t.Helper()
	ctx := f.t.Context()
	var start func()
	must(f.t, f.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{
			ID: id, OwnerID: "usr_1", Name: id, Image: image,
			Harness: "claude-code", Cpus: 1, MemoryMb: 512, IdleMinutes: 15, CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			return err
		}
		var err error
		start, err = f.m.Provision(ctx, q, id)
		return err
	}))
	start()
	f.m.Wait()
}

func (f *fixture) phase(id string) (sandbox.Phase, string) {
	f.t.Helper()
	sb, err := f.st.GetSandbox(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return sandbox.Phase(sb.State), sb.Error
}

func (f *fixture) expectPhase(id string, want sandbox.Phase) {
	f.t.Helper()
	f.m.Wait()
	if got, msg := f.phase(id); got != want {
		f.t.Fatalf("phase = %s (%s), want %s", got, msg, want)
	}
}

func TestLifecycleKeepsHome(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.createAgent("ag_1", "debian:12")
	f.expectPhase("ag_1", sandbox.PhaseRunning)

	sb, _ := f.driver.Get("fake-ag_1")
	must(t, os.WriteFile(filepath.Join(sb.HomeDir, "notes.txt"), []byte("hello"), 0o600))

	must(t, f.m.Stop(ctx, "ag_1"))
	f.expectPhase("ag_1", sandbox.PhaseSleeping)
	if err := f.m.Stop(ctx, "ag_1"); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatalf("stop while sleeping: %v", err)
	}

	must(t, f.m.Start(ctx, "ag_1"))
	f.expectPhase("ag_1", sandbox.PhaseRunning)
	if err := f.m.Start(ctx, "ag_1"); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatalf("start while running: %v", err)
	}

	// Change the image: a rebuild is pending until applied, and home survives it.
	must(t, f.st.UpdateAgent(ctx, db.UpdateAgentParams{ID: "ag_1", Name: "ag_1", Image: "ubuntu:24.04", Cpus: 1, MemoryMb: 512, IdleMinutes: 15}))
	row, _ := f.st.GetAgentWithSandbox(ctx, "ag_1")
	if !f.m.NeedsRebuild(row.Agent, row.Sandbox) {
		t.Fatal("expected pending rebuild")
	}
	must(t, f.m.Rebuild(ctx, "ag_1"))
	f.expectPhase("ag_1", sandbox.PhaseRunning) // was running, so it is started again
	sb, _ = f.driver.Get("fake-ag_1")
	if sb.Spec.Image != "ubuntu:24.04" {
		t.Fatalf("image after rebuild = %s", sb.Spec.Image)
	}
	if b, err := os.ReadFile(filepath.Join(sb.HomeDir, "notes.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("home after rebuild: %q, %v", b, err)
	}
	row, _ = f.st.GetAgentWithSandbox(ctx, "ag_1")
	if f.m.NeedsRebuild(row.Agent, row.Sandbox) {
		t.Fatal("rebuild still pending")
	}

	must(t, f.m.Destroy(ctx, "ag_1"))
	f.m.Wait()
	if _, err := f.st.GetAgent(ctx, "ag_1"); err == nil {
		t.Fatal("agent row still exists")
	}
	if _, ok := f.driver.Get("fake-ag_1"); ok {
		t.Fatal("sandbox still exists")
	}
	if _, err := os.Stat(sb.HomeDir); !os.IsNotExist(err) {
		t.Fatalf("home dir still exists: %v", err)
	}
	if err := f.m.Start(ctx, "ag_1"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("start destroyed: %v", err)
	}
}

func TestCreateFailureCanBeRetried(t *testing.T) {
	f := newFixture(t)
	f.driver.FailNext("create", errors.New("image not found"))
	f.createAgent("ag_1", "nope")
	f.expectPhase("ag_1", sandbox.PhaseError)
	if _, msg := f.phase("ag_1"); msg != "create failed: image not found" {
		t.Fatalf("error = %q", msg)
	}
	must(t, f.m.Start(t.Context(), "ag_1"))
	f.expectPhase("ag_1", sandbox.PhaseRunning)
}

func TestStartFailureThenRetry(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_1", "img")
	must(t, f.m.Stop(t.Context(), "ag_1"))
	f.m.Wait()
	f.driver.FailNext("start", errors.New("boom"))
	must(t, f.m.Start(t.Context(), "ag_1"))
	f.expectPhase("ag_1", sandbox.PhaseError)
	must(t, f.m.Start(t.Context(), "ag_1"))
	f.expectPhase("ag_1", sandbox.PhaseRunning)
}

func TestReconcileAfterRestart(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.createAgent("ag_run", "img")
	f.createAgent("ag_sleep", "img")
	f.createAgent("ag_gone", "img")
	f.createAgent("ag_stuck", "img")
	must(t, f.m.Stop(ctx, "ag_sleep"))
	f.m.Wait()
	f.driver.Forget("fake-ag_gone")
	// Simulate a crash in the middle of a stop: persisted "stopping", driver still running.
	must(t, f.st.SetSandboxState(ctx, db.SetSandboxStateParams{State: "stopping", AgentID: "ag_stuck"}))

	// "Restart": a fresh manager over the same database and driver.
	m2 := sandbox.NewManager(f.st, f.driver, specFor, nil)
	defer m2.Close()
	must(t, m2.Reconcile(ctx))
	m2.Wait()
	for id, want := range map[string]sandbox.Phase{
		"ag_run": sandbox.PhaseRunning, "ag_sleep": sandbox.PhaseSleeping,
		"ag_gone": sandbox.PhaseError, "ag_stuck": sandbox.PhaseRunning,
	} {
		if got, msg := f.phase(id); got != want {
			t.Errorf("%s: %s (%s), want %s", id, got, msg, want)
		}
	}
	// A vanished sandbox comes back with a rebuild.
	f.m = m2
	must(t, m2.Rebuild(ctx, "ag_gone"))
	f.expectPhase("ag_gone", sandbox.PhaseSleeping)
}

func TestConcurrentStartsOnlyOneWins(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_1", "img")
	must(t, f.m.Stop(t.Context(), "ag_1"))
	f.m.Wait()
	errs := make(chan error, 10)
	for range 10 {
		go func() { errs <- f.m.Start(t.Context(), "ag_1") }()
	}
	ok := 0
	for range 10 {
		if err := <-errs; err == nil {
			ok++
		} else if !errors.Is(err, sandbox.ErrConflict) {
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d starts succeeded, want 1", ok)
	}
	f.expectPhase("ag_1", sandbox.PhaseRunning)
}
