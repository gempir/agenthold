// Package audit records who did what to which agent.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Actions.
const (
	Prompt        = "session.prompt"
	Cancel        = "session.cancel"
	Permission    = "session.permission"
	SessionCreate = "session.create"
	SessionDelete = "session.delete"
	// A thread (session) settled or unsettled: by a user, by its agent
	// (platform tools), by the auto-settle job or by new activity.
	SessionSettle   = "session.settle"
	SessionUnsettle = "session.unsettle"
	SessionPin      = "session.pin"
	SessionUnpin    = "session.unpin"
	SessionSnooze   = "session.snooze"
	SessionUnsnooze = "session.unsnooze"
	SessionRewind   = "session.rewind"
	SessionSettings = "session.settings"
	SessionSideChat = "session.side_chat"
	TerminalOpen    = "terminal.open"
	HarnessLogin    = "harness.login"
	FileWrite       = "file.write"
	FileDelete      = "file.delete"
	FileMkdir       = "file.mkdir"
	AgentCreate     = "agent.create"
	AgentUpdate     = "agent.update"
	AgentDelete     = "agent.delete"
	AgentStart      = "agent.start"
	AgentStop       = "agent.stop"
	AgentRebuild    = "agent.rebuild"
	AgentClone      = "agent.clone"
	ShareAdd        = "share.add"
	ShareRemove     = "share.remove"
	SecretAttach    = "secret.attach"
	SecretDetach    = "secret.detach"
	MCPAdd          = "mcp.add"
	MCPAttach       = "mcp.attach"
	MCPDetach       = "mcp.detach"
	SkillAttach     = "skill.attach"
	SkillDetach     = "skill.detach"
	ExposeAdd       = "exposure.add"
	ExposeUpdate    = "exposure.update"
	ExposeRemove    = "exposure.remove"
	BrowserNavigate = "browser.navigate"
	BrowserInteract = "browser.interact"
	BrowserClose    = "browser.close"
	ServicePut      = "service.put"
	ServiceDelete   = "service.delete"
	ServiceStart    = "service.start"
	ServiceStop     = "service.stop"
	ServiceRestart  = "service.restart"
	AppCreate       = "app.create"
	AppUpdate       = "app.update"
	AppDelete       = "app.delete"
	AppStart        = "app.start"
	AppStop         = "app.stop"
	AppRebuild      = "app.rebuild"
	AppDeploy       = "app.deploy"
	AppExec         = "app.exec"
	AppTakeOver     = "app.takeover"

	GitConfigUpdate     = "git.config.update"
	GitCredentialPut    = "git.credential.put"
	GitCredentialDelete = "git.credential.delete"
	GitHubConnect       = "git.github.connect"
	GitHubRefreshFailed = "git.github.refresh_failed"
	GitPush             = "git.push"
	GitWorktreeRemove   = "git.worktree_remove"
	GitClone            = "git.clone"
	GitCheckout         = "git.checkout"
	GitWorktreeAdd      = "git.worktree_add"
	GitPull             = "git.pull"
	GitMerge            = "git.merge"
	GitCommit           = "git.commit"

	ScriptRun = "script.run"

	ProjectThreadStart = "project.thread_start"
	ProjectMerge       = "project.merge"
	ProjectPush        = "project.push"
	ProjectClose       = "project.close"
	TaskAssign         = "task.assign"

	ExecutorCall          = "executor.mcp.call"
	ExecutorAgentUpdate   = "executor.agent.update"
	ExecutorAppImport     = "executor.app.import"
	ExecutorAppDelete     = "executor.app.delete"
	ExecutorAccountLink   = "executor.account.link"
	ExecutorAccountUnlink = "executor.account.unlink"
	ExecutorAccountDelete = "executor.account.delete"
)

// Entry is one audit record. AgentID is empty for account-level actions.
type Entry struct {
	AgentID string
	UserID  string
	Action  string
	Target  string
	Details map[string]any
}

// Log writes audit entries.
type Log struct {
	Store *store.Store
	Log   *slog.Logger
}

// Record writes e. Failures are logged, never returned: auditing must not
// break the action it describes, which has already happened.
func (l *Log) Record(ctx context.Context, e Entry) {
	if l == nil || l.Store == nil {
		return
	}
	details := "{}"
	if len(e.Details) > 0 {
		if b, err := json.Marshal(e.Details); err == nil {
			details = string(b)
		}
	}
	if err := l.Store.InsertAudit(context.WithoutCancel(ctx), db.InsertAuditParams{
		At: time.Now().UnixMilli(), AgentID: e.AgentID, UserID: e.UserID,
		Action: e.Action, Target: e.Target, Details: details,
	}); err != nil && l.Log != nil {
		l.Log.Error("audit", "action", e.Action, "err", err)
	}
}
