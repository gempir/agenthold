package pr_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/pr"
	"github.com/gempir/agenthold/internal/pr/prtest"
	"github.com/gempir/agenthold/internal/store/db"
)

// watched links and watches o/r#n in the fixture's thread.
func (f *fixture) watched(t *testing.T, n int) db.SessionPullRequest {
	t.Helper()
	link, err := f.svc.Link(t.Context(), f.sess, "u1", pr.Ref{Host: "github.com", Repo: "o/r", Number: n})
	if err != nil {
		t.Fatal(err)
	}
	w, err := f.svc.SetWatching(t.Context(), f.sess, "u1", "u1", link.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (f *fixture) poll(t *testing.T, w *pr.Watcher) pr.PollResult {
	t.Helper()
	f.now = f.now.Add(2 * time.Minute)
	res, err := w.Once(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestWatcherWakesOnceForEachChange(t *testing.T) {
	f := newFixture(t)
	w := &pr.Watcher{Service: f.svc}
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 10, Title: "Watch me", HeadRef: "feat", HeadSHA: "sha-a"})
	f.gh.SetChecks("sha-a", prtest.CheckRun{Name: "test", Status: "in_progress"})
	link := f.watched(t, 10)

	// Nothing happened since the baseline.
	if res := f.poll(t, w); res.Checked != 1 || res.Woken != 0 {
		t.Fatalf("idle poll = %+v", res)
	}
	// CI fails: one wake naming the check, and only one.
	f.gh.SetChecks("sha-a", prtest.CheckRun{Name: "test", Status: "completed", Conclusion: "failure"},
		prtest.CheckRun{Name: "lint", Status: "completed", Conclusion: "success"})
	f.poll(t, w)
	p := f.ses.take()
	if len(p) != 1 || !strings.Contains(p[0], "CI failed on sha-a") || !strings.Contains(p[0], "test (failure)") || strings.Contains(p[0], "lint") {
		t.Fatalf("failure prompts = %q", p)
	}
	f.poll(t, w)
	if p := f.ses.take(); len(p) != 0 {
		t.Fatalf("duplicate wake: %q", p)
	}
	// A running turn is left alone; the change waits for the next poll.
	f.gh.AddComment("o/r", 10, prtest.Comment{Author: "me", Body: "my own reply"}) // not news
	f.ses.running["ses1"] = true
	f.gh.AddComment("o/r", 10, prtest.Comment{Author: "bob", Body: "Did you see the flaky test?"})
	if res := f.poll(t, w); res.Checked != 0 {
		t.Fatalf("polled a running thread: %+v", res)
	}
	f.ses.running["ses1"] = false
	f.poll(t, w)
	p = f.ses.take()
	if len(p) != 1 || !strings.Contains(p[0], "1 new comment") || !strings.Contains(p[0], "@bob") || strings.Contains(p[0], "my own reply") {
		t.Fatalf("comment prompts = %q", p)
	}
	// A push: pending is not news, passing is.
	f.gh.Update("o/r", 10, func(p *prtest.PR) { p.HeadSHA = "sha-b" })
	f.gh.SetChecks("sha-b", prtest.CheckRun{Name: "test", Status: "queued"})
	f.poll(t, w)
	if p := f.ses.take(); len(p) != 0 {
		t.Fatalf("woke for pending checks: %q", p)
	}
	f.gh.SetChecks("sha-b", prtest.CheckRun{Name: "test", Status: "completed", Conclusion: "success"})
	f.poll(t, w)
	if p := f.ses.take(); len(p) != 1 || !strings.Contains(p[0], "All checks passed on sha-b") {
		t.Fatalf("pass prompts = %q", p)
	}
	// Merge conflicts.
	f.gh.Update("o/r", 10, func(p *prtest.PR) { p.MergeableState = "dirty" })
	f.poll(t, w)
	if p := f.ses.take(); len(p) != 1 || !strings.Contains(p[0], "merge conflicts with main") {
		t.Fatalf("conflict prompts = %q", p)
	}
	got, _ := f.st.GetSessionPullRequest(t.Context(), link.ID)
	if got.WakeCount != 4 || got.Watching != 1 || got.ChecksState != pr.ChecksSuccess || got.HeadSha != "sha-b" {
		t.Fatalf("after wakes: %+v", got)
	}
	// A busy session (a turn started meanwhile) does not lose the change.
	f.gh.AddComment("o/r", 10, prtest.Comment{Author: "carol", Body: "ping"})
	f.ses.busy = true
	f.poll(t, w)
	f.ses.busy = false
	f.poll(t, w)
	if p := f.ses.take(); len(p) != 1 || !strings.Contains(p[0], "@carol") {
		t.Fatalf("after busy: %q", p)
	}
}

func TestWatcherSettlesMergedThreads(t *testing.T) {
	f := newFixture(t)
	w := &pr.Watcher{Service: f.svc}
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 11, Title: "Merge me", HeadRef: "feat", HeadSHA: "s"})
	link := f.watched(t, 11)
	f.gh.Update("o/r", 11, func(p *prtest.PR) { p.Merged, p.State = true, "closed" })
	if res := f.poll(t, w); res.Stopped != 1 || res.Settled != 1 || res.Woken != 0 {
		t.Fatalf("merged poll = %+v", res)
	}
	got, _ := f.st.GetSessionPullRequest(t.Context(), link.ID)
	sess, _ := f.st.GetAgentSession(t.Context(), "ses1")
	if got.Watching != 0 || got.State != pr.StateMerged || !sess.SettledAt.Valid {
		t.Fatalf("after merge: %+v settled=%v", got, sess.SettledAt)
	}
	if len(f.ses.notices) != 1 || !strings.Contains(f.ses.notices[0], "was merged") {
		t.Fatalf("notices = %q", f.ses.notices)
	}
	if res := f.poll(t, w); res.Checked != 0 {
		t.Fatalf("still polled: %+v", res)
	}
}

