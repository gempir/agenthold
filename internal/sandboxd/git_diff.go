package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/tunnel"
)

// Diff and log limits. A reply stays well below what the server reads
// (tunnel's data-framed JSON limit) even when JSON escaping grows it.
const (
	gitDiffTimeout          = 60 * time.Second
	gitDiffMaxFiles         = 1000
	gitDiffMaxFilePatch     = 256 << 10
	gitDiffMaxPatch         = 4 << 20
	gitDiffMaxUntracked     = 200     // untracked files that get a patch
	gitDiffMaxUntrackedSize = 1 << 20 // larger untracked files get none
	gitDiffMaxContext       = 1000
	gitLogDefault           = 50
	gitLogMax               = 500
	gitLogMaxBody           = 8 << 10
	gitCommitMaxMessage     = 64 << 10
)

// diffBase returns the arguments a diff of mode compares (from, and for a
// commit to) and fills in what the reply says about them.
func (d *Daemon) diffBase(ctx context.Context, top string, req tunnel.GitRequest, set *tunnel.GitDiffSet) (from, to string, untracked bool, err error) {
	switch set.Mode {
	case tunnel.DiffUncommitted:
		if _, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--verify", "-q", "HEAD^{commit}"); err != nil {
			from, err = d.emptyTree(ctx, top) // no commit yet
			return from, "", true, err
		}
		return "HEAD", "", true, nil
	case tunnel.DiffBranch:
		base := req.Base
		if base == "" {
			if base = d.defaultBranch(ctx, top); base == "" {
				return "", "", false, errors.New("diff: no default branch (origin/HEAD, origin/main, origin/master, main or master) to compare with; give a base")
			}
		}
		if err := validRef(base); err != nil {
			return "", "", false, err
		}
		out, err := d.runGit(ctx, gitShortTimeout, top, "merge-base", base, "HEAD")
		if err != nil {
			return "", "", false, fmt.Errorf("diff: no common ancestor of %s and HEAD", base)
		}
		set.Base, set.MergeBase = base, strings.TrimSpace(out)
		return set.MergeBase, "", true, nil
	case tunnel.DiffCommit:
		if err := validRef(req.Ref); err != nil {
			return "", "", false, err
		}
		out, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--verify", "-q", req.Ref+"^{commit}")
		if err != nil {
			return "", "", false, fmt.Errorf("diff: no commit %q", req.Ref)
		}
		sha := strings.TrimSpace(out)
		set.Ref = sha
		if out, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--verify", "-q", sha+"^1"); err == nil {
			return strings.TrimSpace(out), sha, false, nil
		}
		from, err = d.emptyTree(ctx, top) // a root commit
		return from, sha, false, err
	}
	return "", "", false, fmt.Errorf("diff: unknown mode %q (uncommitted, branch or commit)", set.Mode)
}

// emptyTree is the id of the empty tree in the repository's hash format.
func (d *Daemon) emptyTree(ctx context.Context, top string) (string, error) {
	out, err := d.runGit(ctx, gitShortTimeout, top, "hash-object", "-t", "tree", os.DevNull)
	return strings.TrimSpace(out), err
}

// defaultBranch guesses the branch a branch diff compares with.
func (d *Daemon) defaultBranch(ctx context.Context, top string) string {
	if out, err := d.runGit(ctx, gitShortTimeout, top, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimSpace(out); b != "" && validRef(b) == nil {
			return b
		}
	}
	for _, b := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--verify", "-q", b+"^{commit}"); err == nil {
			return b
		}
	}
	return ""
}

// diffStatus maps git's status letters to A, M, D and R.
func diffStatus(s string) string {
	switch s {
	case "A", "C":
		return "A"
	case "D":
		return "D"
	case "R":
		return "R"
	}
	return "M"
}

