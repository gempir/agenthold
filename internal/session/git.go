package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
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
		t.touchedPath, t.touchedDir = last, false
	}
}

// touchedPlace is where a turn last worked: a file, or a directory.
type touchedPlace struct {
	path string
	dir  bool
}

// cdTarget matches a cd in a shell command, also inside a quoted
// `bash -lc '...'` wrapper: its argument, bare or quoted.
var cdTarget = regexp.MustCompile(`(?:^|[\s;&|('"])cd\s+("[^"]+"|'[^']+'|[^\s;&|'")]+)`)

// commandDir is the directory a shell command works in: its working
// directory (Codex reports one) moved by the last cd in it. Empty when it
// says nothing about one. A relative path is relative to home.
func commandDir(command, cwd string) string {
	dir := cwd
	for _, m := range cdTarget.FindAllStringSubmatch(command, -1) {
		arg := strings.Trim(m[1], `"'`)
		switch {
		case arg == "-" || strings.ContainsAny(arg, "$`*?") && !strings.HasPrefix(arg, "$HOME"):
			continue
		case arg == "~" || arg == "$HOME":
			dir = "."
		case strings.HasPrefix(arg, "~/"):
			dir = arg[2:]
		case strings.HasPrefix(arg, "$HOME/"):
			dir = arg[len("$HOME/"):]
		case strings.HasPrefix(arg, "$"):
			continue
		case filepath.IsAbs(arg) || dir == "":
			dir = arg
		default:
			dir = filepath.Join(dir, arg)
		}
	}
	return dir
}

// noteToolInputValue is noteToolInput for a decoded raw input.
func (m *Manager) noteToolInputValue(sessionID string, rawInput any) {
	if rawInput == nil {
		return
	}
	if b, err := json.Marshal(rawInput); err == nil {
		m.noteToolInput(sessionID, b)
	}
}

// noteToolInput records the directory a shell tool call works in, from its
// raw input ({"command": ..., "cwd": ...}; the command may be an argv list).
func (m *Manager) noteToolInput(sessionID string, rawInput json.RawMessage) {
	if len(rawInput) == 0 {
		return
	}
	var in struct {
		Command json.RawMessage `json:"command"`
		Cwd     string          `json:"cwd"`
	}
	if json.Unmarshal(rawInput, &in) != nil {
		return
	}
	var command string
	if json.Unmarshal(in.Command, &command) != nil {
		var argv []string
		if json.Unmarshal(in.Command, &argv) != nil && in.Cwd == "" {
			return
		}
		command = strings.Join(argv, " ")
	}
	dir := commandDir(command, in.Cwd)
	if dir == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.turns[sessionID]
	if t == nil {
		return
	}
	home := ""
	if t.conn != nil {
		home = t.conn.home
	}
	// Home itself, or a place outside it, says nothing about a repository
	// and must not hide one the turn worked in before.
	if rel, ok := relInHome(home, dir); !ok || rel == "." {
		return
	}
	t.touchedPath, t.touchedDir = dir, true
}

// refreshSessionGit updates the repository and branch recorded for the
// session after a turn (the agent may have switched branches or worked in a
// repository). Best effort: failures are only logged.
func (m *Manager) refreshSessionGit(ctx context.Context, conn *harnessConn, sess db.AgentSession, touched touchedPlace) {
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
	var repo *tunnel.GitRepo
	known := false
	// Without a working directory of its own the session is where the turn
	// last worked, if that is a repository; else where it was before.
	if sess.Cwd == "" && touched.path != "" {
		if rel, ok := relInHome(conn.home, touched.path); ok && rel != "." {
			// A file is in its directory's repository.
			dir := rel
			if !touched.dir {
				dir = filepath.Dir(rel)
			}
			repo, _ = status(dir)
		}
	}
	if repo == nil {
		target := sess.Cwd
		if target == "" {
			target = sess.GitRepoPath
		}
		repo, known = status(target)
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
