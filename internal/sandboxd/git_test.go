package sandboxd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/tunnel"
)

// gitT runs git for test fixtures, isolated from the developer's config.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// commit writes name and commits it in repo.
func commit(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", name)
	gitT(t, repo, "commit", "-q", "-m", "add "+name)
}

// newOrigin makes a bare repository named origin.git (branch main, one
// commit) outside the home and returns its path.
func newOrigin(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "work")
	bare := filepath.Join(base, "origin.git")
	gitT(t, base, "init", "-q", "-b", "main", work)
	commit(t, work, "README.md", "hello\n")
	gitT(t, base, "clone", "-q", "--bare", work, bare)
	return bare
}

func gitReq(t *testing.T, s *tunnel.Session, req tunnel.GitRequest) tunnel.GitReply {
	t.Helper()
	r, err := s.Git(t.Context(), req)
	if err != nil {
		t.Fatalf("%s: %v", req.Action, err)
	}
	return r
}

func TestGitStatusNoRepo(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitStatus})
	if r.Repo != nil || r.Home != home {
		t.Fatalf("status = %+v", r)
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitList})
	if len(r.Repos) != 0 {
		t.Fatalf("list = %+v", r.Repos)
	}
	hello, err := s.HelloInfo(t.Context())
	if err != nil || hello.Home != home {
		t.Fatalf("hello = %+v, %v", hello, err)
	}
}

// A home inside some other repository (a temp dir in a checkout) is not a
// repository of the sandbox.
func TestGitIgnoresParentRepo(t *testing.T) {
	requireGit(t)
	// The test's temp dirs share a parent: make it a repository, so the
	// home setup creates next is inside it.
	parent := filepath.Dir(t.TempDir())
	gitT(t, parent, "init", "-q", "-b", "main")
	commit(t, parent, "x.txt", "x")
	s, home := setup(t)
	if !strings.HasPrefix(home, parent+"/") {
		t.Fatalf("home %s is not inside %s", home, parent)
	}
	if err := os.MkdirAll(filepath.Join(home, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if top := gitT(t, home, "rev-parse", "--show-toplevel"); strings.TrimSpace(top) == "" {
		t.Fatal("plain git does not see the parent repository")
	}
	for _, p := range []string{"", "plain"} {
		r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitStatus, Path: p})
		if r.Repo != nil {
			t.Fatalf("status %q found the parent repo: %+v", p, r.Repo)
		}
	}
	if r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitList}); len(r.Repos) != 0 {
		t.Fatalf("list = %+v", r.Repos)
	}
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitPull, Path: "plain"}); err == nil {
		t.Fatal("pull outside a repo succeeded")
	}
}

