package trends

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/model"
)

// ---- 3. Activity Heatmap (#15) ----

// BuildHeatmap builds a 7×24 weekday×hour grid counting assistant message
// activity across all sessions.
func BuildHeatmap(ss []model.Session) []model.HeatmapCell {
	grid := [7][24]int{}
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" {
				continue
			}
			wd := int(m.CreatedAt.Weekday())
			hr := m.CreatedAt.Hour()
			grid[wd][hr]++
		}
	}
	cells := make([]model.HeatmapCell, 0, 7*24)
	maxCount := 0
	for wd := 0; wd < 7; wd++ {
		for hr := 0; hr < 24; hr++ {
			c := grid[wd][hr]
			if c > maxCount {
				maxCount = c
			}
		}
	}
	for wd := 0; wd < 7; wd++ {
		for hr := 0; hr < 24; hr++ {
			cells = append(cells, model.HeatmapCell{
				Weekday: wd,
				Hour:    hr,
				Count:   grid[wd][hr],
			})
		}
	}
	return cells
}

// heatLevel maps a count to a 0-4 intensity level based on the max.
func heatLevel(count, max int) int {
	if max <= 0 || count <= 0 {
		return 0
	}
	ratio := float64(count) / float64(max)
	switch {
	case ratio >= 0.75:
		return 4
	case ratio >= 0.5:
		return 3
	case ratio >= 0.25:
		return 2
	default:
		return 1
	}
}

// heatColors map intensity level → ANSI 256-color code.
var heatColors = []string{"238", "22", "28", "34", "40"}

// RenderHeatmap renders the weekday×hour activity grid with color intensity.
func RenderHeatmap(cells []model.HeatmapCell) string {
	if len(cells) == 0 {
		return "(no data)"
	}
	grid := [7][24]model.HeatmapCell{}
	for _, c := range cells {
		grid[c.Weekday][c.Hour] = c
	}
	maxCount := 0
	for _, c := range cells {
		if c.Count > maxCount {
			maxCount = c.Count
		}
	}

	var b strings.Builder
	b.WriteString("Activity heatmap (weekday × hour)\n\n")
	// Hour header (every 3 hours).
	b.WriteString("    ")
	for hr := 0; hr < 24; hr += 3 {
		b.WriteString(fmt.Sprintf("%-9d", hr))
	}
	b.WriteString("\n")

	weekdays := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	for wd := 0; wd < 7; wd++ {
		b.WriteString(fmt.Sprintf("%-3s ", weekdays[wd]))
		for hr := 0; hr < 24; hr++ {
			c := grid[wd][hr]
			ch := "█"
			if c.Count == 0 {
				ch = "·"
			}
			color := heatColors[heatLevel(c.Count, maxCount)]
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(ch))
		}
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("\nPeak: %d msgs/hr\n", maxCount))
	b.WriteString("Intensity: ")
	for lvl := 0; lvl <= 4; lvl++ {
		color := heatColors[lvl]
		ch := "█"
		if lvl == 0 {
			ch = "·"
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(ch))
	}
	b.WriteString(" (less → more)\n")
	return b.String()
}

// ---- 4. Contribution Calendar (#16) ----

// BuildContributionCalendar builds a full-year GitHub-style contribution
// calendar. Each day is colored by activity level (0-4).
func BuildContributionCalendar(ss []model.Session, year int) []model.ContributionDay {
	if year == 0 {
		year = time.Now().Year()
	}
	// Count assistant messages per day.
	counts := map[string]int{}
	costs := map[string]float64{}
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" {
				continue
			}
			key := m.CreatedAt.Format("2006-01-02")
			counts[key]++
			if m.Metrics != nil {
				p := model.LookupPricing(m.GenerationModel)
				if p.InputPerM == 0 && p.OutputPerM == 0 && !p.Free {
					p = model.LookupPricing(s.Model)
				}
				costs[key] += model.EstimateCost(p, m.Metrics.InputTokens, m.Metrics.OutputTokens,
					m.Metrics.CacheReadTokens, m.Metrics.CacheWriteTokens)
			}
		}
	}
	// Build full year of days.
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.Local)
	maxCount := 0
	var days []model.ContributionDay
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		c := counts[key]
		if c > maxCount {
			maxCount = c
		}
		days = append(days, model.ContributionDay{
			Date:  d,
			Count: c,
			Cost:  costs[key],
		})
	}
	for i := range days {
		days[i].Level = heatLevel(days[i].Count, maxCount)
	}
	return days
}

// RenderContributionCalendar renders the contribution calendar GitHub-style:
// weeks as columns, weekdays as rows.
func RenderContributionCalendar(days []model.ContributionDay, year int) string {
	if len(days) == 0 {
		return "(no data)"
	}
	if year == 0 {
		year = days[0].Date.Year()
	}
	// Organize into weeks (columns). Each week is 7 weekday cells.
	// Align so the first column starts on Sunday.
	type week [7]*model.ContributionDay
	var weeks []week
	// Pad leading empty days so Jan 1 lands on its weekday column.
	firstWD := int(days[0].Date.Weekday())
	cur := week{}
	for i := 0; i < firstWD; i++ {
		cur[i] = nil
	}
	col := firstWD
	for i := range days {
		cur[col] = &days[i]
		col++
		if col == 7 {
			weeks = append(weeks, cur)
			cur = week{}
			col = 0
		}
	}
	if col > 0 {
		weeks = append(weeks, cur)
	}

	totalCount := 0
	totalCost := 0.0
	for _, d := range days {
		totalCount += d.Count
		totalCost += d.Cost
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Contribution calendar %d — %d contributions, %s\n\n", year, totalCount, reportCost(totalCost)))
	// Month labels aligned to week columns.
	b.WriteString("    ")
	for w, wk := range weeks {
		if len(wk) == 0 {
			continue
		}
		// Print month name at the first week whose Sunday is in a new month.
		var firstDay *model.ContributionDay
		for _, d := range wk {
			if d != nil {
				firstDay = d
				break
			}
		}
		if firstDay == nil {
			b.WriteString("  ")
			continue
		}
		if w == 0 || firstDay.Date.Day() <= 7 {
			b.WriteString(firstDay.Date.Format("Jan")[:3])
		} else {
			b.WriteString("   ")
		}
	}
	b.WriteString("\n")

	weekdays := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	for row := 0; row < 7; row++ {
		b.WriteString(fmt.Sprintf("%-3s ", weekdays[row]))
		for _, wk := range weeks {
			d := wk[row]
			if d == nil {
				b.WriteString(" ")
				continue
			}
			color := heatColors[d.Level]
			ch := "█"
			if d.Count == 0 {
				ch = "·"
			}
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(ch))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nLess ")
	for lvl := 0; lvl <= 4; lvl++ {
		color := heatColors[lvl]
		ch := "█"
		if lvl == 0 {
			ch = "·"
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(ch))
	}
	b.WriteString(" More\n")
	return b.String()
}

// reportCost formats a cost for inline display in chart headers.
func reportCost(c float64) string {
	return fmt.Sprintf("$%.2f", c)
}
