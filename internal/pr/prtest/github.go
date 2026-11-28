// Package prtest is a fake GitHub REST API for pull request tests.
package prtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// PR is a fake pull request.
type PR struct {
	Repo           string
	Number         int
	Title, Body    string
	State          string // open, closed
	Draft, Merged  bool
	Author         string
	HeadRef        string
	HeadSHA        string
	HeadRepo       string // default Repo
	BaseRef        string
	MergeableState string
	CloneURL       string // of the repository (default https://github.com/<repo>.git)
	UpdatedAt      time.Time
}

// CheckRun is a fake check run.
type CheckRun struct {
	Name, Status, Conclusion string
}

// Comment is a fake comment, review or line comment.
type Comment struct {
	ID     int64
	Kind   string // comment, review, review_comment
	Author string
	Body   string
	State  string
	Path   string
	Line   int
	At     time.Time
}

// Merge is a merge the fake received.
type Merge struct {
	Repo   string
	Number int
	Method string
	SHA    string
}

// GitHub is the fake server.
type GitHub struct {
	*httptest.Server
	t testing.TB

	mu       sync.Mutex
	Token    string // the token requests must carry (default "ghp_test")
	Login    string // the token's account (default "me")
	PRs      map[string]*PR
	Checks   map[string][]CheckRun // by head sha
	Comments map[string][]Comment  // by repo#number
	Files    map[string][]map[string]any
	Merges   []Merge
	Created  []map[string]any
	Reviews  []map[string]any
	// Fail makes every pull request read fail with this status (0: no).
	Fail int
	// Searches are the q parameters of searches.
	Searches []string
	nextID   int64
}

func key(repo string, n int) string { return repo + "#" + strconv.Itoa(n) }

// NewGitHub starts a fake GitHub API.
func NewGitHub(t testing.TB) *GitHub {
	g := &GitHub{
		t: t, Token: "ghp_test", Login: "me", PRs: map[string]*PR{}, Checks: map[string][]CheckRun{},
		Comments: map[string][]Comment{}, Files: map[string][]map[string]any{}, nextID: 1000,
	}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.Close)
	return g
}

// Add adds (or replaces) a pull request.
func (g *GitHub) Add(p PR) *PR {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p.State == "" {
		p.State = "open"
	}
	if p.Author == "" {
		p.Author = "me"
	}
	if p.BaseRef == "" {
		p.BaseRef = "main"
	}
	if p.MergeableState == "" {
		p.MergeableState = "clean"
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Unix(1_800_000_000+int64(p.Number), 0)
	}
	cp := p
	g.PRs[key(p.Repo, p.Number)] = &cp
	return &cp
}

// Update changes a pull request under the lock.
func (g *GitHub) Update(repo string, n int, fn func(*PR)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	fn(g.PRs[key(repo, n)])
}

// SetChecks sets the check runs of a head commit.
func (g *GitHub) SetChecks(sha string, runs ...CheckRun) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Checks[sha] = runs
}

// AddComment adds a comment (kind comment, review or review_comment) and returns its id.
func (g *GitHub) AddComment(repo string, n int, c Comment) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.ID == 0 {
		g.nextID++
		c.ID = g.nextID
	}
	if c.Kind == "" {
		c.Kind = "comment"
	}
	if c.At.IsZero() {
		c.At = time.Unix(1_800_000_000+c.ID, 0)
	}
	g.Comments[key(repo, n)] = append(g.Comments[key(repo, n)], c)
	return c.ID
}

// SetFiles sets the changed files of a pull request (GitHub's JSON shape).
func (g *GitHub) SetFiles(repo string, n int, files ...map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Files[key(repo, n)] = files
}

// MergesSoFar returns the merges received.
func (g *GitHub) MergesSoFar() []Merge {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Merge(nil), g.Merges...)
}

func (g *GitHub) write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (g *GitHub) clone(p *PR) string {
	if p.CloneURL != "" {
		return p.CloneURL
	}
	return "https://github.com/" + p.Repo + ".git"
}

