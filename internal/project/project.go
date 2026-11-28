// Package project manages projects: the repositories and folders a user's
// agents work on, the workspaces threads work in, and tasks.
//
// A project's sources have one main checkout per agent, at
// projects/<slug>/<name> (or the source's own path) in the agent's home. A
// thread of the project works either in a workspace of its own,
// projects/<slug>/.threads/<key>, holding a git worktree of every source on
// the thread's branch (agenthold/<key>), or directly in the main checkouts
// (projects/<slug>). Folders are git repositories too (made one on first
// use), so threads never share a work tree unless they choose to. Work of a
// thread lands by merging its branch into the main checkouts, or by pushing
// it.
package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/scripts"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Source kinds.
const (
	KindRepo   = "repo"   // cloned from URL
	KindFolder = "folder" // a directory, made a local repository
)

// Task statuses.
const (
	TaskOpen       = "open"
	TaskInProgress = "in_progress"
	TaskReview     = "review"
	TaskDone       = "done"
	TaskCancelled  = "cancelled"
)

// TaskStatuses lists the valid task statuses.
var TaskStatuses = []string{TaskOpen, TaskInProgress, TaskReview, TaskDone, TaskCancelled}

// ValidTaskStatus reports whether s is a task status.
func ValidTaskStatus(s string) bool {
	for _, v := range TaskStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// Dir is the directory of projects in agents' homes.
const Dir = "projects"

// BranchPrefix starts the branches of threads' workspaces.
const BranchPrefix = "agenthold/"

// Errors.
var (
	ErrInvalid = errors.New("invalid project")
	ErrNoWork  = errors.New("the thread has no workspace of its own")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Root is the project's directory in a home.
func Root(p db.Project) string { return path.Join(Dir, p.Slug) }

// MainPath is where the main checkout of a source lives in a home.
func MainPath(p db.Project, s db.ProjectSource) string {
	if s.Path != "" {
		return s.Path
	}
	return path.Join(Root(p), s.Name)
}

// ThreadKey is the short name of a thread in branch and directory names.
func ThreadKey(sessionID string) string {
	k := sessionID
	if i := strings.IndexByte(k, '_'); i >= 0 {
		k = k[i+1:]
	}
	if len(k) > 10 {
		k = k[:10]
	}
	return k
}

// ThreadBranch is the branch of a thread's workspace.
func ThreadBranch(sessionID string) string { return BranchPrefix + ThreadKey(sessionID) }

// WorkspacePath is the workspace of a thread on branch.
func WorkspacePath(p db.Project, branch string) string {
	return path.Join(Root(p), ".threads", strings.TrimPrefix(branch, BranchPrefix))
}

// ThreadCwd is where a thread of the project works.
func ThreadCwd(p db.Project, sess db.AgentSession) string {
	if sess.ProjectBranch == "" {
		return Root(p)
	}
	return WorkspacePath(p, sess.ProjectBranch)
}

var (
	slugBad  = regexp.MustCompile(`[^a-z0-9._-]+`)
	nameRule = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// Slug makes a directory name of a project name.
func Slug(name string) string {
	s := slugBad.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-.")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-.")
	}
	if s == "" {
		s = "project"
	}
	return s
}

// SourceInput describes a source to create or keep.
type SourceInput struct {
	Name          string
	Kind          string
	URL           string
	Path          string
	DefaultBranch string
}

// CleanSources validates sources.
func CleanSources(in []SourceInput) ([]SourceInput, error) {
	seen := map[string]bool{}
	out := make([]SourceInput, 0, len(in))
	for _, s := range in {
		s.Name, s.URL, s.Path, s.DefaultBranch = strings.TrimSpace(s.Name), strings.TrimSpace(s.URL), strings.TrimSpace(s.Path), strings.TrimSpace(s.DefaultBranch)
		if s.Kind == "" {
			s.Kind = KindRepo
			if s.URL == "" {
				s.Kind = KindFolder
			}
		}
		if s.Name == "" && s.Kind == KindRepo {
			s.Name = repoName(s.URL)
		}
		if s.Name == "" && s.Path != "" {
			s.Name = path.Base(s.Path)
		}
		switch {
		case !nameRule.MatchString(s.Name) || s.Name == ".threads":
			return nil, invalid("source name %q: use letters, digits, '.', '_' and '-', up to 64", s.Name)
		case seen[strings.ToLower(s.Name)]:
			return nil, invalid("two sources are called %q", s.Name)
		case s.Kind != KindRepo && s.Kind != KindFolder:
			return nil, invalid("source %s: kind must be repo or folder", s.Name)
		case s.Kind == KindRepo && s.URL == "":
			return nil, invalid("source %s: a repo needs a url", s.Name)
		case s.Kind == KindFolder && s.URL != "":
			return nil, invalid("source %s: a folder has no url", s.Name)
		case strings.HasPrefix(s.URL, "-") || strings.ContainsAny(s.URL, " \t\r\n\x00"):
			return nil, invalid("source %s: invalid url", s.Name)
		case s.DefaultBranch != "" && !validBranch(s.DefaultBranch):
			return nil, invalid("source %s: invalid default branch", s.Name)
		}
		if s.Path != "" {
			p := path.Clean(s.Path)
			if strings.HasPrefix(p, "/") || p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsRune(p, 0) {
				return nil, invalid("source %s: path must be relative to the home and inside it", s.Name)
			}
			s.Path = p
		}
		seen[strings.ToLower(s.Name)] = true
		out = append(out, s)
	}
	return out, nil
}

func validBranch(b string) bool {
	if b == "" || strings.HasPrefix(b, "-") || strings.Contains(b, "..") || strings.HasSuffix(b, ".lock") {
		return false
	}
	return !strings.ContainsAny(b, " \t\r\n~^:?*[\\\x00")
}

// repoName is the last path segment of a remote URL, without .git.
func repoName(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	return strings.TrimSuffix(u, ".git")
}

// Service works with projects on agents' machines.
type Service struct {
	Store *store.Store
	// Machine returns the tunnel to an agent's running machine.
	Machine MachineFunc
	// Sessions creates and prompts threads.
	Sessions *session.Manager
	// Scripts runs the project scripts of new worktrees and settled
	// threads (nil: none run).
	Scripts *scripts.Runner
	Log     *slog.Logger
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Load returns a project and its sources.
func (s *Service) Load(ctx context.Context, id string) (db.Project, []db.ProjectSource, error) {
	p, err := s.Store.GetProject(ctx, id)
	if err != nil {
		return p, nil, err
	}
	src, err := s.Store.ListProjectSources(ctx, id)
	return p, src, err
}

func gitDo(ctx context.Context, ts *tunnel.Session, req tunnel.GitRequest) (tunnel.GitReply, error) {
	return ts.Git(ctx, req)
}

// repoAt returns the repository whose root is exactly p, if there is one.
func repoAt(ctx context.Context, ts *tunnel.Session, p string) (*tunnel.GitReply, bool) {
	r, err := gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitStatus, Path: p})
	if err != nil || r.Repo == nil || r.Repo.Path != p {
		return nil, false
	}
	return &r, true
}

