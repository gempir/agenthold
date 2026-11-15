//go:build docker

package docker

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// TestManagerAcceptance is the M2 acceptance check: creating an agent starts a
// container, stop and start keep files in /home/agent, and state survives a
// server restart (a new manager over the same database).
func TestManagerAcceptance(t *testing.T) {
	d := newTestDriver(t)
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "agenthold.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.CreateUser(ctx, db.CreateUserParams{ID: "usr_1", CreatedAt: 1, UpdatedAt: 1}))

	spec := func(a db.Agent) sandbox.Spec {
		s := sandbox.BaseSpec(a)
		s.Entrypoint = []string{"/bin/sh", "-c", "echo boot >> /home/agent/boots.log; exec sleep infinity"}
		return s
	}
	m := sandbox.NewManager(st, d, spec, nil)
	agentID := strings.ToLower(id.New("ag"))
	var provision func()
	must(st.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{
			ID: agentID, OwnerID: "usr_1", Name: "a", Image: testImage,
			Harness: "claude-code", Cpus: 0.5, MemoryMb: 256, IdleMinutes: 15, CreatedAt: 1, UpdatedAt: 1,
		}); err != nil {
			return err
		}
		var err error
		provision, err = m.Provision(ctx, q, agentID)
		return err
	}))
	provision()
	m.Wait()
	sid := sandbox.ID(d.containerName(agentID))
	t.Cleanup(func() { _ = d.Destroy(t.Context(), sid, sandbox.DestroyOpts{}) })

	phase := func() sandbox.Phase {
		sb, err := st.GetSandbox(ctx, agentID)
		must(err)
		if sb.Error != "" {
			t.Logf("sandbox error: %s", sb.Error)
		}
		return sandbox.Phase(sb.State)
	}
	if p := phase(); p != sandbox.PhaseRunning {
		t.Fatalf("after create: %s", p)
	}
	if st, _ := d.Status(ctx, sid); st.State != sandbox.StateRunning {
		t.Fatalf("container state: %s", st.State)
	}

	must(m.Stop(ctx, agentID))
	m.Wait()
	must(m.Start(ctx, agentID))
	m.Wait()
	if got := readFile(t, d, string(sid), "/home/agent/boots.log"); got != "boot\nboot\n" {
		t.Fatalf("boots.log = %q", got)
	}
	m.Close()

	// Restart: the container keeps running, a new manager agrees.
	m2 := sandbox.NewManager(st, d, spec, nil)
	defer m2.Close()
	must(m2.Reconcile(ctx))
	m2.Wait()
	if p := phase(); p != sandbox.PhaseRunning {
		t.Fatalf("after restart: %s", p)
	}

	// Something stops the container behind the server's back; a restart notices.
	must(d.Stop(ctx, sid))
	m3 := sandbox.NewManager(st, d, spec, nil)
	defer m3.Close()
	must(m3.Reconcile(ctx))
	if p := phase(); p != sandbox.PhaseSleeping {
		t.Fatalf("after external stop: %s", p)
	}

	must(m3.Destroy(ctx, agentID))
	m3.Wait()
	if st, _ := d.Status(ctx, sid); st.State != sandbox.StateMissing {
		t.Fatalf("after destroy: %s", st.State)
	}
}