func (g *GitHub) pullJSON(p *PR) map[string]any {
	headRepo := p.HeadRepo
	if headRepo == "" {
		headRepo = p.Repo
	}
	headClone := g.clone(p)
	if headRepo != p.Repo {
		headClone = "https://github.com/" + headRepo + ".git"
	}
	merged := any(nil)
	if p.Merged {
		merged = p.UpdatedAt.UTC().Format(time.RFC3339)
	}
	state := p.State
	if p.Merged {
		state = "closed"
	}
	return map[string]any{
		"number": p.Number, "html_url": "https://github.com/" + p.Repo + "/pull/" + strconv.Itoa(p.Number),
		"title": p.Title, "body": p.Body, "state": state, "draft": p.Draft, "merged": p.Merged, "merged_at": merged,
		"mergeable": p.MergeableState != "dirty", "mergeable_state": p.MergeableState,
		"user":     map[string]any{"login": p.Author},
		"head":     map[string]any{"ref": p.HeadRef, "sha": p.HeadSHA, "repo": map[string]any{"full_name": headRepo, "clone_url": headClone}},
		"base":     map[string]any{"ref": p.BaseRef, "sha": "base0", "repo": map[string]any{"full_name": p.Repo, "clone_url": g.clone(p)}},
		"comments": 0, "review_comments": 0, "additions": 3, "deletions": 1, "changed_files": 1,
		"created_at": p.UpdatedAt.UTC().Format(time.RFC3339), "updated_at": p.UpdatedAt.UTC().Format(time.RFC3339),
		"repository_url": g.URL + "/repos/" + p.Repo,
		"pull_request":   map[string]any{"merged_at": merged},
	}
}

