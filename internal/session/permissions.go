package session

import (
	"context"
	"encoding/json"
	"strings"

	acp "github.com/coder/acp-go-sdk"
)

// Per-thread permission modes.
const (
	PermissionFullAccess      = "full-access"
	PermissionAutoAcceptEdits = "auto-accept-edits"
	PermissionSupervised      = "supervised"
	PermissionPlan            = "plan"
)

// Parent thread relationship kinds.
const (
	ParentKindSubthread = "subthread"
	ParentKindSideChat  = "side_chat"
	ParentKindA2A       = "a2a"
)

// planModeInstruction is prepended to prompts on harnesses without a native
// plan mode flag when the session is in plan permission mode.
const planModeInstruction = "[Plan mode: You are in read-only planning mode. Explore the codebase, ask clarifying questions, and propose a concrete plan. Do not edit files or run mutating commands without explicit user approval.]\n\n"

// ValidPermissionMode reports whether mode is a supported per-thread permission mode.
func ValidPermissionMode(mode string) bool {
	switch mode {
	case PermissionFullAccess, PermissionAutoAcceptEdits, PermissionSupervised, PermissionPlan:
		return true
	}
	return false
}

// NormalizePermissionMode maps an empty or unknown permission mode to the
// default (full-access), preserving existing behavior for older rows.
func NormalizePermissionMode(mode string) string {
	if ValidPermissionMode(mode) {
		return mode
	}
	return PermissionFullAccess
}

// isReadOnlyToolKind reports whether a normalized tool kind is read-only.
func isReadOnlyToolKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "read", "search", "fetch", "think":
		return true
	}
	return false
}

// isEditToolKind reports whether a normalized tool kind is a file edit.
func isEditToolKind(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "edit")
}

// shouldAutoApprove decides whether a tool call of the given kind can run
// without asking the user under mode.
func shouldAutoApprove(mode, kind string) bool {
	switch NormalizePermissionMode(mode) {
	case PermissionFullAccess:
		return true
	case PermissionAutoAcceptEdits:
		return isReadOnlyToolKind(kind) || isEditToolKind(kind)
	case PermissionSupervised, PermissionPlan:
		return isReadOnlyToolKind(kind)
	default:
		return true
	}
}

// sessionPermissionMode returns the active permission mode for nativeID.
func (c *harnessConn) sessionPermissionMode(nativeID string) string {
	if c == nil || c.m == nil {
		return PermissionFullAccess
	}
	sessionID, ok := c.sessionFor(acp.SessionId(nativeID))
	if !ok {
		return PermissionFullAccess
	}
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	if t := c.m.turns[sessionID]; t != nil && t.permissionMode != "" {
		return NormalizePermissionMode(t.permissionMode)
	}
	return PermissionFullAccess
}

// requestToolApproval gates a native driver (Claude or Codex) tool approval
// callback through the session's permission mode and user approval flow.
func (c *harnessConn) requestToolApproval(ctx context.Context, nativeID, toolCallID, title, kind string, rawInput json.RawMessage) (allowed bool, allowAlways bool) {
	mode := c.sessionPermissionMode(nativeID)
	if shouldAutoApprove(mode, kind) {
		return true, true
	}
	if c == nil {
		return true, true
	}
	tk := acp.ToolKind(kind)
	if tk == "" {
		tk = acp.ToolKindOther
	}
	var inputAny any
	if len(rawInput) > 0 {
		_ = json.Unmarshal(rawInput, &inputAny)
	}
	res, err := c.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: acp.SessionId(nativeID),
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(toolCallID),
			Title:      &title,
			Kind:       &tk,
			RawInput:   inputAny,
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow", OptionId: "allow"},
			{Kind: acp.PermissionOptionKindAllowAlways, Name: "Allow always", OptionId: "allow_always"},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: "reject"},
		},
	})
	if err != nil || res.Outcome.Selected == nil {
		return false, false
	}
	switch res.Outcome.Selected.OptionId {
	case "allow_always":
		return true, true
	case "allow", "allow_once":
		return true, false
	default:
		return false, false
	}
}
