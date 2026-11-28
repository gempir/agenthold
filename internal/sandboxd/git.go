package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// Git limits.
const (
	gitShortTimeout = 15 * time.Second  // list, status, checkout, worktree
	gitLongTimeout  = 300 * time.Second // clone, pull (network)
	gitMaxOutput    = 64 << 10
	gitListDepth    = 3
	gitListMax      = 50
	// gitConfigFile is where GitConfig writes the agent's commit identity,
	// relative to the home.
	gitConfigFile = ".config/agenthold/gitconfig"
)

// gitSkipDirs are never searched for repositories: caches, dependencies,
// build output and agenthold's or the harnesses' own state.
var gitSkipDirs = map[string]bool{
	".cache": true, ".npm": true, ".local": true, ".cargo": true, ".go": true,
	"node_modules": true, "vendor": true, ".git": true, ".fakeagent": true,
	".agenthold": true, ".claude": true, ".codex": true, ".opencode": true,
	"dist": true, "build": true, "target": true, ".venv": true, "venv": true,
	"__pycache__": true,
}

// gitAllowProtocol limits the transports git may use (also for submodules
// and redirects): no ext:: or fd:: remote helpers.
const gitAllowProtocol = "file:git:http:https:ssh"

func (d *Daemon) git(ctx context.Context, st net.Conn, o tunnel.Open) error {
	if o.Git == nil {
		return errors.New("git: no request")
	}
	req := *o.Git
	// The server closing the stream cancels the running git command.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		buf := make([]byte, 1)
		_, _ = st.Read(buf)
		cancel()
	}()
	var (
		reply tunnel.GitReply
		err   error
	)
	switch req.Action {
	case tunnel.GitList:
		reply = d.gitList(ctx)
	case tunnel.GitStatus:
		reply, err = d.gitStatus(ctx, req)
	case tunnel.GitClone:
		reply, err = d.gitClone(ctx, req)
	case tunnel.GitCheckout:
		reply, err = d.gitCheckout(ctx, req)
	case tunnel.GitWorktreeAdd:
		reply, err = d.gitWorktreeAdd(ctx, req)
	case tunnel.GitPull:
		reply, err = d.gitPull(ctx, req)
	case tunnel.GitPush:
		reply, err = d.gitPush(ctx, req)
	case tunnel.GitWorktreeRemove:
		reply, err = d.gitWorktreeRemove(ctx, req)
	case tunnel.GitConfig:
		err = d.gitConfig(req)
	case tunnel.GitInit:
		reply, err = d.gitInit(ctx, req)
	case tunnel.GitChanges:
		reply, err = d.gitChanges(ctx, req)
	case tunnel.GitMerge:
		reply, err = d.gitMerge(ctx, req)
	case tunnel.GitBranchDelete:
		reply, err = d.gitBranchDelete(ctx, req)
	default:
		err = fmt.Errorf("git: unknown action %q", req.Action)
	}
	if err != nil {
		return err
	}
	reply.Home = d.acct.home
	return tunnel.WriteJSON(st, reply)
}

// runGit runs git as the agent account in dir and returns its combined
// output. A failure's error carries the output, with credentials in URLs
// scrubbed; callers scrub what they pass on themselves.
func (d *Daemon) runGit(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = d.env(map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_ALLOW_PROTOCOL":  gitAllowProtocol,
		"GIT_OPTIONAL_LOCKS":  "0",
		"LC_ALL":              "C",
	}, d.shell()[0])
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if d.acct.switchTo {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(d.acct.uid), Gid: uint32(d.acct.gid)}
	}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = time.Second
	out := &cappedBuffer{max: gitMaxOutput}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	out.mu.Lock()
	text := out.buf.String()
	out.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("git %s: %w", args[0], ctx.Err())
		}
		msg := strings.TrimSpace(scrubCredentials(text))
		if msg == "" {
			msg = err.Error()
		}
		return text, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return text, nil
}

var credInURL = regexp.MustCompile(`(://)[^/@\s]+@`)

// scrubCredentials removes user:password@ from URLs in git's output.
func scrubCredentials(s string) string {
	return credInURL.ReplaceAllString(s, "$1")
}

