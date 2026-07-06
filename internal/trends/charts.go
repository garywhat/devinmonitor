// Package trends implements trend and chart features for DevinMonitor:
// ASCII trend charts, daily cost chart, cumulative cost, stacked area,
// 24-hour usage chart, activity heatmap, contribution calendar, period
// comparison, month-over-month, delta banner, and sparklines in tables.
//
// All cobra commands self-register via cli.Register() in cmd.go's init().
package trends

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// chartHeight is the default height (in terminal rows) for ASCII charts.
const chartHeight = 10

// stackedChars are the per-model fill characters used in the stacked area
// chart. Index corresponds to the model's position in the sorted model list.
var stackedChars = []string{"█", "▓", "▒", "░", "■", "□", "#", "*", "+", "=", "~"}

// ---- Trend points ----

// BuildTrendPoints converts daily time rows into TrendPoints covering the
// last `days` days. Labels are reformatted to MM-DD for compact display.
func BuildTrendPoints(rows []report.TimeRow, days int) []model.TrendPoint {
	if len(rows) == 0 {
		return nil
	}
	if days > 0 && len(rows) > days {
		rows = rows[len(rows)-days:]
	}
	pts := make([]model.TrendPoint, 0, len(rows))
	for _, r := range rows {
		label := r.Label
		if t, err := time.Parse("2006-01-02", r.Label); err == nil {
			label = t.Format("01-02")
		}
		pts = append(pts, model.TrendPoint{
			Label:  label,
			Cost:   r.Cost,
			Tokens: r.InputTok + r.OutputTok + r.CacheRead + r.CacheWrite,
		})
	}
	return pts
}

// ---- 1. ASCII Trend Chart (#13) ----

// RenderASCIIChart renders a vertical ASCII bar chart of cost over time.
// Each point becomes one column; bar height is proportional to cost.
func RenderASCIIChart(pts []model.TrendPoint, height int) string {
	if len(pts) == 0 {
		return "(no data)"
	}
	if height <= 0 {
		height = chartHeight
	}
	values := make([]float64, len(pts))
	labels := make([]string, len(pts))
	for i, p := range pts {
		values[i] = p.Cost
		labels[i] = p.Label
	}
	return renderBarChart(values, labels, height, true)
}

// ---- 2. Daily Cost Chart (#14) ----

// RenderDailyCostChart renders a bar chart of daily cost with date labels
// on the x-axis and a cost scale on the y-axis.
func RenderDailyCostChart(rows []report.TimeRow, days int, height int) string {
	pts := BuildTrendPoints(rows, days)
	if len(pts) == 0 {
		return "(no data)"
	}
	if height <= 0 {
		height = chartHeight
	}
	values := make([]float64, len(pts))
	labels := make([]string, len(pts))
	for i, p := range pts {
		values[i] = p.Cost
		labels[i] = p.Label
	}
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	if max <= 0 {
		max = 1
	}
	body := renderBarChart(values, labels, height, true)
	header := fmt.Sprintf("Daily cost (max %s) — last %d days\n",
		report.FormatCost(max, false), len(pts))
	return header + body
}

// ---- 9. Cumulative Cost Trend (#21) ----

// RenderCumulativeCost renders a running-total step chart of cost over time.
func RenderCumulativeCost(rows []report.TimeRow, days int, height int) string {
	pts := BuildTrendPoints(rows, days)
	if len(pts) == 0 {
		return "(no data)"
	}
	if height <= 0 {
		height = chartHeight
	}
	cum := 0.0
	values := make([]float64, len(pts))
	labels := make([]string, len(pts))
	for i, p := range pts {
		cum += p.Cost
		values[i] = cum
		labels[i] = p.Label
	}
	max := cum
	if max <= 0 {
		max = 1
	}
	body := renderStepChart(values, labels, height)
	header := fmt.Sprintf("Cumulative cost (total %s) — last %d days\n",
		report.FormatCost(max, false), len(pts))
	return header + body
}

// ---- 10. Stacked Area Chart (#22) ----

// RenderStackedArea renders a per-model stacked bar chart showing how each
// model contributes to total cost over time. Each model uses a distinct
// fill character (see stackedChars).
func RenderStackedArea(rows []report.TimeRow, days int, height int) string {
	if len(rows) == 0 {
		return "(no data)"
	}
	if days > 0 && len(rows) > days {
		rows = rows[len(rows)-days:]
	}
	if height <= 0 {
		height = chartHeight
	}
	// Collect sorted model names.
	modelSet := map[string]bool{}
	for _, r := range rows {
		for mn := range r.ByModel {
			modelSet[mn] = true
		}
	}
	models := make([]string, 0, len(modelSet))
	for mn := range modelSet {
		models = append(models, mn)
	}
	sort.Strings(models)

	// Per-day per-model cost.
	perDay := make([][]float64, len(rows))
	maxTotal := 0.0
	for i, r := range rows {
		costs := make([]float64, len(models))
		total := 0.0
		for j, mn := range models {
			ms := r.ByModel[mn]
			if ms == nil {
				continue
			}
			p := model.LookupPricing(mn)
			c := model.EstimateCost(p, ms.InputTokens, ms.OutputTokens, ms.CacheRead, ms.CacheWrite)
			costs[j] = c
			total += c
		}
		// Prefer authoritative credit cost when present.
		if r.Cost > 0 && total == 0 {
			total = r.Cost
			costs[0] = r.Cost
		}
		perDay[i] = costs
		if total > maxTotal {
			maxTotal = total
		}
	}
	if maxTotal <= 0 {
		maxTotal = 1
	}

	// Cumulative rows per (day, model) in row units.
	cumRows := make([][]float64, len(rows))
	for i, costs := range perDay {
		cum := 0.0
		cr := make([]float64, len(costs))
		for j, c := range costs {
			cum += c
			cr[j] = cum / maxTotal * float64(height)
		}
		cumRows[i] = cr
	}

	var b strings.Builder
	for h := height; h >= 1; h-- {
		hf := float64(h)
		for i := range rows {
			dayCum := cumRows[i]
			ch := " "
			for j := len(models) - 1; j >= 0; j-- {
				prev := 0.0
				if j > 0 {
					prev = dayCum[j-1]
				}
				if hf > prev && hf <= dayCum[j] {
					ch = stackedChars[j%len(stackedChars)]
					break
				}
			}
			b.WriteString(ch)
		}
		b.WriteString("\n")
	}
	// Legend
	b.WriteString("\nLegend (model → char):\n")
	for j, mn := range models {
		ch := stackedChars[j%len(stackedChars)]
		b.WriteString(fmt.Sprintf("  %s %s\n", ch, mn))
	}
	return b.String()
}

