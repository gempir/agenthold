package sandboxd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/tunnel"
)

func TestGitInit(t *testing.T) {
	requireGit(t)
	s, home := setup(t)

	// A new folder becomes a repository with one commit.
	r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitInit, Path: "projects/p/notes"})
	if r.Repo == nil || r.Repo.Path != "projects/p/notes" || r.Repo.Branch != "main" || r.Repo.Dirty {
		t.Fatalf("init = %+v", r.Repo)
	}
	if got := strings.TrimSpace(gitT(t, filepath.Join(home, "projects/p/notes"), "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("%s commits", got)
	}

	// An existing folder's files are committed.
	dir := filepath.Join(home, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitInit, Path: "docs", Branch: "trunk"})
	if r.Repo.Branch != "trunk" || r.Repo.Dirty {
		t.Fatalf("init docs = %+v", r.Repo)
	}
	if got := strings.TrimSpace(gitT(t, dir, "ls-files")); got != "a.md" {
		t.Fatalf("tracked %q", got)
	}
	// Again: nothing happens.
	head := gitT(t, dir, "rev-parse", "HEAD")
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitInit, Path: "docs"})
	if gitT(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("init again committed")
	}

	for _, bad := range []tunnel.GitRequest{
		{Action: tunnel.GitInit, Path: ""},
		{Action: tunnel.GitInit, Path: "../out"},
		{Action: tunnel.GitInit, Path: "x", Branch: "-bad"},
	} {
		if _, err := s.Git(t.Context(), bad); err == nil {
			t.Fatalf("%+v succeeded", bad)
		}
	}
}

func TestGitChangesMergeAndBranchDelete(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	origin := newOrigin(t)
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitClone, URL: origin})
	repo := filepath.Join(home, "projects/origin")
	r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitWorktreeAdd, Path: "projects/origin", Branch: "ah/one", Create: true, FromRef: "main", WorktreePath: "ws/one"})
	wt := filepath.Join(home, "ws/one")
	if !r.Repo.IsWorktree {
		t.Fatalf("worktree %+v", r.Repo)
	}

	// Nothing changed yet.
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitChanges, Path: "ws/one", FromRef: "main"})
	if r.Changes == nil || r.Changes.Commits != 0 || len(r.Changes.Files) != 0 || r.Changes.Dirty {
		t.Fatalf("changes = %+v", r.Changes)
	}
	// A commit, an uncommitted edit and an untracked file.
	commit(t, wt, "feature.txt", "f")
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitChanges, Path: "ws/one", FromRef: "main"})
	files := map[string]string{}
	for _, f := range r.Changes.Files {
		files[f.Path] = f.Status
	}
	if r.Changes.Commits != 1 || !r.Changes.Dirty || files["feature.txt"] != "A" || files["README.md"] != "M" || files["new.txt"] != "?" {
		t.Fatalf("changes = %+v", r.Changes)
	}

	// Merge refuses a dirty main checkout, merges a clean one.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitMerge, Path: "projects/origin", Branch: "ah/one"}); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty merge: %v", err)
	}
	gitT(t, repo, "checkout", "--", "README.md")
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitMerge, Path: "projects/origin", Branch: "nope"}); err == nil {
		t.Fatal("merged a missing branch")
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitMerge, Path: "projects/origin", Branch: "ah/one", Message: "Merge one"})
	if r.Repo.Branch != "main" {
		t.Fatalf("merge = %+v", r.Repo)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitT(t, repo, "log", "-1", "--format=%s")); got != "Merge one" {
		t.Fatalf("merge commit %q", got)
	}

	// A conflict aborts the merge and names the file.
	gitT(t, wt, "checkout", "--", "README.md")
	commit(t, wt, "README.md", "theirs\n")
	commit(t, repo, "README.md", "ours\n")
	_, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitMerge, Path: "projects/origin", Branch: "ah/one"})
	if err == nil || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("conflict: %v", err)
	}
	if out := gitT(t, repo, "status", "--porcelain", "--untracked-files=no"); strings.TrimSpace(out) != "" {
		t.Fatalf("not aborted: %s", out)
	}

	// Branch delete: after the worktree is gone, unmerged needs Force.
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitWorktreeRemove, Path: "ws/one", Force: true})
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitBranchDelete, Path: "projects/origin", Branch: "ah/one"}); err == nil {
		t.Fatal("deleted an unmerged branch without force")
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitBranchDelete, Path: "projects/origin", Branch: "ah/one", Force: true})
	if slices.Contains(r.Branches, "ah/one") {
		t.Fatalf("branches %v", r.Branches)
	}
}
