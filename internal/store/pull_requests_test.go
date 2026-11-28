package store

import (
	"database/sql"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestPullRequestQueries(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.CreateUser(ctx, db.CreateUserParams{ID: "u1", Name: "Alice", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ag1", OwnerID: "u1", Name: "alpha", Image: "base", Harness: "fake", Cpus: 1, MemoryMb: 512, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ses1", "ses2"} {
		if err := s.CreateAgentSession(ctx, db.CreateAgentSessionParams{
			ID: id, AgentID: "ag1", Title: "Thread " + id, CreatedBy: "u1", CreatedAt: 10, UpdatedAt: 10,
		}); err != nil {
			t.Fatal(err)
		}
	}

	link := db.UpsertSessionPullRequestParams{
		ID: "pr1", SessionID: "ses1", Host: "github.com", Repo: "o/r", Number: 7, Url: "https://github.com/o/r/pull/7",
		Title: "First", State: "open", HeadSha: "aaa", ChecksState: "pending", CreatedAt: 20, UpdatedAt: 20,
	}
	if err := s.UpsertSessionPullRequest(ctx, link); err != nil {
		t.Fatal(err)
	}
	// Linking again refreshes it and keeps the id.
	link.ID, link.Title, link.HeadSha, link.UpdatedAt = "pr-dup", "Renamed", "bbb", 21
	if err := s.UpsertSessionPullRequest(ctx, link); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSessionPullRequestByKey(ctx, db.GetSessionPullRequestByKeyParams{SessionID: "ses1", Host: "github.com", Repo: "o/r", Number: 7})
	if err != nil || got.ID != "pr1" || got.Title != "Renamed" || got.HeadSha != "bbb" || got.Watching != 0 || got.LastCheckedAt.Valid {
		t.Fatalf("after relink: %+v %v", got, err)
	}
	// The same pull request in another session.
	if err := s.UpsertSessionPullRequest(ctx, db.UpsertSessionPullRequestParams{
		ID: "pr2", SessionID: "ses2", Host: "github.com", Repo: "o/r", Number: 7, Url: link.Url, State: "open", CreatedAt: 22, UpdatedAt: 22,
	}); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ListPullRequestSessions(ctx, db.ListPullRequestSessionsParams{Host: "github.com", Repo: "o/r", Number: 7}); err != nil || len(rows) != 2 {
		t.Fatalf("pull request sessions: %+v %v", rows, err)
	}

	// Watching.
	if n, err := s.SetPullRequestWatching(ctx, db.SetPullRequestWatchingParams{
		Watching: 1, LastFingerprint: "fp0", LastCheckedAt: sql.NullInt64{Int64: 30, Valid: true}, UpdatedAt: 30, ID: "pr1",
	}); err != nil || n != 1 {
		t.Fatalf("watch: %d %v", n, err)
	}
	watched, err := s.ListWatchedPullRequests(ctx)
	if err != nil || len(watched) != 1 || watched[0].SessionPullRequest.ID != "pr1" || watched[0].OwnerID != "u1" ||
		watched[0].AgentID != "ag1" || watched[0].SessionTitle != "Thread ses1" {
		t.Fatalf("watched: %+v %v", watched, err)
	}
	poll := db.RecordPullRequestPollParams{
		Title: "Renamed", State: "open", HeadSha: "ccc", ChecksState: "failure", Watching: 1, WakeCount: 1, CommentStreak: 0,
		FailCount: 0, LastCheckedAt: sql.NullInt64{Int64: 40, Valid: true}, LastFingerprint: "fp1", UpdatedAt: 40, ID: "pr1",
	}
	if n, err := s.RecordPullRequestPoll(ctx, poll); err != nil || n != 1 {
		t.Fatalf("poll: %d %v", n, err)
	}
	got, _ = s.GetSessionPullRequest(ctx, "pr1")
	if got.WakeCount != 1 || got.LastFingerprint != "fp1" || got.ChecksState != "failure" || got.LastCheckedAt.Int64 != 40 {
		t.Fatalf("after poll: %+v", got)
	}
	// Settled sessions are not watched.
	if _, err := s.SettleSession(ctx, db.SettleSessionParams{SettledAt: sql.NullInt64{Int64: 50, Valid: true}, SettledBy: "u1", ID: "ses1"}); err != nil {
		t.Fatal(err)
	}
	if watched, _ := s.ListWatchedPullRequests(ctx); len(watched) != 0 {
		t.Fatalf("settled session still watched: %+v", watched)
	}
	// A poll after the user stopped watching changes nothing.
	if _, err := s.SetPullRequestWatching(ctx, db.SetPullRequestWatchingParams{Watching: 0, UpdatedAt: 60, ID: "pr1"}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RecordPullRequestPoll(ctx, poll); err != nil || n != 0 {
		t.Fatalf("poll after unwatch: %d %v", n, err)
	}

	if n, err := s.DeleteSessionPullRequest(ctx, db.DeleteSessionPullRequestParams{ID: "pr1", SessionID: "ses2"}); err != nil || n != 0 {
		t.Fatalf("delete from another session: %d %v", n, err)
	}
	if n, err := s.DeleteSessionPullRequest(ctx, db.DeleteSessionPullRequestParams{ID: "pr1", SessionID: "ses1"}); err != nil || n != 1 {
		t.Fatalf("delete: %d %v", n, err)
	}
	// Deleting a session drops its links.
	if err := s.DeleteAgentSession(ctx, "ses2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionPullRequest(ctx, "pr2"); err == nil {
		t.Fatal("link of a deleted session survived")
	}
}
