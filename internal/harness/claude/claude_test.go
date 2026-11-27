package claude

import (
	"slices"
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

func TestCommandSkipsPermissions(t *testing.T) {
	a, ok := harness.Get("claude-code")
	if !ok {
		t.Fatal("claude-code is not registered")
	}
	if !slices.Contains(a.Command, "--dangerously-skip-permissions") {
		t.Fatalf("command missing --dangerously-skip-permissions: %v", a.Command)
	}
	if slices.Contains(a.Command, "--permission-prompt-tool") {
		t.Fatalf("command should not set --permission-prompt-tool: %v", a.Command)
	}
}
