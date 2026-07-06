package analytics

import (
	"sort"

	"github.com/garywhat/devinmonitor/internal/model"
)

// CompareModels builds a side-by-side comparison of models. If modelNames is
// non-empty, only those models are included (fuzzy matched). Otherwise all
// models are compared.
func CompareModels(ss []model.Session, modelNames []string) model.ModelComparison {
	// Aggregate per-model stats from assistant messages.
	type agg struct {
		requests     int
		input        int64
		output       int64
		cacheRead    int64
		ttfts        []float64
		tokensPerSec []float64
	}
	byModel := map[string]*agg{}

	for i := range ss {
		s := &ss[i]
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.GenerationModel == "" {
				continue
			}
			mn := m.GenerationModel
			a := byModel[mn]
			if a == nil {
				a = &agg{}
				byModel[mn] = a
			}
			a.requests++
			if m.Metrics != nil {
				a.input += m.Metrics.InputTokens
				a.output += m.Metrics.OutputTokens
				a.cacheRead += m.Metrics.CacheReadTokens
				if m.Metrics.TTFTMs > 0 {
					a.ttfts = append(a.ttfts, m.Metrics.TTFTMs)
				}
				if m.Metrics.TokensPerSec > 0 {
					a.tokensPerSec = append(a.tokensPerSec, m.Metrics.TokensPerSec)
				}
			}
		}
	}

	// Filter by requested model names if provided.
	want := map[string]bool{}
	if len(modelNames) > 0 {
		for _, name := range modelNames {
			// Fuzzy match: find models containing the name (case-insensitive).
			lname := toLower(name)
			for mn := range byModel {
				if contains(toLower(mn), lname) {
					want[mn] = true
				}
			}
		}
	}

	var rows []model.ModelCompareRow
	for mn, a := range byModel {
		if len(want) > 0 && !want[mn] {
			continue
		}
		p := model.LookupPricing(mn)
		cost := model.EstimateCost(p, a.input, a.output, a.cacheRead, 0)
		row := model.ModelCompareRow{
			Name:         mn,
			Requests:     a.requests,
			InputTokens:  a.input,
			OutputTokens: a.output,
			Cost:         cost,
			AvgLatency:   avg(a.ttfts),
			TokensPerSec: avg(a.tokensPerSec),
		}
		// Cache hit percentage.
		total := a.input + a.cacheRead
		if total > 0 {
			row.CacheHitPct = float64(a.cacheRead) / float64(total) * 100
		}
		rows = append(rows, row)
	}

	// Sort by total tokens (input + output) descending.
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].InputTokens+rows[i].OutputTokens > rows[j].InputTokens+rows[j].OutputTokens
	})

	return model.ModelComparison{Models: rows}
}

func avg(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, v := range xs {
		sum += v
	}
	return sum / float64(len(xs))
}

func toLower(s string) string {
	// Avoid importing strings here; simple ASCII lowercase.
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
