package store

import (
	"database/sql"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestUsageRecordsAndModelPrices(t *testing.T) {
	s, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()

	for _, id := range []string{"u1", "u2"} {
		if err := s.CreateUser(ctx, db.CreateUserParams{
			ID: id, Name: id, Email: id + "@example.com", CreatedAt: 100, UpdatedAt: 100,
		}); err != nil {
			t.Fatal(err)
		}
	}

	recs := []db.CreateUsageRecordParams{
		{ID: "usg_1", UserID: "u1", AgentID: "ag_1", SessionID: "ses_1", Harness: "claude-code", Model: "claude-sonnet-4-6", InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200, CacheWriteTokens: 100, CostUsd: 0.012, CreatedAt: 1000},
		{ID: "usg_2", UserID: "u1", AgentID: "ag_1", SessionID: "ses_1", Harness: "codex", Model: "gpt-5.3-codex", InputTokens: 2000, OutputTokens: 1000, CostUsd: 0.02, CreatedAt: 2000},
		{ID: "usg_3", UserID: "u2", AgentID: "ag_2", SessionID: "ses_2", Harness: "antigravity", Model: "gemini-2.5-pro", InputTokens: 500, OutputTokens: 300, CostUsd: 0.004, CreatedAt: 3000},
	}
	for _, r := range recs {
		if err := s.CreateUsageRecord(ctx, r); err != nil {
			t.Fatalf("create usage %s: %v", r.ID, err)
		}
	}

	all, err := s.ListUsageRecordsSince(ctx, 1500)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListUsageRecordsSince(1500) = %d, %v; want 2", len(all), err)
	}
	u1, err := s.ListUserUsageRecordsSince(ctx, db.ListUserUsageRecordsSinceParams{UserID: "u1", CreatedAt: 500})
	if err != nil || len(u1) != 2 {
		t.Fatalf("ListUserUsageRecordsSince(u1) = %d, %v; want 2", len(u1), err)
	}

	// Custom model prices upsert, get, list, delete.
	if err := s.UpsertModelPrice(ctx, db.UpsertModelPriceParams{
		Model: "my-custom-model", InputPerM: 1.5, OutputPerM: 6.0, CacheReadPerM: 0.15, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertModelPrice(ctx, db.UpsertModelPriceParams{
		Model: "my-custom-model", InputPerM: 2.0, OutputPerM: 8.0, CacheReadPerM: 0.2, UpdatedAt: 200,
	}); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetModelPrice(ctx, "my-custom-model")
	if err != nil || p.InputPerM != 2.0 || p.OutputPerM != 8.0 || p.UpdatedAt != 200 {
		t.Fatalf("GetModelPrice = %+v, %v", p, err)
	}
	prices, err := s.ListModelPrices(ctx)
	if err != nil || len(prices) != 1 {
		t.Fatalf("ListModelPrices = %d, %v", len(prices), err)
	}
	n, err := s.DeleteModelPrice(ctx, "my-custom-model")
	if err != nil || n != 1 {
		t.Fatalf("DeleteModelPrice = %d, %v", n, err)
	}
}

func TestWorkflowsRunsAndStepsPersistence(t *testing.T) {
	s, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()

	if err := s.CreateUser(ctx, db.CreateUserParams{
		ID: "u1", Name: "Alice", Email: "alice@example.com", CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.CreateWorkflow(ctx, db.CreateWorkflowParams{
		ID: "wfl_1", OwnerID: "u1", Name: "Release", Description: "Ship it",
		DefinitionJson: `{"steps":[]}`, Enabled: 1, CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}

	wf, err := s.GetWorkflow(ctx, "wfl_1")
	if err != nil || wf.Name != "Release" {
		t.Fatalf("GetWorkflow = %+v, %v", wf, err)
	}
	enabled, err := s.ListEnabledWorkflows(ctx)
	if err != nil || len(enabled) != 1 {
		t.Fatalf("ListEnabledWorkflows = %d, %v", len(enabled), err)
	}

	// Create three runs: running, needs_input, queued.
	for _, r := range []db.CreateWorkflowRunParams{
		{ID: "wfr_1", WorkflowID: "wfl_1", OwnerID: "u1", Status: "running", TriggerKind: "manual", InputJson: "{}", OutputJson: "{}", StartedAt: sql.NullInt64{Int64: 101, Valid: true}, CreatedAt: 101, UpdatedAt: 101},
		{ID: "wfr_2", WorkflowID: "wfl_1", OwnerID: "u1", Status: "needs_input", TriggerKind: "schedule", InputJson: "{}", OutputJson: "{}", StartedAt: sql.NullInt64{Int64: 102, Valid: true}, CreatedAt: 102, UpdatedAt: 102},
		{ID: "wfr_3", WorkflowID: "wfl_1", OwnerID: "u1", Status: "queued", TriggerKind: "webhook", InputJson: "{}", OutputJson: "{}", CreatedAt: 103, UpdatedAt: 103},
	} {
		if err := s.CreateWorkflowRun(ctx, r); err != nil {
			t.Fatalf("CreateWorkflowRun %s: %v", r.ID, err)
		}
	}

	activeCnt, err := s.CountActiveRunsForWorkflow(ctx, "wfl_1")
	if err != nil || activeCnt != 2 {
		t.Fatalf("CountActiveRunsForWorkflow = %d, %v; want 2", activeCnt, err)
	}
	queued, err := s.ListQueuedRunsForWorkflow(ctx, "wfl_1")
	if err != nil || len(queued) != 1 || queued[0].ID != "wfr_3" {
		t.Fatalf("ListQueuedRunsForWorkflow = %+v, %v", queued, err)
	}
	activeRuns, err := s.ListActiveWorkflowRuns(ctx)
	if err != nil || len(activeRuns) != 2 {
		t.Fatalf("ListActiveWorkflowRuns = %d, %v; want 2 (running + queued)", len(activeRuns), err)
	}

	// Steps ordering and unique (run_id, step_index).
	for i, name := range []string{"step_a", "step_b"} {
		if err := s.CreateWorkflowStep(ctx, db.CreateWorkflowStepParams{
			ID: "wfs_" + name, RunID: "wfr_1", StepIndex: int64(i), Name: name,
			Kind: "agent", Status: "pending", AskChoicesJson: "[]",
		}); err != nil {
			t.Fatal(err)
		}
	}
	steps, err := s.ListWorkflowSteps(ctx, "wfr_1")
	if err != nil || len(steps) != 2 || steps[0].Name != "step_a" || steps[1].Name != "step_b" {
		t.Fatalf("ListWorkflowSteps = %+v, %v", steps, err)
	}

	// Deleting workflow cascades runs and steps.
	del, err := s.DeleteWorkflow(ctx, "wfl_1")
	if err != nil || del != 1 {
		t.Fatalf("DeleteWorkflow = %d, %v", del, err)
	}
	remainingSteps, err := s.ListWorkflowSteps(ctx, "wfr_1")
	if err != nil || len(remainingSteps) != 0 {
		t.Fatalf("expected cascade delete of steps, got %d, %v", len(remainingSteps), err)
	}
}