// ---- 11. 24-hour Usage Chart (#23) ----

// Build24HourBuckets counts activity (assistant messages) per hour over the
// last 24 hours. Index 0 = 23 hours ago, index 23 = the current hour.
func Build24HourBuckets(ss []model.Session) [24]int {
	var buckets [24]int
	now := time.Now()
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" {
				continue
			}
			d := now.Sub(m.CreatedAt)
			if d < 0 || d >= 24*time.Hour {
				continue
			}
			idx := 23 - int(d.Hours())
			if idx < 0 {
				idx = 0
			}
			if idx >= 24 {
				idx = 23
			}
			buckets[idx]++
		}
	}
	return buckets
}

// Render24HourChart renders the last 24 hours of activity as a bar chart
// with hourly buckets.
func Render24HourChart(ss []model.Session, height int) string {
	buckets := Build24HourBuckets(ss)
	if height <= 0 {
		height = chartHeight
	}
	values := make([]float64, 24)
	labels := make([]string, 24)
	now := time.Now()
	for i := 0; i < 24; i++ {
		values[i] = float64(buckets[i])
		h := now.Add(time.Duration(-(23-i)) * time.Hour)
		labels[i] = fmt.Sprintf("%02d", h.Hour())
	}
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	body := renderBarChart(values, labels, height, true)
	header := fmt.Sprintf("Activity over last 24 hours (peak %.0f msgs/hr)\n", max)
	return header + body
}

// ---- shared renderers ----

// renderBarChart renders a vertical bar chart. Each value is one column;
// bar height is proportional to value/max. Labels are printed beneath,
// thinned to avoid overlap when there are many columns.
func renderBarChart(values []float64, labels []string, height int, colorize bool) string {
	if len(values) == 0 {
		return "(no data)"
	}
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	if max <= 0 {
		max = 1
	}
	var b strings.Builder
	for h := height; h >= 1; h-- {
		threshold := max * float64(h-1) / float64(height)
		for _, v := range values {
			if v > 0 && v >= threshold {
				if colorize {
					b.WriteString(uiBarStyle(v, max))
				} else {
					b.WriteString("█")
				}
			} else {
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	// Label row: thin labels so they don't overlap. Show every Nth label.
	step := labelStep(len(values))
	for i, l := range labels {
		if i%step == 0 {
			b.WriteString(l)
		} else {
			b.WriteString(strings.Repeat(" ", len(l)))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderStepChart renders a step/line chart using block characters that
// approximate a running total. Unlike the bar chart, only the top of each
// column is filled to suggest a line.
func renderStepChart(values []float64, labels []string, height int) string {
	if len(values) == 0 {
		return "(no data)"
	}
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	if max <= 0 {
		max = 1
	}
	// barRows[i] = how many rows tall column i is.
	barRows := make([]int, len(values))
	for i, v := range values {
		barRows[i] = int(v / max * float64(height))
		if v > 0 && barRows[i] < 1 {
			barRows[i] = 1
		}
	}
	var b strings.Builder
	for h := height; h >= 1; h-- {
		for i := range values {
			if barRows[i] == h {
				b.WriteString("█")
			} else if barRows[i] > h {
				b.WriteString("▁")
			} else {
				b.WriteString(" ")
			}
		}
		b.WriteString("\n")
	}
	step := labelStep(len(values))
	for i, l := range labels {
		if i%step == 0 {
			b.WriteString(l)
		} else {
			b.WriteString(strings.Repeat(" ", len(l)))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// labelStep returns how many columns to skip between labels so they fit.
func labelStep(n int) int {
	switch {
	case n <= 10:
		return 1
	case n <= 20:
		return 2
	case n <= 40:
		return 4
	case n <= 90:
		return 10
	default:
		return 15
	}
}

// uiBarStyle returns a color-coded block character based on the value's
// ratio to max. Uses lipgloss styles from the ui package indirectly.
func uiBarStyle(v, max float64) string {
	ratio := v / max
	switch {
	case ratio >= 0.75:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render("█")
	case ratio >= 0.5:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render("█")
	case ratio >= 0.25:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("41")).Render("█")
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("█")
	}
}
