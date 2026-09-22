package budget

import (
	"math"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
)

// fixedNow is a Wednesday, so a week and a month are both partly elapsed and
// period bucketing has something to separate.
var fixedNow = time.Date(2026, 9, 16, 14, 30, 0, 0, time.Local)

// TestGuardrailStateBoundaries pins the state machine. The thresholds are
// inclusive (`>= 80` warns, `>= 100` is over), and a period with no configured
// limit must be "unlimited" rather than a division by zero.
func TestGuardrailStateBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		pct      float64
		hasLimit bool
		want     string
	}{
		{"no limit is unlimited", 0, false, "unlimited"},
		{"no limit ignores a bogus pct", 250, false, "unlimited"},
		{"zero spend", 0, true, "ok"},
		{"just below warn", 79.9, true, "ok"},
		{"exactly at warn", 80, true, "warn"},
		{"between warn and over", 99.9, true, "warn"},
		{"exactly at the limit", 100, true, "over"},
		{"over the limit", 140, true, "over"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := guardrailState(c.pct, c.hasLimit); got != c.want {
				t.Errorf("guardrailState(%v, %v) = %q, want %q", c.pct, c.hasLimit, got, c.want)
			}
		})
	}
}

// TestGuardrailsZeroLimitsAreSafe is the guard against the classic divide-by-
// zero: an all-zero config must report "unlimited" with a zero percentage, and
// the percentage must never be NaN.
func TestGuardrailsZeroLimitsAreSafe(t *testing.T) {
	ss := []model.Session{spendSession("s1", fixedNow.Add(-time.Hour), 5.0)}
	got := Guardrails(ss, &config.Config{}, fixedNow)
	if len(got) != 3 {
		t.Fatalf("Guardrails returned %d statuses, want 3", len(got))
	}
	for _, g := range got {
		if g.State != "unlimited" {
			t.Errorf("%s: state = %q, want unlimited", g.Label, g.State)
		}
		if g.Pct != 0 {
			t.Errorf("%s: pct = %v, want 0", g.Label, g.Pct)
		}
		if math.IsNaN(g.Pct) || math.IsInf(g.Pct, 0) {
			t.Errorf("%s: pct is NaN/Inf", g.Label)
		}
	}
}

// TestGuardrailsComputesPercentage checks spend/limit and the derived state for
// a limit that is actually configured.
func TestGuardrailsComputesPercentage(t *testing.T) {
	ss := []model.Session{spendSession("s1", fixedNow.Add(-time.Hour), 8.0)}
	cfg := &config.Config{BudgetDaily: 10.0}
	daily := Guardrails(ss, cfg, fixedNow)[0]
	if daily.Label != "Daily" {
		t.Fatalf("first status is %q, want Daily", daily.Label)
	}
	if daily.Spend != 8.0 {
		t.Errorf("Spend = %v, want 8", daily.Spend)
	}
	if daily.Pct != 80.0 {
		t.Errorf("Pct = %v, want 80", daily.Pct)
	}
	if daily.State != "warn" {
		t.Errorf("State = %q, want warn", daily.State)
	}
}

// TestSpendByPeriodBucketing checks that a session is counted in exactly the
// periods its last activity falls inside, and that zero/negative-cost sessions
// are skipped.
func TestSpendByPeriodBucketing(t *testing.T) {
	ss := []model.Session{
		spendSession("today", fixedNow.Add(-time.Hour), 1.0),
		spendSession("this-week", fixedNow.AddDate(0, 0, -2), 2.0),
		spendSession("this-month", fixedNow.AddDate(0, 0, -20), 4.0),
		spendSession("older", fixedNow.AddDate(0, -3, 0), 8.0),
		spendSession("free", fixedNow.Add(-time.Hour), 0.0),
	}
	got := SpendByPeriod(ss, fixedNow)
	if got.Daily != 1.0 {
		t.Errorf("Daily = %v, want 1", got.Daily)
	}
	if got.Weekly != 3.0 {
		t.Errorf("Weekly = %v, want 3 (today + 2 days ago)", got.Weekly)
	}
	// The month started on the 1st and now is the 16th, so 20 days ago is in
	// August; only today's and the 2-days-ago sessions count.
	// (2 days before the 16th is the 14th, still inside September.)
	if got.Monthly != 1.0+2.0 {
		t.Errorf("Monthly = %v, want 3", got.Monthly)
	}
	if got.Daily > got.Weekly || got.Weekly > got.Monthly {
		t.Errorf("periods are not nested: daily=%v weekly=%v monthly=%v",
			got.Daily, got.Weekly, got.Monthly)
	}
}

// TestGaugeColorThresholds pins the gauge palette.
func TestGaugeColorThresholds(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{0, "green"}, {59.9, "green"}, {60, "yellow"}, {79.9, "yellow"},
		{80, "red"}, {200, "red"},
	}
	for _, c := range cases {
		if got := GaugeColor(c.pct); got != c.want {
			t.Errorf("GaugeColor(%v) = %q, want %q", c.pct, got, c.want)
		}
	}
}

// TestGaugeWithoutLimit pins the no-limit rendering, which must not print a
// percentage or a bar.
func TestGaugeWithoutLimit(t *testing.T) {
	out := Gauge(GuardrailStatus{Label: "Daily", Limit: 0, Spend: 3}, 10)
	if out == "" {
		t.Fatal("Gauge returned an empty string")
	}
	if got := out; len(got) < 6 || got[:6] != "Daily " {
		t.Errorf("Gauge output %q does not start with the label", got)
	}
}

// TestComputeCostSummaryEmpty is the smoke check for the aggregator: an empty
// input must produce zeroed, finite averages rather than NaN.
func TestComputeCostSummaryEmpty(t *testing.T) {
	got := ComputeCostSummary(nil, fixedNow)
	if got.TotalCost != 0 || got.TotalSessions != 0 {
		t.Errorf("empty summary = %+v, want zeroed totals", got)
	}
	for name, v := range map[string]float64{
		"PerRequest": got.PerRequest, "PerSession": got.PerSession,
		"PerToken": got.PerToken, "PerDay": got.PerDay, "AvgPerSession": got.AvgPerSession,
	} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("%s is NaN/Inf on empty input", name)
		}
	}
}

// TestComputeBurnRateZeroConfig is the same guard for the burn-rate path: with
// no budget configured and no usage, nothing may divide by zero.
func TestComputeBurnRateZeroConfig(t *testing.T) {
	got := ComputeBurnRate(nil, &config.Config{}, fixedNow)
	for name, v := range map[string]float64{
		"PerHour": got.PerHour, "PerDay": got.PerDay,
		"PerWeek": got.PerWeek, "PerMonth": got.PerMonth,
	} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("%s is NaN/Inf", name)
		}
	}
}

// spendSession builds a session with an exact, deterministic cost.
//
// CreditCost is Devin's own accounting and report.SessionCost prefers it when
// non-zero, so the figure is exact. Deriving tokens from a desired USD amount
// instead would round (8/15*1e6 truncates), which made assertions like
// "spend == 8" fail by 5e-6.
func spendSession(id string, lastActivity time.Time, cost float64) model.Session {
	return model.Session{
		ID:             id,
		Model:          "claude-sonnet-4-5",
		CreditCost:     cost,
		CreatedAt:      lastActivity.Add(-time.Hour),
		LastActivityAt: lastActivity,
		AssistantCount: 1,
	}
}
