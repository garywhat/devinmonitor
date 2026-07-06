package trends

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// ---- 5. Delta Banner (#17) ----

// snapshot is the persisted last-check state used to compute deltas.
type snapshot struct {
	Cost   float64 `json:"cost"`
	Tokens int64   `json:"tokens"`
	When   string  `json:"when"`
}

// snapshotPath returns the temp file used to store the last snapshot.
func snapshotPath() string {
	return filepath.Join(os.TempDir(), "devinmonitor_snapshot.json")
}

// loadSnapshot reads the previous snapshot. Returns a zero snapshot if
// the file is missing or unreadable.
func loadSnapshot() snapshot {
	var sn snapshot
	data, err := os.ReadFile(snapshotPath())
	if err != nil {
		return sn
	}
	_ = json.Unmarshal(data, &sn)
	return sn
}

// saveSnapshot persists the current snapshot for the next run.
func saveSnapshot(sn snapshot) error {
	data, err := json.Marshal(sn)
	if err != nil {
		return err
	}
	return os.WriteFile(snapshotPath(), data, 0644)
}

// currentTotals computes total cost and tokens across all sessions.
func currentTotals(ss []model.Session) (float64, int64) {
	var cost float64
	var tokens int64
	for i := range ss {
		s := &ss[i]
		c, _ := report.SessionCost(s)
		cost += c
		tokens += s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
	}
	return cost, tokens
}

// RenderDeltaBanner compares the current totals to the last persisted
// snapshot and renders a "since last check" banner. The current totals are
// then persisted for the next invocation.
func RenderDeltaBanner(ss []model.Session) string {
	curCost, curTokens := currentTotals(ss)
	prev := loadSnapshot()

	var costDelta, tokenDelta string
	if prev.When != "" {
		costDelta = signedCost(curCost - prev.Cost)
		tokenDelta = signedInt(curTokens - prev.Tokens)
	} else {
		costDelta = report.FormatCost(curCost, false)
		tokenDelta = report.FormatTok(curTokens)
	}

	now := time.Now()
	_ = saveSnapshot(snapshot{
		Cost:   curCost,
		Tokens: curTokens,
		When:   now.Format(time.RFC3339),
	})

	label := "since last check"
	if prev.When == "" {
		label = "first check (no prior snapshot)"
	} else if t, err := time.Parse(time.RFC3339, prev.When); err == nil {
		label = "since last check " + humanizeAge(now.Sub(t))
	}

	body := fmt.Sprintf("Δ %s:  %s cost,  %s tokens", label, costDelta, tokenDelta)
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39")).
		Background(lipgloss.Color("236")).
		Padding(0, 2).
		Render(" " + body + " ")
}

// signedCost formats a signed USD delta with a + or - prefix.
func signedCost(d float64) string {
	if d >= 0 {
		return fmt.Sprintf("+$%.2f", d)
	}
	return fmt.Sprintf("-$%.2f", -d)
}

// signedInt formats a signed integer delta with a + or - prefix.
func signedInt(d int64) string {
	if d >= 0 {
		return fmt.Sprintf("+%s", report.FormatTok(d))
	}
	return fmt.Sprintf("-%s", report.FormatTok(-d))
}

// humanizeAge renders a duration as a compact "2h30m ago" style string.
func humanizeAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return "just now"
	}
}

// ---- 6. Sparklines in Tables (#18) ----

// sessionSparkline builds a sparkline of per-request output tokens for a
// single session, showing the token trend across the session's lifetime.
func sessionSparkline(s *model.Session, width int) string {
	var pts []float64
	for _, m := range s.Messages {
		if m.Role != "assistant" || m.Metrics == nil {
			continue
		}
		pts = append(pts, float64(m.Metrics.OutputTokens))
	}
	if len(pts) == 0 {
		return ""
	}
	return ui.Sparkline(pts, width)
}

// RenderSparklineTable renders the session table with an inline sparkline
// column showing each session's per-request token trend.
func RenderSparklineTable(ss []model.Session) string {
	rows := report.BuildSessionRows(ss)
	if len(rows) == 0 {
		return "(no sessions)"
	}
	// Index sessions by ID for quick message lookup.
	byID := map[string]*model.Session{}
	for i := range ss {
		byID[ss[i].ID] = &ss[i]
	}

	t := ui.NewTable(
		i18n.T("common.id"), i18n.T("common.title"), i18n.T("common.model"), i18n.T("common.project"),
		i18n.T("common.requests"), i18n.T("common.cost"), i18n.T("common.trend"),
	).RightAlign(4)

	var totReqs int
	var totCost float64
	for _, r := range rows {
		s := byID[r.ID]
		spark := ""
		if s != nil {
			spark = sessionSparkline(s, 20)
		}
		costStr := report.FormatCost(r.Cost, r.IsFree)
		if r.CostEstimated && r.Cost > 0 {
			costStr += " est"
		}
		t.Row(
			r.ID,
			r.Title,
			r.Model,
			r.Project,
			fmt.Sprintf("%d", r.Requests),
			costStr,
			spark,
		)
		totReqs += r.Requests
		totCost += r.Cost
	}
	t.TotalRow(
		i18n.T("common.totals"), "", "", "",
		fmt.Sprintf("%d", totReqs),
		report.FormatCost(totCost, false),
		"",
	)
	return t.String()
}

// RenderSparklineSummary renders a compact multi-sparkline summary of daily
// cost, input tokens, and output tokens over the given period.
func RenderSparklineSummary(rows []report.TimeRow, days int, width int) string {
	pts := BuildTrendPoints(rows, days)
	if len(pts) == 0 {
		return "(no data)"
	}
	costs := make([]float64, len(pts))
	inputs := make([]float64, len(pts))
	outputs := make([]float64, len(pts))
	for i, p := range pts {
		costs[i] = p.Cost
		// TrendPoint only carries total tokens; approximate input/output split
		// from the underlying rows when available.
		if i < len(rows) {
			inputs[i] = float64(rows[len(rows)-len(pts)+i].InputTok)
			outputs[i] = float64(rows[len(rows)-len(pts)+i].OutputTok)
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Daily cost     %s\n", ui.Sparkline(costs, width)))
	b.WriteString(fmt.Sprintf("Input tokens   %s\n", ui.Sparkline(inputs, width)))
	b.WriteString(fmt.Sprintf("Output tokens  %s\n", ui.Sparkline(outputs, width)))
	return b.String()
}
