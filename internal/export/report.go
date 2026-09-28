package export

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/status"
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
	// CostBasis names the meter behind Cost, so a receipt that prints one
	// dollar figure also says whether it is Devin's ACU billing or our token
	// arithmetic (or both, added together).
	CostBasis status.CostBasis
	Daily     []report.TimeRow
}

// BuildReportSummary aggregates sessions created within the last `days` days
// into a ReportSummary. days<=0 means "all time".
//
// The daily breakdown describes the SAME window as the totals above it. It used
// to be built from the unfiltered slice, so `report --days 1` printed "last 1
// days" over a breakdown listing every date in the database, and --days 1/7/30
// produced byte-identical breakdowns. Both facts are captured by
// report_test.go, and both are what a reader of a usage receipt would call a
// lie rather than a rounding difference.
func BuildReportSummary(ss []model.Session, days int) ReportSummary {
	now := time.Now()
	var from time.Time
	window := "all time"
	if days > 0 {
		from = now.AddDate(0, 0, -days)
		window = fmt.Sprintf("last %d days", days)
	}

	sum := ReportSummary{Window: window, From: from, To: now}
	windowed := make([]model.Session, 0, len(ss))
	bases := make([]status.CostBasis, 0, len(ss))
	for _, s := range ss {
		if !from.IsZero() && s.CreatedAt.Before(from) {
			continue
		}
		windowed = append(windowed, s)
		bases = append(bases, SessionCostBasis(&s))
		sum.Sessions++
		sum.Requests += s.AssistantCount
		sum.InputTok += s.InputTokens
		sum.OutputTok += s.OutputTokens
		sum.Cost += sessionCostCSV(&s)
	}
	// The receipt's cost figure carries the meter it came from; "mixed" here
	// means the receipt is adding ACU-billed and token-estimated sessions.
	sum.CostBasis = status.CombineCostBasis(bases...)
	// One population for the totals and the breakdown; the text writer and the
	// SVG writer both read sum.Daily, so they cannot disagree about the window.
	sum.Daily = windowDaily(report.BuildDaily(windowed), from)
	return sum
}

// receiptDaily is the set of buckets a usage receipt actually shows.
//
// BuildDaily can produce a bucket with neither requests nor cost: it keys cost
// and sub-agent attribution by the session's LAST activity, so a session whose
// only day-level trace is a sub-agent call creates a bucket with Sessions=1 and
// Requests=0. A "date $0.00 0" line is noise, so the text breakdown has always
// skipped those — but the SVG did not, and one zero-height bar next to no line
// is a silent disagreement about how much data the window holds. Both writers
// now ask this function, so the two are consistent by construction rather than
// by two copies of the same condition.
func receiptDaily(rows []report.TimeRow) []report.TimeRow {
	out := make([]report.TimeRow, 0, len(rows))
	for _, d := range rows {
		if d.Cost == 0 && d.Requests == 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}

// windowDaily drops buckets that fall before the window start.
//
// It is belt-and-braces on top of filtering the sessions: BuildDaily buckets by
// MESSAGE time while the window filters by SESSION creation time, and the two
// can disagree for a session that was resumed long after it was created. A
// receipt that says "last 1 days" must not contain a date from last month, no
// matter which timestamp put it there.
func windowDaily(rows []report.TimeRow, from time.Time) []report.TimeRow {
	if from.IsZero() {
		return rows
	}
	start := model.DayStart(from)
	out := make([]report.TimeRow, 0, len(rows))
	for _, r := range rows {
		t, err := time.Parse("2006-01-02", r.Label)
		if err == nil && t.Before(start) {
			continue
		}
		out = append(out, r)
	}
	return out
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
	fmt.Fprintf(&b, "Cost:      $%.2f %s\n", sum.Cost, sum.CostBasis.Tag())
	b.WriteString(sum.CostBasis.Legend() + "\n")
	b.WriteString("----------------------------------------\n")
	if len(sum.Daily) > 0 {
		b.WriteString("Daily breakdown (date  cost  requests):\n")
		for _, d := range receiptDaily(sum.Daily) {
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

	// sum.Daily already covers exactly the requested window (BuildReportSummary
	// windows it), so there is no second trim here. The old `points[len(points)-
	// days:]` cut by BAR COUNT rather than by days and fought the text writer:
	// on a 7-day window that spans 8 calendar dates the SVG drew 7 bars while
	// the text printed 8 lines, which is the kind of disagreement a one-glance
	// report cannot afford.
	//
	// Both writers also go through receiptDaily, so a bucket that is not worth a
	// line (no requests AND no cost) is not worth a bar either.
	points := receiptDaily(sum.Daily)
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
	// The total carries its basis too, and is anchored to the right edge so a
	// long tag cannot run off the canvas.
	fmt.Fprintf(&b, `<text x="%d" y="24" fill="#f9e2af" font-family="sans-serif" font-size="12" text-anchor="end">$%.2f total %s</text>`+"\n",
		width-pad, sum.Cost, svgEscape(sum.CostBasis.Tag()))
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
