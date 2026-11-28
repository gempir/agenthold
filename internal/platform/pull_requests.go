package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gempir/agenthold/internal/pr"
	"github.com/gempir/agenthold/internal/store/db"
)

var prRefArgs = map[string]any{
	"url_or_repo": map[string]any{"type": "string", "description": "The pull request's URL, owner/repo#number, or owner/repo (with number). Default: the thread's linked pull request."},
	"number":      map[string]any{"type": "integer", "description": "The pull request's number (with owner/repo, or alone for the thread's repository)."},
}

// pullRequestTools read and watch pull requests with the owner's git
// credentials (which stay on the server).
var pullRequestTools = []map[string]any{
	{
		"name": "link_pull_request",
		"description": "Link a pull request to the current thread, so that it shows in the thread and can be watched. " +
			"Pull requests you open through agenthold are linked automatically; link the ones you open with gh or the web.",
		"inputSchema": map[string]any{"type": "object", "properties": prRefArgs},
	},
	{
		"name": "watch_pull_request",
		"description": "Watch a pull request around the clock (or stop with watch=false): agenthold checks it every 2 minutes " +
			"and wakes this thread when CI fails, all checks pass, someone else comments or reviews, it gets merge conflicts, " +
			"or it is merged or closed (which also settles the thread). The pull request is linked first if needed; without " +
			"arguments it is the thread's linked pull request. Watching stops after 10 wakes in a row for comments only and " +
			"after 8 failed reads. Sub-threads cannot watch: their parent thread does.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"url_or_repo": prRefArgs["url_or_repo"],
			"number":      prRefArgs["number"],
			"watch":       map[string]any{"type": "boolean", "description": "true (default) starts watching, false stops."},
		}},
	},
	{
		"name":        "list_pull_requests",
		"description": "List pull requests your owner is involved in (authored, assigned, review requested, mentioned), and the ones linked to this thread.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"state": map[string]any{"type": "string", "enum": []string{"open", "closed", "merged", "all"}, "description": "Default open."},
			"repo":  map[string]any{"type": "string", "description": "Only this repository (owner/repo)."},
		}},
	},
	{
		"name":        "get_pull_request",
		"description": "Read a pull request: description, state, head and base, CI checks, reviews and comments, and the changed files.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"repo":   map[string]any{"type": "string", "description": "owner/repo or the pull request's URL (default: the thread's repository)."},
			"number": prRefArgs["number"],
			"diff":   map[string]any{"type": "boolean", "description": "Include each file's patch (default false)."},
		}},
	},
}

type prToolArgs struct {
	URLOrRepo string `json:"url_or_repo"`
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Watch     *bool  `json:"watch"`
	State     string `json:"state"`
	Diff      bool   `json:"diff"`
}

func isPullRequestTool(tool string) bool {
	for _, t := range pullRequestTools {
		if t["name"] == tool {
			return true
		}
	}
	return false
}

// prError makes the hub's errors readable for the agent.
func prError(err error) error {
	if errors.Is(err, pr.ErrNoCredential) {
		return errors.New("your owner has no git credential for that host: they add a token or connect GitHub in Settings → Git")
	}
	return err
}

func (p *Service) pullRequestCall(ctx context.Context, caller db.Agent, sessionID, tool string, raw json.RawMessage) (any, error) {
	var in prToolArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, errors.New("invalid arguments")
		}
	}
	var out any
	var err error
	switch tool {
	case "list_pull_requests":
		out, err = p.listPullRequests(ctx, caller, sessionID, in)
	case "get_pull_request":
		out, err = p.getPullRequest(ctx, caller, sessionID, in)
	case "link_pull_request", "watch_pull_request":
		sess, serr := p.ownThread(ctx, caller, "", sessionID)
		if serr != nil {
			return nil, serr
		}
		if tool == "link_pull_request" {
			out, err = p.linkPullRequest(ctx, caller, sess, in)
		} else {
			out, err = p.watchPullRequest(ctx, caller, sess, in)
		}
	default:
		return nil, fmt.Errorf("unknown tool %q", tool)
	}
	return out, prError(err)
}

func (p *Service) prRef(ctx context.Context, caller db.Agent, sess *db.AgentSession, s string, number int) (pr.Ref, error) {
	ref, err := pr.ParseRef(s, number, p.PullRequests.DefaultHost(ctx, caller.OwnerID))
	if err != nil {
		return pr.Ref{}, err
	}
	if ref.Repo == "" {
		if sess == nil {
			return pr.Ref{}, errors.New("name the pull request's repository (owner/repo)")
		}
		return p.PullRequests.ResolveRef(ctx, *sess, ref)
	}
	return ref, nil
}