// ensureMain makes sure the main checkout of a source exists and returns
// its status and default branch.
func (s *Service) ensureMain(ctx context.Context, ts *tunnel.Session, p db.Project, src db.ProjectSource) (tunnel.GitReply, string, error) {
	mp := MainPath(p, src)
	r, ok := repoAt(ctx, ts, mp)
	if !ok {
		var err error
		var reply tunnel.GitReply
		if src.Kind == KindRepo {
			reply, err = gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitClone, URL: src.Url, Path: mp, Branch: src.DefaultBranch})
		} else {
			reply, err = gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitInit, Path: mp, Branch: src.DefaultBranch, Message: "Initial snapshot of " + src.Name})
		}
		if err != nil {
			return tunnel.GitReply{}, "", fmt.Errorf("%s: %w", src.Name, err)
		}
		if reply.Repo == nil {
			return tunnel.GitReply{}, "", fmt.Errorf("%s: the machine did not describe the repository", src.Name)
		}
		r = &reply
	}
	def := src.DefaultBranch
	if def == "" {
		def = r.Repo.Branch
		if !r.Repo.Detached && def != "" {
			if err := s.Store.SetProjectSourceDefaultBranch(ctx, db.SetProjectSourceDefaultBranchParams{DefaultBranch: def, ID: src.ID}); err != nil {
				s.logger().Warn("record a source's default branch", "source", src.ID, "err", err)
			}
		}
	}
	return *r, def, nil
}

