package store

import (
	"database/sql"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestThreadSharingQueries(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for _, u := range []db.CreateUserParams{
		{ID: "u1", Name: "Alice", Email: "alice@example.com", EmailVerified: 1, CreatedAt: 1, UpdatedAt: 1},
		{ID: "u2", Name: "Bob", Email: "bob@example.com", EmailVerified: 1, CreatedAt: 1, UpdatedAt: 1},
		{ID: "u3", Name: "Carol", Email: "carol@example.com", EmailVerified: 1, CreatedAt: 1, UpdatedAt: 1},
	} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ag1", OwnerID: "u1", Name: "alpha", Image: "base", Harness: "fake", Cpus: 1, MemoryMb: 512, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgentSession(ctx, db.CreateAgentSessionParams{
		ID: "ses1", AgentID: "ag1", Title: "Shared thread", CreatedBy: "u1", CreatedAt: 10, UpdatedAt: 20,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgentSession(ctx, db.CreateAgentSessionParams{
		ID: "ses2", AgentID: "ag1", Title: "Private thread", CreatedBy: "u1", CreatedAt: 11, UpdatedAt: 21,
	}); err != nil {
		t.Fatal(err)
	}

	// Session shares: upsert viewer, upgrade to collaborator, query & delete.
	if err := s.UpsertSessionShare(ctx, db.UpsertSessionShareParams{
		SessionID: "ses1", UserID: "u2", Role: "viewer", CreatedBy: "u1", CreatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	sh, err := s.GetSessionShare(ctx, db.GetSessionShareParams{SessionID: "ses1", UserID: "u2"})
	if err != nil || sh.Role != "viewer" {
		t.Fatalf("get share: %+v err=%v", sh, err)
	}
	if err := s.UpsertSessionShare(ctx, db.UpsertSessionShareParams{
		SessionID: "ses1", UserID: "u2", Role: "collaborator", CreatedBy: "u1", CreatedAt: 101,
	}); err != nil {
		t.Fatal(err)
	}
	sh, err = s.GetSessionShare(ctx, db.GetSessionShareParams{SessionID: "ses1", UserID: "u2"})
	if err != nil || sh.Role != "collaborator" {
		t.Fatalf("upgraded share: %+v err=%v", sh, err)
	}
	if err := s.UpsertSessionShare(ctx, db.UpsertSessionShareParams{
		SessionID: "ses1", UserID: "u3", Role: "viewer", CreatedBy: "u1", CreatedAt: 102,
	}); err != nil {
		t.Fatal(err)
	}
	shares, err := s.ListSessionShares(ctx, "ses1")
	if err != nil || len(shares) != 2 || shares[0].User.ID != "u2" || shares[1].User.ID != "u3" {
		t.Fatalf("list shares: %+v err=%v", shares, err)
	}
	sharedForBob, err := s.ListUserSharedSessions(ctx, "u2")
	if err != nil || len(sharedForBob) != 1 || sharedForBob[0].AgentSession.ID != "ses1" || sharedForBob[0].ShareRole != "collaborator" || sharedForBob[0].AgentName != "alpha" {
		t.Fatalf("list user shared sessions: %+v err=%v", sharedForBob, err)
	}
	forAgent, err := s.ListUserSharedSessionsForAgent(ctx, db.ListUserSharedSessionsForAgentParams{AgentID: "ag1", UserID: "u2"})
	if err != nil || len(forAgent) != 1 || forAgent[0].AgentSession.ID != "ses1" {
		t.Fatalf("list user shared sessions for agent: %+v err=%v", forAgent, err)
	}
	cnt, err := s.HasAnySessionShareOnAgent(ctx, db.HasAnySessionShareOnAgentParams{AgentID: "ag1", UserID: "u2"})
	if err != nil || cnt != 1 {
		t.Fatalf("has any session share on agent: %d err=%v", cnt, err)
	}
	if n, err := s.DeleteSessionShare(ctx, db.DeleteSessionShareParams{SessionID: "ses1", UserID: "u3"}); err != nil || n != 1 {
		t.Fatalf("delete share: n=%d err=%v", n, err)
	}

	// Guest links & guest requests.
	if err := s.CreateSessionGuestLink(ctx, db.CreateSessionGuestLinkParams{
		ID: "gl1", SessionID: "ses1", TokenHash: "hash-abc", CreatedBy: "u1", ExpiresAt: 2000, CreatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	gl, err := s.GetSessionGuestLinkByTokenHash(ctx, "hash-abc")
	if err != nil || gl.ID != "gl1" || gl.RevokedAt.Valid {
		t.Fatalf("get guest link by hash: %+v err=%v", gl, err)
	}
	links, err := s.ListSessionGuestLinks(ctx, "ses1")
	if err != nil || len(links) != 1 {
		t.Fatalf("list guest links: %+v err=%v", links, err)
	}
	if err := s.CreateSessionGuestRequest(ctx, db.CreateSessionGuestRequestParams{
		ID: "greq1", SessionID: "ses1", GuestLinkID: "gl1", GuestName: "External", Text: "Please check tests", CreatedAt: 1100,
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPendingSessionGuestRequests(ctx, "ses1")
	if err != nil || len(pending) != 1 || pending[0].GuestName != "External" {
		t.Fatalf("list pending guest requests: %+v err=%v", pending, err)
	}
	n, err := s.ResolveSessionGuestRequest(ctx, db.ResolveSessionGuestRequestParams{
		Status: "approved", ResolvedBy: sql.NullString{String: "u1", Valid: true}, ResolvedAt: sql.NullInt64{Int64: 1200, Valid: true},
		ID: "greq1", SessionID: "ses1",
	})
	if err != nil || n != 1 {
		t.Fatalf("resolve guest request: n=%d err=%v", n, err)
	}
	// Resolving again is a no-op because status is no longer pending.
	n, err = s.ResolveSessionGuestRequest(ctx, db.ResolveSessionGuestRequestParams{
		Status: "dismissed", ResolvedBy: sql.NullString{String: "u1", Valid: true}, ResolvedAt: sql.NullInt64{Int64: 1201, Valid: true},
		ID: "greq1", SessionID: "ses1",
	})
	if err != nil || n != 0 {
		t.Fatalf("double resolve should be 0, got n=%d err=%v", n, err)
	}
	if n, err := s.RevokeSessionGuestLink(ctx, db.RevokeSessionGuestLinkParams{
		RevokedAt: sql.NullInt64{Int64: 1300, Valid: true}, ID: "gl1", SessionID: "ses1",
	}); err != nil || n != 1 {
		t.Fatalf("revoke guest link: n=%d err=%v", n, err)
	}

	// Collaborative diff review comments.
	if err := s.CreateSessionReviewComment(ctx, db.CreateSessionReviewCommentParams{
		ID: "rc1", SessionID: "ses1", UserID: "u2", UserName: "Bob",
		FilePath: "src/app.ts", StartLine: 10, EndLine: 14, Side: "new", Body: "Extract helper", CreatedAt: 1400,
	}); err != nil {
		t.Fatal(err)
	}
	comments, err := s.ListSessionReviewComments(ctx, "ses1")
	if err != nil || len(comments) != 1 || comments[0].FilePath != "src/app.ts" || comments[0].EndLine != 14 {
		t.Fatalf("list review comments: %+v err=%v", comments, err)
	}
	if n, err := s.ResolveSessionReviewComment(ctx, db.ResolveSessionReviewCommentParams{
		ResolvedAt: sql.NullInt64{Int64: 1500, Valid: true}, ID: "rc1", SessionID: "ses1",
	}); err != nil || n != 1 {
		t.Fatalf("resolve review comment: n=%d err=%v", n, err)
	}
	if n, err := s.DeleteSessionReviewComment(ctx, db.DeleteSessionReviewCommentParams{ID: "rc1", SessionID: "ses1"}); err != nil || n != 1 {
		t.Fatalf("delete review comment: n=%d err=%v", n, err)
	}
}
