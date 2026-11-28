package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GitHub speaks to the REST API of github.com or a GitHub Enterprise Server.
type GitHub struct {
	// Host is the git host pull requests are on (github.com, ghe.example.com).
	Host string
	// API is the REST API's root (https://api.github.com,
	// https://ghe.example.com/api/v3).
	API string
	// HTTP calls the API from the server (never through the egress proxy).
	HTTP *http.Client
}

var _ Provider = (*GitHub)(nil)

// GitHubAPI is the REST API root of a GitHub host.
func GitHubAPI(host string) string {
	if host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// maxPages bounds paginated reads (100 items a page).
const maxPages = 5

// maxBody bounds one API response.
const maxBody = 16 << 20

// APIError is a failed call to a host API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("github: %s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("github: HTTP %d", e.Status)
}

// Is makes a 404 ErrNotFound.
func (e *APIError) Is(target error) bool {
	return e.Status == http.StatusNotFound && target == ErrNotFound
}

func (g *GitHub) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// do calls the API; out (when not nil) receives the decoded JSON answer. It
// returns the response headers for pagination.
func (g *GitHub) do(ctx context.Context, token, method, path string, in, out any) (http.Header, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	target := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		target = strings.TrimSuffix(g.API, "/") + path
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Message
		for _, x := range e.Errors {
			if x.Message != "" {
				msg += ": " + x.Message
			}
		}
		return res.Header, &APIError{Status: res.StatusCode, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.Header, fmt.Errorf("github: %s %s: %w", method, path, err)
		}
	}
	return res.Header, nil
}

// nextLink is the rel="next" URL of a Link header.
func nextLink(h http.Header) string {
	for _, part := range strings.Split(h.Get("Link"), ",") {
		seg := strings.Split(part, ";")
		if len(seg) < 2 || strings.TrimSpace(seg[1]) != `rel="next"` {
			continue
		}
		return strings.Trim(strings.TrimSpace(seg[0]), "<>")
	}
	return ""
}

