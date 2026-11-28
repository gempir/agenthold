// Package pr is the controller's pull request hub: it reads, opens, reviews
// and merges pull requests on the users' git hosts with the credentials they
// stored in their git settings, links pull requests to threads, and watches
// linked ones around the clock (Watcher), waking the thread's agent when
// something it should act on happens.
//
// Tokens are only used here, on the server. They never reach a sandbox:
// agents act on pull requests through the platform tools, which call this
// package, and their own git traffic gets credentials from the egress proxy.
package pr

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gempir/agenthold/internal/git"
)

// Pull request states. A draft is open.
const (
	StateOpen   = "open"
	StateDraft  = "draft"
	StateMerged = "merged"
	StateClosed = "closed"
)

// StateAll lists pull requests in every state (besides StateOpen, StateClosed
// and StateMerged).
const StateAll = "all"

// Check verdicts (ChecksState).
const (
	ChecksNone    = ""
	ChecksPending = "pending"
	ChecksSuccess = "success"
	ChecksFailure = "failure"
)

// Merge methods.
const (
	MergeMerge  = "merge"
	MergeSquash = "squash"
	MergeRebase = "rebase"
)

// Review events.
const (
	ReviewComment        = "comment"
	ReviewApprove        = "approve"
	ReviewRequestChanges = "request_changes"
)

// List filters: whose pull requests to list.
const (
	FilterInvolved        = "involved"
	FilterAuthored        = "authored"
	FilterReviewRequested = "review_requested"
)

// Errors.
var (
	// ErrInvalid is wrapped by errors whose message is meant for the user.
	ErrInvalid = errors.New("invalid pull request request")
	// ErrNotFound: no such pull request (or link).
	ErrNotFound = errors.New("pull request not found")
	// ErrNoCredential: the user stored no token for the host.
	ErrNoCredential = errors.New("no git credential for this host: add a token or connect GitHub in Settings → Git")
	// ErrUnsupportedHost: no provider speaks to the host.
	ErrUnsupportedHost = errors.New("pull requests on this host are not supported (GitHub and GitHub Enterprise are)")
	// ErrHeadMoved: the pull request got new commits since the caller read it.
	ErrHeadMoved = errors.New("the pull request's head moved since it was read; review the new commits first")
	// ErrNotMergeable: the host refused the merge.
	ErrNotMergeable = errors.New("the pull request cannot be merged")
	// ErrSubthread: sub-threads do not watch; their parent thread does.
	ErrSubthread = errors.New("a sub-thread cannot watch pull requests: its parent thread watches instead")
)

type invalidError string

func (e invalidError) Error() string        { return string(e) }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, args ...any) error { return invalidError(fmt.Sprintf(format, args...)) }

// Ref names one pull request.
type Ref struct {
	Host   string `json:"host"`
	Repo   string `json:"repo"` // owner/name
	Number int    `json:"number"`
}

func (r Ref) String() string { return fmt.Sprintf("%s/%s#%d", r.Host, r.Repo, r.Number) }

// Owner is the repository's owner.
func (r Ref) Owner() string { o, _, _ := strings.Cut(r.Repo, "/"); return o }

// Name is the repository's name.
func (r Ref) Name() string { _, n, _ := strings.Cut(r.Repo, "/"); return n }

