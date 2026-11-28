package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/usage"
)

func TestUsageEndpointsAndAdminPricing(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) {
		c.Auth.Admins = []string{"alice@example.com"}
	})
	admin := e.login("alice-sub", "Alice", "alice@example.com")
	regular := e.login("bob-sub", "Bob", "bob@example.com")

	adminUser := admin.me()
	bobUser := regular.me()

	// Record usage for both users.
	if err := e.usage.Record(t.Context(), usage.RecordInput{
		UserID: adminUser.Id, AgentID: "ag_a", SessionID: "s_a", Harness: "claude-code", Model: "claude-sonnet-4-6",
		InputTokens: 1_000_000, OutputTokens: 200_000, CacheReadTokens: 100_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.usage.Record(t.Context(), usage.RecordInput{
		UserID: bobUser.Id, AgentID: "ag_b", SessionID: "s_b", Harness: "codex", Model: "gpt-5.3-codex",
		InputTokens: 500_000, OutputTokens: 100_000,
	}); err != nil {
		t.Fatal(err)
	}

	// Regular user can view own usage, but gets 403 for org scope and PUT /usage/prices.
	var bobSum gen.UsageSummary
	expect(t, "bob usage summary", regular.do("GET", "/usage/summary?range=7d&scope=me", nil, &bobSum), 200)
	if bobSum.Totals.TurnCount != 1 || len(bobSum.ByUser) != 0 {
		t.Fatalf("bob summary = %+v", bobSum)
	}

	expect(t, "bob org scope", regular.do("GET", "/usage/summary?scope=org", nil, nil), 403)
	expect(t, "bob put price", regular.do("PUT", "/usage/prices", map[string]any{"model": "foo", "inputPerM": 1, "outputPerM": 2}, nil), 403)

	// Admin views org scope with per-user breakdown.
	var orgSum gen.UsageSummary
	expect(t, "admin org summary", admin.do("GET", "/usage/summary?range=30d&scope=org", nil, &orgSum), 200)
	if orgSum.Totals.TurnCount != 2 || len(orgSum.ByUser) != 2 || len(orgSum.Series) != 30 {
		t.Fatalf("org summary turns=%d users=%d series=%d", orgSum.Totals.TurnCount, len(orgSum.ByUser), len(orgSum.Series))
	}

	// Admin sets custom model price and sees it reflected in GET /usage/prices and summary.
	var updated gen.UsageModelPrice
	expect(t, "admin put price", admin.do("PUT", "/usage/prices", map[string]any{
		"model": "claude-sonnet-4-6", "inputPerM": 10.0, "outputPerM": 50.0, "cacheReadPerM": 1.0,
	}, &updated), 200)
	if !updated.Custom || updated.InputPerM != 10.0 {
		t.Fatalf("updated price = %+v", updated)
	}

	var prices []gen.UsageModelPrice
	expect(t, "list usage prices", admin.do("GET", "/usage/prices", nil, &prices), 200)
	foundCustom := false
	for _, p := range prices {
		if p.Model == "claude-sonnet-4-6" && p.Custom && p.InputPerM == 10.0 {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Fatal("custom price not present in GET /usage/prices")
	}
}

func TestWorkflowsAPIAndWebhookLifecycle(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice-sub", "Alice", "alice@example.com")
	bob := e.login("bob-sub", "Bob", "bob@example.com")

	// Create an agent for Alice so workflow steps can target it.
	var ag gen.Agent
	expect(t, "create agent", alice.do("POST", "/agents", gen.AgentInput{
		Name: "wf-worker", Harness: ptr("fake"),
	}, &ag), 201)
	e.sandboxes.Wait()

	// Create a multi-stage workflow with shell + ask + agent + webhook.
	createBody := map[string]any{
		"name":        "Deploy pipeline",
		"description": "Verify, approve, and announce",
		"enabled":     true,
		"definition": map[string]any{
			"webhookEnabled":   true,
			"concurrencyLimit": 2,
			"steps": []map[string]any{
				{"name": "check", "kind": "shell", "agentId": ag.Id, "command": "echo build-{{input.tag}}"},
				{"name": "gate", "kind": "ask", "askTitle": "Approve {{steps.check.output}}?", "askChoices": []string{"Approve", "Reject"}, "continueOnChoices": []string{"Approve"}},
				{"name": "announce", "kind": "agent", "agentId": ag.Id, "prompt": "Shipped {{steps.check.output}} ({{steps.gate.output}})"},
			},
		},
	}
	var wf gen.Workflow
	expect(t, "create workflow", alice.do("POST", "/workflows", createBody, &wf), 201)
	if !wf.HasWebhook || wf.WebhookUrl == nil || !strings.Contains(*wf.WebhookUrl, "/api/v1/workflow-hooks/") {
		t.Fatalf("expected webhookUrl on create, got %+v", wf)
	}

	// Bob cannot see or start Alice's workflow.
	expect(t, "bob start alice workflow", bob.do("POST", "/workflows/"+wf.Id+"/run", nil, nil), 404)

	// Trigger via public webhook URL (no auth cookie).
	whReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, *wf.WebhookUrl, bytes.NewBufferString(`{"tag":"v1.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	whReq.Header.Set("Content-Type", "application/json")
	whResp, err := http.DefaultClient.Do(whReq)
	if err != nil {
		t.Fatal(err)
	}
	defer whResp.Body.Close()
	if whResp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook trigger status=%d, want 202", whResp.StatusCode)
	}
	var whPayload struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(whResp.Body).Decode(&whPayload); err != nil {
		t.Fatal(err)
	}

	// Wait until run pauses at the human approval gate (needs_input).
	var run gen.WorkflowRun
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if alice.do("GET", "/workflow-runs/"+whPayload.RunID, nil, &run) == 200 {
			if run.Status == gen.WorkflowRunStatusNeedsInput || run.Status == gen.WorkflowRunStatusFailed {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if run.Status != gen.WorkflowRunStatusNeedsInput {
		t.Fatalf("expected needs_input, got %s (err=%v)", run.Status, run.Error)
	}

	// Filter GET /workflow-runs?status=needs_input returns this run.
	var needs []gen.WorkflowRun
	expect(t, "list needs_input runs", alice.do("GET", "/workflow-runs?status=needs_input", nil, &needs), 200)
	if len(needs) != 1 || needs[0].Id != run.Id {
		t.Fatalf("needs_input list = %+v", needs)
	}

	// Answer the approval gate and wait for agent step to complete.
	gateStep := run.Steps[1]
	expect(t, "answer step", alice.do("POST", "/workflow-runs/"+run.Id+"/steps/"+gateStep.Id+"/answer", map[string]any{
		"choice": "Approve", "reason": "green CI",
	}, &run), 200)

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		alice.do("GET", "/workflow-runs/"+run.Id, nil, &run)
		if run.Status == gen.WorkflowRunStatusCompleted || run.Status == gen.WorkflowRunStatusFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if run.Status != gen.WorkflowRunStatusCompleted {
		t.Fatalf("expected completed run, got %s (err=%v)", run.Status, run.Error)
	}
	if run.Steps[2].SessionId == nil || *run.Steps[2].SessionId == "" {
		t.Fatalf("expected agent step sessionId to be populated: %+v", run.Steps[2])
	}

	// Delete workflow.
	expect(t, "delete workflow", alice.do("DELETE", "/workflows/"+wf.Id, nil, nil), 204)
}
