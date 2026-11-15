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
	return Config{DataDir: dir, BaseImagePath: "/base.ext4", KernelPath: "/kernel", FirecrackerPath: "/firecracker", JailerPath: "/jailer", NetworkPool: netip.MustParsePrefix("172.29.0.0/24")}
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