func (g *GitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+g.Token {
		g.write(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	route := r.Method + " " + r.URL.Path
	switch route {
	case "GET /user":
		g.write(w, 200, map[string]string{"login": g.Login})
		return
	case "GET /search/issues":
		q := r.URL.Query().Get("q")
		g.Searches = append(g.Searches, q)
		var items []map[string]any
		for _, p := range g.PRs {
			switch {
			case strings.Contains(q, "is:merged") && !p.Merged,
				strings.Contains(q, "is:open") && (p.State != "open" || p.Merged),
				strings.Contains(q, "is:unmerged") && p.Merged,
				strings.Contains(q, "is:closed") && p.State == "open" && !p.Merged:
				continue
			}
			if i := strings.Index(q, "repo:"); i >= 0 && !strings.HasPrefix(q[i+5:], p.Repo) {
				continue
			}
			items = append(items, g.pullJSON(p))
		}
		g.write(w, 200, map[string]any{"total_count": len(items), "items": items})
		return
	}
	if len(parts) < 3 || parts[0] != "repos" {
		g.write(w, 404, map[string]string{"message": "Not Found"})
		return
	}
	repo := parts[1] + "/" + parts[2]
	rest := parts[3:]
	if len(rest) == 0 && r.Method == http.MethodGet {
		g.write(w, 200, map[string]any{"full_name": repo, "default_branch": "main", "clone_url": "https://github.com/" + repo + ".git"})
		return
	}
	if len(rest) >= 3 && rest[0] == "commits" {
		sha := rest[1]
		switch rest[2] {
		case "check-runs":
			var runs []map[string]any
			for _, c := range g.Checks[sha] {
				runs = append(runs, map[string]any{"name": c.Name, "status": c.Status, "conclusion": c.Conclusion, "html_url": "https://ci/" + c.Name})
			}
			g.write(w, 200, map[string]any{"total_count": len(runs), "check_runs": runs})
		case "status":
			g.write(w, 200, map[string]any{"state": "success", "statuses": []any{}})
		}
		return
	}
	if len(rest) >= 1 && rest[0] == "pulls" && len(rest) == 1 {
		switch r.Method {
		case http.MethodGet:
			head := r.URL.Query().Get("head")
			var out []map[string]any
			for _, p := range g.PRs {
				if p.Repo == repo && p.State == "open" && (head == "" || head == parts[1]+":"+p.HeadRef) {
					out = append(out, g.pullJSON(p))
				}
			}
			g.write(w, 200, out)
		case http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			g.Created = append(g.Created, in)
			head, _ := in["head"].(string)
			for _, p := range g.PRs {
				if p.Repo == repo && p.HeadRef == head && p.State == "open" {
					g.write(w, 422, map[string]any{"message": "Validation Failed", "errors": []map[string]string{{"message": "A pull request already exists for " + head + "."}}})
					return
				}
			}
			n := 1
			for _, p := range g.PRs {
				if p.Repo == repo && p.Number >= n {
					n = p.Number + 1
				}
			}
			title, _ := in["title"].(string)
			body, _ := in["body"].(string)
			base, _ := in["base"].(string)
			draft, _ := in["draft"].(bool)
			p := &PR{
				Repo: repo, Number: n, Title: title, Body: body, State: "open", Draft: draft, Author: g.Login,
				HeadRef: head, HeadSHA: "new" + strconv.Itoa(n), BaseRef: base, MergeableState: "clean", UpdatedAt: time.Unix(1_900_000_000, 0),
			}
			g.PRs[key(repo, n)] = p
			g.write(w, 201, g.pullJSON(p))
		}
		return
	}
	if len(rest) < 2 || (rest[0] != "pulls" && rest[0] != "issues") {
		g.write(w, 404, map[string]string{"message": "Not Found"})
		return
	}
	n, _ := strconv.Atoi(rest[1])
	p := g.PRs[key(repo, n)]
	if p == nil {
		g.write(w, 404, map[string]string{"message": "Not Found"})
		return
	}
	if g.Fail != 0 {
		g.write(w, g.Fail, map[string]string{"message": "Server Error"})
		return
	}
	sub := strings.Join(rest[2:], "/")
	switch {
	case rest[0] == "pulls" && sub == "" && r.Method == http.MethodGet:
		g.write(w, 200, g.pullJSON(p))
	case rest[0] == "pulls" && sub == "files":
		files := g.Files[key(repo, n)]
		if files == nil {
			files = []map[string]any{}
		}
		g.write(w, 200, files)
	case rest[0] == "pulls" && sub == "merge" && r.Method == http.MethodPut:
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["sha"] != "" && in["sha"] != p.HeadSHA {
			g.write(w, 409, map[string]string{"message": "Head branch was modified. Review and try the merge again."})
			return
		}
		g.Merges = append(g.Merges, Merge{Repo: repo, Number: n, Method: in["merge_method"], SHA: in["sha"]})
		p.Merged, p.State = true, "closed"
		g.write(w, 200, map[string]any{"merged": true, "sha": "merge" + strconv.Itoa(n), "message": "Pull Request successfully merged"})
	case (rest[0] == "issues" && sub == "comments") || (rest[0] == "pulls" && (sub == "reviews" || sub == "comments")):
		kind := map[string]string{"issues/comments": "comment", "pulls/reviews": "review", "pulls/comments": "review_comment"}[rest[0]+"/"+sub]
		if r.Method == http.MethodPost {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			in["kind"] = kind
			g.Reviews = append(g.Reviews, in)
			body, _ := in["body"].(string)
			event, _ := in["event"].(string)
			g.nextID++
			g.Comments[key(repo, n)] = append(g.Comments[key(repo, n)], Comment{ID: g.nextID, Kind: kind, Author: g.Login, Body: body, State: event, At: time.Now()})
			g.write(w, 201, map[string]any{"id": g.nextID})
			return
		}
		out := []map[string]any{}
		for _, c := range g.Comments[key(repo, n)] {
			if c.Kind != kind {
				continue
			}
			at := c.At.UTC().Format(time.RFC3339)
			m := map[string]any{"id": c.ID, "user": map[string]any{"login": c.Author}, "body": c.Body, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d#c%d", repo, n, c.ID)}
			if kind == "review" {
				m["state"], m["submitted_at"] = c.State, at
			} else {
				m["created_at"] = at
			}
			if kind == "review_comment" {
				m["path"], m["line"] = c.Path, c.Line
			}
			out = append(out, m)
		}
		g.write(w, 200, out)
	default:
		g.write(w, 404, map[string]string{"message": "Not Found"})
	}
}