func TestWatcherStopsAfterCommentWakesAndFailures(t *testing.T) {
	f := newFixture(t)
	w := &pr.Watcher{Service: f.svc}
	f.gh.Add(prtest.PR{Repo: "o/r", Number: 12, Title: "Chatty", HeadRef: "feat", HeadSHA: "s"})
	link := f.watched(t, 12)
	for i := range pr.MaxCommentWakes {
		f.gh.AddComment("o/r", 12, prtest.Comment{Author: "bot", Body: "comment"})
		f.poll(t, w)
		p := f.ses.take()
		if len(p) != 1 {
			t.Fatalf("wake %d: %q", i, p)
		}
		if last := i == pr.MaxCommentWakes-1; last != strings.Contains(p[0], "Watching stopped") {
			t.Fatalf("wake %d: %q", i, p[0])
		}
	}
	got, _ := f.st.GetSessionPullRequest(t.Context(), link.ID)
	if got.Watching != 0 || got.CommentStreak != pr.MaxCommentWakes || got.WakeCount != pr.MaxCommentWakes {
		t.Fatalf("after comment wakes: %+v", got)
	}

	// Read failures: watching stops after MaxReadFailures in a row.
	again, err := f.svc.SetWatching(t.Context(), f.sess, "u1", "u1", link.ID, true)
	if err != nil || again.CommentStreak != 0 {
		t.Fatalf("watch again: %+v %v", again, err)
	}
	f.gh.Fail = 502
	for i := 1; i <= pr.MaxReadFailures; i++ {
		res := f.poll(t, w)
		got, _ = f.st.GetSessionPullRequest(t.Context(), link.ID)
		if res.Failed != 1 || got.FailCount != int64(i) || (got.Watching == 1) != (i < pr.MaxReadFailures) {
			t.Fatalf("failure %d: %+v %+v", i, res, got)
		}
	}
	if n := f.ses.notices; len(n) != 1 || !strings.Contains(n[0], "could not be read 8 times") {
		t.Fatalf("notices = %q", n)
	}
	// A success in between resets the count.
	f.gh.Fail = 0
	again, _ = f.svc.SetWatching(t.Context(), f.sess, "u1", "u1", link.ID, true)
	f.gh.Fail = 502
	f.poll(t, w)
	f.gh.Fail = 0
	f.poll(t, w)
	if got, _ = f.st.GetSessionPullRequest(t.Context(), again.ID); got.FailCount != 0 || got.Watching != 1 {
		t.Fatalf("after recovery: %+v", got)
	}
}
