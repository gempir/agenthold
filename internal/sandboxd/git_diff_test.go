package sandboxd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/tunnel"
)

func gitData(t *testing.T, s *tunnel.Session, req tunnel.GitRequest) tunnel.GitReply {
	t.Helper()
	r, err := s.GitData(t.Context(), req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Action, req.Mode, err)
	}
	return r
}

func diffFile(t *testing.T, set *tunnel.GitDiffSet, p string) tunnel.GitDiffFile {
	t.Helper()
	if set == nil {
		t.Fatal("no diff")
	}
	for _, f := range set.Files {
		if f.Path == p {
			return f
		}
	}
	t.Fatalf("no %s in %+v", p, set.Files)
	return tunnel.GitDiffFile{}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitDiffModes(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	repo := filepath.Join(home, "projects", "demo")
	gitT(t, home, "clone", "-q", newOrigin(t), repo)
	commit(t, repo, "keep.txt", "one\ntwo\nthree\n")
	commit(t, repo, "gone.txt", "bye\n")
	commit(t, repo, "old name.txt", "renamed content line\nanother line\nand one more\n")

	// Uncommitted: staged, unstaged, deleted, renamed, untracked, binary.
	write(t, filepath.Join(repo, "keep.txt"), "one\nTWO\nthree\nfour\n")
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "mv", "old name.txt", "new name.txt")
	write(t, filepath.Join(repo, "staged.txt"), "staged\n")
	gitT(t, repo, "add", "staged.txt")
	write(t, filepath.Join(repo, "sub", "untracked.go"), "package sub\n\nfunc A() {}\n")
	write(t, filepath.Join(repo, "blob.bin"), "\x00\x01\x02binary")
	write(t, filepath.Join(repo, ".gitignore"), "ignored.log\n")
	write(t, filepath.Join(repo, "ignored.log"), "noise\n")

	r := gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "projects/demo/sub", Mode: tunnel.DiffUncommitted})
	if r.Repo == nil || r.Repo.Path != "projects/demo" || r.Diff.Mode != tunnel.DiffUncommitted {
		t.Fatalf("reply %+v", r)
	}
	keep := diffFile(t, r.Diff, "keep.txt")
	if keep.Status != "M" || keep.Additions != 2 || keep.Deletions != 1 || !strings.Contains(keep.Patch, "+TWO") || !strings.HasPrefix(keep.Patch, "diff --git a/keep.txt b/keep.txt") {
		t.Fatalf("keep %+v", keep)
	}
	if g := diffFile(t, r.Diff, "gone.txt"); g.Status != "D" || g.Deletions != 1 || !strings.Contains(g.Patch, "-bye") {
		t.Fatalf("gone %+v", g)
	}
	if rn := diffFile(t, r.Diff, "new name.txt"); rn.Status != "R" || rn.OldPath != "old name.txt" {
		t.Fatalf("rename %+v", rn)
	}
	if st := diffFile(t, r.Diff, "staged.txt"); st.Status != "A" || st.Additions != 1 {
		t.Fatalf("staged %+v", st)
	}
	ut := diffFile(t, r.Diff, "sub/untracked.go")
	if ut.Status != "?" || ut.Additions != 3 || !strings.HasPrefix(ut.Patch, "diff --git a/sub/untracked.go b/sub/untracked.go\n") || !strings.Contains(ut.Patch, "+func A() {}") {
		t.Fatalf("untracked %+v", ut)
	}
	if b := diffFile(t, r.Diff, "blob.bin"); !b.Binary || b.Patch != "" {
		t.Fatalf("binary %+v", b)
	}
	for _, f := range r.Diff.Files {
		if f.Path == "ignored.log" {
			t.Fatal("ignored file listed")
		}
	}
	if r.Diff.Additions < 6 || r.Diff.Deletions < 2 {
		t.Fatalf("totals %+v", r.Diff)
	}

	// Context lines.
	zero := 0
	r = gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "projects/demo", Context: &zero})
	if p := diffFile(t, r.Diff, "keep.txt").Patch; strings.Contains(p, "\n one\n") {
		t.Fatalf("context 0 patch has context: %q", p)
	}

	// Commit all, then commit mode and the log.
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitCreateCommit, Path: "projects/demo", Message: "Work\n\nThe body.", All: true})
	if r.Commit == nil || r.Commit.Subject != "Work" || r.Commit.Body != "The body." || r.Commit.FilesChanged < 5 || r.Repo.Dirty {
		t.Fatalf("commit %+v %+v", r.Commit, r.Repo)
	}
	sha := r.Commit.SHA
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitCreateCommit, Path: "projects/demo", Message: "again", All: true}); err == nil || !strings.Contains(err.Error(), "nothing to commit") {
		t.Fatalf("empty commit: %v", err)
	}
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitCreateCommit, Path: "projects/demo", Message: "  "}); err == nil {
		t.Fatal("commit without a message")
	}
	r = gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "projects/demo", Mode: tunnel.DiffCommit, Ref: sha[:10]})
	if r.Diff.Ref != sha || len(r.Diff.Files) < 5 || diffFile(t, r.Diff, "sub/untracked.go").Status != "A" {
		t.Fatalf("commit diff %+v", r.Diff)
	}
	r = gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "projects/demo", Mode: tunnel.DiffUncommitted})
	if len(r.Diff.Files) != 0 {
		t.Fatalf("clean tree diff %+v", r.Diff.Files)
	}

	r = gitData(t, s, tunnel.GitRequest{Action: tunnel.GitLog, Path: "projects/demo", Limit: 3})
	if len(r.Commits) != 3 || r.Commits[0].SHA != sha || r.Commits[0].ShortSHA == "" || r.Commits[0].Author == "" || r.Commits[0].Date == 0 {
		t.Fatalf("log %+v", r.Commits)
	}
	if c := r.Commits[1]; c.Subject != "add old name.txt" || c.FilesChanged != 1 || c.Additions != 3 || c.Deletions != 0 {
		t.Fatalf("log entry %+v", c)
	}

	// Branch mode: against origin's default branch (auto-detected), with
	// uncommitted work included.
	write(t, filepath.Join(repo, "wip.txt"), "wip\n")
	r = gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "projects/demo", Mode: tunnel.DiffBranch})
	if r.Diff.Base != "origin/HEAD" && r.Diff.Base != "origin/main" {
		t.Fatalf("base %q", r.Diff.Base)
	}
	if r.Diff.MergeBase == "" {
		t.Fatal("no merge base")
	}
	diffFile(t, r.Diff, "keep.txt")
	diffFile(t, r.Diff, "wip.txt")
	for _, f := range r.Diff.Files {
		if f.Path == "README.md" {
			t.Fatal("a file of the base shows up in the branch diff")
		}
	}

	// Errors.
	for _, req := range []tunnel.GitRequest{
		{Action: tunnel.GitDiff, Path: "projects/demo", Mode: "bogus"},
		{Action: tunnel.GitDiff, Path: "projects/demo", Mode: tunnel.DiffCommit, Ref: "--output=/tmp/x"},
		{Action: tunnel.GitDiff, Path: "projects/demo", Mode: tunnel.DiffCommit, Ref: "deadbeef"},
		{Action: tunnel.GitDiff, Path: "projects/demo", Mode: tunnel.DiffBranch, Base: "nope"},
		{Action: tunnel.GitDiff, Path: "../outside"},
		{Action: tunnel.GitLog, Path: "/etc"},
	} {
		if _, err := s.GitData(t.Context(), req); err == nil {
			t.Fatalf("%+v: no error", req)
		}
	}
}

