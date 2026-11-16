package sandbox_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store/db"
)

// provisionClone inserts the clone's rows like the API does and returns the
// function that starts the copy (nil if the source cannot be cloned).
func (f *fixture) provisionClone(src, dst string) (func(), error) {
	f.t.Helper()
	ctx := f.t.Context()
	var start func()
	err := f.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateClonedAgent(ctx, db.CreateClonedAgentParams{
			ID: dst, OwnerID: "usr_1", Name: dst, Image: "img", Harness: "claude-code", Cpus: 1, MemoryMb: 512,
			IdleMinutes: 15, ClonedFrom: src, CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			return err
		}
		var err error
		start, err = f.m.ProvisionClone(ctx, q, src, dst)
		return err
	})
	return start, err
}

func (f *fixture) clone(src, dst string) {
	f.t.Helper()
	start, err := f.provisionClone(src, dst)
	must(f.t, err)
	start()
	f.m.Wait()
}

func (f *fixture) home(id string) string {
	sb, ok := f.driver.Get(sandbox.ID("fake-" + id))
	if !ok {
		f.t.Fatalf("no sandbox for %s", id)
	}
	return sb.HomeDir
}

func (f *fixture) starts(id string) int {
	sb, _ := f.driver.Get(sandbox.ID("fake-" + id))
	return sb.Starts
}

func TestCloneOfRunningSourceStopsAndRestartsIt(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_src", "img")
	must(t, os.WriteFile(filepath.Join(f.home("ag_src"), "notes.txt"), []byte("hello"), 0o600))

	f.clone("ag_src", "ag_dst")

	f.expectPhase("ag_dst", sandbox.PhaseSleeping)
	f.expectPhase("ag_src", sandbox.PhaseRunning)
	if got := f.starts("ag_src"); got != 2 {
		t.Fatalf("source started %d times, want 2 (before and after the copy)", got)
	}
	if b, err := os.ReadFile(filepath.Join(f.home("ag_dst"), "notes.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("clone home: %q %v", b, err)
	}
	if sb, _ := f.driver.Get("fake-ag_src"); sb.State != sandbox.StateRunning {
		t.Fatalf("source driver state %s", sb.State)
	}
	sb, err := f.st.GetSandbox(t.Context(), "ag_src")
	if err != nil || sb.Resume != 0 {
		t.Fatalf("resume flag left set: %+v %v", sb, err)
	}
	// The clone is a machine of its own.
	must(t, f.m.Start(t.Context(), "ag_dst"))
	f.expectPhase("ag_dst", sandbox.PhaseRunning)
	f.expectPhase("ag_src", sandbox.PhaseRunning)
}

func TestCloneOfSleepingSourceLeavesItAsleep(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_src", "img")
	must(t, f.m.Stop(t.Context(), "ag_src"))
	f.m.Wait()
	f.clone("ag_src", "ag_dst")
	f.expectPhase("ag_dst", sandbox.PhaseSleeping)
	f.expectPhase("ag_src", sandbox.PhaseSleeping)
	if got := f.starts("ag_src"); got != 1 {
		t.Fatalf("a sleeping source was started again: %d starts", got)
	}
}

func TestCloneNeedsARestingSource(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_src", "img")
	start, err := f.provisionClone("ag_src", "ag_one")
	must(t, err)
	// Held by the first clone: nothing else gets to clone, start or stop it.
	if _, err := f.provisionClone("ag_src", "ag_two"); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatalf("second clone: %v", err)
	}
	if _, err := f.st.GetAgent(t.Context(), "ag_two"); err == nil {
		t.Fatal("a refused clone left its agent row behind")
	}
	if err := f.m.Start(t.Context(), "ag_src"); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatalf("start of a source being cloned: %v", err)
	}
	if err := f.m.Stop(t.Context(), "ag_src"); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatalf("stop of a source being cloned: %v", err)
	}
	if err := f.m.Rebuild(t.Context(), "ag_src"); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatalf("rebuild of a source being cloned: %v", err)
	}
	start()
	f.m.Wait()
	f.expectPhase("ag_src", sandbox.PhaseRunning)

	// A source that never got a sandbox cannot be cloned.
	f.driver.FailNext("create", errors.New("no image"))
	f.createAgent("ag_broken", "img")
	if _, err := f.provisionClone("ag_broken", "ag_three"); !errors.Is(err, sandbox.ErrConflict) {
		t.Fatalf("clone of a broken source: %v", err)
	}
}

func TestFailedCloneGivesTheSourceBack(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_src", "img")
	f.driver.FailNext("clone", errors.New("disk full"))
	f.clone("ag_src", "ag_dst")
	f.expectPhase("ag_src", sandbox.PhaseRunning)
	f.expectPhase("ag_dst", sandbox.PhaseError)
	if _, msg := f.phase("ag_dst"); msg != "clone failed: disk full" {
		t.Fatalf("error = %q", msg)
	}
	// The unfinished clone is not a blank machine waiting to be started.
	if err := f.m.Start(t.Context(), "ag_dst"); !errors.Is(err, sandbox.ErrCloneFailed) {
		t.Fatalf("start of an unfinished clone: %v", err)
	}
	must(t, f.m.Destroy(t.Context(), "ag_dst"))
	f.m.Wait()
	if _, err := f.st.GetAgent(t.Context(), "ag_dst"); err == nil {
		t.Fatal("unfinished clone survived its deletion")
	}

	// A source that will not stop is an error, and says so.
	f.driver.FailNext("stop", errors.New("hung"))
	f.clone("ag_src", "ag_dst2")
	f.expectPhase("ag_dst2", sandbox.PhaseError)
	f.expectPhase("ag_src", sandbox.PhaseError)
}

