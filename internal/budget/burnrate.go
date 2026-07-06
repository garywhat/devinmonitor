package budget

import (
	"sort"
	"time"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ComputeBurnRate calculates spending velocity from recent session activity.
//
// PerHour is derived from the last hour of activity; PerDay is derived from
// the last 24h. PerWeek and PerMonth are extrapolated from PerDay. ACU cost
// is converted to USD via cfg.ACURate when set (otherwise treated as USD).
func ComputeBurnRate(ss []model.Session, cfg *config.Config, now time.Time) model.BurnRate {
	var lastHour, last24h float64
	hourAgo := now.Add(-time.Hour)
	dayAgo := now.Add(-24 * time.Hour)
	for i := range ss {
		s := &ss[i]
		cost, _ := report.SessionCost(s)
		cost += acuToUSD(s.ACUCost, cfg)
		if cost <= 0 {
			continue
		}
		// Distribute cost across the session's active span so we can
		// attribute spend to the windows when activity actually happened.
		start := s.CreatedAt
		end := s.LastActivityAt
		if end.IsZero() || end.Before(start) {
			end = start
		}
		if end.After(now) {
			end = now
		}
		dur := end.Sub(start)
		if dur <= 0 {
			// Instantaneous session: attribute to its creation time.
			if !start.Before(hourAgo) {
				lastHour += cost
			}
			if !start.Before(dayAgo) {
				last24h += cost
			}
			continue
		}
		rate := cost / dur.Hours() // USD per hour over the session span
		// Overlap with [hourAgo, now].
		lastHour += overlapHours(start, end, hourAgo, now) * rate
		last24h += overlapHours(start, end, dayAgo, now) * rate
	}

	perHour := lastHour
	perDay := last24h
	// If the last hour has no data but the last 24h does, derive perHour
	// from the 24h average so the gauge isn't artificially zero.
	if perHour == 0 && perDay > 0 {
		perHour = perDay / 24
	}
	return model.BurnRate{
		PerHour:  perHour,
		PerDay:   perDay,
		PerWeek:  perDay * 7,
		PerMonth: perDay * 30,
	}
}

// acuToUSD converts ACU cost to USD using the configured rate. When no rate
// is configured, ACU is assumed to already be in USD (Devin's default).
func acuToUSD(acu float64, cfg *config.Config) float64 {
	if acu <= 0 {
		return 0
	}
	if cfg.ACURate > 0 {
		return acu * cfg.ACURate
	}
	return acu
}

// overlapHours returns the overlap of [a0,a1] and [b0,b1] in hours.
func overlapHours(a0, a1, b0, b1 time.Time) float64 {
	lo := maxTime(a0, b0)
	hi := minTime(a1, b1)
	if !hi.After(lo) {
		return 0
	}
	return hi.Sub(lo).Hours()
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// ---- Cost projection (#4) ----

// dailySpend computes per-day spend (USD) over the last `days` calendar days,
// keyed by "YYYY-MM-DD". Days with no activity are omitted.
func dailySpend(ss []model.Session, cfg *config.Config, now time.Time, days int) map[string]float64 {
	out := map[string]float64{}
	cutoff := model.DayStart(now.AddDate(0, 0, -(days - 1)))
	for i := range ss {
		s := &ss[i]
		cost, _ := report.SessionCost(s)
		cost += acuToUSD(s.ACUCost, cfg)
		if cost <= 0 {
			continue
		}
		t := s.LastActivityAt
		if t.IsZero() {
			t = s.CreatedAt
		}
		if t.Before(cutoff) {
			continue
		}
		out[t.Format("2006-01-02")] += cost
	}
	return out
}

// ComputeProjection predicts end-of-month spend and days to budget exhaustion.
//
// Confidence is based on how many distinct days of data we have within the
// lookback window (more days = higher confidence, capped at 1.0).
func ComputeProjection(ss []model.Session, cfg *config.Config, now time.Time) model.CostProjection {
	const lookback = 30
	byDay := dailySpend(ss, cfg, now, lookback)

	// Average daily spend over days that had activity.
	var sum float64
	var activeDays int
	for _, v := range byDay {
		sum += v
		activeDays++
	}
	avgDaily := 0.0
	if activeDays > 0 {
		avgDaily = sum / float64(activeDays)
	}

	// Predict end-of-month total: spend so far this month + avgDaily * days left.
	spend := SpendByPeriod(ss, now)
	monthEnd := lastDayOfMonth(now)
	daysLeft := int(monthEnd.Sub(now).Hours()/24) + 1
	if daysLeft < 0 {
		daysLeft = 0
	}
	predicted := spend.Monthly + avgDaily*float64(daysLeft)

	// Days to budget exhaustion based on monthly budget.
	daysToExhaust := 0
	remaining := 0.0
	if cfg.BudgetMonthly > 0 {
		remaining = cfg.BudgetMonthly - spend.Monthly
		if remaining <= 0 {
			daysToExhaust = 0 // already over budget
		} else if avgDaily > 0 {
			daysToExhaust = int(remaining / avgDaily)
		}
	}

	// Confidence: fraction of lookback window with data, scaled.
	confidence := 0.0
	if lookback > 0 {
		confidence = float64(activeDays) / float64(lookback)
	}
	if confidence > 1 {
		confidence = 1
	}

	return model.CostProjection{
		PredictedMonthEnd: predicted,
		RemainingBudget:   remaining,
		DaysToExhaust:     daysToExhaust,
		Confidence:        confidence,
	}
}

// lastDayOfMonth returns the last instant of the month containing t.
func lastDayOfMonth(t time.Time) time.Time {
	firstOfNext := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
	return firstOfNext.Add(-time.Nanosecond)
}

// SortedDailySpend returns daily spend points sorted ascending by date, for
// charting. Only days within the lookback window are included.
func SortedDailySpend(ss []model.Session, cfg *config.Config, now time.Time, days int) []model.TrendPoint {
	byDay := dailySpend(ss, cfg, now, days)
	pts := make([]model.TrendPoint, 0, len(byDay))
	for d, c := range byDay {
		pts = append(pts, model.TrendPoint{Label: d, Cost: c})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].Label < pts[j].Label })
	return pts
}