func (d *Daemon) gitDiff(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	ctxLines := 3
	if req.Context != nil {
		ctxLines = min(max(*req.Context, 0), gitDiffMaxContext)
	}
	set := &tunnel.GitDiffSet{Mode: req.Mode, Files: []tunnel.GitDiffFile{}}
	if set.Mode == "" {
		set.Mode = tunnel.DiffUncommitted
	}
	from, to, untracked, err := d.diffBase(ctx, top, req, set)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	revs := []string{from}
	if to != "" {
		revs = append(revs, to)
	}
	diff := func(maxOut int, extra ...string) gitResult {
		args := append([]string{"-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff", "-M"}, extra...)
		args = append(append(args, revs...), "--")
		return d.runGitCapped(ctx, gitDiffTimeout, top, maxOut, true, args...)
	}

	// Which files changed, how, and by how many lines.
	ns := diff(8<<20, "--name-status", "-z")
	if ns.err != nil {
		return tunnel.GitReply{}, ns.err
	}
	files := parseNameStatus(ns.out)
	if ns.truncated || len(files) > gitDiffMaxFiles {
		set.Truncated = true
		files = files[:min(len(files), gitDiffMaxFiles)]
	}
	if num := diff(8<<20, "--numstat", "-z"); num.err == nil {
		applyNumstat(files, num.out)
	}

	// The patch, split into one part per file (git writes them in the same
	// order as the name-status above).
	budget := gitDiffMaxPatch
	if len(files) > 0 {
		p := diff(gitDiffMaxPatch+gitDiffMaxFilePatch, "-U"+strconv.Itoa(ctxLines))
		if p.err != nil {
			return tunnel.GitReply{}, p.err
		}
		parts := splitPatch(p.out)
		for i := range files {
			f := &files[i]
			if f.Binary {
				continue
			}
			if i >= len(parts) || (p.truncated && i == len(parts)-1) || !patchFor(parts[i], *f) {
				f.Truncated = true
				continue
			}
			if len(parts[i]) > gitDiffMaxFilePatch || len(parts[i]) > budget {
				f.Truncated = true
				continue
			}
			f.Patch = parts[i]
			budget -= len(parts[i])
		}
	}

	if untracked {
		out := d.runGitCapped(ctx, gitShortTimeout, top, 4<<20, true, "-c", "core.quotePath=false", "ls-files", "--others", "--exclude-standard", "-z")
		if out.err != nil {
			return tunnel.GitReply{}, out.err
		}
		patched := 0
		for _, p := range strings.Split(out.out, "\x00") {
			if p == "" {
				continue
			}
			if len(files) >= gitDiffMaxFiles {
				set.Truncated = true
				break
			}
			f := tunnel.GitDiffFile{Path: p, Status: "?"}
			fi, err := os.Lstat(filepath.Join(top, filepath.FromSlash(p)))
			switch {
			case err != nil:
				f.Truncated = true
			case patched >= gitDiffMaxUntracked || fi.Size() > gitDiffMaxUntrackedSize || budget <= 0:
				f.Truncated = true
			default:
				patched++
				r := d.runGitCapped(ctx, gitShortTimeout, top, gitDiffMaxFilePatch+1, true,
					"-c", "core.quotePath=false", "diff", "--no-index", "--no-color", "--no-ext-diff", "-U"+strconv.Itoa(ctxLines), "--", os.DevNull, p)
				switch {
				case r.err != nil:
					f.Truncated = true
				case strings.Contains(r.out, "\nBinary files ") || strings.HasPrefix(r.out, "Binary files "):
					f.Binary = true
				case r.truncated || len(r.out) > budget:
					f.Truncated = true
				default:
					f.Patch = untrackedPatch(r.out, p)
					f.Additions = countAdded(r.out)
					budget -= len(f.Patch)
				}
			}
			files = append(files, f)
		}
	}
	for _, f := range files {
		set.Additions += f.Additions
		set.Deletions += f.Deletions
	}
	set.Files = files
	reply, err := d.repoReply(ctx, top, "")
	if err != nil {
		return tunnel.GitReply{}, err
	}
	reply.Branches = nil
	reply.Diff = set
	return reply, nil
}

