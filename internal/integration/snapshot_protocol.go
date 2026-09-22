package integration

import (
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/state"
	"github.com/garywhat/devinmonitor/internal/status"
)

// protocolOpts carries the snapshot flags that shape the machine-readable
// output.
type protocolOpts struct {
	LimitTokens int64
	LimitACU    float64
}

// clampPercent bounds an internally computed percentage to [0, 100].
//
// Percentages we compute ourselves are always finite and meaningful even when
// far above the limit, so they must NOT go through status.SanitizePercent:
// that function exists to discard garbage coming from upstream payloads (where
// an epoch can leak into a percentage field), and would wrongly drop a genuine
// over-limit reading of, say, 540%.
func clampPercent(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

// round1 rounds to one decimal place, the protocol's convention for every
// percentage and share it emits. Raw float64 ratios would otherwise leak
// values like 84.55880258 into the JSON and the one-line status output.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// confidenceFrom maps the aggregate cost provenance string used elsewhere in
// this package ("official" / "estimated" / "mixed" / "unknown") onto the
// protocol's confidence enum.
//
// "mixed" has no single trust level, so it degrades to "estimated": at least
// part of the total was derived rather than reported, and claiming "official"
// would overstate it.
func confidenceFrom(provenance string) status.Confidence {
	switch provenance {
	case string(status.ConfidenceOfficial):
		return status.ConfidenceOfficial
	case string(status.ConfidenceEstimate):
		return status.ConfidenceEstimate
	case "mixed":
		return status.ConfidenceEstimate
	default:
		return status.ConfidenceUnknown
	}
}

// buildProtocolSnapshot assembles the machine-readable snapshot (schema v1).
//
// Limit windows come from configuration that already exists today: an explicit
// token limit, the monthly cost budget, and the plan's monthly ACU allowance.
// A fresh upstream statusline capture takes precedence over all of them, which
// is the "official wins" rule. The five-hour billing block model is M2 work and
// is deliberately not implemented here.
func buildProtocolSnapshot(ss []model.Session, now time.Time, opts protocolOpts, cfg *config.Config) status.Snapshot {
	sum := computeCostSummary(ss)

	var totalTokens int64
	for _, s := range ss {
		totalTokens += s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
	}

	limits := map[string]*status.Window{}
	statusWindow := ""

	// Official upstream data first.
	if cap, stale, err := state.ReadStatusline(state.DefaultStatuslinePath(), now); err == nil && cap != nil {
		if w := upstreamWindow(cap.FiveHour, stale, status.DefaultWindowSeconds, now); w != nil {
			limits["five_hour"] = w
			if !stale {
				statusWindow = "five_hour"
			}
		}
		if w := upstreamWindow(cap.SevenDay, stale, 7*24*60*60, now); w != nil {
			limits["seven_day"] = w
			if !stale && statusWindow == "" {
				statusWindow = "seven_day"
			}
		}
	}

	// Local monthly window derived from existing configuration.
	limitHit := false
	if w := monthlyWindow(ss, now, opts, cfg, sum.MonthCost); w != nil {
		limits["monthly"] = w
		if statusWindow == "" {
			statusWindow = "monthly"
		}
		if w.ACULimit != nil && w.ACUUsed != nil && *w.ACUUsed >= *w.ACULimit {
			limitHit = true
		}
	}

	return status.BuildSnapshot(status.Input{
		Now:            now,
		TodayCost:      sum.TodayCost,
		WeekCost:       sum.WeekCost,
		MonthCost:      sum.MonthCost,
		TotalCost:      sum.TotalCost,
		TotalSessions:  sum.TotalSess,
		TotalRequests:  sum.TotalReqs,
		TotalTokens:    totalTokens,
		ActiveSessions: sum.ActiveSess,
		CostProvenance: confidenceFrom(sum.Provenance),
		ACU:            cfg.PlanACULimit > 0,
		ByModel:        protocolModelRows(ss),
		Limits:         limits,
		StatusWindow:   statusWindow,
		LimitHit:       limitHit,
	})
}

// upstreamWindow converts one captured upstream window into a protocol window.
//
// A stale capture keeps its slot (so consumers can see that upstream data
// exists but is too old) with its numbers dropped and confidence downgraded —
// we never serve an expired reading as if it were current.
func upstreamWindow(u *state.UpstreamWindow, stale bool, windowSeconds int64, now time.Time) *status.Window {
	if u == nil {
		return nil
	}
	w := &status.Window{
		Source:     status.Source{Kind: status.SourceStatusline},
		Confidence: status.ConfidenceOfficial,
		Stale:      stale,
	}
	if stale {
		w.Confidence = status.ConfidenceUnknown
		return w
	}
	w.UsedPercentage = u.UsedPercentage
	w.ResetsAtEpoch = u.ResetsAtEpoch
	if u.ResetsAtEpoch != nil {
		rs := time.Unix(*u.ResetsAtEpoch, 0).UTC().Format(time.RFC3339)
		w.ResetsAt = &rs
		p := status.ComputePace(u.UsedPercentage, u.ResetsAtEpoch, windowSeconds, now)
		w.Pace = &p
		w.Forecast = status.ForecastExhaustion(u.UsedPercentage, u.ResetsAtEpoch, windowSeconds, now)
	}
	return w
}

// monthlyWindow builds the month-to-date usage window from existing config.
//
// Percentage precedence: an explicit --limit-tokens wins, then the monthly cost
// budget. With neither configured we cannot state a percentage, and we say so
// (nil) instead of inventing one.
func monthlyWindow(ss []model.Session, now time.Time, opts protocolOpts, cfg *config.Config, monthCost float64) *status.Window {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	nextMonth := monthStart.AddDate(0, 1, 0)
	windowSeconds := int64(nextMonth.Sub(monthStart).Seconds())
	resetEpoch := nextMonth.Unix()

	var monthTokens, monthACU float64
	for _, s := range ss {
		if !s.LastActivityAt.After(monthStart) {
			continue
		}
		monthTokens += float64(s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite)
		monthACU += s.ACUCost
	}

	acuLimit := opts.LimitACU
	if acuLimit <= 0 {
		acuLimit = cfg.PlanACULimit
	}

	w := &status.Window{
		Source:        status.Source{Kind: status.SourceConfig},
		Confidence:    status.ConfidenceEstimate,
		ResetsAtEpoch: &resetEpoch,
	}
	rs := nextMonth.UTC().Format(time.RFC3339)
	w.ResetsAt = &rs

	var usedPct float64
	havePct := true
	switch {
	case opts.LimitTokens > 0:
		usedPct = round1(clampPercent(monthTokens / float64(opts.LimitTokens) * 100))
	case cfg.BudgetMonthly > 0:
		usedPct = round1(clampPercent(monthCost / cfg.BudgetMonthly * 100))
	default:
		havePct = false
	}

	if havePct {
		w.UsedPercentage = &usedPct
		p := status.ComputePace(&usedPct, &resetEpoch, windowSeconds, now)
		w.Pace = &p
		w.Forecast = status.ForecastExhaustion(&usedPct, &resetEpoch, windowSeconds, now)
	}
	if opts.LimitTokens > 0 {
		used := int64(monthTokens)
		limit := opts.LimitTokens
		w.TokensUsed, w.TokenLimit = &used, &limit
	}
	if acuLimit > 0 {
		used := monthACU
		w.ACUUsed, w.ACULimit = &used, &acuLimit
	}
	return w
}

// protocolModelRows maps the report layer's model aggregation onto the
// protocol's model rows.
func protocolModelRows(ss []model.Session) []status.ModelRow {
	rows := report.BuildModelRows(ss)
	if len(rows) == 0 {
		return nil
	}
	var totalInOut int64
	for _, r := range rows {
		totalInOut += r.InputTok + r.OutputTok
	}
	out := make([]status.ModelRow, 0, len(rows))
	for _, r := range rows {
		cost := r.CreditCost + r.ACUCost
		if cost == 0 {
			cost = r.EstCost
		}
		var share float64
		if totalInOut > 0 {
			share = round1(float64(r.InputTok+r.OutputTok) / float64(totalInOut) * 100)
		}
		out = append(out, status.ModelRow{
			Model:            r.Name,
			InputTokens:      r.InputTok,
			OutputTokens:     r.OutputTok,
			CacheReadTokens:  r.CacheRead,
			CacheWriteTokens: r.CacheWrite,
			Requests:         r.Requests,
			Cost:             cost,
			Share:            share,
		})
	}
	return out
}

// captureUpstreamStatusline reads an upstream payload from stdin when — and
// only when — stdin is a pipe. On a terminal it returns immediately, so
// `devinmonitor snapshot --statusline` never blocks waiting for input.
func captureUpstreamStatusline() {
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice != 0 {
		return
	}
	payload, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil || len(payload) == 0 {
		return
	}
	if _, err := state.CaptureStatusline(payload, state.DefaultStatuslinePath(), time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "capture statusline: %v\n", err)
	}
}
