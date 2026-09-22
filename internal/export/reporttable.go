package export

import (
	"strconv"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ReportTypes lists the report types accepted by `export <report_type>`
// (excluding the session-oriented default, which uses the richer session
// writers). Kept in sync with BuildReportTable.
var ReportTypes = []string{"daily", "weekly", "monthly", "models", "projects", "agents"}

// IsReportType reports whether t is a supported non-session report type.
func IsReportType(t string) bool {
	for _, x := range ReportTypes {
		if x == t {
			return true
		}
	}
	return false
}

// ReportTypeList returns the supported types as a "|"-joined list for help and
// error messages, including the session default.
func ReportTypeList() string {
	return "sessions|" + strings.Join(ReportTypes, "|")
}

// BuildReportTable builds a generic tabular export for a report type.
//
// It returns ok=false for an unknown type. The session-oriented report is
// intentionally not handled here: `export sessions` uses the richer session
// writers (WriteCSV / WriteMarkdown / WriteHTML / BuildDocument) so the
// existing output stays byte-for-byte compatible.
func BuildReportTable(reportType string, ss []model.Session) (Table, bool) {
	switch reportType {
	case "daily":
		return timeTable("date", report.BuildDaily(ss), nil), true
	case "weekly":
		// Label by week-start date (e.g. 2026-08-17), matching the raw-date
		// style of daily/monthly so the export stays machine-parseable. The
		// ISO week label (2026-W34) is still shown by the `weekly` command UI.
		return timeTable("week_start", report.BuildWeekly(ss, 1 /* Monday */), nil), true
	case "monthly":
		return timeTable("month", report.BuildMonthly(ss), nil), true
	case "models":
		return modelTable(report.BuildModelRows(ss)), true
	case "projects":
		return projectTable(report.BuildProjectRows(ss)), true
	case "agents":
		return agentTable(report.BuildAgentStats(ss)), true
	default:
		return Table{}, false
	}
}

func timeTable(labelHeader string, rows []report.TimeRow, labelFn func(string) string) Table {
	t := Table{Headers: []string{
		labelHeader, "requests", "sessions", "subagents",
		"input", "output", "cache_read", "cache_write", "cost",
	}}
	for _, r := range rows {
		label := r.Label
		if labelFn != nil {
			label = labelFn(label)
		}
		t.Rows = append(t.Rows, []string{
			label,
			strconv.Itoa(r.Requests),
			strconv.Itoa(r.Sessions),
			strconv.Itoa(r.SubAgents),
			strconv.FormatInt(r.InputTok, 10),
			strconv.FormatInt(r.OutputTok, 10),
			strconv.FormatInt(r.CacheRead, 10),
			strconv.FormatInt(r.CacheWrite, 10),
			formatCost(r.Cost),
		})
	}
	return t
}

func modelTable(rows []report.ModelRow) Table {
	t := Table{Headers: []string{
		"model", "requests", "sessions", "input", "output", "cache_read",
		"cache_write", "cost", "cost_pct", "ttft_p50_ms", "ttft_p95_ms",
		"total_p50_ms", "total_p95_ms", "tok_per_sec_p50", "trunc_pct",
	}}
	for _, r := range rows {
		t.Rows = append(t.Rows, []string{
			r.Name,
			strconv.Itoa(r.Requests),
			strconv.Itoa(r.Sessions),
			strconv.FormatInt(r.InputTok, 10),
			strconv.FormatInt(r.OutputTok, 10),
			strconv.FormatInt(r.CacheRead, 10),
			strconv.FormatInt(r.CacheWrite, 10),
			formatCost(r.CreditCost + r.ACUCost + r.EstCost),
			strconv.FormatFloat(r.CostPct, 'f', 2, 64),
			strconv.FormatFloat(r.TTFTP50, 'f', 1, 64),
			strconv.FormatFloat(r.TTFTP95, 'f', 1, 64),
			strconv.FormatFloat(r.TotalP50, 'f', 1, 64),
			strconv.FormatFloat(r.TotalP95, 'f', 1, 64),
			strconv.FormatFloat(r.TokPerSecP50, 'f', 1, 64),
			strconv.FormatFloat(r.TruncPct, 'f', 2, 64),
		})
	}
	return t
}

func projectTable(rows []report.ProjectRow) Table {
	t := Table{Headers: []string{
		"project", "sessions", "requests", "input", "output",
		"cache_read", "cache_write", "cost", "models",
	}}
	for _, r := range rows {
		t.Rows = append(t.Rows, []string{
			r.Name,
			strconv.Itoa(r.Sessions),
			strconv.Itoa(r.Requests),
			strconv.FormatInt(r.InputTok, 10),
			strconv.FormatInt(r.OutputTok, 10),
			strconv.FormatInt(r.CacheRead, 10),
			strconv.FormatInt(r.CacheWrite, 10),
			formatCost(r.Cost),
			joinModels(r.Models),
		})
	}
	return t
}

func agentTable(rows []report.AgentStats) Table {
	t := Table{Headers: []string{
		"profile", "calls", "sessions", "background", "foreground", "completed",
		"avg_duration_s", "max_duration_s", "avg_task_len", "max_task_len",
		"avg_output_len", "max_output_len",
	}}
	for _, r := range rows {
		t.Rows = append(t.Rows, []string{
			r.Profile,
			strconv.Itoa(r.Calls),
			strconv.Itoa(r.Sessions),
			strconv.Itoa(r.Background),
			strconv.Itoa(r.Foreground),
			strconv.Itoa(r.Completed),
			formatSeconds(r.AvgDuration),
			formatSeconds(r.MaxDuration),
			strconv.Itoa(r.AvgTaskLen),
			strconv.Itoa(r.MaxTaskLen),
			strconv.Itoa(r.AvgOutputLen),
			strconv.Itoa(r.MaxOutputLen),
		})
	}
	return t
}

func formatCost(v float64) string {
	return strconv.FormatFloat(v, 'f', 4, 64)
}

func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 1, 64)
}

func joinModels(models []string) string {
	return strings.Join(models, " ")
}
