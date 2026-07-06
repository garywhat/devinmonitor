// Package filterexport wires the filter, search, and export features (WT4)
// into cobra commands via the cli.Register registry. main.go imports this
// package (blank or otherwise) during integration so the init() runs.
//
// All commands here are self-contained: they open their own reader from the
// inherited --data-dir persistent flag and render output to stdout.
package filterexport

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/export"
	"github.com/garywhat/devinmonitor/internal/filter"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// filteringReader is the reader facet exposing the v1Reader extension methods.
// reader.Reader only guarantees Sessions/Session/DBPath; the search and filter
// helpers live on the concrete *v1Reader, so we type-assert to this interface.
type filteringReader interface {
	reader.Reader
	FilteredSessions(opts model.FilterOptions) ([]model.Session, error)
	SearchMessages(query string, limit int) ([]model.SearchResult, error)
}

func init() {
	cli.Register(cmdFilter)
	cli.Register(cmdSearch)
	cli.Register(cmdReport)
	cli.Register(cmdStatus)
	cli.Register(cmdBackup)
	cli.Register(cmdExport)
}

// openReader opens the session DB using the inherited --data-dir flag.
func openReader(cmd *cobra.Command) reader.Reader {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	r, err := reader.Open(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open reader: %v\n", err)
		os.Exit(1)
	}
	return r
}

func asFiltering(r reader.Reader) (filteringReader, bool) {
	fr, ok := r.(filteringReader)
	return fr, ok
}

// ---- filter (#42, #43, #44, #45) ----

func cmdFilter() *cobra.Command {
	c := &cobra.Command{
		Use:   "filter",
		Short: i18n.T("cmd.filter"),
		Run: func(cmd *cobra.Command, args []string) {
			modelName, _ := cmd.Flags().GetString("model")
			project, _ := cmd.Flags().GetString("project")
			mode, _ := cmd.Flags().GetString("mode")
			fromStr, _ := cmd.Flags().GetString("from-date")
			toStr, _ := cmd.Flags().GetString("to-date")
			exclude, _ := cmd.Flags().GetString("exclude")
			sortBy, _ := cmd.Flags().GetString("sort")
			asJSON, _ := cmd.Flags().GetBool("json")

			from, err := filter.ParseDate(fromStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --from-date: %v\n", err)
				os.Exit(1)
			}
			to, err := filter.ParseDate(toStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --to-date: %v\n", err)
				os.Exit(1)
			}
			// Inclusive end: add 24h so a --to-date of the same day includes it.
			if !to.IsZero() {
				to = to.Add(24 * 60 * 60 * 1e9) // +1 day
			}

			opts := model.FilterOptions{
				Model:    modelName,
				Project:  project,
				Mode:     mode,
				FromDate: from,
				ToDate:   to,
				SortBy:   sortBy,
				SortDesc: true,
			}

			r := openReader(cmd)
			defer r.Close()

			var ss []model.Session
			if fr, ok := asFiltering(r); ok && (modelName != "" || project != "" || mode != "") {
				ss, err = fr.FilteredSessions(opts)
			} else {
				ss, err = r.Sessions()
				if err == nil {
					ss = filter.Apply(ss, opts)
				}
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "read sessions: %v\n", err)
				os.Exit(1)
			}
			ss = filter.ExcludeProjects(ss, exclude)
			if sortBy != "" {
				filter.SortBy(ss, sortBy, true)
			}

			if asJSON {
				writeJSONSessionList(ss)
				return
			}
			renderSessionTable(ss)
		},
	}
	c.Flags().String("model", "", "filter by model name (substring)")
	c.Flags().String("project", "", "filter by project (substring on working dir)")
	c.Flags().String("mode", "", "filter by agent mode (normal|plan|bypass)")
	c.Flags().String("from-date", "", "from date YYYY-MM-DD (inclusive)")
	c.Flags().String("to-date", "", "to date YYYY-MM-DD (inclusive)")
	c.Flags().String("exclude", "", "comma-separated project substrings to exclude")
	c.Flags().String("sort", "", "sort by: cost|tokens|context|duration|recent")
	c.Flags().Bool("json", false, "output as JSON")
	return c
}

// ---- search (#46) ----

