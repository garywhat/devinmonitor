package live

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
)

// RenderSessionDetailPopup renders a full-detail popup for a single session.
// Shows: title, model, cost breakdown, token breakdown, tool usage, duration, messages count.
func RenderSessionDetailPopup(s *model.Session, width, height int) string {
	if width < 40 {
		width = 40
	}
	if height < 10 {
		height = 10
	}

	modelName := s.LatestModel
	if modelName == "" {
		modelName = s.Model
	}

	dur := s.LastActivityAt.Sub(s.CreatedAt)
	cost, est := report.SessionCost(s)
	p := model.LookupPricing(modelName)
	if cost == 0 && !p.Free && p.InputPerM > 0 {
		cost = model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
		est = true
	}
	costStr := report.FormatCost(cost, p.Free)
	if est && cost > 0 {
		costStr += " (est)"
	}

	totalTok := s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite

	var b strings.Builder
	b.WriteString(titleStyle.Render("Session Detail") + "\n")
	b.WriteString(strings.Repeat("─", width-4) + "\n")
	b.WriteString(fmt.Sprintf("%s  %s\n", labelStyle.Render("ID:"), valueStyle.Render(s.ID)))
	b.WriteString(fmt.Sprintf("%s  %s\n", labelStyle.Render("Title:"), valueStyle.Render(s.Title)))
	b.WriteString(fmt.Sprintf("%s  %s\n", labelStyle.Render("Model:"), valueStyle.Render(modelName)))
	b.WriteString(fmt.Sprintf("%s  %s\n", labelStyle.Render("Mode:"), valueStyle.Render(s.AgentMode)))
	b.WriteString(fmt.Sprintf("%s  %s\n", labelStyle.Render("Project:"), valueStyle.Render(s.WorkingDir)))
	b.WriteString(fmt.Sprintf("%s  %s\n", labelStyle.Render("Duration:"), valueStyle.Render(report.FormatDur(dur))))
	b.WriteString(fmt.Sprintf("%s  %d\n", labelStyle.Render("Messages:"), len(s.Messages)))
	b.WriteString(fmt.Sprintf("%s  %d\n", labelStyle.Render("Requests:"), s.AssistantCount))
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Token Breakdown") + "\n")
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Input:"), tokenStyle.Render(report.FormatTok(s.InputTokens))))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Output:"), tokenStyle.Render(report.FormatTok(s.OutputTokens))))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Cache Read:"), tokenStyle.Render(report.FormatTok(s.CacheRead))))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Cache Write:"), tokenStyle.Render(report.FormatTok(s.CacheWrite))))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Total:"), tokenStyle.Render(report.FormatTok(totalTok))))
	b.WriteString("\n")
	b.WriteString(titleStyle.Render("Cost Breakdown") + "\n")
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Session Cost:"), costStyle.Render(costStr)))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Credit:"), costStyle.Render(fmt.Sprintf("$%.4f", s.CreditCost))))
	b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("ACU:"), costStyle.Render(fmt.Sprintf("$%.4f", s.ACUCost))))
	if s.AssistantCount > 0 {
		b.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("Per Request:"), costStyle.Render(fmt.Sprintf("$%.4f", cost/float64(s.AssistantCount)))))
	}
	b.WriteString("\n")

	// Tool usage
	if len(s.ToolCalls) > 0 {
		b.WriteString(titleStyle.Render("Tool Usage") + "\n")
		names := reader.SortedToolNames(s.ToolCalls)
		total := 0
		for _, c := range s.ToolCalls {
			total += c
		}
		for _, n := range names {
			c := s.ToolCalls[n]
			pct := float64(c) / float64(total) * 100
			b.WriteString(fmt.Sprintf("  %-16s %4d  %s\n", n, c, progressBar(pct, 20)))
		}
		b.WriteString("\n")
	}

	// Sub-agents
	if len(s.SubAgentCalls) > 0 {
		b.WriteString(titleStyle.Render("Sub-Agents") + "\n")
		for i, sa := range s.SubAgentCalls {
			bg := ""
			if sa.IsBackground {
				bg = " [bg]"
			}
			title := sa.Title
			if title == "" {
				title = "-"
			}
			b.WriteString(fmt.Sprintf("  %d. [%s] %s%s\n", i+1, sa.Profile, title, bg))
		}
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render("Press Esc to close"))

	content := b.String()
	return centerPopup(content, width, height)
}

// modelBreakdownRow holds per-model aggregated stats.
type modelBreakdownRow struct {
	Name      string
	Requests  int
	InputTok  int64
	OutputTok int64
	Cost      float64
	TTFTP50   float64
	TotalP50  float64
	CacheHit  float64
}

