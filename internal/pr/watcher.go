package pr

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

// Watching limits.
const (
	// DefaultWatchInterval is how often watched pull requests are read.
	DefaultWatchInterval = 2 * time.Minute
	// MaxCommentWakes consecutive wakes for nothing but comments stop watching.
	MaxCommentWakes = 10
	// MaxReadFailures consecutive failures to read a pull request stop watching.
	MaxReadFailures = 8
)

// WatcherName is who the watcher's prompts come from.
const WatcherName = "Pull request watcher"

// Observation is what the watcher acts on in a pull request: its state, the
// verdict of the head's checks, conflicts and other people's comments.
type Observation struct {
	State    string
	HeadSHA  string
	Checks   string
	Failed   []Check
	Conflict bool
	// MaxComment is the newest comment id by someone else, per kind.
	MaxComment map[string]int64
	// Others are the comments by someone else than the viewer.
	Others []Comment
}

// Observe reduces a pull request (read with comments and checks) to what
// the watcher wakes for. viewer's own comments (the agent's, as they are
// made with its owner's account) are not news.
func Observe(d Detail, viewer string) Observation {
	o := Observation{State: d.State, HeadSHA: d.HeadSHA, Checks: d.ChecksState, Conflict: d.Conflicted(), MaxComment: map[string]int64{}}
	if o.Checks == "" {
		o.Checks = ChecksVerdict(d.Checks)
	}
	for _, c := range d.Checks {
		if c.Failed() {
			o.Failed = append(o.Failed, c)
		}
	}
	for _, c := range d.Comments {
		if viewer != "" && strings.EqualFold(c.Author, viewer) {
			continue
		}
		o.Others = append(o.Others, c)
		if c.ID > o.MaxComment[c.Kind] {
			o.MaxComment[c.Kind] = c.ID
		}
	}
	return o
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// keys are the facts the watcher wakes for, each once: a key the previous
// fingerprint had is not news.
func (o Observation) keys() []string {
	var k []string
	switch o.State {
	case StateMerged, StateClosed:
		k = append(k, "state:"+o.State)
	}
	switch o.Checks {
	case ChecksFailure:
		names := make([]string, 0, len(o.Failed))
		for _, c := range o.Failed {
			names = append(names, c.Name)
		}
		sort.Strings(names)
		sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
		k = append(k, "fail:"+short(o.HeadSHA)+":"+hex.EncodeToString(sum[:4]))
	case ChecksSuccess:
		k = append(k, "pass:"+short(o.HeadSHA))
	}
	if o.Conflict {
		k = append(k, "conflict:"+short(o.HeadSHA))
	}
	if len(o.Others) > 0 {
		k = append(k, fmt.Sprintf("comments:%d.%d.%d", o.MaxComment[KindComment], o.MaxComment[KindReview], o.MaxComment[KindReviewComment]))
	}
	sort.Strings(k)
	return k
}

// Fingerprint is the deterministic summary the watcher compares polls by.
func (o Observation) Fingerprint() string {
	k := o.keys()
	if len(k) == 0 {
		return "-" // read, nothing to act on (not the empty "never read")
	}
	return strings.Join(k, " ")
}

// commentMax reads the per-kind newest comment ids out of a fingerprint.
func commentMax(fp string) map[string]int64 {
	out := map[string]int64{}
	for _, k := range strings.Fields(fp) {
		v, ok := strings.CutPrefix(k, "comments:")
		if !ok {
			continue
		}
		parts := strings.Split(v, ".")
		for i, kind := range []string{KindComment, KindReview, KindReviewComment} {
			if i < len(parts) {
				n, _ := strconv.ParseInt(parts[i], 10, 64)
				out[kind] = n
			}
		}
	}
	return out
}

// Change is what happened since the previous fingerprint.
type Change struct {
	Failed      []Check
	Passed      bool
	Conflict    bool
	NewComments []Comment
}

// Empty reports whether nothing worth a wake happened.
func (c Change) Empty() bool {
	return len(c.Failed) == 0 && !c.Passed && !c.Conflict && len(c.NewComments) == 0
}

// CommentOnly reports whether only comments arrived.
func (c Change) CommentOnly() bool {
	return len(c.NewComments) > 0 && len(c.Failed) == 0 && !c.Passed && !c.Conflict
}

// Diff is what o has that the fingerprint prev had not.
func Diff(prev string, o Observation) Change {
	had := map[string]bool{}
	for _, k := range strings.Fields(prev) {
		had[k] = true
	}
	var c Change
	for _, k := range o.keys() {
		if had[k] {
			continue
		}
		switch {
		case strings.HasPrefix(k, "fail:"):
			c.Failed = o.Failed
		case strings.HasPrefix(k, "pass:"):
			c.Passed = true
		case strings.HasPrefix(k, "conflict:"):
			c.Conflict = true
		case strings.HasPrefix(k, "comments:"):
			seen := commentMax(prev)
			for _, cm := range o.Others {
				if cm.ID > seen[cm.Kind] {
					c.NewComments = append(c.NewComments, cm)
				}
			}
		}
	}
	return c
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Prompt is the message that wakes the agent.
func (c Change) Prompt(r db.SessionPullRequest, d Detail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The pull request you are watching, %s#%d (%q), needs your attention:\n", d.Repo, d.Number, d.Title)
	if len(c.Failed) > 0 {
		fmt.Fprintf(&b, "\n- CI failed on %s:\n", short(d.HeadSHA))
		for i, f := range c.Failed {
			if i == 20 {
				fmt.Fprintf(&b, "  - … and %d more\n", len(c.Failed)-i)
				break
			}
			line := "  - " + f.Name + " (" + f.Conclusion + ")"
			if f.URL != "" {
				line += " " + f.URL
			}
			b.WriteString(line + "\n")
		}
	}
	if c.Passed {
		fmt.Fprintf(&b, "\n- All checks passed on %s.\n", short(d.HeadSHA))
	}
	if c.Conflict {
		fmt.Fprintf(&b, "\n- It has merge conflicts with %s: rebase or merge the base branch and resolve them.\n", d.BaseRef)
	}
	if len(c.NewComments) > 0 {
		n := len(c.NewComments)
		word := "comments"
		if n == 1 {
			word = "comment"
		}
		fmt.Fprintf(&b, "\n- %d new %s:\n", n, word)
		for i, cm := range c.NewComments {
			if i == 10 {
				fmt.Fprintf(&b, "  - … and %d more\n", n-i)
				break
			}
			where := cm.Kind
			if cm.Kind == KindReview && cm.State != "" {
				where = "review, " + strings.ToLower(strings.ReplaceAll(cm.State, "_", " "))
			}
			if cm.Path != "" {
				where += " on " + cm.Path
				if cm.Line > 0 {
					where += ":" + strconv.Itoa(cm.Line)
				}
			}
			fmt.Fprintf(&b, "  - @%s (%s): %s\n", cm.Author, where, clip(cm.Body, 500))
		}
	}
	fmt.Fprintf(&b, "\n%s\nRead it with get_pull_request and act on it. Watching continues; watch_pull_request with watch=false stops it.", d.URL)
	return b.String()
}

// Watcher polls watched pull requests and wakes their threads.
type Watcher struct {
	Service *Service
	// Every is the poll interval. Default DefaultWatchInterval.
	Every time.Duration
}

// Run polls until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	every := w.Every
	if every <= 0 {
		every = DefaultWatchInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := w.Once(ctx); err != nil && ctx.Err() == nil {
			w.Service.logger().Warn("watch pull requests", "err", err)
		}
	}
}

type read struct {
	d      Detail
	viewer string
	err    error
}

// PollResult counts what one poll did.
type PollResult struct {
	Checked, Woken, Stopped, Settled, Failed int
}

// Once polls every watched pull request of active threads that are not
// running a turn.
func (w *Watcher) Once(ctx context.Context) (PollResult, error) {
	s := w.Service
	var res PollResult
	rows, err := s.Store.ListWatchedPullRequests(ctx)
	if err != nil {
		return res, err
	}
	reads := map[string]read{} // one read per owner and pull request
	for _, row := range rows {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		r := row.SessionPullRequest
		if s.Sessions != nil && s.Sessions.Running(r.SessionID) {
			continue
		}
		ref := Ref{Host: r.Host, Repo: r.Repo, Number: int(r.Number)}
		key := row.OwnerID + "\x00" + ref.String()
		rd, ok := reads[key]
		if !ok {
			rd = w.read(ctx, row.OwnerID, ref)
			reads[key] = rd
		}
		res.Checked++
		now := s.now().Unix()
		rec := db.RecordPullRequestPollParams{
			Title: r.Title, State: r.State, HeadSha: r.HeadSha, ChecksState: r.ChecksState, Watching: 1,
			WakeCount: r.WakeCount, CommentStreak: r.CommentStreak, FailCount: 0,
			LastCheckedAt: sql.NullInt64{Int64: now, Valid: true}, LastFingerprint: r.LastFingerprint, UpdatedAt: now, ID: r.ID,
		}
		if rd.err != nil {
			res.Failed++
			rec.FailCount = r.FailCount + 1
			if rec.FailCount >= MaxReadFailures {
				rec.Watching = 0
				res.Stopped++
				w.notice(ctx, r.SessionID, fmt.Sprintf("Stopped watching %s: it could not be read %d times in a row (%v).", ref, rec.FailCount, rd.err))
			}
			w.record(ctx, rec)
			continue
		}
		d := rd.d
		obs := Observe(d, rd.viewer)
		rec.Title, rec.State, rec.HeadSha, rec.ChecksState = d.Title, d.State, d.HeadSHA, obs.Checks
		rec.LastFingerprint = obs.Fingerprint()

		if d.State == StateMerged || d.State == StateClosed {
			rec.Watching = 0
			res.Stopped++
			if w.record(ctx, rec) {
				if w.settle(ctx, row, d) {
					res.Settled++
				}
			}
			continue
		}
		if r.LastFingerprint == "" {
			w.record(ctx, rec) // the first read is the baseline
			continue
		}
		change := Diff(r.LastFingerprint, obs)
		if change.Empty() {
			w.record(ctx, rec)
			continue
		}
		text := change.Prompt(r, d)
		streak := int64(0)
		if change.CommentOnly() {
			streak = r.CommentStreak + 1
		}
		if streak >= MaxCommentWakes {
			text += fmt.Sprintf("\n\nWatching stopped after %d wakes in a row for comments only; call watch_pull_request to watch it again.", streak)
		}
		if s.Sessions == nil {
			return res, errors.New("pull request watcher: no session manager")
		}
		err := s.Sessions.PromptWith(ctx, r.SessionID, row.OwnerID, WatcherName, text, nil, session.PromptOpts{})
		if errors.Is(err, session.ErrBusy) {
			continue // a turn started meanwhile: the next poll tells it
		}
		if err != nil {
			s.logger().Warn("wake a pull request's thread", "session", r.SessionID, "pr", ref.String(), "err", err)
			res.Failed++
			rec = db.RecordPullRequestPollParams{
				Title: rec.Title, State: rec.State, HeadSha: rec.HeadSha, ChecksState: rec.ChecksState, Watching: 1,
				WakeCount: r.WakeCount, CommentStreak: r.CommentStreak, FailCount: r.FailCount + 1,
				LastCheckedAt: rec.LastCheckedAt, LastFingerprint: r.LastFingerprint, UpdatedAt: now, ID: r.ID,
			}
			if rec.FailCount >= MaxReadFailures {
				rec.Watching = 0
				res.Stopped++
			}
			w.record(ctx, rec)
			continue
		}
		res.Woken++
		rec.WakeCount = r.WakeCount + 1
		rec.CommentStreak = streak
		if streak >= MaxCommentWakes {
			rec.Watching = 0
			res.Stopped++
		}
		w.record(ctx, rec)
		s.Audit.Record(ctx, audit.Entry{
			AgentID: row.AgentID, UserID: row.OwnerID, Action: audit.PullRequestWake, Target: r.SessionID,
			Details: map[string]any{
				"pullRequest": ref.String(), "failedChecks": len(change.Failed), "passed": change.Passed,
				"conflict": change.Conflict, "comments": len(change.NewComments),
			},
		})
	}
	return res, nil
}

func (w *Watcher) read(ctx context.Context, ownerID string, ref Ref) read {
	s := w.Service
	p, token, err := s.client(ctx, ownerID, ref.Host)
	if err != nil {
		return read{err: err}
	}
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	d, err := p.Get(rctx, token, ref, GetOptions{Checks: true, Comments: true})
	if err != nil {
		return read{err: err}
	}
	viewer, err := s.viewer(rctx, p, ownerID, ref.Host, token)
	if err != nil {
		return read{err: err}
	}
	return read{d: d, viewer: viewer}
}

// record stores a poll; false when the pull request was unwatched meanwhile.
func (w *Watcher) record(ctx context.Context, p db.RecordPullRequestPollParams) bool {
	n, err := w.Service.Store.RecordPullRequestPoll(ctx, p)
	if err != nil {
		w.Service.logger().Warn("record a pull request poll", "id", p.ID, "err", err)
		return false
	}
	return n > 0
}

// settle settles the thread of a merged or closed pull request.
func (w *Watcher) settle(ctx context.Context, row db.ListWatchedPullRequestsRow, d Detail) bool {
	s := w.Service
	r := row.SessionPullRequest
	what := "was merged"
	if d.State == StateClosed {
		what = "was closed"
	}
	if s.Sessions != nil && s.Sessions.Running(r.SessionID) {
		w.notice(ctx, r.SessionID, fmt.Sprintf("Pull request %s#%d %s: stopped watching it.", d.Repo, d.Number, what))
		return false
	}
	_, changed, err := session.Settle(ctx, s.Store, r.SessionID, session.SettledByAuto, s.now())
	if err != nil {
		s.logger().Warn("settle a pull request's thread", "session", r.SessionID, "err", err)
		return false
	}
	w.notice(ctx, r.SessionID, fmt.Sprintf("Pull request %s#%d %s: stopped watching it and settled the thread.", d.Repo, d.Number, what))
	if changed {
		s.Audit.Record(ctx, audit.Entry{
			AgentID: row.AgentID, UserID: row.OwnerID, Action: audit.SessionSettle, Target: r.SessionID,
			Details: map[string]any{"reason": "pull_request_" + d.State, "pullRequest": d.Ref().String(), "session": row.SessionTitle},
		})
	}
	return changed
}

func (w *Watcher) notice(ctx context.Context, sessionID, msg string) {
	if n, ok := w.Service.Sessions.(noticer); ok {
		if err := n.Notice(ctx, sessionID, msg); err != nil {
			w.Service.logger().Warn("pull request watcher notice", "session", sessionID, "err", err)
		}
	}
}
