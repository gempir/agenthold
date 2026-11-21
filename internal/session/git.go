package session

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Timeouts of the git bookkeeping around turns.
var (
	helloTimeout     = 5 * time.Second
	gitStatusTimeout = 3 * time.Second
)

// sandboxHome asks sandboxd for the agent account's home (HomeDir if it
// does not say).
func sandboxHome(ctx context.Context, ts *tunnel.Session) string {
	hctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	h, err := ts.HelloInfo(hctx)
	if err != nil || h.Home == "" {
		return HomeDir
	}
	return h.Home
}

// sessionCwd is the absolute working directory of a session whose cwd is rel
// (relative to home). Anything that would leave the home is the home.
func sessionCwd(home, rel string) string {
	if home == "" {
		home = HomeDir
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return home
	}
	rel = filepath.Clean(rel)
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") || rel == "." {
		return home
	}
	return filepath.Join(home, rel)
}

// relInHome returns p (absolute, or relative to home) relative to home, and
// whether it is inside it.
func relInHome(home, p string) (string, bool) {
	if home == "" {
		home = HomeDir
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(home, p)
	}
	rel, err := filepath.Rel(home, filepath.Clean(p))
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// noteLocations records the last file a tool call reported, for the turn of
// the session.
func (m *Manager) noteLocations(sessionID string, locs []acp.ToolCallLocation) {
	last := ""
	for _, l := range locs {
		if l.Path != "" {
			last = l.Path
		}
	}
	if last == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.turns[sessionID]; t != nil {
		t.touchedPath = last
	}
}

// refreshSessionGit updates the repository and branch recorded for the
// session after a turn (the agent may have switched branches or worked in a
// repository). Best effort: failures are only logged.
func (m *Manager) refreshSessionGit(ctx context.Context, conn *harnessConn, sess db.AgentSession, touchedPath string) {
	defer func() {
		if r := recover(); r != nil {
			m.lg.Warn("git status after the turn panicked", "session", sess.ID, "panic", r)
		}
	}()
	if latest, err := m.store.GetAgentSession(ctx, sess.ID); err == nil {
		sess = latest
	}
	// status is the repository path is in; ok is false when that is not
	// known (an older sandboxd, a timeout).
	status := func(path string) (repo *tunnel.GitRepo, ok bool) {
		gctx, cancel := context.WithTimeout(ctx, gitStatusTimeout)
		defer cancel()
		reply, err := conn.ts.Git(gctx, tunnel.GitRequest{Action: tunnel.GitStatus, Path: path})
		if err != nil {
			m.lg.Debug("git status after the turn", "session", sess.ID, "path", path, "err", err)
			return nil, false
		}
		return reply.Repo, true
	}
	target := sess.Cwd
	if target == "" {
		target = sess.GitRepoPath
	}
	repo, known := status(target)
	if repo == nil && sess.Cwd == "" && touchedPath != "" {
		if rel, ok := relInHome(conn.home, touchedPath); ok && rel != "." {
			// A file (most tool locations are) or a directory: its directory
			// is in the same repository either way, unless it is the root.
			dir := filepath.Dir(rel)
			repo, _ = status(dir)
		}
	}
	switch {
	case repo != nil:
		if repo.Project == sess.GitProject && repo.Branch == sess.GitBranch && repo.Path == sess.GitRepoPath {
			return
		}
		if err := m.store.UpdateSessionGitStatus(ctx, db.UpdateSessionGitStatusParams{
			GitProject: repo.Project, GitBranch: repo.Branch, GitRepoPath: repo.Path, ID: sess.ID,
		}); err != nil {
			m.lg.Warn("record the session's git status", "session", sess.ID, "err", err)
		}
	case known && sess.Cwd != "" && (sess.GitProject != "" || sess.GitBranch != "" || sess.GitRepoPath != ""):
		// The working directory is no repository (anymore).
		if err := m.store.UpdateSessionGitStatus(ctx, db.UpdateSessionGitStatusParams{ID: sess.ID}); err != nil {
			m.lg.Warn("clear the session's git status", "session", sess.ID, "err", err)
		}
	}
}