// Prepare makes sure the workspace of a project thread exists on the
// machine and returns its working directory (relative to the home).
// It is idempotent: an existing workspace is kept as it is.
func (s *Service) Prepare(ctx context.Context, ts *tunnel.Session, sess db.AgentSession) (string, error) {
	p, sources, err := s.Load(ctx, sess.ProjectID)
	if err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	cwd := ThreadCwd(p, sess)
	var created []string // worktrees made now
	for _, src := range sources {
		main, def, err := s.ensureMain(ctx, ts, p, src)
		if err != nil {
			return "", err
		}
		if sess.ProjectBranch == "" {
			// Directly in the main checkouts: a source kept elsewhere is
			// linked into the project's directory.
			if src.Path != "" {
				rel := relPath(Root(p), src.Path)
				if err := ts.Symlink(ctx, path.Join(Root(p), src.Name), rel); err != nil {
					return "", fmt.Errorf("%s: link the source into the project: %w", src.Name, err)
				}
			}
			continue
		}
		wt := path.Join(cwd, src.Name)
		if r, ok := repoAt(ctx, ts, wt); ok && r.Repo.IsWorktree {
			continue
		}
		// Start from the newest default branch: fast-forward a clean main
		// checkout that is on it (best effort).
		if main.Repo.RemoteURL != "" && main.Repo.Branch == def && !main.Repo.Dirty {
			if _, err := gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitPull, Path: MainPath(p, src)}); err != nil {
				s.logger().Debug("update the main checkout", "source", src.Name, "err", err)
			}
		}
		exists := false
		for _, b := range main.Branches {
			if b == sess.ProjectBranch {
				exists = true
			}
		}
		req := tunnel.GitRequest{Action: tunnel.GitWorktreeAdd, Path: MainPath(p, src), Branch: sess.ProjectBranch, WorktreePath: wt}
		if !exists {
			req.Create, req.FromRef = true, def
		}
		if _, err := gitDo(ctx, ts, req); err != nil {
			return "", fmt.Errorf("%s: %w", src.Name, err)
		}
		created = append(created, wt)
	}
	if err := ts.Mkdir(ctx, cwd, true); err != nil {
		return "", fmt.Errorf("project directory: %w", err)
	}
	guide := Guide(p, sources, sess)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		body := guide
		if name == "CLAUDE.md" {
			body = "@AGENTS.md\n"
		}
		if err := ts.WriteFile(ctx, path.Join(cwd, name), 0o644, bytes.NewReader([]byte(body))); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}
	if s.Scripts != nil {
		for _, wt := range created {
			s.Scripts.OnWorktreeCreate(ctx, ts, sess.AgentID, wt)
		}
	}
	return cwd, nil
}

// ScriptDirs are the directories whose project scripts belong to a thread:
// each repository of an isolated project thread's workspace, else its
// working directory (none for the home).
func (s *Service) ScriptDirs(ctx context.Context, sess db.AgentSession) []string {
	if sess.ProjectID != "" && sess.ProjectBranch != "" {
		p, sources, err := s.Load(ctx, sess.ProjectID)
		if err != nil {
			return nil
		}
		ws := WorkspacePath(p, sess.ProjectBranch)
		dirs := make([]string, 0, len(sources))
		for _, src := range sources {
			dirs = append(dirs, path.Join(ws, src.Name))
		}
		return dirs
	}
	if sess.Cwd == "" {
		return nil
	}
	return []string{sess.Cwd}
}