// realHome is the home with symlinks resolved (git reports real paths).
func (d *Daemon) realHome() string {
	if p, err := filepath.EvalSymlinks(d.acct.home); err == nil {
		return p
	}
	return filepath.Clean(d.acct.home)
}

// inside reports whether p is root or below it.
func inside(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// homePath validates a request path and returns it as an absolute path
// inside the home (also after resolving symlinks of its existing part).
func (d *Daemon) homePath(p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", errors.New("invalid path")
	}
	if strings.HasPrefix(p, "-") {
		return "", fmt.Errorf("invalid path %q", p)
	}
	abs := d.resolve(p)
	home := filepath.Clean(d.acct.home)
	if !inside(home, abs) {
		return "", fmt.Errorf("path %q is outside the home", p)
	}
	// The deepest existing ancestor must resolve inside the home too, so a
	// symlink cannot lead out of it.
	existing, rest := abs, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if !inside(d.realHome(), filepath.Join(real, rest)) {
		return "", fmt.Errorf("path %q is outside the home", p)
	}
	return abs, nil
}

// homeRel returns p (a real path inside the home) relative to the home.
func (d *Daemon) gitHomeRel(p string) (string, bool) {
	real := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		real = r
	}
	rh := d.realHome()
	if !inside(rh, real) {
		return "", false
	}
	rel, err := filepath.Rel(rh, real)
	if err != nil {
		return "", false
	}
	return rel, true
}

// validRef checks a branch or ref name: nothing git would read as an option
// or a range, no whitespace or control characters (git check-ref-format's
// rules, slightly stricter).
func validRef(name string) error {
	bad := func() error { return fmt.Errorf("invalid branch or ref name %q", name) }
	if name == "" || len(name) > 255 || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasPrefix(name, ".") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") ||
		strings.HasSuffix(name, ".lock") || strings.Contains(name, "..") || strings.Contains(name, "//") ||
		strings.Contains(name, "@{") || strings.Contains(name, "/.") || name == "@" {
		return bad()
	}
	for _, r := range name {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return bad()
		}
	}
	return nil
}

