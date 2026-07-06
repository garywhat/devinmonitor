package budget

import (
	"sort"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// CostSummary aggregates all per-unit cost metrics across a set of sessions.
type CostSummary struct {
	TotalCost      float64
	TotalSessions  int
	TotalRequests  int
	TotalTokens    int64
	PerRequest     float64
	PerSession     float64
	PerToken       float64
	PerDay         float64
	ActiveDays     int
	MostExpensive  []SessionCostRow
	AvgPerSession  float64
}

// SessionCostRow is a single session ranked by cost.
type SessionCostRow struct {
	ID         string
	Title      string
	Model      string
	Cost       float64
	Estimated  bool
	Requests   int
	CreatedAt  time.Time
}

// ComputeCostSummary computes the full set of cost metrics from sessions.
func ComputeCostSummary(ss []model.Session, now time.Time) CostSummary {
	var out CostSummary
	rows := make([]SessionCostRow, 0, len(ss))
	days := map[string]bool{}
	var totalCost float64
	var totalRequests int
	var totalTokens int64

	for i := range ss {
		s := &ss[i]
		cost, est := report.SessionCost(s)
		rows = append(rows, SessionCostRow{
			ID:        s.ID,
			Title:     s.Title,
			Model:     s.Model,
			Cost:      cost,
			Estimated: est,
			Requests:  s.AssistantCount,
			CreatedAt: s.CreatedAt,
		})
		totalCost += cost
		totalRequests += s.AssistantCount
		totalTokens += s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
		t := s.LastActivityAt
		if t.IsZero() {
			t = s.CreatedAt
		}
		days[model.DayStart(t).Format("2006-01-02")] = true
	}

	out.TotalCost = totalCost
	out.TotalSessions = len(ss)
	out.TotalRequests = totalRequests
	out.TotalTokens = totalTokens
	out.ActiveDays = len(days)
	if totalRequests > 0 {
		out.PerRequest = totalCost / float64(totalRequests)
	}
	if len(ss) > 0 {
		out.PerSession = totalCost / float64(len(ss))
		out.AvgPerSession = out.PerSession
	}
	if totalTokens > 0 {
		out.PerToken = totalCost / float64(totalTokens)
	}
	if out.ActiveDays > 0 {
		out.PerDay = totalCost / float64(out.ActiveDays)
	}

	// Sort by cost descending for MostExpensive.
	sort.Slice(rows, func(i, j int) bool { return rows[i].Cost > rows[j].Cost })
	out.MostExpensive = rows
	return out
}

// TopN returns the top N most expensive sessions.
func (c CostSummary) TopN(n int) []SessionCostRow {
	if n <= 0 {
		return nil
	}
	if n > len(c.MostExpensive) {
		n = len(c.MostExpensive)
	}
	return c.MostExpensive[:n]
}

// Breakdown returns a model.CostBreakdown view of the summary.
func (c CostSummary) Breakdown() model.CostBreakdown {
	return model.CostBreakdown{
		PerRequest: c.PerRequest,
		PerSession: c.PerSession,
		PerToken:   c.PerToken,
		PerDay:     c.PerDay,
	}
}