// SettleHooks starts OnSettle in the background for a thread that was just
// settled.
func (s *Service) SettleHooks(ctx context.Context, sess db.AgentSession) {
	if s == nil || s.Scripts == nil || len(s.ScriptDirs(ctx, sess)) == 0 {
		return
	}
	go func() {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), (scripts.ExecTimeout+60)*time.Second)
		defer cancel()
		s.OnSettle(bctx, sess)
	}()
}

// OnSettle runs the settle hooks of the thread's project scripts
// (stopOnSettle, runOnSettle) when its agent's machine runs. Best effort.
func (s *Service) OnSettle(ctx context.Context, sess db.AgentSession) {
	if s == nil || s.Scripts == nil || s.Machine == nil {
		return
	}
	dirs := s.ScriptDirs(ctx, sess)
	if len(dirs) == 0 {
		return
	}
	ts, err := s.Machine(ctx, sess.AgentID)
	if err != nil || ts == nil {
		return
	}
	s.Scripts.OnSettle(ctx, ts, sess.AgentID, dirs)
}

// relPath is target (relative to the home) relative to the directory dir
// (relative to the home too).
func relPath(dir, target string) string {
	up := strings.Count(path.Clean(dir), "/") + 1
	return strings.Repeat("../", up) + target
}

// Guide is the AGENTS.md written into a thread's working directory.
func Guide(p db.Project, sources []db.ProjectSource, sess db.AgentSession) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Project %s\n\n", p.Name)
	if p.Description != "" {
		b.WriteString(strings.TrimSpace(p.Description) + "\n\n")
	}
	b.WriteString("This file is written by agenthold; changes to it are overwritten.\n\n## Layout\n\n")
	for _, src := range sources {
		kind := "git repository"
		if src.Kind == KindFolder {
			kind = "folder (a local git repository)"
		}
		fmt.Fprintf(&b, "- `%s/`: %s", src.Name, kind)
		if src.Url != "" {
			fmt.Fprintf(&b, " from %s", src.Url)
		}
		if src.DefaultBranch != "" {
			fmt.Fprintf(&b, ", default branch `%s`", src.DefaultBranch)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## How to work here\n\n")
	if sess.ProjectBranch != "" {
		fmt.Fprintf(&b, "This thread has a workspace of its own: every directory above is a git worktree on the branch "+
			"`%s`. Other threads and agents work in their own worktrees, so you do not get in each other's way.\n\n"+
			"- Commit your work on this branch in each repository you change. Do not switch branches and do not "+
			"touch the main checkouts in `%s/`.\n"+
			"- Your owner merges or pushes the branch from agenthold when the work is done.\n", sess.ProjectBranch, Root(p))
	} else {
		fmt.Fprintf(&b, "This thread works directly in the project's main checkouts (`%s/`). Other threads may work "+
			"here too: check `project_status` before larger changes.\n", Root(p))
	}
	b.WriteString("- The agenthold tools `project_status` (who works on what, which files overlap), `list_tasks`, " +
		"`create_task`, `claim_task`, `update_task` and `assign_task` coordinate work with other threads and agents.\n")
	if strings.TrimSpace(p.Instructions) != "" {
		b.WriteString("\n## Instructions\n\n" + strings.TrimSpace(p.Instructions) + "\n")
	}
	return b.String()
}

// SourceChanges is what a thread changed in one source.
type SourceChanges struct {
	Source  string
	Branch  string
	Base    string // the default branch it forked from
	Commits int
	Dirty   bool
	Files   []tunnel.GitFileChange
	Remote  bool
	Error   string
}

// Changes reports what an isolated thread changed in each source.
func (s *Service) Changes(ctx context.Context, ts *tunnel.Session, p db.Project, sources []db.ProjectSource, sess db.AgentSession) ([]SourceChanges, error) {
	if sess.ProjectBranch == "" {
		return nil, ErrNoWork
	}
	ws := WorkspacePath(p, sess.ProjectBranch)
	var out []SourceChanges
	for _, src := range sources {
		c := SourceChanges{Source: src.Name, Branch: sess.ProjectBranch, Base: src.DefaultBranch}
		wt := path.Join(ws, src.Name)
		if c.Base == "" {
			if r, ok := repoAt(ctx, ts, MainPath(p, src)); ok {
				c.Base = r.Repo.Branch
			}
		}
		if c.Base == "" {
			c.Error = "no main checkout"
			out = append(out, c)
			continue
		}
		r, err := gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitChanges, Path: wt, FromRef: c.Base})
		switch {
		case err != nil:
			c.Error = err.Error()
		case r.Changes != nil:
			c.Commits, c.Dirty, c.Files = r.Changes.Commits, r.Changes.Dirty, r.Changes.Files
			c.Remote = r.Repo != nil && r.Repo.RemoteURL != ""
		}
		out = append(out, c)
	}
	return out, nil
}

