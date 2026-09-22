package limit

import (
	"math"
	"time"
)

// The wire form of a block is part of the domain, not of any one command: the
// `blocks` CLI and the MCP `get_blocks` tool must emit byte-identical
// documents, and they did not when each carried its own encoder. It lives here
// so there is exactly one implementation, matching docs/blocks.schema.json.
//
// Field names, null-vs-absent rules and rounding are all part of that
// contract; changing any of them is a breaking change to both surfaces.

// WireTokens is the per-block token object: exactly the four raw counters.
// Every derived figure stays at the top level, because the schema's tokens
// definition forbids additional properties.
type WireTokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// WireBurnRate mirrors BurnRate.
type WireBurnRate struct {
	TokensPerMinute        float64 `json:"tokensPerMinute"`
	TokensPerMinuteDisplay float64 `json:"tokensPerMinuteDisplay"`
	CostPerHour            float64 `json:"costPerHour"`
}

// WireProjection mirrors Projection.
type WireProjection struct {
	TotalTokens      int64   `json:"totalTokens"`
	TotalCost        float64 `json:"totalCost"`
	RemainingMinutes int64   `json:"remainingMinutes"`
}

// WireBlock is the machine-readable form of one billing window.
//
// The raw entries array is deliberately omitted (too large); the aggregate
// token counts carry its meaning. burnRate and projection are optional, so
// they are omitted when unknown; actualEndTime and usedPercent are emitted as
// JSON null in that case.
type WireBlock struct {
	ID             string          `json:"id"`
	StartTime      string          `json:"startTime"`
	EndTime        string          `json:"endTime"`
	ActualEndTime  *string         `json:"actualEndTime"`
	IsActive       bool            `json:"isActive"`
	IsGap          bool            `json:"isGap"`
	Models         []string        `json:"models"`
	Cost           float64         `json:"cost"`
	Tokens         WireTokens      `json:"tokens"`
	TotalTokens    int64           `json:"totalTokens"`
	NonCacheTokens int64           `json:"nonCacheTokens"`
	UsedPercent    *float64        `json:"usedPercent"`
	BurnRate       *WireBurnRate   `json:"burnRate,omitempty"`
	Projection     *WireProjection `json:"projection,omitempty"`
}

// RoundTo rounds v to the given number of decimal places: rates carry one
// decimal and money two, matching docs/blocks.example.json. Percentages are
// already rounded by UsedPercent.
func RoundTo(v float64, decimals int) float64 {
	p := math.Pow10(decimals)
	return math.Round(v*p) / p
}

// WireBlockOf converts one block into its wire form.
//
// now is the clock the projection extrapolates to; tokenLimit is the limit the
// caller is measuring against (0 = unset, which leaves usedPercent null).
func WireBlockOf(b Block, now time.Time, tokenLimit int64) WireBlock {
	models := b.Models
	if models == nil {
		models = []string{}
	}
	var actualEnd *string
	if !b.ActualEndTime.IsZero() {
		s := b.ActualEndTime.Format(time.RFC3339)
		actualEnd = &s
	}

	out := WireBlock{
		ID:            b.ID,
		StartTime:     b.StartTime.Format(time.RFC3339),
		EndTime:       b.EndTime.Format(time.RFC3339),
		ActualEndTime: actualEnd,
		IsActive:      b.IsActive,
		IsGap:         b.IsGap,
		Models:        models,
		Cost:          b.Cost,
		Tokens: WireTokens{
			Input:      b.Tokens.Input,
			Output:     b.Tokens.Output,
			CacheRead:  b.Tokens.CacheRead,
			CacheWrite: b.Tokens.CacheWrite,
		},
		TotalTokens:    b.Tokens.Total(),
		NonCacheTokens: b.Tokens.NonCache(),
	}

	// Gap blocks carry no usage, so none of the derived figures apply.
	if b.IsGap {
		return out
	}

	if tokenLimit > 0 {
		out.UsedPercent = UsedPercent(&b, tokenLimit)
	}
	if rate := BurnRateOf(&b); rate != nil {
		out.BurnRate = &WireBurnRate{
			TokensPerMinute:        RoundTo(rate.TokensPerMinute, 1),
			TokensPerMinuteDisplay: RoundTo(rate.TokensPerMinuteDisplay, 1),
			CostPerHour:            RoundTo(rate.CostPerHour, 2),
		}
	}
	if b.IsActive {
		if p := Project(&b, now); p != nil {
			out.Projection = &WireProjection{
				TotalTokens:      p.TotalTokens,
				TotalCost:        RoundTo(p.TotalCost, 2),
				RemainingMinutes: p.RemainingMinutes,
			}
		}
	}
	return out
}

// WireBlocks converts blocks into their wire form, preserving the caller's
// order (callers that want newest-first sort before calling).
func WireBlocks(blocks []Block, now time.Time, tokenLimit int64) []WireBlock {
	out := make([]WireBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, WireBlockOf(b, now, tokenLimit))
	}
	return out
}
