package export

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ReportSummary is the aggregated usage window used by the shareable report.
type ReportSummary struct {
	Window    string
	From      time.Time
	To        time.Time
	Sessions  int
	Requests  int
	InputTok  int64
	OutputTok int64
	Cost      float64
	Daily     []report.TimeRow
}

// BuildReportSummary aggregates sessions created within the last `days` days
// into a ReportSummary. days<=0 means "all time".
func BuildReportSummary(ss []model.Session, days int) ReportSummary {
	now := time.Now()
	var from time.Time
	window := "all time"
	if days > 0 {
		from = now.AddDate(0, 0, -days)
		window = fmt.Sprintf("last %d days", days)
	}

	sum := ReportSummary{Window: window, From: from, To: now}
	for _, s := range ss {
		if !from.IsZero() && s.CreatedAt.Before(from) {
			continue
		}
		sum.Sessions++
		sum.Requests += s.AssistantCount
		sum.InputTok += s.InputTokens
		sum.OutputTok += s.OutputTokens
		sum.Cost += sessionCostCSV(&s)
	}
	sum.Daily = report.BuildDaily(ss)
	return sum
}

// WriteReport writes a text-based shareable usage receipt.
func WriteReport(w io.Writer, ss []model.Session, days int) error {
	sum := BuildReportSummary(ss, days)
	var b strings.Builder
	b.WriteString("========================================\n")
	b.WriteString(" DevinMonitor Usage Report\n")
	b.WriteString("========================================\n")
	fmt.Fprintf(&b, "Window:    %s\n", sum.Window)
	if !sum.From.IsZero() {
		fmt.Fprintf(&b, "From:      %s\n", sum.From.Format("2006-01-02"))
	}
	fmt.Fprintf(&b, "To:        %s\n", sum.To.Format("2006-01-02"))
	b.WriteString("----------------------------------------\n")
	fmt.Fprintf(&b, "Sessions:  %d\n", sum.Sessions)
	fmt.Fprintf(&b, "Requests:  %d\n", sum.Requests)
	fmt.Fprintf(&b, "Input:     %s tokens\n", report.FormatTok(sum.InputTok))
	fmt.Fprintf(&b, "Output:    %s tokens\n", report.FormatTok(sum.OutputTok))
	fmt.Fprintf(&b, "Cost:      $%.2f\n", sum.Cost)
	b.WriteString("----------------------------------------\n")
	if len(sum.Daily) > 0 {
		b.WriteString("Daily breakdown (date  cost  requests):\n")
		for _, d := range sum.Daily {
			if d.Cost == 0 && d.Requests == 0 {
				continue
			}
			fmt.Fprintf(&b, "  %s  $%.2f  %d\n", d.Label, d.Cost, d.Requests)
		}
	}
	b.WriteString("========================================\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteReportSVG writes a simple SVG bar chart of daily cost for the window.
// It is intentionally minimal so it can be embedded in READMEs / PR comments.
func WriteReportSVG(w io.Writer, ss []model.Session, days int) error {
	sum := BuildReportSummary(ss, days)
	const width = 760
	const height = 240
	const pad = 40
	const barW = 18

	points := sum.Daily
	// Trim to the requested window for the chart.
	if days > 0 && len(points) > days {
		points = points[len(points)-days:]
	}
	var maxCost float64
	for _, p := range points {
		if p.Cost > maxCost {
			maxCost = p.Cost
		}
	}
	if maxCost == 0 {
		maxCost = 1
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`+"\n", width, height, width, height)
	b.WriteString(`<rect width="100%" height="100%" fill="#1e1e2e"/>` + "\n")
	fmt.Fprintf(&b, `<text x="%d" y="24" fill="#cba6f7" font-family="sans-serif" font-size="16">DevinMonitor — %s</text>`+"\n", pad, svgEscape(sum.Window))
	chartH := height - pad - 30
	n := len(points)
	for i, p := range points {
		x := pad + i*(barW+4)
		h := int(float64(chartH) * p.Cost / maxCost)
		y := height - 30 - h
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="%d" fill="#89b4fa"/>`+"\n", x, y, barW, h)
		if i%7 == 0 {
			fmt.Fprintf(&b, `<text x="%d" y="%d" fill="#9399b2" font-family="sans-serif" font-size="10" text-anchor="middle">%s</text>`+"\n", x+barW/2, height-12, p.Label[5:])
		}
	}
	_ = n
	fmt.Fprintf(&b, `<text x="%d" y="%d" fill="#f9e2af" font-family="sans-serif" font-size="12">$%.2f total</text>`+"\n", width-160, 24, sum.Cost)
	b.WriteString("</svg>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func svgEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