// PullRequest is what a list shows of a pull request.
type PullRequest struct {
	Host      string `json:"host"`
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	Author    string `json:"author"`
	HeadRef   string `json:"headRef,omitempty"`
	HeadSHA   string `json:"headSha,omitempty"`
	BaseRef   string `json:"baseRef,omitempty"`
	Comments  int    `json:"comments"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Ref is the pull request's reference.
func (p PullRequest) Ref() Ref { return Ref{Host: p.Host, Repo: p.Repo, Number: p.Number} }

// Check is one CI check or commit status of the head commit.
type Check struct {
	Name string `json:"name"`
	// Status is queued, in_progress or completed.
	Status string `json:"status"`
	// Conclusion of a completed check: success, failure, neutral, cancelled,
	// skipped, timed_out, action_required.
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

// Failed reports whether the check completed unsuccessfully.
func (c Check) Failed() bool {
	switch c.Conclusion {
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "error":
		return true
	}
	return false
}

// Comment kinds.
const (
	KindComment       = "comment"        // on the conversation
	KindReview        = "review"         // a review (approve, request changes, comment)
	KindReviewComment = "review_comment" // on a line of the diff
)

// Comment is a conversation comment, a review or a line comment.
type Comment struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Author string `json:"author"`
	Body   string `json:"body"`
	// State of a review: APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED.
	State     string `json:"state,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	URL       string `json:"url,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// File is a file the pull request changes.
type File struct {
	Path         string `json:"path"`
	PreviousPath string `json:"previousPath,omitempty"`
	// Status is A, M, D or R (like the workspace diffs).
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}

// Detail is everything about one pull request.
type Detail struct {
	PullRequest
	Body string `json:"body"`
	// Mergeable is nil while the host computes it.
	Mergeable *bool `json:"mergeable,omitempty"`
	// MergeableState is the host's view (GitHub: clean, dirty = conflicts,
	// blocked, behind, unstable, unknown, draft).
	MergeableState string    `json:"mergeableState,omitempty"`
	BaseSHA        string    `json:"baseSha,omitempty"`
	HeadRepo       string    `json:"headRepo,omitempty"`
	HeadCloneURL   string    `json:"headCloneUrl,omitempty"`
	BaseCloneURL   string    `json:"baseCloneUrl,omitempty"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   int       `json:"changedFiles"`
	Checks         []Check   `json:"checks"`
	ChecksState    string    `json:"checksState"`
	Comments       []Comment `json:"comments"`
	Files          []File    `json:"files,omitempty"`
	// Viewer is the login of the account the token belongs to.
	Viewer string `json:"viewer,omitempty"`
}

// Conflicted reports whether the pull request has merge conflicts.
func (d Detail) Conflicted() bool { return d.MergeableState == "dirty" }

// ChecksVerdict folds checks into one verdict.
func ChecksVerdict(checks []Check) string {
	if len(checks) == 0 {
		return ChecksNone
	}
	pending := false
	for _, c := range checks {
		if c.Failed() {
			return ChecksFailure
		}
		if c.Status != "completed" {
			pending = true
		}
	}
	if pending {
		return ChecksPending
	}
	return ChecksSuccess
}

// ListOptions filter a list.
type ListOptions struct {
	State  string // StateOpen (default), StateClosed, StateMerged, StateAll
	Filter string // FilterInvolved (default), FilterAuthored, FilterReviewRequested
	Repo   string // only this owner/name
	Query  string // free text
	Limit  int    // default 50, at most 100
}

// GetOptions choose what Get reads besides the pull request.
type GetOptions struct {
	Files    bool
	Comments bool
	Checks   bool
}

// CreateInput opens a pull request.
type CreateInput struct {
	Repo  string
	Head  string // the branch
	Base  string // default: the repository's default branch
	Title string
	Body  string
	Draft bool
}

// ReviewInput comments on or reviews a pull request.
type ReviewInput struct {
	Body  string
	Event string // ReviewComment (default: a conversation comment), ReviewApprove, ReviewRequestChanges
}

// MergeInput merges a pull request.
type MergeInput struct {
	Method string // MergeMerge (default), MergeSquash, MergeRebase
	// SHA is the head the caller read: the merge is refused if it moved.
	SHA string
}

// MergeResult is a merge's outcome.
type MergeResult struct {
	Merged  bool   `json:"merged"`
	SHA     string `json:"sha,omitempty"`
	Message string `json:"message,omitempty"`
}

// Provider speaks to one kind of git host. GitHub implements it; GitLab
// and others plug in the same way.
type Provider interface {
	// Viewer is the login of the token's account.
	Viewer(ctx context.Context, token string) (string, error)
	List(ctx context.Context, token string, opts ListOptions) ([]PullRequest, error)
	Get(ctx context.Context, token string, ref Ref, opts GetOptions) (Detail, error)
	// Create opens a pull request; one already open for the branch is
	// returned instead (existing true).
	Create(ctx context.Context, token string, in CreateInput) (pr PullRequest, existing bool, err error)
	Review(ctx context.Context, token string, ref Ref, in ReviewInput) error
	Merge(ctx context.Context, token string, ref Ref, in MergeInput) (MergeResult, error)
}

var (
	repoPart = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	shortRef = regexp.MustCompile(`^(?:([^/#\s]+)/)?([^/#\s]+)/([^/#\s]+)#(\d+)$`)
)

// ValidRepo reports whether repo is owner/name.
func ValidRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	return ok && repoPart.MatchString(owner) && repoPart.MatchString(name) && owner != "." && owner != ".." && name != "." && name != ".."
}

// ParseRef reads a pull request reference: a URL
// (https://github.com/o/r/pull/12), "o/r#12", "host/o/r#12", "o/r" or
// "host/o/r" with number, or just a number ("12", "#12"; Repo is empty then
// and the caller fills it in from the thread's repository). defaultHost is
// the host of references that name none.
func ParseRef(s string, number int, defaultHost string) (Ref, error) {
	s = strings.TrimSpace(s)
	if number < 0 {
		return Ref{}, invalid("number must be positive")
	}
	if s == "" || strings.Trim(s, "#0123456789") == "" {
		if s != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
			if err != nil || n <= 0 {
				return Ref{}, invalid("%q is not a pull request number", s)
			}
			number = n
		}
		if number <= 0 {
			return Ref{}, invalid("name a pull request: its URL, owner/repo#number, or a number")
		}
		return Ref{Host: defaultHost, Number: number}, nil
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return Ref{}, invalid("%q is not a pull request URL", s)
		}
		host, err := git.NormalizeHost(u.Hostname())
		if err != nil {
			return Ref{}, invalid("%q is not a pull request URL", s)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		// github: o/r/pull/12[/files]; gitlab: group/r/-/merge_requests/12
		for i := 2; i+1 < len(parts); i++ {
			if parts[i] == "pull" || parts[i] == "pulls" || parts[i] == "merge_requests" {
				ownerEnd := i
				if parts[i-1] == "-" {
					ownerEnd = i - 1
				}
				if ownerEnd < 2 {
					break
				}
				n, err := strconv.Atoi(parts[i+1])
				if err != nil || n <= 0 {
					break
				}
				repo := strings.Join(parts[ownerEnd-2:ownerEnd], "/")
				if !ValidRepo(repo) {
					break
				}
				return Ref{Host: host, Repo: repo, Number: n}, nil
			}
		}
		if len(parts) == 2 && number > 0 && ValidRepo(strings.TrimSuffix(parts[0]+"/"+parts[1], ".git")) {
			return Ref{Host: host, Repo: strings.TrimSuffix(parts[0]+"/"+parts[1], ".git"), Number: number}, nil
		}
		return Ref{}, invalid("%q is not a pull request URL", s)
	}
	if m := shortRef.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[4])
		return refFrom(m[1], m[2]+"/"+m[3], n, defaultHost, s)
	}
	parts := strings.Split(s, "/")
	switch len(parts) {
	case 2:
		return refFrom("", s, number, defaultHost, s)
	case 3:
		return refFrom(parts[0], parts[1]+"/"+parts[2], number, defaultHost, s)
	}
	return Ref{}, invalid("%q is not a pull request reference (use its URL or owner/repo#number)", s)
}

