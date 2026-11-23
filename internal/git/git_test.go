package git_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/git"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

type fixture struct {
	svc         *git.Service
	st          *store.Store
	box         *secrets.Box
	invalidated int
	user        db.User
	agent       db.Agent
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.NewBox(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, db.CreateUserParams{ID: "usr_1", Email: "ada@example.com", Name: "Ada", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, db.CreateUserParams{ID: "usr_2", Email: "bob@example.com", Name: "Bob", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{ID: "agt_1", OwnerID: "usr_1", Name: "web", Image: "x", Harness: "fake", Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{st: st, box: box}
	f.svc = &git.Service{
		Store: st, Box: box, Audit: &audit.Log{Store: st}, Invalidate: func() { f.invalidated++ },
		Now: func() time.Time { return time.Unix(1000, 0) },
	}
	if f.user, err = st.GetUser(ctx, "usr_1"); err != nil {
		t.Fatal(err)
	}
	if f.agent, err = st.GetAgent(ctx, "agt_1"); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestConfig(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	s, err := f.svc.GetSettings(ctx, f.user)
	if err != nil {
		t.Fatal(err)
	}
	if s.AuthorName != "" || s.AuthorEmail != "" || s.DefaultAuthorName != "Ada" || s.DefaultAuthorEmail != "ada@example.com" ||
		s.Credentials == nil || len(s.Credentials) != 0 {
		t.Fatalf("defaults = %+v", s)
	}
	files, err := f.svc.BootFiles(ctx, "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(files[git.GuestGitConfigFile]); !strings.Contains(got, `name = "Ada"`) || !strings.Contains(got, `email = "ada@example.com"`) {
		t.Fatalf("default boot config = %q", got)
	}

	if err := f.svc.UpdateConfig(ctx, "usr_1", "  Ada L. ", ""); err != nil {
		t.Fatal(err)
	}
	s, _ = f.svc.GetSettings(ctx, f.user)
	if s.AuthorName != "Ada L." || s.AuthorEmail != "" {
		t.Fatalf("updated = %+v", s)
	}
	files, _ = f.svc.BootFiles(ctx, "agt_1")
	if got := string(files[git.GuestGitConfigFile]); !strings.Contains(got, `name = "Ada L."`) || !strings.Contains(got, `email = "ada@example.com"`) {
		t.Fatalf("boot config with fallback email = %q", got)
	}

	for _, bad := range [][2]string{
		{"a\nb", ""},
		{"a\rb", ""},
		{"a\x00", ""},
		{strings.Repeat("a", 300), ""},
		{"", "x\n[core]"},
		{"", "a b@example.com"},
		{"", "<a@b>"},
	} {
		if err := f.svc.UpdateConfig(ctx, "usr_1", bad[0], bad[1]); !errors.Is(err, git.ErrInvalid) {
			t.Errorf("UpdateConfig(%q, %q) = %v", bad[0], bad[1], err)
		}
	}
	var n int
	if err := f.st.DB.QueryRowContext(ctx, "SELECT count(*) FROM audit_log WHERE action = 'git.config.update' AND user_id = 'usr_1'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit entries = %d, %v", n, err)
	}
}

func TestBootFilesEmptyAndEscaped(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	if err := f.st.CreateAgent(ctx, db.CreateAgentParams{ID: "agt_2", OwnerID: "usr_2", Name: "b", Image: "x", Harness: "fake", Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB.ExecContext(ctx, "UPDATE users SET name = '', email = '' WHERE id = 'usr_2'"); err != nil {
		t.Fatal(err)
	}
	files, err := f.svc.BootFiles(ctx, "agt_2")
	if err != nil {
		t.Fatal(err)
	}
	if data, ok := files[git.GuestGitConfigFile]; !ok || len(data) != 0 {
		t.Fatalf("empty identity = %q (present %v)", data, ok)
	}

	name := `Robert "Bob" \ Tables; # [core]`
	if err := f.svc.UpdateConfig(ctx, "usr_2", name, "bob@example.com"); err != nil {
		t.Fatal(err)
	}
	files, err = f.svc.BootFiles(ctx, "agt_2")
	if err != nil {
		t.Fatal(err)
	}
	cfg := files[git.GuestGitConfigFile]
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	path := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(path, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(key string) string {
		out, err := exec.Command("git", "config", "--file", path, key).CombinedOutput()
		if err != nil {
			t.Fatalf("git config %s: %v\n%s\n%s", key, err, out, cfg)
		}
		return strings.TrimSuffix(string(out), "\n")
	}
	if got := read("user.name"); got != name {
		t.Fatalf("name = %q, want %q", got, name)
	}
	if got := read("user.email"); got != "bob@example.com" {
		t.Fatalf("email = %q", got)
	}
	if out, _ := exec.Command("git", "config", "--file", path, "--list").Output(); strings.Contains(string(out), "core.") {
		t.Fatalf("injected section: %s", out)
	}
	if _, err := git.RenderConfig("a\nb", ""); err == nil {
		t.Fatal("newline rendered")
	}
}

func TestNormalizeHost(t *testing.T) {
	for _, c := range [][2]string{
		{"github.com", "github.com"},
		{"GitLab.com", "gitlab.com"},
		{"https://git.example.com/", "git.example.com"},
		{"\tgit.example.com. ", "git.example.com"},
		{"a-b.example.co.uk", "a-b.example.co.uk"},
	} {
		in, want := c[0], c[1]
		if got, err := git.NormalizeHost(in); err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{
		"", "localhost", "1.2.3.4", "*.github.com", "github.com:443", "git hub.com",
		"github.com/owner", "-a.com", "a-.com", "a..com", "ex_ample.com", "[::1]", "ssh://github.com",
	} {
		if got, err := git.NormalizeHost(in); !errors.Is(err, git.ErrInvalid) {
			t.Errorf("NormalizeHost(%q) = %q, %v", in, got, err)
		}
	}
}

func TestCredentials(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	gh, err := f.svc.UpsertCredential(ctx, "usr_1", "https://GitHub.com", "", " ghp_one ", "work")
	if err != nil {
		t.Fatal(err)
	}
	if gh.Host != "github.com" || gh.Username != "x-access-token" || gh.Label != "work" || !strings.HasPrefix(gh.ID, "gcr_") {
		t.Fatalf("github = %+v", gh)
	}
	gl, err := f.svc.UpsertCredential(ctx, "usr_1", "gitlab.com", "", "glpat", "")
	if err != nil || gl.Username != "oauth2" {
		t.Fatalf("gitlab = %+v %v", gl, err)
	}
	own, err := f.svc.UpsertCredential(ctx, "usr_1", "git.example.com", "me", "tok", "")
	if err != nil || own.Username != "me" {
		t.Fatalf("custom = %+v %v", own, err)
	}
	other, err := f.svc.UpsertCredential(ctx, "usr_1", "forge.example.org", "", "x", "")
	if err != nil || other.Username != "git" {
		t.Fatalf("default user = %+v %v", other, err)
	}
	if f.invalidated != 4 {
		t.Fatalf("invalidated %d times", f.invalidated)
	}

	// The token is sealed with the credential's id.
	row, err := f.st.GetGitCredential(ctx, gh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.Ciphertext, []byte("ghp_one")) {
		t.Fatal("token stored in the clear")
	}
	if tok, err := f.box.Open(row.Ciphertext, []byte(gh.ID)); err != nil || string(tok) != "ghp_one" {
		t.Fatalf("open = %q %v", tok, err)
	}

	// Updating the host's credential keeps its id.
	gh2, err := f.svc.UpsertCredential(ctx, "usr_1", "github.com", "", "ghp_two", "new")
	if err != nil {
		t.Fatal(err)
	}
	if gh2.ID != gh.ID || gh2.Label != "new" || gh2.CreatedAt != gh.CreatedAt {
		t.Fatalf("update = %+v, was %+v", gh2, gh)
	}
	row, _ = f.st.GetGitCredential(ctx, gh.ID)
	if tok, err := f.box.Open(row.Ciphertext, []byte(gh.ID)); err != nil || string(tok) != "ghp_two" {
		t.Fatalf("open updated = %q %v", tok, err)
	}

	s, err := f.svc.GetSettings(ctx, f.user)
	if err != nil || len(s.Credentials) != 4 {
		t.Fatalf("settings = %+v %v", s, err)
	}

	for _, bad := range []struct{ host, user, token, label string }{
		{"1.2.3.4", "", "t", ""},
		{"github.com", "", "", ""},
		{"github.com", "", "a\nb", ""},
		{"github.com", "", strings.Repeat("t", 20000), ""},
		{"github.com", "a:b", "t", ""},
		{"github.com", "a\nb", "t", ""},
		{"github.com", "", "t", strings.Repeat("l", 200)},
	} {
		if _, err := f.svc.UpsertCredential(ctx, "usr_1", bad.host, bad.user, bad.token, bad.label); !errors.Is(err, git.ErrInvalid) {
			t.Errorf("UpsertCredential(%+v) = %v", bad, err)
		}
	}

	// Another user cannot delete it.
	if err := f.svc.DeleteCredential(ctx, "usr_2", gh.ID); !errors.Is(err, git.ErrNotFound) {
		t.Fatalf("foreign delete = %v", err)
	}
	if err := f.svc.DeleteCredential(ctx, "usr_1", gh.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteCredential(ctx, "usr_1", gh.ID); !errors.Is(err, git.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if f.invalidated != 6 {
		t.Fatalf("invalidated %d times", f.invalidated)
	}
}

func basic(user, token string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
}

func TestEgress(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	for _, c := range [][3]string{{"github.com", "", "ghp"}, {"gitlab.com", "", "glpat"}, {"git.example.com", "me", "tok"}} {
		if _, err := f.svc.UpsertCredential(ctx, "usr_1", c[0], c[1], c[2], ""); err != nil {
			t.Fatal(err)
		}
	}
	// Another user's credentials are not the agent's.
	if _, err := f.svc.UpsertCredential(ctx, "usr_2", "bitbucket.org", "", "bb", ""); err != nil {
		t.Fatal(err)
	}
	injs, err := f.svc.Egress(ctx, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	byHost := map[string]secrets.Injection{}
	for _, inj := range injs {
		if !strings.HasPrefix(inj.SecretID, "git:gcr_") || inj.Rule.Header != "Authorization" {
			t.Fatalf("injection = %+v", inj)
		}
		byHost[inj.Rule.Hosts[0]] = inj
	}
	if len(injs) != 3 {
		t.Fatalf("%d injections", len(injs))
	}
	check := func(host, user, token string) {
		t.Helper()
		inj := byHost[host]
		req, _ := http.NewRequest(http.MethodGet, "https://"+host+"/owner/repo.git/info/refs", nil)
		if ok, err := inj.Apply(req); !ok || err != nil {
			t.Fatalf("%s: apply = %v %v", host, ok, err)
		}
		if got := req.Header.Get("Authorization"); got != "Basic "+basic(user, token) {
			t.Fatalf("%s: Authorization = %q", host, got)
		}
	}
	check("github.com", "x-access-token", "ghp")
	check("gitlab.com", "oauth2", "glpat")
	check("git.example.com", "me", "tok")
	if hosts := byHost["github.com"].Rule.Hosts; strings.Join(hosts, ",") != "github.com,api.github.com,uploads.github.com" {
		t.Fatalf("github hosts = %v", hosts)
	}
	if hosts := byHost["gitlab.com"].Rule.Hosts; len(hosts) != 1 {
		t.Fatalf("gitlab hosts = %v", hosts)
	}

	// A credential that cannot be opened is left out and reported.
	glRow, err := f.st.GetGitCredentialByHost(ctx, db.GetGitCredentialByHostParams{UserID: "usr_1", Host: "gitlab.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpdateGitCredential(ctx, db.UpdateGitCredentialParams{
		Username: glRow.Username, Ciphertext: []byte("garbage-garbage-garbage-garbage"), Label: glRow.Label, Source: glRow.Source,
		RefreshCiphertext: []byte{}, UpdatedAt: glRow.UpdatedAt, ID: glRow.ID,
	}); err != nil {
		t.Fatal(err)
	}
	injs, err = f.svc.Egress(ctx, f.agent)
	if err == nil || len(injs) != 2 {
		t.Fatalf("broken credential: %d injections, %v", len(injs), err)
	}
}

// Secrets attached to the agent win over account git credentials for the
// same host.
func TestDirectoryPrecedence(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	if _, err := f.svc.UpsertCredential(ctx, "usr_1", "github.com", "", "ghp_account", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpsertCredential(ctx, "usr_1", "gitlab.com", "", "glpat", ""); err != nil {
		t.Fatal(err)
	}
	ct, err := f.box.Seal([]byte("ghp_agent"), []byte("sec_1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateSecret(ctx, db.CreateSecretParams{
		ID: "sec_1", OwnerID: "usr_1", Name: "GH", Ciphertext: ct,
		Rule: `{"hosts":["api.github.com"],"header":"Authorization","template":"Bearer {{.Value}}"}`, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.AttachSecret(ctx, db.AttachSecretParams{AgentID: "agt_1", SecretID: "sec_1", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	dir := &egress.Directory{Store: f.st, Box: f.box, Git: f.svc}
	ag, err := dir.Agent(context.Background(), "agt_1")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, inj := range ag.Injections {
		ids = append(ids, inj.SecretID)
		if strings.HasPrefix(inj.SecretID, "git:") && inj.Rule.MatchHost("github.com") {
			t.Fatalf("account github credential injected next to the agent's secret: %+v", ag.Injections)
		}
	}
	if len(ids) != 2 || ids[0] != "sec_1" || !strings.HasPrefix(ids[1], "git:") {
		t.Fatalf("injections = %v", ids)
	}
}

// The GitHub CLI's token marker: with no credential for api.github.com the
// proxy answers gh itself (how to connect GitHub); with one it injects it.
func TestDirectoryGitHubCLIMarker(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	dir := &egress.Directory{Store: f.st, Box: f.box, Git: f.svc}
	answer := func() (*http.Response, bool) {
		t.Helper()
		ag, err := dir.Agent(ctx, "agt_1")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range ag.Responders {
			if r.Host != "api.github.com" {
				continue
			}
			req := httptest.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
			req.Header.Set("Authorization", "token "+egress.GitHubTokenMarker)
			rec := httptest.NewRecorder()
			if r.Handle(rec, req) {
				return rec.Result(), true
			}
			// Requests with a real token of their own go through.
			other := httptest.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
			other.Header.Set("Authorization", "token ghp_mine")
			if r.Handle(httptest.NewRecorder(), other) {
				t.Fatal("answered a request with its own token")
			}
		}
		return nil, false
	}
	res, ok := answer()
	if !ok || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential: answered %v (%v)", ok, res)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "Settings → Git") {
		t.Fatalf("body = %s", body)
	}
	if env := egress.GitHubCLIEnv(); env["GH_TOKEN"] != egress.GitHubTokenMarker {
		t.Fatalf("gh env = %v", env)
	}

	if _, err := f.svc.UpsertCredential(ctx, "usr_1", "github.com", "", "ghp_account", ""); err != nil {
		t.Fatal(err)
	}
	dir.Invalidate()
	if _, ok := answer(); ok {
		t.Fatal("answered although a github.com credential is injected")
	}
}