// MergeResult is the outcome of merging a thread's branch into one source.
type MergeResult struct {
	Source  string
	Merged  bool
	Commits int
	Error   string
}

// Merge merges an isolated thread's branch into the main checkouts of the
// sources it has commits in. Uncommitted work, a main checkout that is not
// clean or not on its default branch, stops it before anything is merged; a
// conflict stops it at that source.
func (s *Service) Merge(ctx context.Context, ts *tunnel.Session, sess db.AgentSession) ([]MergeResult, error) {
	p, sources, err := s.Load(ctx, sess.ProjectID)
	if err != nil {
		return nil, err
	}
	changes, err := s.Changes(ctx, ts, p, sources, sess)
	if err != nil {
		return nil, err
	}
	bySource := map[string]db.ProjectSource{}
	for _, src := range sources {
		bySource[src.Name] = src
	}
	var problems []string
	for _, c := range changes {
		switch {
		case c.Error != "":
			problems = append(problems, c.Source+": "+c.Error)
		case c.Dirty:
			problems = append(problems, c.Source+": the thread has uncommitted changes; commit them first")
		case c.Commits > 0:
			r, ok := repoAt(ctx, ts, MainPath(p, bySource[c.Source]))
			switch {
			case !ok:
				problems = append(problems, c.Source+": no main checkout")
			case r.Repo.Branch != c.Base || r.Repo.Detached:
				problems = append(problems, fmt.Sprintf("%s: the main checkout is on %s, not %s", c.Source, r.Repo.Branch, c.Base))
			}
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, strings.Join(problems, "; "))
	}
	var out []MergeResult
	for _, c := range changes {
		res := MergeResult{Source: c.Source, Commits: c.Commits}
		if c.Commits > 0 {
			msg := fmt.Sprintf("Merge %s: %s", sess.ProjectBranch, sess.Title)
			if _, err := gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitMerge, Path: MainPath(p, bySource[c.Source]), Branch: sess.ProjectBranch, Message: msg}); err != nil {
				res.Error = err.Error()
				out = append(out, res)
				return out, nil
			}
			res.Merged = true
		}
		out = append(out, res)
	}
	return out, nil
}

