package usage

import (
	"sort"
	"strings"
	"time"
)

// Price is the USD per-million-token pricing for a model.
type Price struct {
	Model         string     `json:"model"`
	InputPerM     float64    `json:"inputPerM"`
	OutputPerM    float64    `json:"outputPerM"`
	CacheReadPerM float64    `json:"cacheReadPerM"`
	Custom        bool       `json:"custom"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
}

// defaultPrices are built-in API-equivalent USD per-million-token rates for
// Claude, Codex/GPT, and Gemini/Antigravity models. Admins may override any
// entry (or add new models) via the model_prices table.
var defaultPrices = map[string]Price{
	// Claude (Anthropic)
	"claude-opus-4-6":   {Model: "claude-opus-4-6", InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50},
	"claude-opus-4-5":   {Model: "claude-opus-4-5", InputPerM: 5.0, OutputPerM: 25.0, CacheReadPerM: 0.50},
	"claude-opus-4-1":   {Model: "claude-opus-4-1", InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50},
	"claude-opus-4":     {Model: "claude-opus-4", InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50},
	"claude-sonnet-4-6": {Model: "claude-sonnet-4-6", InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30},
	"claude-sonnet-4-5": {Model: "claude-sonnet-4-5", InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30},
	"claude-sonnet-4":   {Model: "claude-sonnet-4", InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30},
	"claude-3-7-sonnet": {Model: "claude-3-7-sonnet", InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30},
	"claude-3-5-sonnet": {Model: "claude-3-5-sonnet", InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30},
	"claude-haiku-4-5":  {Model: "claude-haiku-4-5", InputPerM: 1.0, OutputPerM: 5.0, CacheReadPerM: 0.10},
	"claude-3-5-haiku":  {Model: "claude-3-5-haiku", InputPerM: 0.80, OutputPerM: 4.0, CacheReadPerM: 0.08},

	// Codex / OpenAI GPT
	"gpt-5.4":       {Model: "gpt-5.4", InputPerM: 2.50, OutputPerM: 15.0, CacheReadPerM: 0.25},
	"gpt-5.3-codex": {Model: "gpt-5.3-codex", InputPerM: 1.75, OutputPerM: 14.0, CacheReadPerM: 0.175},
	"gpt-5.2-codex": {Model: "gpt-5.2-codex", InputPerM: 1.75, OutputPerM: 14.0, CacheReadPerM: 0.175},
	"gpt-5.2":       {Model: "gpt-5.2", InputPerM: 1.75, OutputPerM: 14.0, CacheReadPerM: 0.175},
	"gpt-5.1-codex": {Model: "gpt-5.1-codex", InputPerM: 1.25, OutputPerM: 10.0, CacheReadPerM: 0.125},
	"gpt-5":         {Model: "gpt-5", InputPerM: 1.25, OutputPerM: 10.0, CacheReadPerM: 0.125},
	"gpt-5-mini":    {Model: "gpt-5-mini", InputPerM: 0.25, OutputPerM: 2.0, CacheReadPerM: 0.025},
	"gpt-4.1":       {Model: "gpt-4.1", InputPerM: 2.0, OutputPerM: 8.0, CacheReadPerM: 0.50},
	"gpt-4o":        {Model: "gpt-4o", InputPerM: 2.50, OutputPerM: 10.0, CacheReadPerM: 1.25},
	"o3":            {Model: "o3", InputPerM: 2.0, OutputPerM: 8.0, CacheReadPerM: 0.50},
	"o4-mini":       {Model: "o4-mini", InputPerM: 1.10, OutputPerM: 4.40, CacheReadPerM: 0.275},

	// Gemini / Antigravity
	"gemini-3-pro":     {Model: "gemini-3-pro", InputPerM: 2.0, OutputPerM: 12.0, CacheReadPerM: 0.20},
	"gemini-3-flash":   {Model: "gemini-3-flash", InputPerM: 0.50, OutputPerM: 3.0, CacheReadPerM: 0.05},
	"gemini-2.5-pro":   {Model: "gemini-2.5-pro", InputPerM: 1.25, OutputPerM: 10.0, CacheReadPerM: 0.125},
	"gemini-2.5-flash": {Model: "gemini-2.5-flash", InputPerM: 0.30, OutputPerM: 2.50, CacheReadPerM: 0.03},
	"gemini-2.0-flash": {Model: "gemini-2.0-flash", InputPerM: 0.10, OutputPerM: 0.40, CacheReadPerM: 0.025},
}

// DefaultPrices returns a sorted copy of the built-in default pricing table.
func DefaultPrices() []Price {
	out := make([]Price, 0, len(defaultPrices))
	for _, p := range defaultPrices {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// normalizeModel strips optional whitespace and provider prefixes (e.g.
// "anthropic/claude-sonnet-4-5" -> "claude-sonnet-4-5").
func normalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(m, "/"); idx >= 0 && idx < len(m)-1 {
		m = m[idx+1:]
	}
	return m
}

// LookupPrice resolves the effective price for model using admin overrides
// first, then built-in defaults (exact match, longest prefix match, and family
// fallback). Returns ok=false if the model is unknown/unpriced.
func LookupPrice(model string, overrides map[string]Price) (Price, bool) {
	raw := strings.ToLower(strings.TrimSpace(model))
	norm := normalizeModel(model)
	if norm == "" {
		return Price{}, false
	}

	// 1. Exact match in overrides (raw or normalized).
	if p, ok := overrides[raw]; ok {
		return p, true
	}
	if p, ok := overrides[norm]; ok {
		return p, true
	}

	// 2. Longest prefix match in overrides.
	if p, ok := longestPrefixMatch(norm, overrides); ok {
		return p, true
	}

	// 3. Exact match in built-in defaults.
	if p, ok := defaultPrices[norm]; ok {
		return p, true
	}

	// 4. Longest prefix match in built-in defaults.
	if p, ok := longestPrefixMatch(norm, defaultPrices); ok {
		return p, true
	}

	// 5. Family fallback for short aliases (e.g. "opus", "sonnet", "haiku", "codex").
	switch {
	case strings.Contains(norm, "opus"):
		return defaultPrices["claude-opus-4-6"], true
	case strings.Contains(norm, "sonnet"):
		return defaultPrices["claude-sonnet-4-6"], true
	case strings.Contains(norm, "haiku"):
		return defaultPrices["claude-haiku-4-5"], true
	case strings.Contains(norm, "codex"):
		return defaultPrices["gpt-5.3-codex"], true
	case strings.Contains(norm, "gemini") && strings.Contains(norm, "flash"):
		return defaultPrices["gemini-2.5-flash"], true
	case strings.Contains(norm, "gemini"):
		return defaultPrices["gemini-2.5-pro"], true
	}
	return Price{}, false
}

func longestPrefixMatch(norm string, table map[string]Price) (Price, bool) {
	var best Price
	bestLen := 0
	for k, p := range table {
		nk := normalizeModel(k)
		if nk == "" || len(nk) <= bestLen {
			continue
		}
		if strings.HasPrefix(norm, nk) {
			// Require either exact match or a delimiter after prefix so "gpt-5"
			// doesn't accidentally match "gpt-50" (though "gpt-5-2026" is fine).
			if len(norm) == len(nk) || norm[len(nk)] == '-' || norm[len(nk)] == '_' || norm[len(nk)] == '.' || norm[len(nk)] == '@' || norm[len(nk)] == ':' {
				best = p
				bestLen = len(nk)
			}
		}
	}
	return best, bestLen > 0
}

// EstimateCost calculates the estimated USD cost for a turn given token counts
// and a per-million price schedule. Cache writes default to 1.25x input rate.
func EstimateCost(inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64, p Price) float64 {
	const million = 1_000_000.0
	cost := (float64(inputTokens) / million) * p.InputPerM
	cost += (float64(outputTokens) / million) * p.OutputPerM
	cost += (float64(cacheReadTokens) / million) * p.CacheReadPerM
	if cacheWriteTokens > 0 {
		cost += (float64(cacheWriteTokens) / million) * (p.InputPerM * 1.25)
	}
	return cost
}
