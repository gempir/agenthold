package usage_test

import (
	"math"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/usage"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-6
}

func TestPricingLookupAndEstimate(t *testing.T) {
	overrides := map[string]usage.Price{
		"claude-sonnet-4-6": {Model: "claude-sonnet-4-6", InputPerM: 4.0, OutputPerM: 20.0, CacheReadPerM: 0.4, Custom: true},
		"custom-local-llm":  {Model: "custom-local-llm", InputPerM: 0.5, OutputPerM: 1.0, CacheReadPerM: 0.05, Custom: true},
	}

	// Override wins over built-in default.
	p, ok := usage.LookupPrice("claude-sonnet-4-6", overrides)
	if !ok || !p.Custom || p.InputPerM != 4.0 {
		t.Fatalf("expected custom override, got %+v ok=%v", p, ok)
	}

	// Prefix match with version suffix and provider prefix.
	p, ok = usage.LookupPrice("anthropic/claude-opus-4-5-20251101", nil)
	if !ok || p.Model != "claude-opus-4-5" || p.InputPerM != 5.0 {
		t.Fatalf("expected prefix match claude-opus-4-5, got %+v ok=%v", p, ok)
	}

	// Codex & Gemini defaults.
	if p, ok = usage.LookupPrice("gpt-5.3-codex", nil); !ok || p.OutputPerM != 14.0 {
		t.Fatalf("expected gpt-5.3-codex, got %+v ok=%v", p, ok)
	}
	if p, ok = usage.LookupPrice("gemini-2.5-pro-preview", nil); !ok || p.Model != "gemini-2.5-pro" {
		t.Fatalf("expected gemini-2.5-pro, got %+v ok=%v", p, ok)
	}

	// Unknown model is unpriced.
	if _, ok = usage.LookupPrice("completely-unknown-xyz", nil); ok {
		t.Fatal("expected unknown model to be unpriced")
	}

	// EstimateCost math: 1M input ($3) + 500k output ($7.50) + 1M cache read ($0.30) + 200k cache write ($0.75) = $11.55.
	cost := usage.EstimateCost(1_000_000, 500_000, 1_000_000, 200_000, usage.Price{
		InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30,
	})
	if !almostEqual(cost, 11.55) {
		t.Fatalf("EstimateCost = %.6f, want 11.55", cost)
	}
}

func TestServiceRecordSummaryAndCustomPricing(t *testing.T) {
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()

	for _, u := range []db.CreateUserParams{
		{ID: "u1", Name: "Alice", Email: "alice@example.com", CreatedAt: 1, UpdatedAt: 1},
		{ID: "u2", Name: "Bob", Email: "bob@example.com", CreatedAt: 1, UpdatedAt: 1},
	} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	cur := time.Date(2026, 11, 20, 14, 30, 0, 0, time.UTC)
	svc := &usage.Service{Store: st, Now: func() time.Time { return cur }}

	// Day 1 (3 days ago): Alice uses claude-sonnet-4-6.
	cur = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	if err := svc.Record(ctx, usage.RecordInput{
		UserID: "u1", AgentID: "a1", SessionID: "s1", Harness: "claude-code", Model: "claude-sonnet-4-6",
		InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 500_000,
	}); err != nil {
		t.Fatal(err)
	}

	// Today: Alice uses an unpriced custom model; Bob uses gpt-5.3-codex.
	cur = time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	if err := svc.Record(ctx, usage.RecordInput{
		UserID: "u1", AgentID: "a1", SessionID: "s2", Harness: "opencode", Model: "local-llama-99",
		InputTokens: 500_000, OutputTokens: 500_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Record(ctx, usage.RecordInput{
		UserID: "u2", AgentID: "a2", SessionID: "s3", Harness: "codex", Model: "gpt-5.3-codex",
		InputTokens: 2_000_000, OutputTokens: 100_000,
	}); err != nil {
		t.Fatal(err)
	}

	// Zero-token record is silently skipped.
	if err := svc.Record(ctx, usage.RecordInput{UserID: "u1", AgentID: "a1", SessionID: "s4", Harness: "codex"}); err != nil {
		t.Fatal(err)
	}

	// Personal summary (7d) sees only Alice's 2 turns.
	meSum, err := svc.Summary(ctx, "u1", usage.Range7d, usage.ScopeMe)
	if err != nil {
		t.Fatal(err)
	}
	if len(meSum.Series) != 7 {
		t.Fatalf("7d series len = %d, want 7", len(meSum.Series))
	}
	if meSum.Totals.TurnCount != 2 || meSum.Totals.TotalTokens != 2_600_000 {
		t.Fatalf("me totals = %+v", meSum.Totals)
	}
	// Expected cost for Alice: 1M*3 + 0.1M*15 + 0.5M*0.3 = 3 + 1.5 + 0.15 = 4.65 (local-llama-99 unpriced = 0).
	if !almostEqual(meSum.Totals.TotalCostUSD, 4.65) {
		t.Fatalf("me cost = %.4f, want 4.65", meSum.Totals.TotalCostUSD)
	}
	if len(meSum.ByUser) != 0 {
		t.Fatalf("me scope should not include ByUser, got %d", len(meSum.ByUser))
	}

	// Org summary (7d) includes both Alice & Bob and populates ByUser.
	orgSum, err := svc.Summary(ctx, "u1", usage.Range7d, usage.ScopeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if orgSum.Totals.TurnCount != 3 || len(orgSum.ByUser) != 2 {
		t.Fatalf("org summary turns=%d users=%d", orgSum.Totals.TurnCount, len(orgSum.ByUser))
	}
	// Bob's cost: 2M*1.75 + 0.1M*14 = 3.5 + 1.4 = 4.90 -> Bob ranks #1 ahead of Alice ($4.65).
	if orgSum.ByUser[0].UserID != "u2" || !almostEqual(orgSum.ByUser[0].CostUSD, 4.90) {
		t.Fatalf("top org user = %+v", orgSum.ByUser[0])
	}

	// Admin sets custom price for local-llama-99 ($2 in, $4 out -> 0.5M*2 + 0.5M*4 = $3.00).
	saved, err := svc.PutPrice(ctx, "local-llama-99", 2.0, 4.0, 0.0, false)
	if err != nil || !saved.Custom {
		t.Fatalf("PutPrice = %+v, %v", saved, err)
	}
	meSum2, err := svc.Summary(ctx, "u1", usage.Range7d, usage.ScopeMe)
	if err != nil {
		t.Fatal(err)
	}
	if !almostEqual(meSum2.Totals.TotalCostUSD, 7.65) {
		t.Fatalf("after custom price, me cost = %.4f, want 7.65", meSum2.Totals.TotalCostUSD)
	}

	// 24h range returns 24 hourly buckets and excludes the 3-day-old turn.
	h24, err := svc.Summary(ctx, "u1", usage.Range24h, usage.ScopeMe)
	if err != nil {
		t.Fatal(err)
	}
	if len(h24.Series) != 24 || h24.Totals.TurnCount != 1 {
		t.Fatalf("24h summary len=%d turns=%d", len(h24.Series), h24.Totals.TurnCount)
	}

	// Reset custom price removes override.
	resetP, err := svc.PutPrice(ctx, "local-llama-99", 0, 0, 0, true)
	if err != nil || resetP.Custom {
		t.Fatalf("reset price = %+v, %v", resetP, err)
	}
}