func TestTemplateCannotStart(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_tpl", "img")
	must(t, f.m.Stop(t.Context(), "ag_tpl"))
	f.m.Wait()
	must(t, f.st.SetAgentTemplate(t.Context(), db.SetAgentTemplateParams{IsTemplate: 1, ID: "ag_tpl"}))
	if err := f.m.Start(t.Context(), "ag_tpl"); !errors.Is(err, sandbox.ErrTemplate) {
		t.Fatalf("start of a template: %v", err)
	}
	f.expectPhase("ag_tpl", sandbox.PhaseSleeping)
	// A template is cloned like any machine at rest.
	f.clone("ag_tpl", "ag_new")
	f.expectPhase("ag_new", sandbox.PhaseSleeping)
	f.expectPhase("ag_tpl", sandbox.PhaseSleeping)
	must(t, f.st.SetAgentTemplate(t.Context(), db.SetAgentTemplateParams{IsTemplate: 0, ID: "ag_tpl"}))
	must(t, f.m.Start(t.Context(), "ag_tpl"))
	f.expectPhase("ag_tpl", sandbox.PhaseRunning)
}

// restart is a fresh manager over the same database and driver, the way the
// server comes back after a crash.
func (f *fixture) restart() *sandbox.Manager {
	f.t.Helper()
	m2 := sandbox.NewManager(f.st, f.driver, specFor, nil)
	f.t.Cleanup(m2.Close)
	must(f.t, m2.Reconcile(f.t.Context()))
	m2.Wait()
	f.m = m2
	return m2
}

// A crash before the copy began: the destination has no disks, the source is
// held and still running. Reconcile runs the clone and boots the source again.
func TestReconcileResumesACloneThatNeverStarted(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_src", "img")
	must(t, os.WriteFile(filepath.Join(f.home("ag_src"), "notes.txt"), []byte("hello"), 0o600))
	_, err := f.provisionClone("ag_src", "ag_dst") // the start function is never called: the server died
	must(t, err)
	f.m.Wait()
	if p, _ := f.phase("ag_src"); p != sandbox.PhaseStopping {
		t.Fatalf("source = %s", p)
	}
	if sb, _ := f.st.GetSandbox(t.Context(), "ag_src"); sb.Resume != 1 {
		t.Fatal("the intent to boot the source again was not persisted")
	}

	f.restart()

	f.expectPhase("ag_dst", sandbox.PhaseSleeping)
	f.expectPhase("ag_src", sandbox.PhaseRunning)
	if b, err := os.ReadFile(filepath.Join(f.home("ag_dst"), "notes.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("clone home: %q %v", b, err)
	}
	if got := f.starts("ag_src"); got != 2 {
		t.Fatalf("source started %d times", got)
	}
}

// A crash after the disks were copied and recorded, before the source came back.
func TestReconcileFinishesACloneThatCopiedEverything(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.createAgent("ag_src", "img")
	_, err := f.provisionClone("ag_src", "ag_dst")
	must(t, err)
	// What the job did before the crash: stop the source, copy, record the clone.
	must(t, f.driver.Stop(ctx, "fake-ag_src"))
	dst, _ := f.st.GetAgent(ctx, "ag_dst")
	id, err := f.driver.Clone(ctx, "fake-ag_src", specFor(dst))
	must(t, err)
	must(t, f.st.SetSandboxBuilt(ctx, db.SetSandboxBuiltParams{DriverID: string(id), AgentID: "ag_dst"}))

	f.restart()

	f.expectPhase("ag_dst", sandbox.PhaseSleeping)
	f.expectPhase("ag_src", sandbox.PhaseRunning)
	if sb, _ := f.st.GetSandbox(ctx, "ag_src"); sb.Resume != 0 {
		t.Fatal("resume flag not cleared")
	}
}

// A source that was asleep stays asleep through the same crash.
func TestReconcileDoesNotWakeASourceThatWasAsleep(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_src", "img")
	must(t, f.m.Stop(t.Context(), "ag_src"))
	f.m.Wait()
	_, err := f.provisionClone("ag_src", "ag_dst")
	must(t, err)
	f.restart()
	f.expectPhase("ag_dst", sandbox.PhaseSleeping)
	f.expectPhase("ag_src", sandbox.PhaseSleeping)
}

// If the source disappeared meanwhile the clone ends in error, with a reason.
func TestReconcileCloneWhoseSourceIsGone(t *testing.T) {
	f := newFixture(t)
	f.createAgent("ag_src", "img")
	_, err := f.provisionClone("ag_src", "ag_dst")
	must(t, err)
	must(t, f.st.DeleteAgent(t.Context(), "ag_src"))
	f.restart()
	f.expectPhase("ag_dst", sandbox.PhaseError)
	if _, msg := f.phase("ag_dst"); msg != "clone failed: the source machine was deleted" {
		t.Fatalf("error = %q", msg)
	}
}
