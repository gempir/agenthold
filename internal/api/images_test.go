package api_test

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/config"
)

func waitImageBuildReady(t *testing.T, ops *client, imageID, buildID string) gen.ImageBuildDetail {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var b gen.ImageBuildDetail
		expect(t, "get image build", ops.do("GET", "/images/"+imageID+"/builds/"+buildID, nil, &b), 200)
		if b.Status == gen.ImageBuildDetailStatusReady || b.Status == gen.ImageBuildDetailStatusFailed {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("image build %s did not complete in time", buildID)
	return gen.ImageBuildDetail{}
}

func TestImagesAPIRBACAndCRUD(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"ops@example.com"} })
	ops := e.login("ops", "Ops", "ops@example.com")
	dev := e.login("dev", "Dev", "dev@example.com")

	// Regular users cannot create, update, build, or delete images.
	expect(t, "create as user", dev.do("POST", "/images", gen.ImageInput{Name: "nope"}, nil), 403)
	expect(t, "delete as user", dev.do("DELETE", "/images/img_1", nil, nil), 403)

	// Node list reports architecture.
	var nodes []gen.NodeInfo
	expect(t, "nodes", ops.do("GET", "/nodes", nil, &nodes), 200)
	if len(nodes) != 1 || nodes[0].Arch == nil || *nodes[0].Arch != runtime.GOARCH {
		t.Fatalf("nodes = %+v, want arch %s", nodes, runtime.GOARCH)
	}

	// Admin creates a custom image.
	var created gen.CustomImage
	expect(t, "create image", ops.do("POST", "/images", gen.ImageInput{
		Name:        "Python ML",
		Description: ptr("Python 3.12 with uv and ruff"),
		AptPackages: &[]string{"python3-venv", "ripgrep"},
		SetupScript: ptr("echo secret-setup-line"),
		Env:         &map[string]string{"PIP_DISABLE_PIP_VERSION_CHECK": "1"},
	}, &created), 201)
	if created.Name != "Python ML" || created.LatestRevision != 0 || created.IsDefault {
		t.Fatalf("created = %+v", created)
	}

	// Regular user sees the image in the list, with setupScript redacted.
	var list gen.ListImages200JSONResponse
	expect(t, "list images as user", dev.do("GET", "/images", nil, &list), 200)
	if list.Base.Name != "agenthold Base" || !list.Base.IsDefault {
		t.Fatalf("base = %+v", list.Base)
	}
	if len(list.Images) != 1 || list.Images[0].Id != created.Id {
		t.Fatalf("list = %+v", list)
	}
	if list.Images[0].SetupScript != "" {
		t.Fatalf("setupScript should be redacted for non-admin, got %q", list.Images[0].SetupScript)
	}

	// Regular user cannot trigger a build or read build logs.
	expect(t, "build as user", dev.do("POST", "/images/"+created.Id+"/builds", nil, nil), 403)
	expect(t, "list builds as user", dev.do("GET", "/images/"+created.Id+"/builds", nil, nil), 403)

	// Admin triggers a build and reads logs.
	var started []gen.ImageBuild
	expect(t, "start build", ops.do("POST", "/images/"+created.Id+"/builds", nil, &started), 202)
	if len(started) != 1 {
		t.Fatalf("started builds = %+v", started)
	}
	b := waitImageBuildReady(t, ops, created.Id, started[0].Id)
	if b.Status != gen.ImageBuildDetailStatusReady || b.Revision != 1 || b.Arch != runtime.GOARCH {
		t.Fatalf("build detail = %+v", b)
	}
	if !strings.Contains(b.Log, "Build finished") {
		t.Fatalf("build log = %q", b.Log)
	}

	// Image detail reflects revision 1 and build history for admin.
	var detail gen.CustomImageDetail
	expect(t, "get image detail", ops.do("GET", "/images/"+created.Id, nil, &detail), 200)
	if detail.LatestRevision != 1 || len(detail.ReadyArches) != 1 || detail.ReadyArches[0] != runtime.GOARCH || len(detail.Builds) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestAgentsAndAppsUseCustomImages(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"ops@example.com"} })
	ops := e.login("ops", "Ops", "ops@example.com")
	dev := e.login("dev", "Dev", "dev@example.com")

	// Create and build an image (rev 1).
	var img gen.CustomImage
	expect(t, "create image", ops.do("POST", "/images", gen.ImageInput{
		Name:        "Rust Toolchain",
		AptPackages: &[]string{"build-essential"},
		SetupScript: ptr("echo rust-v1"),
	}, &img), 201)

	// Cannot create an agent with an unbuilt image.
	expect(t, "agent unbuilt image", dev.do("POST", "/agents", gen.AgentInput{
		Name: "too-early", Harness: ptr("fake"), ImageId: &img.Id,
	}, nil), 409)

	var started []gen.ImageBuild
	expect(t, "build rev 1", ops.do("POST", "/images/"+img.Id+"/builds", nil, &started), 202)
	b1 := waitImageBuildReady(t, ops, img.Id, started[0].Id)
	if b1.Status != gen.ImageBuildDetailStatusReady {
		t.Fatalf("rev 1 failed: %+v", b1)
	}

	// Create an agent on rev 1.
	var ag gen.Agent
	expect(t, "create agent with image", dev.do("POST", "/agents", gen.AgentInput{
		Name: "rust-agent", Harness: ptr("fake"), ImageId: &img.Id,
	}, &ag), 201)
	if ag.ImageId == nil || *ag.ImageId != img.Id || ag.ImageRevision == nil || *ag.ImageRevision != 1 || ag.ImageName == nil || *ag.ImageName != "Rust Toolchain" {
		t.Fatalf("agent image fields = %+v", ag)
	}
	if ag.ImageUpdateAvailable != nil && *ag.ImageUpdateAvailable {
		t.Fatal("agent should not report updateAvailable on rev 1")
	}
	e.sandboxes.Wait()
	ag = dev.agent(ag.Id)
	if ag.State != gen.SandboxStateRunning || ag.NeedsRebuild {
		t.Fatalf("running agent = %+v", ag)
	}

	// Clone preserves image selection.
	expect(t, "stop before clone", dev.do("POST", "/agents/"+ag.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	var cloned gen.Agent
	expect(t, "clone agent", dev.do("POST", "/agents/"+ag.Id+"/clone", gen.CloneInput{Name: "rust-clone"}, &cloned), 201)
	if cloned.ImageId == nil || *cloned.ImageId != img.Id || cloned.ImageRevision == nil || *cloned.ImageRevision != 1 {
		t.Fatalf("cloned image fields = %+v", cloned)
	}
	e.sandboxes.Wait()

	// Build rev 2; existing agents report imageUpdateAvailable=true.
	started = nil
	expect(t, "build rev 2", ops.do("POST", "/images/"+img.Id+"/builds", nil, &started), 202)
	b2 := waitImageBuildReady(t, ops, img.Id, started[0].Id)
	if b2.Status != gen.ImageBuildDetailStatusReady || b2.Revision != 2 {
		t.Fatalf("rev 2 = %+v", b2)
	}
	ag = dev.agent(ag.Id)
	if ag.ImageUpdateAvailable == nil || !*ag.ImageUpdateAvailable {
		t.Fatalf("expected imageUpdateAvailable=true, got %+v", ag)
	}

	// Upgrade agent to rev 2 -> needsRebuild becomes true.
	var updated gen.Agent
	expect(t, "upgrade agent image", dev.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{
		ImageId: &img.Id, ImageRevision: ptr(int64(2)),
	}, &updated), 200)
	if updated.ImageRevision == nil || *updated.ImageRevision != 2 || !updated.NeedsRebuild {
		t.Fatalf("upgraded agent = %+v", updated)
	}
	if updated.ImageUpdateAvailable != nil && *updated.ImageUpdateAvailable {
		t.Fatalf("upgraded agent still says updateAvailable: %+v", updated)
	}

	// Create an app using the custom image (gets rev 2).
	var ap gen.App
	expect(t, "create app with image", dev.do("POST", "/apps", gen.AppInput{
		Name: "rust-app", ImageId: &img.Id,
	}, &ap), 201)
	if ap.ImageId == nil || *ap.ImageId != img.Id || ap.ImageRevision == nil || *ap.ImageRevision != 2 {
		t.Fatalf("app image fields = %+v", ap)
	}
	e.sandboxes.Wait()

	// Set org default: new agents without imageId inherit it; "base" overrides it.
	expect(t, "set default", ops.do("PATCH", "/images/"+img.Id, gen.ImageInput{
		Name:      "Rust Toolchain",
		IsDefault: ptr(true),
	}, &img), 200)
	var defAg gen.Agent
	expect(t, "agent inherits default", dev.do("POST", "/agents", gen.AgentInput{Name: "from-default", Harness: ptr("fake")}, &defAg), 201)
	if defAg.ImageId == nil || *defAg.ImageId != img.Id || defAg.ImageRevision == nil || *defAg.ImageRevision != 2 {
		t.Fatalf("default agent = %+v", defAg)
	}
	var baseAg gen.Agent
	expect(t, "agent explicit base", dev.do("POST", "/agents", gen.AgentInput{Name: "from-base", Harness: ptr("fake"), ImageId: ptr("base")}, &baseAg), 201)
	if (baseAg.ImageId != nil && *baseAg.ImageId != "") || baseAg.ImageName == nil || *baseAg.ImageName != "agenthold Base" {
		t.Fatalf("base agent = %+v", baseAg)
	}
	e.sandboxes.Wait()

	// Image cannot be deleted while still referenced.
	expect(t, "delete in use", ops.do("DELETE", "/images/"+img.Id, nil, nil), 409)

	// Switch app back to base and delete agents referencing the image.
	var patchedApp gen.App
	expect(t, "switch app to base", dev.do("PATCH", "/apps/"+ap.Id, gen.AppPatch{ImageId: ptr("base")}, &patchedApp), 200)
	if patchedApp.ImageId != nil && *patchedApp.ImageId != "" {
		t.Fatalf("switched app still has imageId: %+v", patchedApp)
	}
	for _, id := range []string{ag.Id, cloned.Id, defAg.Id} {
		expect(t, "delete agent", dev.do("DELETE", "/agents/"+id, nil, nil), 202)
	}
	e.sandboxes.Wait()

	// Now delete succeeds.
	expect(t, "delete unused image", ops.do("DELETE", "/images/"+img.Id, nil, nil), 204)
}
