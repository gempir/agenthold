package claude

import (
	"testing"

	"github.com/gempir/agenthold/internal/harness"
)

func TestEnvironmentCapsRetries(t *testing.T) {
	a, ok := harness.Get("claude-code")
	if !ok {
		t.Fatal("claude-code is not registered")
	}
	env := a.Environment(harness.Config{Model: "opus"})
	if env["CLAUDE_CODE_MAX_RETRIES"] != "3" {
		t.Fatalf("CLAUDE_CODE_MAX_RETRIES = %q", env["CLAUDE_CODE_MAX_RETRIES"])
	}
	if env["CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"] != "false" {
		t.Fatalf("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION = %q", env["CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"])
	}
	if env["ANTHROPIC_MODEL"] != "opus" {
		t.Fatalf("ANTHROPIC_MODEL = %q", env["ANTHROPIC_MODEL"])
	}
}