func TestGitFlow(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	origin := newOrigin(t)

	// clone into the default path
	r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitClone, URL: origin})
	if r.Repo == nil || r.Repo.Path != "projects/origin" || r.Repo.Project != "origin" || r.Repo.Branch != "main" ||
		r.Repo.Dirty || r.Repo.IsWorktree || r.Repo.RemoteURL != origin {
		t.Fatalf("clone = %+v", r.Repo)
	}
	if len(r.Branches) != 1 || r.Branches[0] != "main" {
		t.Fatalf("branches = %v", r.Branches)
	}
	repo := filepath.Join(home, "projects/origin")
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitClone, URL: origin}); err == nil {
		t.Fatal("clone into an existing directory succeeded")
	}

	// status from a subdirectory, dirty
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sub/new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitStatus, Path: "projects/origin/sub"})
	if r.Repo == nil || r.Repo.Path != "projects/origin" || !r.Repo.Dirty {
		t.Fatalf("dirty status = %+v", r.Repo)
	}
	if err := os.RemoveAll(filepath.Join(repo, "sub")); err != nil {
		t.Fatal(err)
	}

	// checkout: new branch, then back to an existing one
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitCheckout, Path: "projects/origin", Branch: "feature/x", Create: true, FromRef: "main"})
	if r.Repo.Branch != "feature/x" || len(r.Branches) != 2 {
		t.Fatalf("checkout -b = %+v %v", r.Repo, r.Branches)
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitCheckout, Path: "projects/origin", Branch: "main"})
	if r.Repo.Branch != "main" {
		t.Fatalf("checkout = %+v", r.Repo)
	}
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitCheckout, Path: "projects/origin", Branch: "nope"}); err == nil {
		t.Fatal("checkout of a missing branch succeeded")
	}

	// worktree for a new branch, at the default path
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitWorktreeAdd, Path: "projects/origin", Branch: "wt/one", Create: true})
	if r.Repo == nil || r.Repo.Path != ".worktrees/origin/wt-one" || !r.Repo.IsWorktree ||
		r.Repo.MainPath != "projects/origin" || r.Repo.Branch != "wt/one" || r.Repo.Project != "origin" {
		t.Fatalf("worktree = %+v", r.Repo)
	}
	// worktree of the existing feature branch at an explicit path
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitWorktreeAdd, Path: "projects/origin", Branch: "feature/x", WorktreePath: "projects/origin-feature"})
	if r.Repo.Path != "projects/origin-feature" || r.Repo.Branch != "feature/x" || !r.Repo.IsWorktree {
		t.Fatalf("worktree existing = %+v", r.Repo)
	}

	// list: main repos first, then worktrees; .cache is skipped
	cached := filepath.Join(home, ".cache/hidden")
	gitT(t, home, "init", "-q", "-b", "main", cached)
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitList})
	var paths []string
	for _, rp := range r.Repos {
		paths = append(paths, rp.Path)
	}
	want := []string{"projects/origin", ".worktrees/origin/wt-one", "projects/origin-feature"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("list = %v, want %v", paths, want)
	}

	// pull: a new commit on the origin
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, filepath.Dir(other), "clone", "-q", origin, other)
	commit(t, other, "second.txt", "2")
	gitT(t, other, "push", "-q", "origin", "main")
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitPull, Path: "projects/origin", FetchOnly: true})
	if r.Repo.Behind != 1 || r.Repo.Ahead != 0 {
		t.Fatalf("after fetch = %+v", r.Repo)
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitPull, Path: "projects/origin"})
	if r.Repo.Behind != 0 {
		t.Fatalf("after pull = %+v", r.Repo)
	}
	if _, err := os.Stat(filepath.Join(repo, "second.txt")); err != nil {
		t.Fatal(err)
	}
	// a local commit is ahead
	commit(t, repo, "local.txt", "l")
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitStatus, Path: "projects/origin"})
	if r.Repo.Ahead != 1 || r.Repo.Behind != 0 {
		t.Fatalf("ahead = %+v", r.Repo)
	}

	// detached HEAD
	gitT(t, repo, "checkout", "-q", "--detach")
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitStatus, Path: "projects/origin"})
	if !r.Repo.Detached || r.Repo.Branch == "" || r.Repo.Branch == "main" {
		t.Fatalf("detached = %+v", r.Repo)
	}
}

func TestGitCloneBranchAndPath(t *testing.T) {
	requireGit(t)
	s, _ := setup(t)
	origin := newOrigin(t)
	r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitClone, URL: "file://" + origin, Path: "code/mine", Branch: "main"})
	if r.Repo.Path != "code/mine" || r.Repo.Branch != "main" || r.Repo.Project != "origin" {
		t.Fatalf("clone = %+v", r.Repo)
	}
}

