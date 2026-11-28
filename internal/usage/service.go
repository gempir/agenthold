// Package usage records per-turn token/cost usage and computes analytics
// summaries across time ranges, models, harnesses, and users.
package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Supported time ranges and scopes.
const (
	Range24h = "24h"
	Range7d  = "7d"
	Range30d = "30d"
	Range90d = "90d"

	ScopeMe  = "me"
	ScopeOrg = "org"
)

// Validation errors.
var (
	ErrInvalidRange = errors.New("range must be 24h, 7d, 30d, or 90d")
	ErrInvalidScope = errors.New("scope must be me or org")
	ErrInvalidPrice = errors.New("invalid model price")
)

// Service persists usage records and serves analytics queries.
type Service struct {
	Store *store.Store
	Now   func() time.Time
	Log   *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// RecordInput is one turn's token usage to record.
type RecordInput struct {
	UserID           string
	AgentID          string
	SessionID        string
	Harness          string
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReportedCostUSD  float64
}

// Record stores a usage_records row. If ReportedCostUSD is 0, it estimates cost
// from admin model_prices overrides and the built-in default pricing table.
func (s *Service) Record(ctx context.Context, in RecordInput) error {
	if s == nil || s.Store == nil {
		return nil
	}
	if in.UserID == "" {
		return nil
	}
	total := in.InputTokens + in.OutputTokens + in.CacheReadTokens + in.CacheWriteTokens
	if total <= 0 && in.ReportedCostUSD <= 0 {
		return nil
	}
	costUSD := in.ReportedCostUSD
	if costUSD <= 0 && in.Model != "" {
		overrides, err := s.overrideMap(ctx)
		if err != nil {
			s.log().Warn("usage: load model prices", "err", err)
		}
		if p, ok := LookupPrice(in.Model, overrides); ok {
			costUSD = EstimateCost(in.InputTokens, in.OutputTokens, in.CacheReadTokens, in.CacheWriteTokens, p)
		}
	}
	return s.Store.CreateUsageRecord(ctx, db.CreateUsageRecordParams{
		ID:               id.New("usg"),
		UserID:           in.UserID,
		AgentID:          in.AgentID,
		SessionID:        in.SessionID,
		Harness:          in.Harness,
		Model:            strings.TrimSpace(in.Model),
		InputTokens:      max(0, in.InputTokens),
		OutputTokens:     max(0, in.OutputTokens),
		CacheReadTokens:  max(0, in.CacheReadTokens),
		CacheWriteTokens: max(0, in.CacheWriteTokens),
		CostUsd:          max(0, costUSD),
		CreatedAt:        s.now().Unix(),
	})
}

// Totals aggregates token counts, estimated cost, and turn counts.
type Totals struct {
	TotalTokens      int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TotalCostUSD     float64
	TurnCount        int64
}

// Bucket is one time bucket in a summary series.
type Bucket struct {
	Bucket           string
	TotalTokens      int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	TurnCount        int64
}

// ModelBreakdown summarizes usage for a (harness, model) pair.
type ModelBreakdown struct {
	Harness          string
	Model            string
	TotalTokens      int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	TurnCount        int64
	Priced           bool
}

// UserBreakdown summarizes usage for one user (org scope).
type UserBreakdown struct {
	UserID       string
	UserName     string
	UserEmail    string
	TotalTokens  int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	TurnCount    int64
}

// SummaryResult is the complete usage analytics response.
type SummaryResult struct {
	Range   string
	Scope   string
	Totals  Totals
	Series  []Bucket
	ByModel []ModelBreakdown
	ByUser  []UserBreakdown
}

// ParseRange validates rng (empty defaults to 7d) and returns the normalized
// name, window duration, and whether buckets are hourly.
func ParseRange(rng string) (name string, dur time.Duration, hourly bool, err error) {
	switch strings.TrimSpace(rng) {
	case "", Range7d:
		return Range7d, 7 * 24 * time.Hour, false, nil
	case Range24h:
		return Range24h, 24 * time.Hour, true, nil
	case Range30d:
		return Range30d, 30 * 24 * time.Hour, false, nil
	case Range90d:
		return Range90d, 90 * 24 * time.Hour, false, nil
	default:
		return "", 0, false, ErrInvalidRange
	}
}

// Summary returns aggregated usage metrics for the given range and scope.
func (s *Service) Summary(ctx context.Context, userID, rng, scope string) (SummaryResult, error) {
	normRange, dur, hourly, err := ParseRange(rng)
	if err != nil {
		return SummaryResult{}, err
	}
	normScope := strings.TrimSpace(scope)
	if normScope == "" {
		normScope = ScopeMe
	}
	if normScope != ScopeMe && normScope != ScopeOrg {
		return SummaryResult{}, ErrInvalidScope
	}

	now := s.now()
	var since time.Time
	var buckets []Bucket
	bucketIdx := map[string]int{}
	if hourly {
		endHour := now.Truncate(time.Hour)
		startHour := endHour.Add(-23 * time.Hour)
		since = startHour
		buckets = make([]Bucket, 0, 24)
		for i := 0; i < 24; i++ {
			t := startHour.Add(time.Duration(i) * time.Hour)
			label := t.Format("2006-01-02T15:00Z")
			bucketIdx[label] = len(buckets)
			buckets = append(buckets, Bucket{Bucket: label})
		}
	} else {
		days := int(dur / (24 * time.Hour))
		endDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		startDay := endDay.AddDate(0, 0, -(days - 1))
		since = startDay
		buckets = make([]Bucket, 0, days)
		for i := 0; i < days; i++ {
			t := startDay.AddDate(0, 0, i)
			label := t.Format("2006-01-02")
			bucketIdx[label] = len(buckets)
			buckets = append(buckets, Bucket{Bucket: label})
		}
	}

	var rows []db.UsageRecord
	if normScope == ScopeOrg {
		rows, err = s.Store.ListUsageRecordsSince(ctx, since.Unix())
	} else {
		rows, err = s.Store.ListUserUsageRecordsSince(ctx, db.ListUserUsageRecordsSinceParams{
			UserID:    userID,
			CreatedAt: since.Unix(),
		})
	}
	if err != nil {
		return SummaryResult{}, err
	}

	overrides, err := s.overrideMap(ctx)
	if err != nil {
		return SummaryResult{}, err
	}

	type modelKey struct {
		harness string
		model   string
	}
	byModelMap := map[modelKey]*ModelBreakdown{}
	byUserMap := map[string]*UserBreakdown{}
	var totals Totals

	for _, r := range rows {
		tot := r.InputTokens + r.OutputTokens + r.CacheReadTokens + r.CacheWriteTokens
		cost, priced := effectiveCost(r, overrides)

		totals.TotalTokens += tot
		totals.InputTokens += r.InputTokens
		totals.OutputTokens += r.OutputTokens
		totals.CacheReadTokens += r.CacheReadTokens
		totals.CacheWriteTokens += r.CacheWriteTokens
		totals.TotalCostUSD += cost
		totals.TurnCount++

		recTime := time.Unix(r.CreatedAt, 0).UTC()
		var label string
		if hourly {
			label = recTime.Truncate(time.Hour).Format("2006-01-02T15:00Z")
		} else {
			label = recTime.Format("2006-01-02")
		}
		if idx, ok := bucketIdx[label]; ok {
			b := &buckets[idx]
			b.TotalTokens += tot
			b.InputTokens += r.InputTokens
			b.OutputTokens += r.OutputTokens
			b.CacheReadTokens += r.CacheReadTokens
			b.CacheWriteTokens += r.CacheWriteTokens
			b.CostUSD += cost
			b.TurnCount++
		}

		mk := modelKey{harness: r.Harness, model: r.Model}
		mb := byModelMap[mk]
		if mb == nil {
			mb = &ModelBreakdown{Harness: r.Harness, Model: r.Model, Priced: priced}
			byModelMap[mk] = mb
		}
		mb.TotalTokens += tot
		mb.InputTokens += r.InputTokens
		mb.OutputTokens += r.OutputTokens
		mb.CacheReadTokens += r.CacheReadTokens
		mb.CacheWriteTokens += r.CacheWriteTokens
		mb.CostUSD += cost
		mb.TurnCount++
		if priced {
			mb.Priced = true
		}

		if normScope == ScopeOrg {
			ub := byUserMap[r.UserID]
			if ub == nil {
				ub = &UserBreakdown{UserID: r.UserID}
				byUserMap[r.UserID] = ub
			}
			ub.TotalTokens += tot
			ub.InputTokens += r.InputTokens
			ub.OutputTokens += r.OutputTokens
			ub.CostUSD += cost
			ub.TurnCount++
		}
	}

	byModel := make([]ModelBreakdown, 0, len(byModelMap))
	for _, mb := range byModelMap {
		byModel = append(byModel, *mb)
	}
	sort.Slice(byModel, func(i, j int) bool {
		if byModel[i].CostUSD != byModel[j].CostUSD {
			return byModel[i].CostUSD > byModel[j].CostUSD
		}
		if byModel[i].TotalTokens != byModel[j].TotalTokens {
			return byModel[i].TotalTokens > byModel[j].TotalTokens
		}
		if byModel[i].Harness != byModel[j].Harness {
			return byModel[i].Harness < byModel[j].Harness
		}
		return byModel[i].Model < byModel[j].Model
	})

	byUser := make([]UserBreakdown, 0, len(byUserMap))
	if normScope == ScopeOrg && len(byUserMap) > 0 {
		usersByID := map[string]db.ListUsersWithIdentityRow{}
		if users, err := s.Store.ListUsersWithIdentity(ctx); err == nil {
			for _, u := range users {
				usersByID[u.ID] = u
			}
		}
		for uid, ub := range byUserMap {
			if u, ok := usersByID[uid]; ok {
				ub.UserName = u.Name
				ub.UserEmail = u.Email
			} else if u, err := s.Store.GetUser(ctx, uid); err == nil {
				ub.UserName = u.Name
				ub.UserEmail = u.Email
			} else {
				ub.UserName = uid
			}
			byUser = append(byUser, *ub)
		}
		sort.Slice(byUser, func(i, j int) bool {
			if byUser[i].CostUSD != byUser[j].CostUSD {
				return byUser[i].CostUSD > byUser[j].CostUSD
			}
			if byUser[i].TotalTokens != byUser[j].TotalTokens {
				return byUser[i].TotalTokens > byUser[j].TotalTokens
			}
			return byUser[i].UserName < byUser[j].UserName
		})
	}

	return SummaryResult{
		Range:   normRange,
		Scope:   normScope,
		Totals:  totals,
		Series:  buckets,
		ByModel: byModel,
		ByUser:  byUser,
	}, nil
}

// effectiveCost applies admin custom overrides dynamically; otherwise prefers
// the stored cost_usd (or computes from built-in defaults if cost_usd == 0).
func effectiveCost(r db.UsageRecord, overrides map[string]Price) (float64, bool) {
	if p, ok := LookupPrice(r.Model, overrides); ok {
		if p.Custom {
			return EstimateCost(r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, p), true
		}
		if r.CostUsd > 0 {
			return r.CostUsd, true
		}
		return EstimateCost(r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, p), true
	}
	if r.CostUsd > 0 {
		return r.CostUsd, true
	}
	return 0, false
}

func (s *Service) overrideMap(ctx context.Context) (map[string]Price, error) {
	rows, err := s.Store.ListModelPrices(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Price, len(rows))
	for _, r := range rows {
		t := time.Unix(r.UpdatedAt, 0).UTC()
		out[strings.ToLower(strings.TrimSpace(r.Model))] = Price{
			Model:         r.Model,
			InputPerM:     r.InputPerM,
			OutputPerM:    r.OutputPerM,
			CacheReadPerM: r.CacheReadPerM,
			Custom:        true,
			UpdatedAt:     &t,
		}
	}
	return out, nil
}

// ListPrices returns the merged list of built-in defaults and admin custom
// overrides, sorted by model name.
func (s *Service) ListPrices(ctx context.Context) ([]Price, error) {
	overrides, err := s.overrideMap(ctx)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]Price, len(defaultPrices)+len(overrides))
	for k, p := range defaultPrices {
		merged[k] = p
	}
	for k, p := range overrides {
		merged[k] = p
	}
	out := make([]Price, 0, len(merged))
	for _, p := range merged {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

// PutPrice upserts a custom price for model, or resets it to the built-in
// default when reset is true.
func (s *Service) PutPrice(ctx context.Context, model string, inputPerM, outputPerM, cacheReadPerM float64, reset bool) (Price, error) {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 128 {
		return Price{}, fmt.Errorf("%w: model must be 1 to 128 characters", ErrInvalidPrice)
	}
	if reset {
		_, err := s.Store.DeleteModelPrice(ctx, model)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Price{}, err
		}
		if p, ok := defaultPrices[strings.ToLower(model)]; ok {
			return p, nil
		}
		return Price{Model: model, Custom: false}, nil
	}
	if inputPerM < 0 || outputPerM < 0 || cacheReadPerM < 0 {
		return Price{}, fmt.Errorf("%w: prices must be non-negative", ErrInvalidPrice)
	}
	now := s.now()
	if err := s.Store.UpsertModelPrice(ctx, db.UpsertModelPriceParams{
		Model:         model,
		InputPerM:     inputPerM,
		OutputPerM:    outputPerM,
		CacheReadPerM: cacheReadPerM,
		UpdatedAt:     now.Unix(),
	}); err != nil {
		return Price{}, err
	}
	return Price{
		Model:         model,
		InputPerM:     inputPerM,
		OutputPerM:    outputPerM,
		CacheReadPerM: cacheReadPerM,
		Custom:        true,
		UpdatedAt:     &now,
	}, nil
}