// A branch diff shows only what the branch changed since it forked.
func TestGitDiffBranchOnlyBranchChanges(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	repo := filepath.Join(home, "r")
	gitT(t, home, "init", "-q", "-b", "main", repo)
	commit(t, repo, "a.txt", "a\n")
	gitT(t, repo, "switch", "-q", "-c", "feature")
	commit(t, repo, "b.txt", "b\n")
	r := gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "r", Mode: tunnel.DiffBranch})
	if r.Diff.Base != "main" || len(r.Diff.Files) != 1 || r.Diff.Files[0].Path != "b.txt" || r.Diff.Files[0].Status != "A" {
		t.Fatalf("branch diff %+v", r.Diff)
	}
	r = gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "r", Mode: tunnel.DiffBranch, Base: "feature"})
	if len(r.Diff.Files) != 0 {
		t.Fatalf("against itself %+v", r.Diff.Files)
	}
}

func TestGitDiffFreshRepo(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	repo := filepath.Join(home, "fresh")
	gitT(t, home, "init", "-q", "-b", "main", repo)
	write(t, filepath.Join(repo, "new.txt"), "x\n")
	gitT(t, repo, "add", "new.txt")
	write(t, filepath.Join(repo, "loose.txt"), "y\n")
	r := gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "fresh"})
	if diffFile(t, r.Diff, "new.txt").Status != "A" || diffFile(t, r.Diff, "loose.txt").Status != "?" {
		t.Fatalf("fresh diff %+v", r.Diff.Files)
	}
	r = gitData(t, s, tunnel.GitRequest{Action: tunnel.GitLog, Path: "fresh"})
	if len(r.Commits) != 0 {
		t.Fatalf("log of a fresh repo %+v", r.Commits)
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitCreateCommit, Path: "fresh", Message: "first"})
	if r.Commit == nil || r.Commit.FilesChanged != 1 || !r.Repo.Dirty {
		t.Fatalf("first commit (staged only) %+v %+v", r.Commit, r.Repo)
	}
	r = gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "fresh", Mode: tunnel.DiffCommit, Ref: "HEAD"})
	if len(r.Diff.Files) != 1 || r.Diff.Files[0].Path != "new.txt" {
		t.Fatalf("root commit diff %+v", r.Diff.Files)
	}
}

func TestGitDiffLargeFileTruncated(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	repo := filepath.Join(home, "big")
	gitT(t, home, "init", "-q", "-b", "main", repo)
	commit(t, repo, "big.txt", "start\n")
	write(t, filepath.Join(repo, "big.txt"), strings.Repeat("a long line of text that repeats\n", 20000))
	write(t, filepath.Join(repo, "small.txt"), "small\n")
	gitT(t, repo, "add", "small.txt")
	r := gitData(t, s, tunnel.GitRequest{Action: tunnel.GitDiff, Path: "big"})
	big := diffFile(t, r.Diff, "big.txt")
	if !big.Truncated || big.Patch != "" || big.Additions != 20000 {
		t.Fatalf("big %+v", big.Truncated)
	}
	if small := diffFile(t, r.Diff, "small.txt"); small.Truncated || small.Patch == "" {
		t.Fatalf("small %+v", small)
	}
}
