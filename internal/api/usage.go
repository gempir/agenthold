package api

import (
	"context"
	"errors"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/usage"
)

func (a *API) usageSvc() *usage.Service {
	if a.Usage != nil {
		return a.Usage
	}
	return &usage.Service{Store: a.Store, Now: a.Now}
}

// GetUsageSummary implements gen.StrictServerInterface.
func (a *API) GetUsageSummary(ctx context.Context, req gen.GetUsageSummaryRequestObject) (gen.GetUsageSummaryResponseObject, error) {
	rng := usage.Range7d
	if req.Params.Range != nil && *req.Params.Range != "" {
		rng = string(*req.Params.Range)
	}
	scope := usage.ScopeMe
	if req.Params.Scope != nil && *req.Params.Scope != "" {
		scope = string(*req.Params.Scope)
	}
	if scope == usage.ScopeOrg {
		if err := a.requireAdmin(ctx); err != nil {
			return nil, err
		}
	}
	res, err := a.usageSvc().Summary(ctx, me(ctx).ID, rng, scope)
	if err != nil {
		if errors.Is(err, usage.ErrInvalidRange) || errors.Is(err, usage.ErrInvalidScope) {
			return nil, badRequest(err.Error())
		}
		return nil, err
	}
	series := make([]gen.UsageBucket, 0, len(res.Series))
	for _, b := range res.Series {
		series = append(series, gen.UsageBucket{
			Bucket:           b.Bucket,
			TotalTokens:      b.TotalTokens,
			InputTokens:      b.InputTokens,
			OutputTokens:     b.OutputTokens,
			CacheReadTokens:  b.CacheReadTokens,
			CacheWriteTokens: b.CacheWriteTokens,
			CostUsd:          b.CostUSD,
			TurnCount:        b.TurnCount,
		})
	}
	byModel := make([]gen.UsageModelBreakdown, 0, len(res.ByModel))
	for _, m := range res.ByModel {
		byModel = append(byModel, gen.UsageModelBreakdown{
			Harness:          m.Harness,
			Model:            m.Model,
			TotalTokens:      m.TotalTokens,
			InputTokens:      m.InputTokens,
			OutputTokens:     m.OutputTokens,
			CacheReadTokens:  m.CacheReadTokens,
			CacheWriteTokens: m.CacheWriteTokens,
			CostUsd:          m.CostUSD,
			TurnCount:        m.TurnCount,
			Priced:           m.Priced,
		})
	}
	byUser := make([]gen.UsageUserBreakdown, 0, len(res.ByUser))
	for _, u := range res.ByUser {
		byUser = append(byUser, gen.UsageUserBreakdown{
			UserId:       u.UserID,
			UserName:     u.UserName,
			UserEmail:    u.UserEmail,
			TotalTokens:  u.TotalTokens,
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			CostUsd:      u.CostUSD,
			TurnCount:    u.TurnCount,
		})
	}
	return gen.GetUsageSummary200JSONResponse{
		Range: gen.UsageSummaryRange(res.Range),
		Scope: gen.UsageSummaryScope(res.Scope),
		Totals: gen.UsageTotals{
			TotalTokens:      res.Totals.TotalTokens,
			InputTokens:      res.Totals.InputTokens,
			OutputTokens:     res.Totals.OutputTokens,
			CacheReadTokens:  res.Totals.CacheReadTokens,
			CacheWriteTokens: res.Totals.CacheWriteTokens,
			TotalCostUsd:     res.Totals.TotalCostUSD,
			TurnCount:        res.Totals.TurnCount,
		},
		Series:  series,
		ByModel: byModel,
		ByUser:  byUser,
	}, nil
}

func apiUsagePrice(p usage.Price) gen.UsageModelPrice {
	return gen.UsageModelPrice{
		Model:         p.Model,
		InputPerM:     p.InputPerM,
		OutputPerM:    p.OutputPerM,
		CacheReadPerM: p.CacheReadPerM,
		Custom:        p.Custom,
		UpdatedAt:     p.UpdatedAt,
	}
}

// ListUsagePrices implements gen.StrictServerInterface.
func (a *API) ListUsagePrices(ctx context.Context, _ gen.ListUsagePricesRequestObject) (gen.ListUsagePricesResponseObject, error) {
	prices, err := a.usageSvc().ListPrices(ctx)
	if err != nil {
		return nil, err
	}
	out := make(gen.ListUsagePrices200JSONResponse, 0, len(prices))
	for _, p := range prices {
		out = append(out, apiUsagePrice(p))
	}
	return out, nil
}

// PutUsagePrice implements gen.StrictServerInterface.
func (a *API) PutUsagePrice(ctx context.Context, req gen.PutUsagePriceRequestObject) (gen.PutUsagePriceResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	cacheRead := 0.0
	if req.Body.CacheReadPerM != nil {
		cacheRead = *req.Body.CacheReadPerM
	}
	reset := req.Body.Reset != nil && *req.Body.Reset
	p, err := a.usageSvc().PutPrice(ctx, req.Body.Model, req.Body.InputPerM, req.Body.OutputPerM, cacheRead, reset)
	if err != nil {
		if errors.Is(err, usage.ErrInvalidPrice) {
			return nil, badRequest(err.Error())
		}
		return nil, err
	}
	return gen.PutUsagePrice200JSONResponse(apiUsagePrice(p)), nil
}
