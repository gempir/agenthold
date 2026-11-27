package codex

import (
	"slices"
	"testing"

	"github.com/gempir/agenthold/internal/harness"
)

func TestCommandDisablesApprovalsAndSandbox(t *testing.T) {
	a, ok := harness.Get("codex")
	if !ok {
		t.Fatal("codex is not registered")
	}
	for _, want := range []string{`approval_policy="never"`, `sandbox_mode="danger-full-access"`} {
		if !slices.Contains(a.Command, want) {
			t.Fatalf("command missing %s: %v", want, a.Command)
		}
	}
}
