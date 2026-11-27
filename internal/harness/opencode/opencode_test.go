package opencode

import (
	"encoding/json"
	"testing"

	"github.com/gempir/agenthold/internal/harness"
)

func TestEnvironmentAllowsAllPermissions(t *testing.T) {
	a, ok := harness.Get("opencode")
	if !ok {
		t.Fatal("opencode is not registered")
	}
	env := a.Environment(harness.Config{Model: "anthropic/claude-sonnet-5-5"})
	var cfg map[string]any
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT invalid JSON: %v", err)
	}
	if cfg["autoupdate"] != false {
		t.Fatalf("autoupdate = %v, want false", cfg["autoupdate"])
	}
	if cfg["permission"] != "allow" {
		t.Fatalf("permission = %v, want allow", cfg["permission"])
	}
	if cfg["model"] != "anthropic/claude-sonnet-5-5" {
		t.Fatalf("model = %v", cfg["model"])
	}
}