// parseNameStatus parses git diff --name-status -z.
func parseNameStatus(out string) []tunnel.GitDiffFile {
	f := strings.Split(out, "\x00")
	var files []tunnel.GitDiffFile
	for i := 0; i < len(f); i++ {
		st := f[i]
		if st == "" {
			continue
		}
		letter := st[:1]
		if (letter == "R" || letter == "C") && i+2 < len(f) {
			files = append(files, tunnel.GitDiffFile{OldPath: f[i+1], Path: f[i+2], Status: diffStatus(letter)})
			i += 2
			continue
		}
		if i+1 < len(f) {
			files = append(files, tunnel.GitDiffFile{Path: f[i+1], Status: diffStatus(letter)})
			i++
		}
	}
	return files
}

// applyNumstat adds the line counts of git diff --numstat -z to files.
func applyNumstat(files []tunnel.GitDiffFile, out string) {
	byPath := make(map[string]int, len(files))
	for i, f := range files {
		byPath[f.Path] = i
	}
	f := strings.Split(out, "\x00")
	for i := 0; i < len(f); i++ {
		parts := strings.SplitN(f[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		p := parts[2]
		if p == "" && i+2 < len(f) { // a rename: old and new path follow
			p = f[i+2]
			i += 2
		}
		idx, ok := byPath[p]
		if !ok {
			continue
		}
		if parts[0] == "-" && parts[1] == "-" {
			files[idx].Binary = true
			continue
		}
		files[idx].Additions, _ = strconv.Atoi(parts[0])
		files[idx].Deletions, _ = strconv.Atoi(parts[1])
	}
}

// splitPatch splits a unified diff into one part per file.
func splitPatch(out string) []string {
	var parts []string
	start := -1
	for i := 0; i < len(out); {
		if strings.HasPrefix(out[i:], "diff --git ") {
			if start >= 0 {
				parts = append(parts, out[start:i])
			}
			start = i
		}
		nl := strings.IndexByte(out[i:], '\n')
		if nl < 0 {
			break
		}
		i += nl + 1
	}
	if start >= 0 {
		parts = append(parts, out[start:])
	}
	return parts
}

// patchFor checks that a patch part is about f (a sanity check of the
// order git writes them in).
func patchFor(part string, f tunnel.GitDiffFile) bool {
	header, _, _ := strings.Cut(part, "\n")
	if f.Status == "D" {
		return strings.Contains(header, " a/"+f.Path)
	}
	return strings.HasSuffix(header, " b/"+f.Path) || strings.HasSuffix(header, "/"+f.Path+`"`)
}

// untrackedPatch makes git diff --no-index's patch of a new file look like
// one of a tracked file (a/ and b/ paths instead of /dev/null).
func untrackedPatch(out, p string) string {
	header, rest, ok := strings.Cut(out, "\n")
	if !ok || !strings.HasPrefix(header, "diff --git ") {
		return out
	}
	return "diff --git a/" + p + " b/" + p + "\n" + rest
}

// countAdded counts the added lines of a patch.
func countAdded(patch string) int {
	n := 0
	inHunk := false
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(l, "+"):
			n++
		}
	}
	return n
}

// gitLogFormat separates commits with RS and fields with US; --shortstat
// follows the fields.
const gitLogFormat = "--format=%x1e%H%x1f%h%x1f%an%x1f%ae%x1f%at%x1f%s%x1f%b%x1f"

var (
	statFiles = regexp.MustCompile(`(\d+) files? changed`)
	statAdds  = regexp.MustCompile(`(\d+) insertions?\(\+\)`)
	statDels  = regexp.MustCompile(`(\d+) deletions?\(-\)`)
)

