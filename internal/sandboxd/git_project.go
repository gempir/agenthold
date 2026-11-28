package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gempir/agenthold/internal/tunnel"
)

// gitMaxChangedFiles bounds the files GitChanges reports.
const gitMaxChangedFiles = 500

// identityArgs are -c options that give git a commit identity when the
// repository at dir has none configured (a machine whose owner set none).
func (d *Daemon) identityArgs(ctx context.Context, dir string) []string {
	args := []string{"-c", "commit.gpgsign=false"}
	if out, err := d.runGit(ctx, gitShortTimeout, dir, "config", "user.email"); err != nil || strings.TrimSpace(out) == "" {
		args = append(args, "-c", "user.name=agenthold", "-c", "user.email=agenthold@localhost")
	}
	return args
}

func (d *Daemon) gitInit(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	branch := req.Branch
	if branch == "" {
		branch = "main"
	}
	if err := validRef(branch); err != nil {
		return tunnel.GitReply{}, err
	}
	abs, err := d.homePath(req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if abs == filepath.Clean(d.acct.home) {
		return tunnel.GitReply{}, errors.New("init: cannot make the home a repository")
	}
	if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
		return tunnel.GitReply{}, fmt.Errorf("init: %s is not a directory", abs)
	}
	if err := d.mkdirAll(abs); err != nil {
		return tunnel.GitReply{}, err
	}
	if top, ok := d.toplevel(ctx, abs); ok && top == realPath(abs) {
		return d.repoReply(ctx, top, "")
	}
	out, err := d.runGit(ctx, gitShortTimeout, abs, "init", "-q", "-b", branch)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if _, err := d.runGit(ctx, gitLongTimeout, abs, "add", "-A"); err != nil {
		return tunnel.GitReply{}, err
	}
	msg := req.Message
	if msg == "" {
		msg = "Initial snapshot"
	}
	args := append(d.identityArgs(ctx, abs), "commit", "-q", "--allow-empty", "-m", msg)
	if _, err := d.runGit(ctx, gitLongTimeout, abs, args...); err != nil {
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, realPath(abs), out)
}

func (d *Daemon) gitChanges(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	if err := validRef(req.FromRef); err != nil {
		return tunnel.GitReply{}, err
	}
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	out, err := d.runGit(ctx, gitShortTimeout, top, "merge-base", req.FromRef, "HEAD")
	if err != nil {
		return tunnel.GitReply{}, err
	}
	base := strings.TrimSpace(out)
	ch := &tunnel.GitChangeSet{Base: base}
	if out, err := d.runGit(ctx, gitShortTimeout, top, "rev-list", "--count", base+"..HEAD"); err == nil {
		ch.Commits, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	seen := map[string]bool{}
	add := func(path, status string) {
		if path == "" || seen[path] {
			return
		}
		if len(ch.Files) >= gitMaxChangedFiles {
			ch.Truncated = true
			return
		}
		seen[path] = true
		ch.Files = append(ch.Files, tunnel.GitFileChange{Path: path, Status: status})
	}
	// The work tree against the fork point: committed and uncommitted
	// changes of tracked files.
	out, err = d.runGit(ctx, gitShortTimeout, top, "-c", "core.quotePath=false", "diff", "--name-status", "--no-renames", base)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	for _, l := range strings.Split(out, "\n") {
		if status, path, ok := strings.Cut(l, "\t"); ok {
			add(path, status[:1])
		}
	}
	if out, err := d.runGit(ctx, gitShortTimeout, top, "-c", "core.quotePath=false", "ls-files", "--others", "--exclude-standard"); err == nil {
		for _, l := range strings.Split(out, "\n") {
			add(strings.TrimSpace(l), "?")
		}
	}
	if out, err := d.runGit(ctx, gitShortTimeout, top, "status", "--porcelain", "--untracked-files=normal"); err == nil {
		ch.Dirty = strings.TrimSpace(out) != ""
	}
	reply, err := d.repoReply(ctx, top, "")
	if err != nil {
		return tunnel.GitReply{}, err
	}
	reply.Changes = ch
	return reply, nil
}

func (d *Daemon) gitMerge(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	if err := validRef(req.Branch); err != nil {
		return tunnel.GitReply{}, err
	}
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if _, err := d.runGit(ctx, gitShortTimeout, top, "symbolic-ref", "-q", "HEAD"); err != nil {
		return tunnel.GitReply{}, errors.New("merge: HEAD is detached; switch to a branch first")
	}
	if out, err := d.runGit(ctx, gitShortTimeout, top, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return tunnel.GitReply{}, err
	} else if strings.TrimSpace(out) != "" {
		return tunnel.GitReply{}, fmt.Errorf("merge: %s has uncommitted changes; commit or stash them first", top)
	}
	if _, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--verify", "-q", "refs/heads/"+req.Branch); err != nil {
		return tunnel.GitReply{}, fmt.Errorf("merge: no branch %q", req.Branch)
	}
	msg := req.Message
	if msg == "" {
		msg = "Merge branch '" + req.Branch + "'"
	}
	args := append(d.identityArgs(ctx, top), "merge", "--no-ff", "--no-edit", "-m", msg, "refs/heads/"+req.Branch)
	out, err := d.runGit(ctx, gitLongTimeout, top, args...)
	if err != nil {
		conflicts, _ := d.runGit(ctx, gitShortTimeout, top, "-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=U")
		_, _ = d.runGit(ctx, gitShortTimeout, top, "merge", "--abort")
		if files := strings.Fields(conflicts); len(files) > 0 {
			return tunnel.GitReply{}, fmt.Errorf("merge: conflicts in %s; the merge was aborted", strings.Join(files, ", "))
		}
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, top, out)
}

func (d *Daemon) gitBranchDelete(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	if err := validRef(req.Branch); err != nil {
		return tunnel.GitReply{}, err
	}
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	flag := "-d"
	if req.Force {
		flag = "-D"
	}
	out, err := d.runGit(ctx, gitShortTimeout, top, "branch", flag, req.Branch)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	return d.repoReply(ctx, top, out)
}
