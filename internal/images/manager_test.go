package images

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

func newTestManager(t *testing.T) (*Manager, *store.Store, *node.Local) {
	t.Helper()
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	nodeDir := t.TempDir()
	local := node.NewLocal(fake.New(t.TempDir()), nil)
	local.SetDataDir(nodeDir)
	local.SetLayerBuilder(FakeLayerBuilder(nodeDir))

	mgr := NewManager(Config{
		Store:   st,
		DataDir: t.TempDir(),
		Nodes:   SingleNodeProvider{Local: local},
	})
	if err := mgr.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr, st, local
}

func waitBuildDone(t *testing.T, mgr *Manager, imageID, buildID string) db.ImageBuild {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := mgr.GetBuild(t.Context(), imageID, buildID)
		if err != nil {
			t.Fatal(err)
		}
		if b.Status == "ready" || b.Status == "failed" {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("build %s did not finish in time", buildID)
	return db.ImageBuild{}
}

func TestValidateRecipe(t *testing.T) {
	bad := []Recipe{
		{Name: ""},
		{Name: "base"},
		{Name: "agenthold Base"},
		{Name: "ok", AptPackages: []string{"bad;rm -rf /"}},
		{Name: "ok", Env: map[string]string{"1BAD": "v"}},
		{Name: "ok", Env: map[string]string{"GOOD": "line1\nline2"}},
	}
	for _, r := range bad {
		r := r
		if err := ValidateRecipe(&r); !errors.Is(err, ErrValidation) {
			t.Fatalf("ValidateRecipe(%+v) = %v, want ErrValidation", r, err)
		}
	}
	good := Recipe{
		Name:        "  Python 3.12  ",
		AptPackages: []string{"ripgrep", "ripgrep", "python3-venv"},
		Env:         map[string]string{"PATH": "/opt/venv/bin:$PATH"},
	}
	if err := ValidateRecipe(&good); err != nil {
		t.Fatalf("ValidateRecipe(good): %v", err)
	}
	if good.Name != "Python 3.12" || len(good.AptPackages) != 2 {
		t.Fatalf("normalized = %+v", good)
	}
}

func TestManagerLifecycleBuildAndPrepare(t *testing.T) {
	mgr, st, _ := newTestManager(t)
	ctx := t.Context()

	// Create custom image.
	img, err := mgr.Create(ctx, "user_admin", Recipe{
		Name:        "data-science",
		Description: "Python + uv",
		AptPackages: []string{"python3-venv"},
		SetupScript: "echo installing-tools",
		Env:         map[string]string{"DATA_ENV": "1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if img.Row.LatestRev != 0 || len(img.ReadyArches) != 0 {
		t.Fatalf("fresh image = %+v", img)
	}
	// Duplicate name is rejected.
	if _, err := mgr.Create(ctx, "user_admin", Recipe{Name: "data-science"}); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate Create err = %v, want ErrNameTaken", err)
	}
	// Unbuilt image cannot be used for new machines.
	if _, _, _, err := mgr.ResolveForNewMachine(ctx, img.Row.ID); !errors.Is(err, ErrNotBuilt) {
		t.Fatalf("ResolveForNewMachine unbuilt = %v, want ErrNotBuilt", err)
	}

	// Build revision 1.
	builds, err := mgr.StartBuild(ctx, "user_admin", img.Row.ID)
	if err != nil || len(builds) != 1 {
		t.Fatalf("StartBuild = (%v, %v)", builds, err)
	}
	b1 := waitBuildDone(t, mgr, img.Row.ID, builds[0].ID)
	if b1.Status != "ready" || b1.Revision != 1 || b1.Arch != runtime.GOARCH {
		t.Fatalf("build 1 = %+v", b1)
	}
	if !strings.Contains(b1.LogText, "Build finished") {
		t.Fatalf("build 1 log = %q", b1.LogText)
	}

	// Set as org default and verify resolution.
	isDef := true
	img, err = mgr.Update(ctx, "user_admin", img.Row.ID, Recipe{
		Name:        "data-science",
		Description: "Python + uv",
		AptPackages: []string{"python3-venv"},
		SetupScript: "__AGENTHOLD_FAIL_BUILD__",
		Env:         map[string]string{"DATA_ENV": "1"},
		IsDefault:   &isDef,
	})
	if err != nil || img.Row.IsDefault != 1 {
		t.Fatalf("Update default = (%+v, %v)", img, err)
	}
	id, rev, arches, err := mgr.ResolveForNewMachine(ctx, "")
	if err != nil || id != img.Row.ID || rev != 1 || len(arches) != 1 || arches[0] != runtime.GOARCH {
		t.Fatalf("ResolveForNewMachine default = (%q, %d, %v, %v)", id, rev, arches, err)
	}
	// Explicit "base" overrides org default.
	id, rev, arches, err = mgr.ResolveForNewMachine(ctx, BuiltinBaseID)
	if err != nil || id != "" || rev != 0 || len(arches) != 0 {
		t.Fatalf("ResolveForNewMachine base = (%q, %d, %v, %v)", id, rev, arches, err)
	}

	// Build revision 2 fails; revision 1 remains the latest ready revision.
	builds2, err := mgr.StartBuild(ctx, "user_admin", img.Row.ID)
	if err != nil {
		t.Fatalf("StartBuild 2: %v", err)
	}
	b2 := waitBuildDone(t, mgr, img.Row.ID, builds2[0].ID)
	if b2.Status != "failed" || b2.Revision != 2 {
		t.Fatalf("build 2 = %+v, want failed rev 2", b2)
	}
	cur, err := mgr.Get(ctx, img.Row.ID)
	if err != nil || cur.Row.LatestRev != 1 {
		t.Fatalf("after failed build, latestRev = %d (err=%v), want 1", cur.Row.LatestRev, err)
	}

	// PrepareLayers pushes the cached layer to a clean node that lacks it.
	freshNodeDir := t.TempDir()
	freshNode := node.NewLocal(fake.New(t.TempDir()), nil)
	freshNode.SetDataDir(freshNodeDir)
	mgr.Nodes = SingleNodeProvider{Local: freshNode}
	if err := mgr.PrepareLayers(ctx, node.LocalID, db.Agent{ImageID: img.Row.ID, ImageRevision: 1}); err != nil {
		t.Fatalf("PrepareLayers: %v", err)
	}
	has, err := freshNode.HasLayer(ctx, LayerRef(img.Row.ID, 1), b1.LayerSha256)
	if err != nil || !has {
		t.Fatalf("fresh node HasLayer = (%v, %v), want true", has, err)
	}

	// Delete refuses while an agent references the image.
	now := time.Now().Unix()
	if err := st.CreateUser(ctx, db.CreateUserParams{ID: "u_1", Email: "u@e", Name: "u", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ag_1", OwnerID: "u_1", Name: "a1", Image: "base", Harness: "fake", Cpus: 1, MemoryMb: 512, IdleMinutes: 15, PermissionPolicy: "auto", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentImage(ctx, db.SetAgentImageParams{ImageID: img.Row.ID, ImageRevision: 1, UpdatedAt: now, ID: "ag_1"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete(ctx, img.Row.ID); !errors.Is(err, ErrImageInUse) {
		t.Fatalf("Delete in use = %v, want ErrImageInUse", err)
	}
	if err := st.SetAgentImage(ctx, db.SetAgentImageParams{ImageID: "", ImageRevision: 0, UpdatedAt: now, ID: "ag_1"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete(ctx, img.Row.ID); err != nil {
		t.Fatalf("Delete unused: %v", err)
	}
}

func TestManagerStartMarksStaleBuildsFailed(t *testing.T) {
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().Unix()
	if err := st.CreateImage(t.Context(), db.CreateImageParams{
		ID: "img_stale", Name: "stale", AptPackages: "[]", EnvJson: "{}", SecretIds: "[]", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateImageBuild(t.Context(), db.CreateImageBuildParams{
		ID: "bld_stale", ImageID: "img_stale", Revision: 1, Arch: runtime.GOARCH, NodeID: "local", Status: "building", RecipeJson: "{}", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(Config{Store: st, DataDir: t.TempDir()})
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	b, err := mgr.GetBuild(t.Context(), "img_stale", "bld_stale")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "failed" || !strings.Contains(b.Error, "interrupted") {
		t.Fatalf("stale build = %+v", b)
	}
}