func TestGitRemoteURLs(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	repo := filepath.Join(home, "projects/r")
	gitT(t, home, "init", "-q", "-b", "main", repo)
	r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitStatus, Path: "projects/r"})
	if r.Repo.Project != "r" || r.Repo.RemoteURL != "" || r.Repo.Branch != "main" {
		t.Fatalf("no remote = %+v", r.Repo)
	}
	gitT(t, repo, "remote", "add", "origin", "https://example.com/x.git")
	for _, c := range []struct{ url, sanitized, project string }{
		{"https://user:ghp_secret@github.com/owner/repo.git", "https://github.com/owner/repo.git", "owner/repo"},
		{"https://x-access-token@github.com/owner/repo", "https://github.com/owner/repo", "owner/repo"},
		{"git@github.com:owner/repo.git", "git@github.com:owner/repo.git", "owner/repo"},
		{"ssh://git:pw@gitlab.com/group/sub/repo.git", "ssh://git@gitlab.com/group/sub/repo.git", "sub/repo"},
		{"https://git.example.com/solo.git", "https://git.example.com/solo.git", "solo"},
	} {
		gitT(t, repo, "remote", "set-url", "origin", c.url)
		r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitStatus, Path: "projects/r"})
		if r.Repo.RemoteURL != c.sanitized || r.Repo.Project != c.project {
			t.Errorf("%s: remote %q project %q", c.url, r.Repo.RemoteURL, r.Repo.Project)
		}
		if strings.Contains(r.Repo.RemoteURL, "secret") || strings.Contains(r.Repo.RemoteURL, "pw") {
			t.Errorf("credentials leaked: %q", r.Repo.RemoteURL)
		}
	}
	if got := sandboxd.SanitizeRemoteURL("user:pass@host:owner/repo"); got != "user@host:owner/repo" {
		t.Errorf("scp sanitize = %q", got)
	}
}