// RenderModelBreakdownPopup renders a popup showing per-model distribution
// with an ASCII pie chart of cost distribution.
func RenderModelBreakdownPopup(ss []model.Session, width, height int) string {
	if width < 50 {
		width = 50
	}
	if height < 12 {
		height = 12
	}

	// Aggregate per-model stats.
	agg := map[string]*modelBreakdownRow{}
	for _, s := range ss {
		modelName := s.LatestModel
		if modelName == "" {
			modelName = s.Model
		}
		row, ok := agg[modelName]
		if !ok {
			row = &modelBreakdownRow{Name: modelName}
			agg[modelName] = row
		}
		row.Requests += s.AssistantCount
		row.InputTok += s.InputTokens
		row.OutputTok += s.OutputTokens

		cost, _ := report.SessionCost(&s)
		p := model.LookupPricing(modelName)
		if cost == 0 && !p.Free && p.InputPerM > 0 {
			cost = model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
		}
		row.Cost += cost

		// Latency.
		for _, msg := range s.Messages {
			if msg.Role != "assistant" || msg.Metrics == nil {
				continue
			}
			if msg.Metrics.TTFTMs > 0 {
				// accumulate for percentile later (simplified: just track)
			}
		}

		// Cache hit rate.
		totalIn := s.CacheRead + s.InputTokens
		if totalIn > 0 {
			row.CacheHit += float64(s.CacheRead) / float64(totalIn) * 100
		}
	}

	// Sort by cost descending.
	var rows []modelBreakdownRow
	for _, r := range agg {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Cost > rows[j].Cost })

	var b strings.Builder
	b.WriteString(titleStyle.Render("Model Breakdown") + "\n")
	b.WriteString(strings.Repeat("─", width-4) + "\n")

	// Table header.
	b.WriteString(fmt.Sprintf("  %-24s %6s %8s %8s %9s %6s\n",
		"Model", "Reqs", "Input", "Output", "Cost", "Cache%"))
	b.WriteString(strings.Repeat("·", width-4) + "\n")

	totalCost := 0.0
	for _, r := range rows {
		cachePct := 0.0
		if r.Requests > 0 {
			cachePct = r.CacheHit / float64(r.Requests)
		}
		b.WriteString(fmt.Sprintf("  %-24s %6d %8s %8s %9s %5.1f%%\n",
			truncateStr(r.Name, 24),
			r.Requests,
			report.FormatTok(r.InputTok),
			report.FormatTok(r.OutputTok),
			report.FormatCost(r.Cost, false),
			cachePct))
		totalCost += r.Cost
	}
	b.WriteString(fmt.Sprintf("  %s %9s\n", labelStyle.Render("Total:"), costStyle.Render(report.FormatCost(totalCost, false))))

	// ASCII pie chart of cost distribution.
	if len(rows) > 0 && totalCost > 0 {
		b.WriteString("\n")
		b.WriteString(titleStyle.Render("Cost Distribution") + "\n")
		chart := asciiPieChart(rows, totalCost, width-6)
		b.WriteString(chart)
	}

	b.WriteString("\n" + dimStyle.Render("Press Esc to close"))

	content := b.String()
	return centerPopup(content, width, height)
}

// asciiPieChart renders a simple horizontal bar "pie" chart showing
// cost distribution across models.
func asciiPieChart(rows []modelBreakdownRow, total float64, width int) string {
	if width < 20 {
		width = 20
	}
	colors := []lipgloss.Color{
		lipgloss.Color("39"),
		lipgloss.Color("41"),
		lipgloss.Color("220"),
		lipgloss.Color("203"),
		lipgloss.Color("99"),
		lipgloss.Color("42"),
		lipgloss.Color("208"),
		lipgloss.Color("129"),
	}
	var b strings.Builder
	// Build the bar.
	bar := strings.Builder{}
	for i, r := range rows {
		pct := r.Cost / total * 100
		segLen := int(float64(width) * pct / 100)
		if segLen < 1 {
			segLen = 1
		}
		color := colors[i%len(colors)]
		seg := strings.Repeat("█", segLen)
		bar.WriteString(lipgloss.NewStyle().Foreground(color).Render(seg))
	}
	b.WriteString(bar.String() + "\n\n")
	// Legend.
	for i, r := range rows {
		if i >= 8 {
			b.WriteString(fmt.Sprintf("  ... %d more models\n", len(rows)-8))
			break
		}
		pct := r.Cost / total * 100
		color := colors[i%len(colors)]
		swatch := lipgloss.NewStyle().Foreground(color).Render("█")
		b.WriteString(fmt.Sprintf("  %s %-24s %5.1f%%  %s\n",
			swatch, truncateStr(r.Name, 24), pct, report.FormatCost(r.Cost, false)))
	}
	return b.String()
}

// centerPopup wraps content in a bordered box and centers it within the
// given width/height. Used for all popup overlays.
func centerPopup(content string, width, height int) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Padding(1, 2).
		Width(width - 4).
		Render(content)

	return lipgloss.Place(width, height,
		lipgloss.Center, lipgloss.Center,
		box,
		lipgloss.WithWhitespaceChars(" "),
	)
}

// truncateStr truncates s to maxLen display width, appending "…" if truncated.
func truncateStr(s string, maxLen int) string {
	w := lipgloss.Width(s)
	if w <= maxLen {
		return s
	}
	// Simple rune-based truncation.
	runes := []rune(s)
	for i := len(runes) - 1; i >= 0; i-- {
		truncated := string(runes[:i]) + "…"
		if lipgloss.Width(truncated) <= maxLen {
			return truncated
		}
	}
	return "…"
}
