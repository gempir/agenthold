package firecracker

import (
	"context"
	"math"
	"net/netip"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/sandbox"
)

func testConfig(dir string) Config {
	return Config{DataDir: dir, BaseImagePath: "/base.ext4", KernelPath: "/kernel", FirecrackerPath: "/firecracker", JailerPath: "/jailer", SandboxdPath: "/sandboxd", NetworkPool: netip.MustParsePrefix("172.29.0.0/24")}
}

func TestConfigAndResources(t *testing.T) {
	cfg, err := testConfig("/tmp/agenthold").normalized()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UIDBase == 0 || cfg.GIDBase == 0 || cfg.StopTimeout <= 0 {
		t.Fatal("unsafe defaults")
	}
	spec := sandbox.Spec{AgentID: "ag_test", Resources: sandbox.Resources{DiskGB: 1}}
	system, home := capacities(cfg, spec)
	if system+home != 1<<30 || system < 64<<20 || home < 64<<20 {
		t.Fatalf("capacity %d %d", system, home)
	}
	for _, id := range []string{"../bad", "/bad", "", ".", "a b", "a\x00b"} {
		if validID(id) {
			t.Fatalf("accepted %q", id)
		}
	}
	for _, cpu := range []float64{-1, 33, math.NaN(), math.Inf(1)} {
		spec.Resources.CPUs = cpu
		if validateSpec(spec) == nil {
			t.Fatalf("accepted CPU %v", cpu)
		}
	}
	cfg.RegisterNetwork = func(context.Context, string, netip.Addr) error { return nil }
	if _, err = cfg.normalized(); err == nil {
		t.Fatal("unpaired network callbacks accepted")
	}
}

func TestTapName(t *testing.T) {
	a, b := TapName("ag_one"), TapName("ag_two")
	if a == b || len(a) > 15 || !strings.HasPrefix(a, "ah") || a != TapName("ag_one") {
		t.Fatalf("invalid tap names %q %q", a, b)
	}
}

func TestLayerValidation(t *testing.T) {
	cfg := testConfig("/tmp/agenthold")
	cfg.DefaultLayers = []LayerFile{{Path: "/harnesses.ext4"}, {Path: "/custom.ext4", Subdir: "upper"}}
	if _, err := cfg.normalized(); err != nil {
		t.Fatalf("valid default layers rejected: %v", err)
	}
	for name, bad := range map[string][]LayerFile{
		"relative": {{Path: "rel.ext4"}},
		"badsub":   {{Path: "/a.ext4", Subdir: "../etc"}},
		"colon":    {{Path: "/a.ext4", Subdir: "a:b"}},
		"too many": make([]LayerFile, 9),
	} {
		c := testConfig("/tmp/agenthold")
		c.DefaultLayers = bad
		if _, err := c.normalized(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := validateSpec(sandbox.Spec{AgentID: "ag_1", Layers: []string{"img_1:v1"}}); err != nil {
		t.Fatalf("valid spec layers rejected: %v", err)
	}
	for name, bad := range map[string][]string{
		"empty ref":  {""},
		"whitespace": {"a b"},
		"too many":   {"1", "2", "3", "4", "5", "6", "7", "8", "9"},
	} {
		if err := validateSpec(sandbox.Spec{AgentID: "ag_1", Layers: bad}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