func TestGitRejectsBadInput(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	repo := filepath.Join(home, "projects/r")
	gitT(t, home, "init", "-q", "-b", "main", repo)
	commit(t, repo, "a.txt", "a")
	outside := t.TempDir()
	gitT(t, outside, "init", "-q", "-b", "main")
	if err := os.Symlink(outside, filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "pwned")
	bad := []tunnel.GitRequest{
		// paths
		{Action: tunnel.GitStatus, Path: "../.."},
		{Action: tunnel.GitStatus, Path: outside},
		{Action: tunnel.GitStatus, Path: "/etc"},
		{Action: tunnel.GitStatus, Path: "escape"},
		{Action: tunnel.GitStatus, Path: "-C/etc"},
		{Action: tunnel.GitStatus, Path: "projects/r\x00"},
		{Action: tunnel.GitPull, Path: "escape"},
		{Action: tunnel.GitClone, URL: "/nonexistent", Path: "../evil"},
		{Action: tunnel.GitClone, URL: "/nonexistent", Path: "escape/sub"},
		{Action: tunnel.GitWorktreeAdd, Path: "projects/r", Branch: "w", Create: true, WorktreePath: "../wt"},
		{Action: tunnel.GitWorktreeAdd, Path: "projects/r", Branch: "w", Create: true, WorktreePath: "escape/wt"},
		// options and odd refs
		{Action: tunnel.GitClone, URL: "--upload-pack=touch " + marker},
		{Action: tunnel.GitClone, URL: "ext::sh -c touch% " + marker},
		{Action: tunnel.GitClone, URL: "fd::17"},
		{Action: tunnel.GitClone, URL: ""},
		{Action: tunnel.GitClone, URL: "https://example.com/r.git", Branch: "--foo"},
		{Action: tunnel.GitCheckout, Path: "projects/r", Branch: "--orphan=x"},
		{Action: tunnel.GitCheckout, Path: "projects/r", Branch: "-"},
		{Action: tunnel.GitCheckout, Path: "projects/r", Branch: "a..b"},
		{Action: tunnel.GitCheckout, Path: "projects/r", Branch: "a b"},
		{Action: tunnel.GitCheckout, Path: "projects/r", Branch: "HEAD~1"},
		{Action: tunnel.GitCheckout, Path: "projects/r", Branch: "x", Create: true, FromRef: "--detach"},
		{Action: tunnel.GitCheckout, Path: "projects/r", Branch: ""},
		{Action: tunnel.GitWorktreeAdd, Path: "projects/r", Branch: "-b"},
		{Action: "rm -rf"},
	}
	for _, req := range bad {
		if _, err := s.Git(t.Context(), req); err == nil {
			t.Errorf("%+v succeeded", req)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an injected command ran")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(home), "evil")); err == nil {
		t.Fatal("cloned outside the home")
	}
}

func TestGitConfig(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitConfig, AuthorName: `Ada "The" Lovelace\`, AuthorEmail: "ada@example.com"})
	path := filepath.Join(home, ".config/agenthold/gitconfig")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	name := strings.TrimSpace(gitT(t, home, "config", "--file", path, "user.name"))
	email := strings.TrimSpace(gitT(t, home, "config", "--file", path, "user.email"))
	if name != `Ada "The" Lovelace\` || email != "ada@example.com" {
		t.Fatalf("config %q: name %q email %q", data, name, email)
	}
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitConfig, AuthorName: "x\n[core]\n\tsshCommand = evil"}); err == nil {
		t.Fatal("newline in name accepted")
	}
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitConfig})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("config not removed: %v", err)
	}
}

func TestGitPushAndWorktreeRemove(t *testing.T) {
	requireGit(t)
	s, home := setup(t)
	origin := newOrigin(t)
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitClone, URL: origin})
	repo := filepath.Join(home, "projects/origin")

	// a new branch has no upstream: the first push sets it on origin
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitCheckout, Path: "projects/origin", Branch: "feature/push", Create: true})
	commit(t, repo, "pushed.txt", "p")
	r := gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitPush, Path: "projects/origin"})
	if r.Repo == nil || r.Repo.Branch != "feature/push" || r.Repo.Ahead != 0 {
		t.Fatalf("push = %+v", r.Repo)
	}
	if got := strings.TrimSpace(gitT(t, origin, "rev-parse", "feature/push")); got != strings.TrimSpace(gitT(t, repo, "rev-parse", "HEAD")) {
		t.Fatalf("origin has %s for the pushed branch", got)
	}
	// later pushes use the upstream
	commit(t, repo, "again.txt", "a")
	if r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitPush, Path: "projects/origin"}); r.Repo.Ahead != 0 {
		t.Fatalf("second push = %+v", r.Repo)
	}
	// a rewritten branch needs Force (--force-with-lease)
	gitT(t, repo, "commit", "-q", "--amend", "-m", "rewritten")
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitPush, Path: "projects/origin"}); err == nil {
		t.Fatal("non-fast-forward push succeeded without force")
	}
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitPush, Path: "projects/origin", Force: true})
	// detached HEAD is refused
	gitT(t, repo, "checkout", "-q", "--detach")
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitPush, Path: "projects/origin"}); err == nil || !strings.Contains(err.Error(), "detached") {
		t.Fatalf("detached push: %v", err)
	}
	gitT(t, repo, "checkout", "-q", "main")

	// worktrees: removing the main repository is refused; a dirty worktree needs Force
	gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitWorktreeAdd, Path: "projects/origin", Branch: "wt/rm", Create: true})
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitWorktreeRemove, Path: "projects/origin"}); err == nil {
		t.Fatal("removing the main repository as a worktree succeeded")
	}
	wt := filepath.Join(home, ".worktrees/origin/wt-rm")
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Git(t.Context(), tunnel.GitRequest{Action: tunnel.GitWorktreeRemove, Path: ".worktrees/origin/wt-rm"}); err == nil {
		t.Fatal("removing a dirty worktree succeeded without force")
	}
	r = gitReq(t, s, tunnel.GitRequest{Action: tunnel.GitWorktreeRemove, Path: ".worktrees/origin/wt-rm", Force: true})
	if r.Repo == nil || r.Repo.Path != "projects/origin" || r.Repo.IsWorktree {
		t.Fatalf("worktree remove = %+v", r.Repo)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	// the branch stays
	found := false
	for _, b := range r.Branches {
		found = found || b == "wt/rm"
	}
	if !found {
		t.Fatalf("branches after removing the worktree = %v", r.Branches)
	}
}
