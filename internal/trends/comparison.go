package trends

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ---- 7. Period Comparison (#19) ----

// bucketMetrics holds the 8 metrics compared between two periods.
type bucketMetrics struct {
	Sessions         int
	Requests         int
	InputTokens      int64
	OutputTokens     int64
	CacheRead        int64
	TotalTokens      int64
	Cost             float64
	AvgCostPerSess   float64
}

// metricNames is the ordered list of 8 metrics rendered in the comparison table.
var metricNames = []string{
	"Sessions", "Requests", "Input tokens", "Output tokens",
	"Cache read", "Total tokens", "Cost", "Avg $/session",
}

// computeBucketMetrics aggregates sessions whose activity falls within
// [start, end).
func computeBucketMetrics(ss []model.Session, start, end time.Time) bucketMetrics {
	var m bucketMetrics
	for _, s := range ss {
		// A session belongs to the period if it has any assistant message
		// within [start, end), or its LastActivityAt falls in range.
		inRange := false
		for _, msg := range s.Messages {
			if msg.Role != "assistant" {
				continue
			}
			if !msg.CreatedAt.Before(start) && msg.CreatedAt.Before(end) {
				inRange = true
				m.Requests++
				if msg.Metrics != nil {
					m.InputTokens += msg.Metrics.InputTokens
					m.OutputTokens += msg.Metrics.OutputTokens
					m.CacheRead += msg.Metrics.CacheReadTokens
				}
			}
		}
		if !inRange {
			continue
		}
		m.Sessions++
		cost, _ := report.SessionCost(&s)
		// Attribute only a proportional share if the session spans periods;
		// for simplicity we attribute the full session cost to each period it
		// touches (consistent with how BuildDaily attributes credit cost).
		m.Cost += cost
	}
	m.TotalTokens = m.InputTokens + m.OutputTokens + m.CacheRead
	if m.Sessions > 0 {
		m.AvgCostPerSess = m.Cost / float64(m.Sessions)
	}
	return m
}

// BuildPeriodComparison compares two time periods side by side and computes
// the per-metric percentage delta.
func BuildPeriodComparison(ss []model.Session, curStart, curEnd, prevStart, prevEnd time.Time) *model.PeriodComparison {
	cur := computeBucketMetrics(ss, curStart, curEnd)
	prev := computeBucketMetrics(ss, prevStart, prevEnd)

	delta := map[string]float64{}
	delta["Sessions"] = pctDelta(float64(prev.Sessions), float64(cur.Sessions))
	delta["Requests"] = pctDelta(float64(prev.Requests), float64(cur.Requests))
	delta["Input tokens"] = pctDelta(float64(prev.InputTokens), float64(cur.InputTokens))
	delta["Output tokens"] = pctDelta(float64(prev.OutputTokens), float64(cur.OutputTokens))
	delta["Cache read"] = pctDelta(float64(prev.CacheRead), float64(cur.CacheRead))
	delta["Total tokens"] = pctDelta(float64(prev.TotalTokens), float64(cur.TotalTokens))
	delta["Cost"] = pctDelta(prev.Cost, cur.Cost)
	delta["Avg $/session"] = pctDelta(prev.AvgCostPerSess, cur.AvgCostPerSess)

	return &model.PeriodComparison{
		Current: model.TimeBucket{
			Label:       curStart.Format("2006-01-02") + " ~ " + curEnd.Add(-time.Second).Format("2006-01-02"),
			Requests:    cur.Requests,
			InputTokens: cur.InputTokens,
			OutputTokens: cur.OutputTokens,
			CacheRead:   cur.CacheRead,
			CreditCost:  cur.Cost,
		},
		Previous: model.TimeBucket{
			Label:       prevStart.Format("2006-01-02") + " ~ " + prevEnd.Add(-time.Second).Format("2006-01-02"),
			Requests:    prev.Requests,
			InputTokens: prev.InputTokens,
			OutputTokens: prev.OutputTokens,
			CacheRead:   prev.CacheRead,
			CreditCost:  prev.Cost,
		},
		DeltaPct: delta,
	}
}

// pctDelta returns the percentage change from prev to cur.
// Returns 0 when prev is 0 (avoid div-by-zero); +100 when prev==0 && cur>0.
func pctDelta(prev, cur float64) float64 {
	if prev == 0 {
		if cur > 0 {
			return 100
		}
		return 0
	}
	return (cur - prev) / prev * 100
}

// RenderPeriodComparison renders a side-by-side comparison table with the
// 8 metrics, their current/previous values, and a color-coded delta %.
func RenderPeriodComparison(pc *model.PeriodComparison) string {
	if pc == nil {
		return "(no data)"
	}
	cur := pc.Current
	prev := pc.Previous

	curM := bucketMetrics{
		Requests:    cur.Requests,
		InputTokens: cur.InputTokens,
		OutputTokens: cur.OutputTokens,
		CacheRead:   cur.CacheRead,
		TotalTokens: cur.InputTokens + cur.OutputTokens + cur.CacheRead,
		Cost:        cur.CreditCost,
	}
	prevM := bucketMetrics{
		Requests:    prev.Requests,
		InputTokens: prev.InputTokens,
		OutputTokens: prev.OutputTokens,
		CacheRead:   prev.CacheRead,
		TotalTokens: prev.InputTokens + prev.OutputTokens + prev.CacheRead,
		Cost:        prev.CreditCost,
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Period comparison\n  Current:  %s\n  Previous: %s\n\n", cur.Label, prev.Label))

	b.WriteString(fmt.Sprintf("  %-16s %14s %14s %10s\n", "Metric", "Current", "Previous", "Delta"))
	b.WriteString("  " + strings.Repeat("─", 56) + "\n")

	rows := []struct {
		name string
		cur  string
		prev string
	}{
		{"Sessions", fmt.Sprintf("%d", cur.Requests), fmt.Sprintf("%d", prev.Requests)},
		{"Requests", fmt.Sprintf("%d", curM.Requests), fmt.Sprintf("%d", prevM.Requests)},
		{"Input tokens", report.FormatTok(curM.InputTokens), report.FormatTok(prevM.InputTokens)},
		{"Output tokens", report.FormatTok(curM.OutputTokens), report.FormatTok(prevM.OutputTokens)},
		{"Cache read", report.FormatTok(curM.CacheRead), report.FormatTok(prevM.CacheRead)},
		{"Total tokens", report.FormatTok(curM.TotalTokens), report.FormatTok(prevM.TotalTokens)},
		{"Cost", report.FormatCost(curM.Cost, false), report.FormatCost(prevM.Cost, false)},
		{"Avg $/session", report.FormatCost(curM.Cost/float64(max1(cur.Requests)), false),
			report.FormatCost(prevM.Cost/float64(max1(prev.Requests)), false)},
	}
	for _, r := range rows {
		delta := pc.DeltaPct[r.name]
		b.WriteString(fmt.Sprintf("  %-16s %14s %14s %s\n",
			r.name, r.cur, r.prev, formatDelta(delta)))
	}
	return b.String()
}

// formatDelta renders a delta percentage with color and a +/- sign.
func formatDelta(pct float64) string {
	sign := "+"
	if pct < 0 {
		sign = ""
	}
	s := fmt.Sprintf("%s%.1f%%", sign, pct)
	switch {
	case pct > 0:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(s)
	case pct < 0:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("41")).Render(s)
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(s)
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// ---- 8. Month-over-Month Comparison (#20) ----

// BuildMonthOverMonth compares the current calendar month to the previous
// calendar month.
func BuildMonthOverMonth(ss []model.Session) *model.PeriodComparison {
	now := time.Now()
	curStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	curEnd := curStart.AddDate(0, 1, 0)
	prevStart := curStart.AddDate(0, -1, 0)
	prevEnd := curStart
	return BuildPeriodComparison(ss, curStart, curEnd, prevStart, prevEnd)
}

// RenderMonthOverMonth renders the month-over-month comparison. It reuses
// the period comparison renderer; the labels already reflect the month
// ranges, so no separate header is needed.
func RenderMonthOverMonth(pc *model.PeriodComparison) string {
	if pc == nil {
		return "(no data)"
	}
	return "Month-over-month\n" + RenderPeriodComparison(pc)
}