// parseLog parses git log with gitLogFormat and --shortstat.
func parseLog(out string) []tunnel.GitCommit {
	var commits []tunnel.GitCommit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(rec, "\x1f", 8)
		if len(f) < 8 {
			continue
		}
		c := tunnel.GitCommit{SHA: f[0], ShortSHA: f[1], Author: f[2], Email: f[3], Subject: f[5], Body: strings.TrimSpace(f[6])}
		c.Date, _ = strconv.ParseInt(f[4], 10, 64)
		if len(c.Body) > gitLogMaxBody {
			c.Body = c.Body[:gitLogMaxBody]
		}
		stat := f[7]
		if m := statFiles.FindStringSubmatch(stat); m != nil {
			c.FilesChanged, _ = strconv.Atoi(m[1])
		}
		if m := statAdds.FindStringSubmatch(stat); m != nil {
			c.Additions, _ = strconv.Atoi(m[1])
		}
		if m := statDels.FindStringSubmatch(stat); m != nil {
			c.Deletions, _ = strconv.Atoi(m[1])
		}
		commits = append(commits, c)
	}
	return commits
}

func (d *Daemon) gitLog(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = gitLogDefault
	}
	limit = min(limit, gitLogMax)
	ref := "HEAD"
	if req.Ref != "" {
		if err := validRef(req.Ref); err != nil {
			return tunnel.GitReply{}, err
		}
		ref = req.Ref
	}
	reply, err := d.repoReply(ctx, top, "")
	if err != nil {
		return tunnel.GitReply{}, err
	}
	reply.Branches = nil
	reply.Commits = []tunnel.GitCommit{}
	if _, err := d.runGit(ctx, gitShortTimeout, top, "rev-parse", "--verify", "-q", ref+"^{commit}"); err != nil {
		if req.Ref != "" {
			return tunnel.GitReply{}, fmt.Errorf("log: no commit %q", req.Ref)
		}
		return reply, nil // no commit yet
	}
	r := d.runGitCapped(ctx, gitDiffTimeout, top, 8<<20, true,
		"-c", "core.quotePath=false", "log", "--no-color", "-n", strconv.Itoa(limit), gitLogFormat, "--shortstat", ref, "--")
	if r.err != nil {
		return tunnel.GitReply{}, r.err
	}
	if c := parseLog(r.out); c != nil {
		reply.Commits = c
	}
	return reply, nil
}

func (d *Daemon) gitCommit(ctx context.Context, req tunnel.GitRequest) (tunnel.GitReply, error) {
	msg := strings.TrimSpace(req.Message)
	switch {
	case msg == "":
		return tunnel.GitReply{}, errors.New("commit: a message is required")
	case len(msg) > gitCommitMaxMessage || strings.ContainsRune(msg, 0):
		return tunnel.GitReply{}, errors.New("commit: the message is too long or contains NUL bytes")
	}
	top, err := d.repoAt(ctx, req.Path)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	if req.All {
		if _, err := d.runGit(ctx, gitLongTimeout, top, "add", "-A"); err != nil {
			return tunnel.GitReply{}, err
		}
	}
	staged := d.runGitCapped(ctx, gitShortTimeout, top, gitMaxOutput, true, "diff", "--cached", "--quiet")
	if staged.err != nil {
		return tunnel.GitReply{}, staged.err
	}
	if staged.code == 0 {
		return tunnel.GitReply{}, errors.New("commit: nothing to commit (stage changes first, or commit all)")
	}
	args := append(d.identityArgs(ctx, top), "commit", "-q", "-m", msg)
	out, err := d.runGit(ctx, gitLongTimeout, top, args...)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	reply, err := d.repoReply(ctx, top, out)
	if err != nil {
		return tunnel.GitReply{}, err
	}
	r := d.runGitCapped(ctx, gitShortTimeout, top, 1<<20, true, "log", "--no-color", "-n", "1", gitLogFormat, "--shortstat", "HEAD", "--")
	if c := parseLog(r.out); r.err == nil && len(c) == 1 {
		reply.Commit = &c[0]
	}
	return reply, nil
}