// pages reads up to maxPages pages of a paginated list.
func pages[T any](ctx context.Context, g *GitHub, token, path string) ([]T, error) {
	var all []T
	next := path
	for i := 0; next != "" && i < maxPages; i++ {
		var page []T
		h, err := g.do(ctx, token, http.MethodGet, next, nil, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		next = nextLink(h)
		// Only follow links to the same API.
		if next != "" && !strings.HasPrefix(next, strings.TrimSuffix(g.API, "/")+"/") {
			next = ""
		}
	}
	return all, nil
}

func repoPath(ref Ref) string {
	return "/repos/" + url.PathEscape(ref.Owner()) + "/" + url.PathEscape(ref.Name())
}

func unix(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

type ghUser struct {
	Login string `json:"login"`
}

type ghRepo struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
}

type ghBranch struct {
	Ref  string  `json:"ref"`
	SHA  string  `json:"sha"`
	Repo *ghRepo `json:"repo"`
}

type ghPull struct {
	Number         int      `json:"number"`
	HTMLURL        string   `json:"html_url"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	State          string   `json:"state"`
	Draft          bool     `json:"draft"`
	Merged         bool     `json:"merged"`
	MergedAt       string   `json:"merged_at"`
	Mergeable      *bool    `json:"mergeable"`
	MergeableState string   `json:"mergeable_state"`
	User           ghUser   `json:"user"`
	Head           ghBranch `json:"head"`
	Base           ghBranch `json:"base"`
	Comments       int      `json:"comments"`
	ReviewComments int      `json:"review_comments"`
	Additions      int      `json:"additions"`
	Deletions      int      `json:"deletions"`
	ChangedFiles   int      `json:"changed_files"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

func state(open bool, draft, merged bool) string {
	switch {
	case merged:
		return StateMerged
	case !open:
		return StateClosed
	case draft:
		return StateDraft
	}
	return StateOpen
}

func (g *GitHub) pullRequest(repo string, p ghPull) PullRequest {
	return PullRequest{
		Host: g.Host, Repo: repo, Number: p.Number, URL: p.HTMLURL, Title: p.Title,
		State:  state(p.State == "open", p.Draft, p.Merged || p.MergedAt != ""),
		Draft:  p.Draft,
		Author: p.User.Login, HeadRef: p.Head.Ref, HeadSHA: p.Head.SHA, BaseRef: p.Base.Ref,
		Comments: p.Comments + p.ReviewComments, CreatedAt: unix(p.CreatedAt), UpdatedAt: unix(p.UpdatedAt),
	}
}

// Viewer implements Provider.
func (g *GitHub) Viewer(ctx context.Context, token string) (string, error) {
	var u ghUser
	if _, err := g.do(ctx, token, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("github: GET /user: no login")
	}
	return u.Login, nil
}

// List implements Provider. It searches, so that one call covers every
// repository the account can see.
func (g *GitHub) List(ctx context.Context, token string, opts ListOptions) ([]PullRequest, error) {
	q := []string{"is:pr"}
	switch opts.State {
	case "", StateOpen:
		q = append(q, "is:open")
	case StateClosed:
		q = append(q, "is:closed", "is:unmerged")
	case StateMerged:
		q = append(q, "is:merged")
	case StateAll:
	default:
		return nil, invalid("state must be open, closed, merged or all")
	}
	switch opts.Filter {
	case "", FilterInvolved:
		if opts.Repo == "" {
			q = append(q, "involves:@me")
		}
	case FilterAuthored:
		q = append(q, "author:@me")
	case FilterReviewRequested:
		q = append(q, "review-requested:@me")
	default:
		return nil, invalid("filter must be involved, authored or review_requested")
	}
	if opts.Repo != "" {
		if !ValidRepo(opts.Repo) {
			return nil, invalid("%q is not a repository (owner/name)", opts.Repo)
		}
		q = append(q, "repo:"+opts.Repo)
	}
	if t := strings.TrimSpace(opts.Query); t != "" {
		q = append(q, t)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	var res struct {
		Items []struct {
			ghPull
			RepositoryURL string `json:"repository_url"`
			PullRequest   struct {
				MergedAt string `json:"merged_at"`
			} `json:"pull_request"`
		} `json:"items"`
	}
	v := url.Values{"q": {strings.Join(q, " ")}, "sort": {"updated"}, "order": {"desc"}, "per_page": {strconv.Itoa(limit)}}
	if _, err := g.do(ctx, token, http.MethodGet, "/search/issues?"+v.Encode(), nil, &res); err != nil {
		return nil, err
	}
	out := make([]PullRequest, 0, len(res.Items))
	for _, it := range res.Items {
		parts := strings.Split(strings.TrimSuffix(it.RepositoryURL, "/"), "/")
		if len(parts) < 2 {
			continue
		}
		repo := parts[len(parts)-2] + "/" + parts[len(parts)-1]
		p := it.ghPull
		p.MergedAt = it.PullRequest.MergedAt
		out = append(out, g.pullRequest(repo, p))
	}
	return out, nil
}

// Get implements Provider.
func (g *GitHub) Get(ctx context.Context, token string, ref Ref, opts GetOptions) (Detail, error) {
	var p ghPull
	base := repoPath(ref) + "/pulls/" + strconv.Itoa(ref.Number)
	if _, err := g.do(ctx, token, http.MethodGet, base, nil, &p); err != nil {
		return Detail{}, err
	}
	d := Detail{
		PullRequest: g.pullRequest(ref.Repo, p), Body: p.Body, Mergeable: p.Mergeable, MergeableState: p.MergeableState,
		BaseSHA: p.Base.SHA, Additions: p.Additions, Deletions: p.Deletions, ChangedFiles: p.ChangedFiles,
		Checks: []Check{}, Comments: []Comment{},
	}
	if p.Head.Repo != nil {
		d.HeadRepo, d.HeadCloneURL = p.Head.Repo.FullName, p.Head.Repo.CloneURL
	}
	if p.Base.Repo != nil {
		d.BaseCloneURL = p.Base.Repo.CloneURL
	}
	if opts.Checks && p.Head.SHA != "" {
		checks, err := g.checks(ctx, token, ref, p.Head.SHA)
		if err != nil {
			return Detail{}, err
		}
		d.Checks = checks
		d.ChecksState = ChecksVerdict(checks)
	}
	if opts.Comments {
		comments, err := g.comments(ctx, token, ref)
		if err != nil {
			return Detail{}, err
		}
		d.Comments = comments
	}
	if opts.Files {
		type ghFile struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
			Status           string `json:"status"`
			Additions        int    `json:"additions"`
			Deletions        int    `json:"deletions"`
			Patch            string `json:"patch"`
		}
		files, err := pages[ghFile](ctx, g, token, base+"/files?per_page=100")
		if err != nil {
			return Detail{}, err
		}
		for _, f := range files {
			st := "M"
			switch f.Status {
			case "added":
				st = "A"
			case "removed":
				st = "D"
			case "renamed":
				st = "R"
			}
			d.Files = append(d.Files, File{
				Path: f.Filename, PreviousPath: f.PreviousFilename, Status: st,
				Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch,
			})
		}
		if d.Files == nil {
			d.Files = []File{}
		}
	}
	return d, nil
}

// checks reads the check runs and commit statuses of sha.
func (g *GitHub) checks(ctx context.Context, token string, ref Ref, sha string) ([]Check, error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	commit := repoPath(ref) + "/commits/" + url.PathEscape(sha)
	if _, err := g.do(ctx, token, http.MethodGet, commit+"/check-runs?per_page=100", nil, &runs); err != nil {
		return nil, err
	}
	out := []Check{}
	for _, r := range runs.CheckRuns {
		out = append(out, Check{Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL})
	}
	var status struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if _, err := g.do(ctx, token, http.MethodGet, commit+"/status", nil, &status); err != nil {
		return nil, err
	}
	for _, s := range status.Statuses {
		c := Check{Name: s.Context, Status: "completed", Conclusion: s.State, URL: s.TargetURL}
		if s.State == "pending" {
			c.Status, c.Conclusion = "in_progress", ""
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// comments reads the conversation, the reviews and the line comments.
func (g *GitHub) comments(ctx context.Context, token string, ref Ref) ([]Comment, error) {
	type ghComment struct {
		ID          int64  `json:"id"`
		User        ghUser `json:"user"`
		Body        string `json:"body"`
		State       string `json:"state"`
		Path        string `json:"path"`
		Line        int    `json:"line"`
		HTMLURL     string `json:"html_url"`
		CreatedAt   string `json:"created_at"`
		SubmittedAt string `json:"submitted_at"`
	}
	n := strconv.Itoa(ref.Number)
	var out []Comment
	for _, src := range []struct{ kind, path string }{
		{KindComment, repoPath(ref) + "/issues/" + n + "/comments?per_page=100"},
		{KindReview, repoPath(ref) + "/pulls/" + n + "/reviews?per_page=100"},
		{KindReviewComment, repoPath(ref) + "/pulls/" + n + "/comments?per_page=100"},
	} {
		items, err := pages[ghComment](ctx, g, token, src.path)
		if err != nil {
			return nil, err
		}
		for _, c := range items {
			if src.kind == KindReview && c.Body == "" && c.State == "COMMENTED" {
				continue // the review of line comments listed on their own
			}
			at := c.CreatedAt
			if at == "" {
				at = c.SubmittedAt
			}
			out = append(out, Comment{
				ID: c.ID, Kind: src.kind, Author: c.User.Login, Body: c.Body, State: c.State,
				Path: c.Path, Line: c.Line, URL: c.HTMLURL, CreatedAt: unix(at),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	if out == nil {
		out = []Comment{}
	}
	return out, nil
}

// Create implements Provider.
func (g *GitHub) Create(ctx context.Context, token string, in CreateInput) (PullRequest, bool, error) {
	ref := Ref{Host: g.Host, Repo: in.Repo}
	base := in.Base
	if base == "" {
		var r ghRepo
		if _, err := g.do(ctx, token, http.MethodGet, repoPath(ref), nil, &r); err != nil {
			return PullRequest{}, false, err
		}
		base = r.DefaultBranch
	}
	var p ghPull
	_, err := g.do(ctx, token, http.MethodPost, repoPath(ref)+"/pulls", map[string]any{
		"title": in.Title, "body": in.Body, "head": in.Head, "base": base, "draft": in.Draft,
	}, &p)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity && strings.Contains(strings.ToLower(ae.Message), "already exists") {
		v := url.Values{"head": {ref.Owner() + ":" + in.Head}, "state": {"open"}}
		var open []ghPull
		if _, err := g.do(ctx, token, http.MethodGet, repoPath(ref)+"/pulls?"+v.Encode(), nil, &open); err == nil && len(open) > 0 {
			return g.pullRequest(in.Repo, open[0]), true, nil
		}
	}
	if err != nil {
		return PullRequest{}, false, err
	}
	return g.pullRequest(in.Repo, p), false, nil
}

// Review implements Provider.
func (g *GitHub) Review(ctx context.Context, token string, ref Ref, in ReviewInput) error {
	n := strconv.Itoa(ref.Number)
	switch in.Event {
	case "", ReviewComment:
		_, err := g.do(ctx, token, http.MethodPost, repoPath(ref)+"/issues/"+n+"/comments", map[string]string{"body": in.Body}, nil)
		return err
	case ReviewApprove, ReviewRequestChanges:
		_, err := g.do(ctx, token, http.MethodPost, repoPath(ref)+"/pulls/"+n+"/reviews",
			map[string]string{"body": in.Body, "event": strings.ToUpper(in.Event)}, nil)
		return err
	}
	return invalid("event must be comment, approve or request_changes")
}

// Merge implements Provider.
func (g *GitHub) Merge(ctx context.Context, token string, ref Ref, in MergeInput) (MergeResult, error) {
	method := in.Method
	if method == "" {
		method = MergeMerge
	}
	body := map[string]string{"merge_method": method}
	if in.SHA != "" {
		body["sha"] = in.SHA // GitHub refuses the merge too when the head moved
	}
	var res struct {
		Merged  bool   `json:"merged"`
		SHA     string `json:"sha"`
		Message string `json:"message"`
	}
	_, err := g.do(ctx, token, http.MethodPut, repoPath(ref)+"/pulls/"+strconv.Itoa(ref.Number)+"/merge", body, &res)
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusConflict:
			if strings.Contains(strings.ToLower(ae.Message), "head") {
				return MergeResult{}, ErrHeadMoved
			}
			return MergeResult{}, fmt.Errorf("%w: %s", ErrNotMergeable, ae.Message)
		case http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
			return MergeResult{}, fmt.Errorf("%w: %s", ErrNotMergeable, ae.Message)
		}
	}
	if err != nil {
		return MergeResult{}, err
	}
	return MergeResult{Merged: res.Merged, SHA: res.SHA, Message: res.Message}, nil
}