func refFrom(host, repo string, number int, defaultHost, raw string) (Ref, error) {
	if host == "" {
		host = defaultHost
	} else {
		h, err := git.NormalizeHost(host)
		if err != nil {
			return Ref{}, invalid("%q is not a pull request reference", raw)
		}
		host = h
	}
	if !ValidRepo(repo) {
		return Ref{}, invalid("%q is not a repository (owner/name)", repo)
	}
	if number <= 0 {
		return Ref{}, invalid("which pull request of %s? give its number", repo)
	}
	return Ref{Host: host, Repo: repo, Number: number}, nil
}

// ParseRemote reads the host and owner/name of a git remote URL
// (https://github.com/o/r.git, git@github.com:o/r.git, ssh://git@host/o/r).
func ParseRemote(remote string) (host, repo string, err error) {
	remote = strings.TrimSpace(remote)
	bad := invalid("the repository's remote %q is not on a supported git host", remote)
	var path string
	switch {
	case strings.Contains(remote, "://"):
		u, perr := url.Parse(remote)
		if perr != nil || u.Hostname() == "" {
			return "", "", bad
		}
		host, path = u.Hostname(), u.Path
	case strings.Contains(remote, ":") && !strings.HasPrefix(remote, "/"):
		// scp-like: [user@]host:owner/repo.git
		h, p, _ := strings.Cut(remote, ":")
		if i := strings.LastIndex(h, "@"); i >= 0 {
			h = h[i+1:]
		}
		host, path = h, p
	default:
		return "", "", bad
	}
	host, herr := git.NormalizeHost(host)
	if herr != nil {
		return "", "", bad
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return "", "", bad
	}
	repo = parts[len(parts)-2] + "/" + strings.TrimSuffix(parts[len(parts)-1], ".git")
	if !ValidRepo(repo) {
		return "", "", bad
	}
	return host, repo, nil
}
