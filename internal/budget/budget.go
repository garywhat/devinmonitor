// Package budget implements budget guardrails, burn-rate tracking, cost
// projection, multi-currency conversion, and per-unit cost metrics for
// DevinMonitor.
//
// Commands self-register via init() through the internal/cli registry so
// that feature packages can be developed in parallel without merge conflicts.
package budget

import (
	"fmt"
	"time"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// PeriodSpend holds current spend aggregated over a calendar period.
type PeriodSpend struct {
	Daily   float64 // spend within the current calendar day (local)
	Weekly  float64 // spend within the current calendar week (Mon start)
	Monthly float64 // spend within the current calendar month
}

// SpendByPeriod computes spend per period from sessions. Cost is taken from
// report.SessionCost (authoritative credit/ACU when present, else estimate).
// Only sessions whose LastActivityAt falls inside the period are counted.
func SpendByPeriod(ss []model.Session, now time.Time) PeriodSpend {
	var out PeriodSpend
	dayStart := model.DayStart(now)
	weekStart := startOfWeek(now, time.Monday)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	for i := range ss {
		s := &ss[i]
		cost, _ := report.SessionCost(s)
		if cost <= 0 {
			continue
		}
		t := s.LastActivityAt
		if t.IsZero() {
			t = s.CreatedAt
		}
		if !t.Before(dayStart) {
			out.Daily += cost
		}
		if !t.Before(weekStart) {
			out.Weekly += cost
		}
		if !t.Before(monthStart) {
			out.Monthly += cost
		}
	}
	return out
}

// startOfWeek returns the start of the week containing t, snapped to the
// given weekday (0=Sunday).
func startOfWeek(t time.Time, startDay time.Weekday) time.Time {
	days := (int(t.Weekday()) - int(startDay) + 7) % 7
	return model.DayStart(t.AddDate(0, 0, -days))
}

// GuardrailStatus is the status of a single budget period.
type GuardrailStatus struct {
	Label   string  // "Daily", "Weekly", "Monthly"
	Limit   float64 // configured limit (USD); 0 = no limit
	Spend   float64 // current spend (USD)
	Pct     float64 // spend/limit * 100 (0 when no limit)
	State   string  // "ok", "warn", "over", "unlimited"
}

// State color thresholds per TASK.md:
//   - 80% yellow (warn)
//   - 100% red (over)
func guardrailState(pct float64, hasLimit bool) string {
	if !hasLimit {
		return "unlimited"
	}
	switch {
	case pct >= 100:
		return "over"
	case pct >= 80:
		return "warn"
	default:
		return "ok"
	}
}

// Guardrails computes the guardrail status for each configured budget period.
func Guardrails(ss []model.Session, cfg *config.Config, now time.Time) []GuardrailStatus {
	spend := SpendByPeriod(ss, now)
	periods := []struct {
		label string
		limit float64
		spent float64
	}{
		{"Daily", cfg.BudgetDaily, spend.Daily},
		{"Weekly", cfg.BudgetWeekly, spend.Weekly},
		{"Monthly", cfg.BudgetMonthly, spend.Monthly},
	}
	out := make([]GuardrailStatus, 0, len(periods))
	for _, p := range periods {
		hasLimit := p.limit > 0
		pct := 0.0
		if hasLimit {
			pct = p.spent / p.limit * 100
		}
		out = append(out, GuardrailStatus{
			Label: p.label,
			Limit: p.limit,
			Spend: p.spent,
			Pct:   pct,
			State: guardrailState(pct, hasLimit),
		})
	}
	return out
}

// GaugeColor returns the color threshold state for a budget gauge.
// Per TASK.md #3: green <60%, yellow 60-80%, red >80%.
func GaugeColor(pct float64) string {
	switch {
	case pct >= 80:
		return "red"
	case pct >= 60:
		return "yellow"
	default:
		return "green"
	}
}

// Gauge renders a single budget gauge line (label + progress bar + pct).
// width is the bar width in cells.
func Gauge(status GuardrailStatus, width int) string {
	if status.Limit <= 0 {
		return fmt.Sprintf("%-8s %s  (no limit, spent %s)",
			status.Label,
			report.FormatCost(status.Spend, false),
			report.FormatCost(status.Spend, false))
	}
	bar := gaugeBar(status.Pct, width, GaugeColor(status.Pct))
	return fmt.Sprintf("%-8s %s  %s  %.0f%%  (%s / %s)",
		status.Label,
		bar,
		stateTag(status.State),
		status.Pct,
		report.FormatCost(status.Spend, false),
		report.FormatCost(status.Limit, false),
	)
}

// gaugeBar renders a colored progress bar. color is "green"/"yellow"/"red".
func gaugeBar(pct float64, width int, color string) string {
	if width < 4 {
		width = 4
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(float64(width) * pct / 100)
	if filled > width {
		filled = width
	}
	empty := width - filled
	colorCode := map[string]string{
		"green":  "\x1b[38;5;42m",
		"yellow": "\x1b[38;5;220m",
		"red":    "\x1b[38;5;203m",
	}[color]
	reset := "\x1b[0m"
	return colorCode + repeatRune('█', filled) + repeatRune('░', empty) + reset
}

func repeatRune(r rune, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

func stateTag(state string) string {
	switch state {
	case "over":
		return "\x1b[38;5;203mOVER\x1b[0m"
	case "warn":
		return "\x1b[38;5;220mWARN\x1b[0m"
	case "unlimited":
		return "\x1b[38;5;240m----\x1b[0m"
	default:
		return "\x1b[38;5;42m OK \x1b[0m"
	}
}

// ---- Plan tracking (#6) ----

// PlanStatus tracks ACU usage against the configured Devin plan limit.
type PlanStatus struct {
	Plan        string  // configured plan name
	PlanMonthly float64 // monthly plan cost (USD)
	ACULimit    float64 // monthly ACU limit (0 = unlimited)
	ACUUsed     float64 // ACU consumed this month
	Overage     float64 // ACU beyond limit (0 if within)
	Pct         float64 // used/limit * 100 (0 when no limit)
}

// PlanUsage computes plan tracking for the current month.
func PlanUsage(ss []model.Session, cfg *config.Config, now time.Time) PlanStatus {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	var used float64
	for i := range ss {
		s := &ss[i]
		t := s.LastActivityAt
		if t.IsZero() {
			t = s.CreatedAt
		}
		if t.Before(monthStart) {
			continue
		}
		used += s.ACUCost
	}
	st := PlanStatus{
		Plan:        cfg.Plan,
		PlanMonthly: cfg.PlanMonthly,
		ACULimit:    cfg.PlanACULimit,
		ACUUsed:     used,
	}
	if cfg.PlanACULimit > 0 {
		st.Pct = used / cfg.PlanACULimit * 100
		if used > cfg.PlanACULimit {
			st.Overage = used - cfg.PlanACULimit
		}
	}
	return st
}

// ---- Subscription savings (#5) ----

// SubscriptionSavings compares API-equivalent spend vs the configured plan.
type SubscriptionSavings struct {
	Plan         string
	PlanMonthly  float64
	APIEquivalent float64 // estimated API cost for the month
	Savings      float64 // APIEquivalent - PlanMonthly
	SavingsPct   float64 // Savings / APIEquivalent * 100
}

// ComputeSavings compares this month's API-equivalent spend to the plan cost.
func ComputeSavings(ss []model.Session, cfg *config.Config, now time.Time) SubscriptionSavings {
	spend := SpendByPeriod(ss, now)
	out := SubscriptionSavings{
		Plan:          cfg.Plan,
		PlanMonthly:   cfg.PlanMonthly,
		APIEquivalent: spend.Monthly,
	}
	if cfg.PlanMonthly > 0 && spend.Monthly > cfg.PlanMonthly {
		out.Savings = spend.Monthly - cfg.PlanMonthly
		out.SavingsPct = out.Savings / spend.Monthly * 100
	}
	return out
}