func cmdSearch() *cobra.Command {
	c := &cobra.Command{
		Use:   "search <query>",
		Short: i18n.T("cmd.search"),
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			limit, _ := cmd.Flags().GetInt("limit")
			asJSON, _ := cmd.Flags().GetBool("json")
			query := args[0]

			r := openReader(cmd)
			defer r.Close()
			fr, ok := asFiltering(r)
			if !ok {
				fmt.Fprintln(os.Stderr, "search not supported by this reader")
				os.Exit(1)
			}
			results, err := fr.SearchMessages(query, limit)
			if err != nil {
				fmt.Fprintf(os.Stderr, "search: %v\n", err)
				os.Exit(1)
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(results)
				return
			}
			if len(results) == 0 {
				fmt.Println("No matches.")
				return
			}
			t := ui.NewTable(i18n.T("common.session"), i18n.T("common.node"), i18n.T("common.role"), i18n.T("common.timestamp"), i18n.T("common.snippet"))
			for _, sr := range results {
				t.Row(sr.SessionID, fmt.Sprintf("%d", sr.NodeID), sr.Role,
					sr.Timestamp.Format("2006-01-02 15:04"), sr.Snippet)
			}
			fmt.Println(t.String())
		},
	}
	c.Flags().Int("limit", 50, "max results")
	c.Flags().Bool("json", false, "output as JSON")
	return c
}

// ---- report (#51) ----

func cmdReport() *cobra.Command {
	c := &cobra.Command{
		Use:   "report",
		Short: i18n.T("cmd.report"),
		Run: func(cmd *cobra.Command, args []string) {
			days, _ := cmd.Flags().GetInt("days")
			month, _ := cmd.Flags().GetBool("month")
			asSVG, _ := cmd.Flags().GetBool("svg")
			asJSON, _ := cmd.Flags().GetBool("json")

			if month {
				days = 30
			}
			if days <= 0 {
				days = 7
			}

			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "read sessions: %v\n", err)
				os.Exit(1)
			}

			if asJSON {
				sum := export.BuildReportSummary(ss, days)
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(sum)
				return
			}
			if asSVG {
				if err := export.WriteReportSVG(os.Stdout, ss, days); err != nil {
					fmt.Fprintf(os.Stderr, "svg: %v\n", err)
					os.Exit(1)
				}
				return
			}
			if err := export.WriteReport(os.Stdout, ss, days); err != nil {
				fmt.Fprintf(os.Stderr, "report: %v\n", err)
				os.Exit(1)
			}
		},
	}
	c.Flags().Int("days", 7, "report window in days")
	c.Flags().Bool("month", false, "report last 30 days")
	c.Flags().Bool("svg", false, "output an SVG chart instead of text")
	c.Flags().Bool("json", false, "output as JSON")
	return c
}

// ---- status (#53, #54, #55, #56, #57) ----

func cmdStatus() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: i18n.T("cmd.status"),
		Run: func(cmd *cobra.Command, args []string) {
			compact, _ := cmd.Flags().GetBool("compact")
			shell, _ := cmd.Flags().GetBool("shell")
			writeState, _ := cmd.Flags().GetBool("write-state")
			stateFile, _ := cmd.Flags().GetString("state-file")
			setTitle, _ := cmd.Flags().GetBool("set-title")
			titleFmt, _ := cmd.Flags().GetString("title-format")
			asJSON, _ := cmd.Flags().GetBool("json")

			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "read sessions: %v\n", err)
				os.Exit(1)
			}
			snap := export.BuildStatusSnapshot(ss)

			if writeState {
				if err := export.WriteState(snap, stateFile); err != nil {
					fmt.Fprintf(os.Stderr, "write state: %v\n", err)
					os.Exit(1)
				}
			}

			switch {
			case asJSON:
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(snap)
			case shell:
				fmt.Println(export.ShellStatus(snap))
			case setTitle:
				export.SetTerminalTitle(os.Stdout, export.FormatTitle(snap, titleFmt))
			case compact:
				fmt.Println(export.CompactStatus(snap))
			default:
				fmt.Println(export.CompactStatus(snap))
			}
		},
	}
	c.Flags().Bool("compact", false, "single-line status: Today: $X | Month: $Y | Sessions: Z")
	c.Flags().Bool("shell", false, "output just today's cost number (for PS1)")
	c.Flags().Bool("write-state", false, "write snapshot to state file")
	c.Flags().String("state-file", "", "path to state file (with --write-state)")
	c.Flags().Bool("set-title", false, "set terminal title from snapshot")
	c.Flags().String("title-format", "{cost} {sessions}", "title template: {cost} {sessions} {month}")
	c.Flags().Bool("json", false, "output as JSON")
	return c
}

// ---- backup (#58) ----

func cmdBackup() *cobra.Command {
	c := &cobra.Command{
		Use:   "backup",
		Short: i18n.T("cmd.backup"),
		Run: func(cmd *cobra.Command, args []string) {
			output, _ := cmd.Flags().GetString("output")
			asDB, _ := cmd.Flags().GetBool("db")

			r := openReader(cmd)
			defer r.Close()

			if asDB {
				dst := output
				if dst == "" {
					dst = "sessions-backup.db"
				}
				if err := export.CopyDB(r.DBPath(), dst); err != nil {
					fmt.Fprintf(os.Stderr, "backup db: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Backed up DB to %s\n", dst)
				return
			}

			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "read sessions: %v\n", err)
				os.Exit(1)
			}
			out := os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					fmt.Fprintf(os.Stderr, "create output: %v\n", err)
					os.Exit(1)
				}
				defer f.Close()
				out = f
			}
			if err := export.WriteBackupJSON(out, ss, r.DBPath()); err != nil {
				fmt.Fprintf(os.Stderr, "backup json: %v\n", err)
				os.Exit(1)
			}
			if output != "" {
				fmt.Printf("Backed up to %s\n", output)
			}
		},
	}
	c.Flags().String("output", "", "output file (default stdout for JSON, sessions-backup.db for --db)")
	c.Flags().Bool("db", false, "copy the raw sessions.db file instead of JSON")
	return c
}

