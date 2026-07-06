package analytics

import (
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// EfficiencyMetrics holds the composite efficiency score plus supporting
// productivity metrics for display.
type EfficiencyMetrics struct {
	Score           model.EfficiencyScore
	Grade           string
	OutputVerbosity float64
	TokensPerMin    float64
	CodeRatio       float64
	TotalCost       float64
	TotalRequests   int
	TotalOutput     int64
	TotalInput      int64
}

// ComputeEfficiency computes the composite token efficiency score and
// supporting productivity metrics across all sessions.
func ComputeEfficiency(ss []model.Session) EfficiencyMetrics {
	var totalInput, totalOutput, totalCacheRead int64
	var totalRequests, totalMessages, totalToolCalls int
	var totalCost float64
	var totalDuration time.Duration

	for i := range ss {
		s := &ss[i]
		totalInput += s.InputTokens
		totalOutput += s.OutputTokens
		totalCacheRead += s.CacheRead
		totalRequests += s.AssistantCount
		totalMessages += len(s.Messages)
		for _, c := range s.ToolCalls {
			totalToolCalls += c
		}
		cost, _ := report.SessionCost(s)
		totalCost += cost
		dur := s.LastActivityAt.Sub(s.CreatedAt)
		if dur > 0 {
			totalDuration += dur
		}
	}

	em := EfficiencyMetrics{
		TotalInput:    totalInput,
		TotalOutput:   totalOutput,
		TotalRequests: totalRequests,
		TotalCost:     totalCost,
	}

	// Tokens per dollar.
	if totalCost > 0 {
		em.Score.TokensPerDollar = float64(totalInput+totalOutput) / totalCost
	}
	// Tokens per request.
	if totalRequests > 0 {
		em.Score.TokensPerRequest = float64(totalInput+totalOutput) / float64(totalRequests)
	}
	// Output verbosity = output_tokens / request_count.
	if totalRequests > 0 {
		em.OutputVerbosity = float64(totalOutput) / float64(totalRequests)
		em.Score.OutputVerbosity = em.OutputVerbosity
	}
	// Cache savings percentage.
	cacheStats := CacheStatsAggregate(ss)
	em.Score.CacheSavingsPct = cacheStats.HitRatio * 100

	// Tokens per minute = output_tokens / session_duration_minutes.
	minutes := totalDuration.Minutes()
	if minutes > 0 {
		em.TokensPerMin = float64(totalOutput) / minutes
	}

	// Code ratio = tool_calls / total_messages.
	if totalMessages > 0 {
		em.CodeRatio = float64(totalToolCalls) / float64(totalMessages)
	}

	em.Grade = EfficiencyGrade(em.Score)
	return em
}

// EfficiencyGrade maps a composite efficiency score to a letter grade A-F.
// The score blends cache savings, tokens-per-dollar, and tokens-per-request
// into a 0-100 numeric score.
func EfficiencyGrade(score model.EfficiencyScore) string {
	// Build a 0-100 composite.
	// Cache savings pct contributes up to 40 points.
	cachePts := score.CacheSavingsPct * 0.4
	if cachePts > 40 {
		cachePts = 40
	}
	// Tokens per dollar contributes up to 30 points (normalized: 10k t/$ = full).
	tpdPts := 0.0
	if score.TokensPerDollar > 0 {
		tpdPts = score.TokensPerDollar / 10000 * 30
	}
	if tpdPts > 30 {
		tpdPts = 30
	}
	// Tokens per request contributes up to 30 points (normalized: 5k t/req = full).
	tprPts := 0.0
	if score.TokensPerRequest > 0 {
		tprPts = score.TokensPerRequest / 5000 * 30
	}
	if tprPts > 30 {
		tprPts = 30
	}
	total := cachePts + tpdPts + tprPts
	switch {
	case total >= 90:
		return "A"
	case total >= 80:
		return "B"
	case total >= 70:
		return "C"
	case total >= 60:
		return "D"
	default:
		return "F"
	}
}