// Push pushes an isolated thread's branch of every repository that has
// commits on it and a remote.
func (s *Service) Push(ctx context.Context, ts *tunnel.Session, sess db.AgentSession) ([]MergeResult, error) {
	p, sources, err := s.Load(ctx, sess.ProjectID)
	if err != nil {
		return nil, err
	}
	changes, err := s.Changes(ctx, ts, p, sources, sess)
	if err != nil {
		return nil, err
	}
	ws := WorkspacePath(p, sess.ProjectBranch)
	var out []MergeResult
	for _, c := range changes {
		res := MergeResult{Source: c.Source, Commits: c.Commits}
		switch {
		case c.Error != "":
			res.Error = c.Error
		case c.Commits == 0:
		case !c.Remote:
			res.Error = "no remote to push to"
		default:
			if _, err := gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitPush, Path: path.Join(ws, c.Source)}); err != nil {
				res.Error = err.Error()
			} else {
				res.Merged = true
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// Close removes an isolated thread's workspace: its worktrees and branches.
// Without force it refuses while there are uncommitted changes or commits
// that are not merged into the main checkouts.
func (s *Service) Close(ctx context.Context, ts *tunnel.Session, sess db.AgentSession, force bool) error {
	p, sources, err := s.Load(ctx, sess.ProjectID)
	if err != nil {
		return err
	}
	if sess.ProjectBranch == "" {
		return ErrNoWork
	}
	ws := WorkspacePath(p, sess.ProjectBranch)
	if !force {
		changes, err := s.Changes(ctx, ts, p, sources, sess)
		if err != nil {
			return err
		}
		var problems []string
		for _, c := range changes {
			switch {
			case c.Error != "":
				problems = append(problems, c.Source+": "+c.Error)
			case c.Dirty:
				problems = append(problems, c.Source+" has uncommitted changes")
			case c.Commits > 0:
				problems = append(problems, fmt.Sprintf("%s has %d commits that are not merged", c.Source, c.Commits))
			}
		}
		if len(problems) > 0 {
			return fmt.Errorf("%w: %s; merge them, or discard them to close anyway", ErrInvalid, strings.Join(problems, ", "))
		}
	}
	for _, src := range sources {
		wt := path.Join(ws, src.Name)
		if r, ok := repoAt(ctx, ts, wt); ok && r.Repo.IsWorktree {
			if _, err := gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitWorktreeRemove, Path: wt, Force: true}); err != nil {
				return fmt.Errorf("%s: %w", src.Name, err)
			}
		}
		if _, ok := repoAt(ctx, ts, MainPath(p, src)); !ok {
			continue
		}
		// Checked above: without force, the branch is merged.
		if _, err := gitDo(ctx, ts, tunnel.GitRequest{Action: tunnel.GitBranchDelete, Path: MainPath(p, src), Branch: sess.ProjectBranch, Force: true}); err != nil {
			if !strings.Contains(err.Error(), "not found") {
				return fmt.Errorf("%s: %w", src.Name, err)
			}
		}
	}
	if err := ts.Remove(ctx, ws, true); err != nil && !strings.Contains(err.Error(), "no such file") {
		return err
	}
	return nil
}

// Thread is a thread of a project with what it changed.
type Thread struct {
	Session   db.AgentSession
	Agent     db.Agent
	Connected bool
	Changes   []SourceChanges
	Error     string
}

// Overlap is a file more than one thread changed.
type Overlap struct {
	Source  string
	Path    string
	Threads []string // session ids
}

// Status reports the active threads of a project and the files they
// changed (for agents whose machines run), and the files several threads
// changed.
func (s *Service) Status(ctx context.Context, p db.Project, sources []db.ProjectSource, includeSettled bool) ([]Thread, []Overlap, error) {
	rows, err := s.Store.ListProjectSessions(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	machines := map[string]*tunnel.Session{}
	var out []Thread
	for _, sess := range rows {
		if sess.SettledAt.Valid && !includeSettled {
			continue
		}
		ag, err := s.Store.GetAgent(ctx, sess.AgentID)
		if err != nil {
			continue
		}
		t := Thread{Session: sess, Agent: ag}
		ts, seen := machines[ag.ID]
		if !seen {
			ts, _ = s.Machine(ctx, ag.ID)
			machines[ag.ID] = ts
		}
		if ts != nil {
			t.Connected = true
			if sess.ProjectBranch != "" {
				if t.Changes, err = s.Changes(ctx, ts, p, sources, sess); err != nil {
					t.Error = err.Error()
				}
			}
		}
		out = append(out, t)
	}
	return out, Overlaps(out), nil
}

// Overlaps lists the files more than one thread changed.
func Overlaps(threads []Thread) []Overlap {
	by := map[[2]string][]string{}
	for _, t := range threads {
		for _, c := range t.Changes {
			for _, f := range c.Files {
				k := [2]string{c.Source, f.Path}
				by[k] = append(by[k], t.Session.ID)
			}
		}
	}
	var out []Overlap
	for k, ids := range by {
		if len(ids) > 1 {
			out = append(out, Overlap{Source: k[0], Path: k[1], Threads: ids})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Path < out[j].Path
	})
	return out
}