var remoteHelper = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*::`)

// validCloneURL rejects URLs git would read as options or that use a remote
// helper (ext::, fd::).
func validCloneURL(u string) error {
	switch {
	case u == "":
		return errors.New("clone: no URL")
	case strings.HasPrefix(u, "-"), strings.ContainsAny(u, "\x00 \t\r\n"):
		return fmt.Errorf("clone: invalid URL %q", u)
	case remoteHelper.MatchString(u):
		return errors.New("clone: remote helper URLs are not allowed")
	}
	return nil
}

// SanitizeRemoteURL removes credentials from a remote URL: all user info of
// http(s) URLs, the password of others. Unparsable URLs become "".
func SanitizeRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		// scp-like (git@host:owner/repo) or a local path: no password possible
		// before the colon form, but strip a "user:pass@" just in case.
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			if colon := strings.Index(raw[:at], ":"); colon >= 0 && !strings.Contains(raw[:at], "/") {
				return raw[:colon] + raw[at:]
			}
		}
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.User != nil {
		if u.Scheme == "http" || u.Scheme == "https" {
			u.User = nil
		} else {
			u.User = url.User(u.User.Username())
		}
	}
	return u.String()
}

// projectName names a repository: owner/repo from its origin URL, else the
// directory's name.
func projectName(remoteURL, repoAbsPath string) string {
	p := remotePath(remoteURL)
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	p = strings.Trim(p, "/")
	if p == "" {
		return filepath.Base(repoAbsPath)
	}
	segs := strings.Split(p, "/")
	if len(segs) == 1 {
		return segs[0]
	}
	return segs[len(segs)-2] + "/" + segs[len(segs)-1]
}

// remotePath is the path part of a remote URL ("" for local remotes, which
// are named after their directory).
func remotePath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "file" {
			if err == nil {
				return filepath.Base(u.Path)
			}
			return ""
		}
		return u.Path
	}
	// scp-like: [user@]host:path, where host has no slash.
	if colon := strings.Index(raw, ":"); colon > 0 && !strings.Contains(raw[:colon], "/") {
		return raw[colon+1:]
	}
	return filepath.Base(raw) // a local path
}

// repoBase is the last segment of a project name.
func repoBase(project string) string {
	if i := strings.LastIndex(project, "/"); i >= 0 {
		return project[i+1:]
	}
	return project
}

// toplevel returns the root of the repository dir is in, if that root is
// inside the home (a home inside some other repository is not a repository).
func (d *Daemon) toplevel(ctx context.Context, dir string) (string, bool) {
	out, err := d.runGit(ctx, gitShortTimeout, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", false
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	if !inside(d.realHome(), top) {
		return "", false
	}
	return top, true
}

// inspect describes the repository whose root is top (a real path).
func (d *Daemon) inspect(ctx context.Context, top string) (tunnel.GitRepo, error) {
	rel, ok := d.gitHomeRel(top)
	if !ok {
		return tunnel.GitRepo{}, fmt.Errorf("%s is outside the home", top)
	}
	var repo tunnel.GitRepo
	repo.Path = rel
	remote, _ := d.runGit(ctx, gitShortTimeout, top, "config", "--get", "remote.origin.url")
	remote = strings.TrimSpace(remote)
	repo.RemoteURL = SanitizeRemoteURL(remote)
	repo.Project = projectName(remote, top)

	if out, err := d.runGit(ctx, gitShortTimeout, top, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		repo.Branch = strings.TrimSpace(out)
	} else {
		repo.Detached = true
		if out, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--short", "HEAD"); err == nil {
			repo.Branch = strings.TrimSpace(out)
		}
	}
	if out, err := d.runGit(ctx, gitShortTimeout, top, "status", "--porcelain", "--untracked-files=normal"); err == nil {
		repo.Dirty = strings.TrimSpace(out) != ""
	}
	if out, err := d.runGit(ctx, gitShortTimeout, top, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		if f := strings.Fields(out); len(f) == 2 {
			repo.Ahead, _ = strconv.Atoi(f[0])
			repo.Behind, _ = strconv.Atoi(f[1])
		}
	}
	if out, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir"); err == nil {
		if f := strings.Split(strings.TrimSpace(out), "\n"); len(f) == 2 {
			gitDir, common := realPath(f[0]), realPath(f[1])
			if gitDir != common {
				repo.IsWorktree = true
				main := common
				if filepath.Base(common) == ".git" {
					main = filepath.Dir(common)
				}
				if mrel, ok := d.gitHomeRel(main); ok {
					repo.MainPath = mrel
				}
			}
		}
	}
	return repo, nil
}

func realPath(p string) string {
	p = strings.TrimSpace(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func (d *Daemon) branches(ctx context.Context, top string) []string {
	out, err := d.runGit(ctx, gitShortTimeout, top, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	var bs []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			bs = append(bs, l)
		}
	}
	return bs
}

// repoAt returns the repository rooted exactly at the request path.
func (d *Daemon) repoAt(ctx context.Context, p string) (string, error) {
	abs, err := d.homePath(p)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	top, ok := d.toplevel(ctx, abs)
	if !ok {
		return "", fmt.Errorf("%s is not in a git repository", abs)
	}
	return top, nil
}

func (d *Daemon) gitList(ctx context.Context) tunnel.GitReply {
	var repos []tunnel.GitRepo
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if len(repos) >= gitListMax || ctx.Err() != nil {
			return
		}
		isRepo := false
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			if top, ok := d.toplevel(ctx, dir); ok && top == realPath(dir) {
				if repo, err := d.inspect(ctx, top); err == nil {
					repos = append(repos, repo)
					isRepo = true
				}
			}
		}
		if depth >= gitListDepth {
			return
		}
		if isRepo && depth > 0 {
			// Only a repository's .worktrees are searched further.
			wt := filepath.Join(dir, ".worktrees")
			if fi, err := os.Lstat(wt); err == nil && fi.IsDir() {
				walk(wt, depth+1)
			}
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || gitSkipDirs[e.Name()] {
				continue
			}
			walk(filepath.Join(dir, e.Name()), depth+1)
		}
	}
	walk(d.acct.home, 0)
	sort.SliceStable(repos, func(i, j int) bool {
		if repos[i].IsWorktree != repos[j].IsWorktree {
			return !repos[i].IsWorktree
		}
		return repos[i].Path < repos[j].Path
	})
	return tunnel.GitReply{Repos: repos}
}

func (d *Daemon) gitStatus(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	abs, err := d.homePath(req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return tunnel.GitReply{}, fmt.Errorf("%s is not a directory", abs)
	}
	top, ok := d.toplevel(ctx, abs)
	if !ok {
		return tunnel.GitReply{}, nil
	}
	return d.repoReply(ctx, top, "")
}

func (d *Daemon) repoReply(ctx context.Context, top, output string) (tunnel.GitReply, error) {
	repo, err := d.inspect(ctx, top)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return tunnel.GitReply{Repo: &repo, Branches: d.branches(ctx, top), Output: scrubCredentials(output)}, nil
}

func (d *Daemon) gitClone(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	if err := validCloneURL(req.URL); err != nil {
		return tunnel.GitReply{}, err
	}
	if req.Branch != "" {
		if err := validRef(req.Branch); err != nil {
			return tunnel.GitReply{}, err
		}
	}
	target := req.Path
	if target == "" {
		name := repoBase(projectName(req.URL, strings.TrimSuffix(req.URL, "/")))
		name = strings.TrimSuffix(name, ".git")
		if name == "" || name == "." || name == ".." || strings.HasPrefix(name, "-") {
			return tunnel.GitReply{}, errors.New("clone: cannot derive a directory name from the URL; give a path")
		}
		target = filepath.Join("projects", name)
	}
	abs, err := d.homePath(target)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if abs == filepath.Clean(d.acct.home) {
		return tunnel.GitReply{}, errors.New("clone: cannot clone into the home itself")
	}
	if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 {
		return tunnel.GitReply{}, fmt.Errorf("clone: %s already exists and is not empty", abs)
	}
	if err := d.mkdirAll(filepath.Dir(abs)); err != nil {
		return tunnel.GitReply{}, err
	}
	args := []string{"clone"}
	if req.Branch != "" {
		args = append(args, "-b", req.Branch)
	}
	args = append(args, "--", req.URL, abs)
	out, err := d.runGit(ctx, gitLongTimeout, filepath.Dir(abs), args...)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, realPath(abs), out)
}

func (d *Daemon) gitCheckout(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	if err := validRef(req.Branch); err != nil {
		return tunnel.GitReply{}, err
	}
	if req.FromRef != "" {
		if err := validRef(req.FromRef); err != nil {
			return tunnel.GitReply{}, err
		}
	}
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	// git switch takes no pathspecs, so a branch can never be read as one;
	// validRef already keeps names from looking like options.
	args := []string{"switch"}
	if req.Create {
		args = append(args, "-c", req.Branch)
		if req.FromRef != "" {
			args = append(args, req.FromRef)
		}
	} else {
		args = append(args, req.Branch)
	}
	out, err := d.runGit(ctx, gitShortTimeout, top, args...)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, top, out)
}

// sanitizeBranchDir turns a branch name into one directory name.
func sanitizeBranchDir(b string) string {
	var sb strings.Builder
	for _, r := range b {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	s := strings.Trim(sb.String(), "-.")
	if s == "" {
		s = "worktree"
	}
	return s
}

func (d *Daemon) gitWorktreeAdd(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	if err := validRef(req.Branch); err != nil {
		return tunnel.GitReply{}, err
	}
	if req.FromRef != "" {
		if err := validRef(req.FromRef); err != nil {
			return tunnel.GitReply{}, err
		}
	}
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	wtPath := req.WorktreePath
	if wtPath == "" {
		wtPath = filepath.Join(".worktrees", filepath.Base(top), sanitizeBranchDir(req.Branch))
	}
	abs, err := d.homePath(wtPath)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if _, err := os.Lstat(abs); err == nil {
		return tunnel.GitReply{}, fmt.Errorf("worktree: %s already exists", abs)
	}
	if err := d.mkdirAll(filepath.Dir(abs)); err != nil {
		return tunnel.GitReply{}, err
	}
	args := []string{"worktree", "add"}
	if req.Create {
		args = append(args, "-b", req.Branch, "--", abs)
		if req.FromRef != "" {
			args = append(args, req.FromRef)
		}
	} else {
		args = append(args, "--", abs, req.Branch)
	}
	out, err := d.runGit(ctx, gitShortTimeout, top, args...)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, realPath(abs), out)
}

func (d *Daemon) gitPull(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	args := []string{"pull", "--ff-only"}
	if req.FetchOnly {
		args = []string{"fetch", "--prune"}
	}
	out, err := d.runGit(ctx, gitLongTimeout, top, args...)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, top, out)
}

func (d *Daemon) gitPush(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if _, err := d.runGit(ctx, gitShortTimeout, top, "symbolic-ref", "-q", "HEAD"); err != nil {
		return tunnel.GitReply{}, errors.New("push: HEAD is detached; switch to a branch first")
	}
	args := []string{"push"}
	if req.Force {
		args = append(args, "--force-with-lease")
	}
	if _, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		// No upstream yet: push to origin (or the only remote) and track it.
		remote, err := d.pushRemote(ctx, top)
		if err != nil {
			return tunnel.GitReply{}, err
		}
		args = append(args, "--set-upstream", remote, "HEAD")
	}
	out, err := d.runGit(ctx, gitLongTimeout, top, args...)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, top, out)
}

// pushRemote is the remote a branch without an upstream is pushed to:
// origin, or the repository's only remote.
func (d *Daemon) pushRemote(ctx context.Context, top string) (string, error) {
	out, err := d.runGit(ctx, gitShortTimeout, top, "remote")
	if err != nil {
		return "", err
	}
	var remotes []string
	for _, r := range strings.Fields(out) {
		if r == "origin" {
			return r, nil
		}
		if !strings.HasPrefix(r, "-") {
			remotes = append(remotes, r)
		}
	}
	switch len(remotes) {
	case 0:
		return "", errors.New("push: the repository has no remote")
	case 1:
		return remotes[0], nil
	}
	return "", fmt.Errorf("push: the branch has no upstream and there is no origin among the remotes %s; push once with git push -u <remote>", strings.Join(remotes, ", "))
}

func (d *Daemon) gitWorktreeRemove(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	repo, err := d.inspect(ctx, top)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if !repo.IsWorktree || repo.MainPath == "" {
		return tunnel.GitReply{}, fmt.Errorf("worktree: %s is not a linked worktree of a repository in the home", top)
	}
	main, err := d.homePath(repo.MainPath)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	args := []string{"worktree", "remove"}
	if req.Force {
		args = append(args, "--force")
	}
	args = append(args, "--", top)
	out, err := d.runGit(ctx, gitShortTimeout, main, args...)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, realPath(main), out)
}

// gitConfigValue quotes an INI value for a git config file.
func gitConfigValue(v string) (string, error) {
	if strings.ContainsAny(v, "\r\n\x00") {
		return "", errors.New("git config values cannot contain line breaks")
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`, nil
}

func (d *Daemon) gitConfig(req tunnel.GitRequest) error {
	path := filepath.Join(d.acct.home, gitConfigFile)
	if req.AuthorName == "" && req.AuthorEmail == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	var b strings.Builder
	b.WriteString("[user]\n")
	for _, kv := range [][2]string{{"name", req.AuthorName}, {"email", req.AuthorEmail}} {
		if kv[1] == "" {
			continue
		}
		v, err := gitConfigValue(kv[1])
		if err != nil {
			return err
		}
		b.WriteString("\t" + kv[0] + " = " + v + "\n")
	}
	if _, err := d.homePath(gitConfigFile); err != nil {
		return err
	}
	if err := d.mkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(b.String()), 0o644); err != nil {
		return err
	}
	d.own(path)
	return nil
}
