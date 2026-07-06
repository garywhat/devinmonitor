// Package analytics implements efficiency and analytics features for
// DevinMonitor: cache metrics, efficiency scoring, one-shot/retry rates,
// task categorization, waste detection, compaction events, model comparison,
// and context window analysis.
//
// Commands self-register via cli.Register() in init() so that main.go can
// pull them in with a blank import, avoiding edits to existing files.
package analytics

import (
	"fmt"
	"strings"

	"github.com/garywhat/devinmonitor/internal/model"
)

// CacheStatsForSession computes cache metrics for a single session.
func CacheStatsForSession(s *model.Session) model.CacheStats {
	return computeCacheStats(s.CacheRead, s.CacheWrite, s.InputTokens)
}

// CacheStatsAggregate computes aggregate cache metrics across all sessions.
func CacheStatsAggregate(ss []model.Session) model.CacheStats {
	var cacheRead, cacheWrite, input int64
	for i := range ss {
		cacheRead += ss[i].CacheRead
		cacheWrite += ss[i].CacheWrite
		input += ss[i].InputTokens
	}
	st := computeCacheStats(cacheRead, cacheWrite, input)
	st.SavingsUSD = CacheSavingsDetailed(ss)
	return st
}

func computeCacheStats(cacheRead, cacheWrite, input int64) model.CacheStats {
	st := model.CacheStats{
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		InputTokens: input,
	}
	denom := cacheRead + input
	if denom > 0 {
		st.HitRatio = float64(cacheRead) / float64(denom)
	}
	totalInput := cacheRead + input
	if totalInput > 0 {
		st.Leverage = float64(cacheRead) / float64(totalInput)
	}
	return st
}

// CacheSavingsDetailed computes the cost saved by caching across all
// sessions, using per-model pricing. Savings = cache_read * (input_price -
// cache_read_price) per model.
func CacheSavingsDetailed(ss []model.Session) float64 {
	byModel := map[string]int64{}
	for i := range ss {
		s := &ss[i]
		modelName := s.LatestModel
		if modelName == "" {
			modelName = s.Model
		}
		if modelName == "" {
			modelName = "unknown"
		}
		byModel[modelName] += s.CacheRead
	}
	var savings float64
	for modelName, cacheRead := range byModel {
		if cacheRead <= 0 {
			continue
		}
		p := model.LookupPricing(modelName)
		if p.Free || p.InputPerM == 0 {
			continue
		}
		fullCost := float64(cacheRead) / 1e6 * p.InputPerM
		cacheCost := float64(cacheRead) / 1e6 * p.CacheReadPerM
		savings += fullCost - cacheCost
	}
	return savings
}

// CacheDonut renders an ASCII donut/bar chart of cache hit vs miss.
// Uses █ for hit portion and ░ for miss portion.
func CacheDonut(hitRatio float64, width int) string {
	if width < 10 {
		width = 40
	}
	if hitRatio < 0 {
		hitRatio = 0
	}
	if hitRatio > 1 {
		hitRatio = 1
	}
	filled := int(float64(width) * hitRatio)
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return bar + "  " + fmt.Sprintf("%.1f%%", hitRatio*100)
}