// ---- export (#49, #50, #52) ----

func cmdExport() *cobra.Command {
	c := &cobra.Command{
		Use:   "export",
		Short: i18n.T("cmd.export"),
		Run: func(cmd *cobra.Command, args []string) {
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("output")
			detailed, _ := cmd.Flags().GetBool("detailed")

			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "read sessions: %v\n", err)
				os.Exit(1)
			}

			out := os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					fmt.Fprintf(os.Stderr, "create output: %v\n", err)
					os.Exit(1)
				}
				defer f.Close()
				out = f
			}

			switch format {
			case "csv":
				if err := export.WriteCSV(out, ss); err != nil {
					fmt.Fprintf(os.Stderr, "csv: %v\n", err)
					os.Exit(1)
				}
			case "markdown", "md":
				if err := export.WriteMarkdown(out, ss); err != nil {
					fmt.Fprintf(os.Stderr, "markdown: %v\n", err)
					os.Exit(1)
				}
			case "html":
				if err := export.WriteHTML(out, ss); err != nil {
					fmt.Fprintf(os.Stderr, "html: %v\n", err)
					os.Exit(1)
				}
			case "json", "":
				doc := export.BuildDocument(ss, detailed)
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(doc); err != nil {
					fmt.Fprintf(os.Stderr, "json: %v\n", err)
					os.Exit(1)
				}
			default:
				fmt.Fprintf(os.Stderr, "unknown format %q (use csv|markdown|html|json)\n", format)
				os.Exit(1)
			}
			if output != "" {
				fmt.Printf("Exported %d sessions (%s) to %s\n", len(ss), format, output)
			}
		},
	}
	c.Flags().String("format", "json", "export format: csv|markdown|html|json")
	c.Flags().String("output", "", "output file (default stdout)")
	c.Flags().Bool("detailed", false, "include per-request detail (JSON only)")
	return c
}

// ---- output helpers ----

func renderSessionTable(ss []model.Session) {
	rows := report.BuildSessionRows(ss)
	if len(rows) == 0 {
		fmt.Println("No sessions match.")
		return
	}
	t := ui.NewTable(i18n.T("common.id"), i18n.T("common.title"), i18n.T("common.model"), i18n.T("common.mode"), i18n.T("common.project"), i18n.T("common.requests"), i18n.T("common.input"), i18n.T("common.output"), i18n.T("common.duration"), i18n.T("common.cost"))
	t.RightAlign(5, 6, 7, 9)
	var totReq int
	var totIn, totOut int64
	var totDur time.Duration
	var totCost float64
	anyEstimated := false
	for _, row := range rows {
		costStr := report.FormatCost(row.Cost, row.IsFree)
		if row.CostEstimated {
			costStr += " est"
		}
		t.Row(row.ID, row.Title, row.Model, row.Mode, row.Project,
			fmt.Sprintf("%d", row.Requests),
			report.FormatTok(row.InputTok),
			report.FormatTok(row.OutputTok),
			report.FormatDur(row.Duration),
			costStr)
		totReq += row.Requests
		totIn += row.InputTok
		totOut += row.OutputTok
		totDur += row.Duration
		totCost += row.Cost
		if row.CostEstimated {
			anyEstimated = true
		}
	}
	totCostStr := report.FormatCost(totCost, false)
	if anyEstimated {
		totCostStr += " est"
	}
	t.TotalRow(
		i18n.T("common.totals"), "", "", "", "",
		fmt.Sprintf("%d", totReq),
		report.FormatTok(totIn),
		report.FormatTok(totOut),
		report.FormatDur(totDur),
		totCostStr,
	)
	fmt.Println(t.String())
}

func writeJSONSessionList(ss []model.Session) {
	items := make([]model.SessionListItem, 0, len(ss))
	for _, s := range ss {
		dur := s.LastActivityAt.Sub(s.CreatedAt)
		cost, _ := report.SessionCost(&s)
		items = append(items, model.SessionListItem{
			ID:       s.ID,
			Title:    s.Title,
			Model:    s.Model,
			Project:  s.WorkingDir,
			Cost:     cost,
			Tokens:   s.InputTokens + s.OutputTokens,
			Duration: report.FormatDur(dur),
			Status:   s.AgentMode,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(items)
}