type linkedInfo struct {
	ID       string `json:"id"`
	Ref      string `json:"pullRequest"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Checks   string `json:"checks,omitempty"`
	Watching bool   `json:"watching"`
	Wakes    int64  `json:"wakes,omitempty"`
}

func toLinked(r db.SessionPullRequest) linkedInfo {
	return linkedInfo{
		ID: r.ID, Ref: fmt.Sprintf("%s#%d", r.Repo, r.Number), URL: r.Url, Title: r.Title, State: r.State,
		Checks: r.ChecksState, Watching: r.Watching == 1, Wakes: r.WakeCount,
	}
}

func (p *Service) linkPullRequest(ctx context.Context, caller db.Agent, sess db.AgentSession, in prToolArgs) (string, error) {
	if strings.TrimSpace(in.URLOrRepo) == "" && in.Number == 0 {
		return "", errors.New("name the pull request: url_or_repo (its URL or owner/repo#number) or number")
	}
	ref, err := p.prRef(ctx, caller, &sess, in.URLOrRepo, in.Number)
	if err != nil {
		return "", err
	}
	link, err := p.PullRequests.Link(ctx, sess, caller.OwnerID, ref)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Linked %s#%d (%q, %s) to this thread. watch_pull_request watches it.", link.Repo, link.Number, link.Title, link.State), nil
}

func (p *Service) watchPullRequest(ctx context.Context, caller db.Agent, sess db.AgentSession, in prToolArgs) (string, error) {
	watch := in.Watch == nil || *in.Watch
	if watch {
		if err := pr.CanWatch(sess); err != nil {
			return "", err
		}
	}
	var link db.SessionPullRequest
	if strings.TrimSpace(in.URLOrRepo) == "" && in.Number == 0 {
		rows, err := p.PullRequests.Linked(ctx, sess.ID)
		if err != nil {
			return "", err
		}
		var open []db.SessionPullRequest
		for _, r := range rows {
			if r.State != pr.StateMerged && r.State != pr.StateClosed {
				open = append(open, r)
			}
		}
		switch len(open) {
		case 0:
			return "", errors.New("this thread has no open linked pull request: name one (url_or_repo)")
		case 1:
			link = open[0]
		default:
			return "", errors.New("this thread has several open linked pull requests: name one (url_or_repo)")
		}
	} else {
		ref, err := p.prRef(ctx, caller, &sess, in.URLOrRepo, in.Number)
		if err != nil {
			return "", err
		}
		rows, err := p.PullRequests.Linked(ctx, sess.ID)
		if err != nil {
			return "", err
		}
		for _, r := range rows {
			if r.Host == ref.Host && r.Repo == ref.Repo && int(r.Number) == ref.Number {
				link = r
			}
		}
		if link.ID == "" {
			if !watch {
				return "", errors.New("that pull request is not linked to this thread")
			}
			if link, err = p.PullRequests.Link(ctx, sess, caller.OwnerID, ref); err != nil {
				return "", err
			}
		}
	}
	w, err := p.PullRequests.SetWatching(ctx, sess, caller.OwnerID, caller.OwnerID, link.ID, watch)
	if err != nil {
		return "", err
	}
	if !watch {
		return fmt.Sprintf("Stopped watching %s#%d.", w.Repo, w.Number), nil
	}
	return fmt.Sprintf("Watching %s#%d (%q). You will be woken in this thread when CI fails or passes, someone comments or "+
		"reviews, it gets conflicts, or it is merged or closed. End your turn while you wait.", w.Repo, w.Number, w.Title), nil
}

func (p *Service) listPullRequests(ctx context.Context, caller db.Agent, sessionID string, in prToolArgs) (string, error) {
	repo := strings.TrimSpace(in.Repo)
	if repo != "" && !pr.ValidRepo(repo) {
		return "", fmt.Errorf("%q is not a repository (owner/repo)", repo)
	}
	list, hosts, failed, err := p.PullRequests.List(ctx, caller.OwnerID, "", pr.ListOptions{State: in.State, Repo: repo, Limit: 50})
	if err != nil {
		return "", err
	}
	out := map[string]any{"pullRequests": list}
	if sess, err := p.ownThread(ctx, caller, "", sessionID); err == nil {
		if rows, err := p.PullRequests.Linked(ctx, sess.ID); err == nil && len(rows) > 0 {
			linked := make([]linkedInfo, 0, len(rows))
			for _, r := range rows {
				linked = append(linked, toLinked(r))
			}
			out["linkedToThisThread"] = linked
		}
	}
	if len(hosts) == 0 {
		out["note"] = "your owner has no git credential for a supported host (Settings → Git)"
	}
	for _, f := range failed {
		out["errors"] = append(asSlice(out["errors"]), f.Host+": "+prError(f.Err).Error())
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b), nil
}

func asSlice(v any) []string {
	s, _ := v.([]string)
	return s
}

func (p *Service) getPullRequest(ctx context.Context, caller db.Agent, sessionID string, in prToolArgs) (string, error) {
	var sess *db.AgentSession
	if s, err := p.ownThread(ctx, caller, "", sessionID); err == nil {
		sess = &s
	}
	s := strings.TrimSpace(in.Repo)
	if s == "" {
		s = in.URLOrRepo
	}
	if s == "" && in.Number == 0 && sess != nil {
		// The thread's only linked pull request.
		if rows, err := p.PullRequests.Linked(ctx, sess.ID); err == nil && len(rows) == 1 {
			s, in.Number = rows[0].Host+"/"+rows[0].Repo, int(rows[0].Number)
		}
	}
	ref, err := p.prRef(ctx, caller, sess, s, in.Number)
	if err != nil {
		return "", err
	}
	d, err := p.PullRequests.Get(ctx, caller.OwnerID, ref, true)
	if err != nil {
		return "", err
	}
	const maxBody, maxPatch, maxComments = 4000, 4000, 50
	d.Body = truncate(d.Body, maxBody)
	if len(d.Comments) > maxComments {
		d.Comments = d.Comments[len(d.Comments)-maxComments:]
	}
	for i := range d.Comments {
		d.Comments[i].Body = truncate(d.Comments[i].Body, 1000)
	}
	for i := range d.Files {
		if in.Diff {
			d.Files[i].Patch = truncate(d.Files[i].Patch, maxPatch)
		} else {
			d.Files[i].Patch = ""
		}
	}
	b, _ := json.MarshalIndent(d, "", "  ")
	return string(b), nil
}
