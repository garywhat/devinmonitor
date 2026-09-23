// Package main is the devinmonitor CLI entry point.
package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/live"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/pricing"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"

	// Feature packages self-register commands via cli.Register() in init().
	_ "github.com/garywhat/devinmonitor/internal/analytics"
	_ "github.com/garywhat/devinmonitor/internal/blocks"
	_ "github.com/garywhat/devinmonitor/internal/budget"
	_ "github.com/garywhat/devinmonitor/internal/errscan"
	_ "github.com/garywhat/devinmonitor/internal/filterexport"
	_ "github.com/garywhat/devinmonitor/internal/integration"
	_ "github.com/garywhat/devinmonitor/internal/project"
	_ "github.com/garywhat/devinmonitor/internal/share"
	_ "github.com/garywhat/devinmonitor/internal/trends"
	_ "github.com/garywhat/devinmonitor/internal/tuiext"
)

var (
	flagDataDir   string
	flagLocale    string
	flagNoCost    bool
	flagBreakdown bool
	flagStartDay  string
	flagLast      int
	flagInterval  int

	// version is injected at build time via ldflags:
	//   -ldflags "-X main.version=v0.1.0"
	// Defaults to "dev" when running `go run` or `go build` without ldflags.
	version = "dev"
)

// minIntValue is a pflag.Value for an integer flag that rejects values below
// `min`. It enforces the same floor as the live poller (live.MinIntervalMs) so
// the requested --interval never diverges from the effective refresh interval.
type minIntValue struct {
	dst *int
	min int
	def int
}

func (m *minIntValue) String() string {
	if m.dst == nil {
		return fmt.Sprintf("%d", m.def)
	}
	return fmt.Sprintf("%d", *m.dst)
}

func (m *minIntValue) Set(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	if n < m.min {
		return fmt.Errorf("--interval must be >= %dms (got %d)", m.min, n)
	}
	*m.dst = n
	return nil
}

func (m *minIntValue) Type() string { return "int" }

// installPricingSources loads the user's price overrides and model aliases and
// installs them process-wide.
//
// Two sources feed overrides: the dedicated pricing.json file and the legacy
// config.customPricing map. The file wins on a conflict so the newer, schema-
// carrying source is authoritative; nothing writes the legacy map any more.
// refreshPricingCache downloads the price catalogue and stores it for next run.
//
// It is best-effort by design: a warning on stderr, never a fatal error, and a
// bounded timeout so an offline machine or a slow endpoint cannot stall the
// command. Nothing about the local machine is transmitted - the request is a
// bare GET for a public price list.
func refreshPricingCache(ttl time.Duration, now time.Time) {
	c, err := pricing.Fetch("", pricing.DefaultFetchTimeout, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: price catalogue not refreshed: %v\n", err)
		return
	}
	if err := pricing.SaveCache(pricing.CachePath(), c); err != nil {
		fmt.Fprintf(os.Stderr, "warning: price catalogue not saved: %v\n", err)
	}
}

// applyNoCost suppresses cost columns when --no-cost was passed.
//
// Only table columns are affected; cost lines inside panels and popups (the
// snapshot panel, the blocks detail view, the live dashboard) are not, and the
// comment on the flag says so. Hiding those would need a different mechanism,
// and the use case this exists for is screenshotting a table.
func applyNoCost() {
	if !flagNoCost {
		ui.HideColumns()
		return
	}
	ui.HideColumns(
		i18n.T("common.cost"),
		i18n.T("common.costPct"),
	)
}

func installPricingSources() {
	cfg := config.Global()
	now := time.Now()
	if cfg != nil && len(cfg.ModelAliases) > 0 {
		model.SetModelAliases(cfg.ModelAliases)
	}

	overrides := make(map[string]model.Pricing)
	if cfg != nil {
		for name, cp := range cfg.CustomPricing {
			overrides[name] = model.Pricing{
				Model:          name,
				InputPerM:      cp.InputPerM,
				OutputPerM:     cp.OutputPerM,
				CacheReadPerM:  cp.CacheReadPerM,
				CacheWritePerM: cp.CacheWritePerM,
			}
		}
	}

	// The fetched catalogue sits BELOW the user's own file: a refresh must never
	// override a price the user set deliberately.
	cacheTTL := pricing.DefaultCacheTTL
	if c, err := pricing.LoadCache(pricing.CachePath()); err == nil && c != nil {
		for name, o := range c.Models {
			if _, taken := overrides[name]; taken {
				continue
			}
			overrides[name] = model.Pricing{
				Model:          name,
				InputPerM:      o.InputPerM,
				OutputPerM:     o.OutputPerM,
				CacheReadPerM:  o.CacheReadPerM,
				CacheWritePerM: o.CacheWritePerM,
				Free:           o.Free,
			}
		}
		// Auto-refresh is opt-in and bounded: at most once per TTL, with a short
		// timeout, and a failure only warns. Reports are never blocked on it.
		if cfg != nil && cfg.PricingAutoFetch && pricing.NeedsRefresh(c, cacheTTL, now) {
			refreshPricingCache(cacheTTL, now)
		}
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "warning: ignoring %s: %v\n", pricing.CachePath(), err)
	}

	path := pricing.DefaultPath()
	f, err := pricing.Load(path)
	switch {
	case err != nil:
		// A malformed override file must be visible. Silently ignoring it would
		// leave the user staring at unchanged numbers after setting a price.
		fmt.Fprintf(os.Stderr, "warning: ignoring %s: %v\n", path, err)
	case f != nil:
		for name, o := range f.Models {
			overrides[name] = model.Pricing{
				Model:          name,
				InputPerM:      o.InputPerM,
				OutputPerM:     o.OutputPerM,
				CacheReadPerM:  o.CacheReadPerM,
				CacheWritePerM: o.CacheWritePerM,
				Free:           o.Free,
			}
		}
	}

	model.SetPricingOverrides(overrides)
}

func main() {
	if err := i18n.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "i18n init: %v\n", err)
	}
	// Pre-scan --locale so that Short descriptions are translated before
	// commands are built (cobra evaluates Short at registration time).
	for i, arg := range os.Args {
		if arg == "--locale" && i+1 < len(os.Args) {
			i18n.SetLocale(os.Args[i+1])
			break
		}
		if strings.HasPrefix(arg, "--locale=") {
			i18n.SetLocale(strings.TrimPrefix(arg, "--locale="))
			break
		}
	}
	root := &cobra.Command{
		Use:   "devinmonitor",
		Short: i18n.T("app.tagline"),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if flagLocale != "" {
				i18n.SetLocale(flagLocale)
			}
			// --no-cost removes cost columns from every table. The headers are
			// resolved AFTER any locale switch above, because they are matched
			// by their translated text.
			applyNoCost()
		},
	}
	root.PersistentFlags().StringVar(&flagDataDir, "data-dir", "", i18n.T("help.dataDir"))
	root.PersistentFlags().StringVar(&flagLocale, "locale", "", i18n.T("help.locale"))
	root.PersistentFlags().BoolVar(&flagNoCost, "no-cost", false, i18n.T("help.noCost"))

	// Prices and aliases are process-global: install them once here so the
	// reports, the live dashboard, MCP and the web API all resolve the same
	// way instead of each picking its own source.
	installPricingSources()

	// Disable alphabetical sorting so commands appear in the explicit
	// logical grouping defined below.
	cobra.EnableCommandSorting = false

	// Build the fully ordered command list (core + feature interleaved
	// by logical group). Core commands from main.go are created directly;
	// feature commands are pulled from the cli registry by name.
	ordered := buildOrderedCommands()

	// Track which feature command names we've already added via the
	// explicit list, so we can append any stragglers at the end.
	featureNames := make([]string, 0, len(ordered))
	for _, cmd := range ordered {
		if cmd.Name != "" {
			featureNames = append(featureNames, cmd.Name)
		}
	}

	// Add all commands in the explicit grouped order.
	for _, c := range ordered {
		if c.Cmd != nil {
			root.AddCommand(c.Cmd)
		}
	}

	// Safety net: add any feature commands not in the explicit list.
	for _, fn := range cli.Remaining(featureNames...) {
		root.AddCommand(fn())
	}

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// cmdEntry is a single command in the ordered list.
// For core commands (from main.go), Name is "" and Cmd is set directly.
// For feature commands, Name is the cobra Use name used to look up
// the factory from the cli registry.
type cmdEntry struct {
	Name string         // feature command name, "" for core
	Cmd  *cobra.Command // resolved cobra command
}

// buildOrderedCommands builds the full interleaved command list in
// logical group order, mixing core (main.go) and feature commands.
func buildOrderedCommands() []cmdEntry {
	// Helper to create a core command entry.
	core := func(cmd *cobra.Command) cmdEntry {
		return cmdEntry{Cmd: cmd}
	}
	// Helper to create a feature command entry by registry name.
	feat := func(name string) cmdEntry {
		fn := cli.Get(name)
		if fn == nil {
			return cmdEntry{Name: name} // placeholder, will be skipped
		}
		return cmdEntry{Name: name, Cmd: fn()}
	}

	return []cmdEntry{
		// --- Live & TUI ---
		core(cmdLive()),
		feat("theme"), feat("replay"), feat("timeline"),

		// --- Sessions ---
		core(cmdSession()),
		feat("sessions"), feat("filter"), feat("search"),

		// --- Time Reports ---
		core(cmdWeekly()),
		core(cmdMonthly()),
		feat("daily"),
		feat("24h"),

		// --- Cost & Budget ---
		feat("cost"), feat("budget"), feat("burn-rate"), feat("projection"),
		feat("top-cost"), feat("plan"), feat("currency"),
		feat("blocks"),

		// --- Analytics ---
		feat("cache"), feat("efficiency"), feat("tasks"), feat("optimize"),
		feat("compaction"), feat("context"), feat("analytics"),
		feat("model-compare"), feat("yield"), feat("errors"),

		// --- Trends & Charts ---
		feat("trends"), feat("heatmap"), feat("calendar"), feat("compare"),

		// --- Projects & Tools ---
		feat("projects"), feat("project"), feat("tools"),
		feat("mcp-stats"), feat("shell-usage"), feat("activities"), feat("git"),

		// --- Models ---
		core(cmdModels()),
		core(cmdModel()),
		core(cmdAgents()),

		// --- Export & Backup ---
		feat("export"), feat("report"), feat("backup"), feat("status"), feat("share"),

		// --- Integration ---
		feat("mcp"), feat("web"), feat("notify"),
		feat("snapshot"), feat("alerts"),

		// --- Config ---
		feat("config"), feat("alias"), feat("pricing"), feat("warehouse"),

		// --- System ---
		core(cmdMetrics()),
		core(cmdVersion()),
	}
}

func openReader() reader.Reader {
	r, err := reader.Open(flagDataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", i18n.T("err.readFail", map[string]interface{}{"Err": err.Error()}))
		os.Exit(1)
	}
	return r
}

// ---- live ----

func cmdLive() *cobra.Command {
	var (
		demo  bool
		once  bool
		light bool
		theme string
	)
	c := &cobra.Command{
		Use:   "live",
		Short: i18n.T("cmd.live"),
		Run: func(cmd *cobra.Command, args []string) {
			// The `refreshInterval` config value (milliseconds) is the default
			// when --interval is not passed explicitly, so the "Refresh
			// Interval" preference actually takes effect.
			interval := flagInterval
			if !cmd.Flags().Changed("interval") {
				if cfg := config.Global(); cfg != nil && cfg.RefreshInterval > 0 {
					interval = cfg.RefreshInterval
				}
			}
			// If any extended flags are set, use RunLiveExt; otherwise use the
			// original Run for backward compatibility.
			if demo || once || light || theme != "" {
				opts := live.RunOptions{
					Demo:  demo,
					Once:  once,
					Light: light,
					Theme: theme,
				}
				if err := live.RunLiveExt(flagDataDir, interval, opts); err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}
				return
			}
			if err := live.Run(flagDataDir, interval); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		},
	}
	c.Flags().Var(
		&minIntValue{dst: &flagInterval, min: live.MinIntervalMs, def: 500},
		"interval",
		i18n.T("help.interval"),
	)
	c.Flags().BoolVar(&demo, "demo", false, "run with synthetic demo data (no database needed)")
	c.Flags().BoolVar(&once, "once", false, "render one frame and exit (non-interactive)")
	c.Flags().BoolVar(&light, "light", false, "minimal rendering for slow terminals")
	c.Flags().StringVar(&theme, "theme", "", "override theme (auto, dark, dracula, nord, ...)")
	return c
}

// ---- sessions ----

func cmdSessions() *cobra.Command {
	var verbose bool
	c := &cobra.Command{
		Use:   "sessions",
		Short: i18n.T("cmd.sessions"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			rows := report.BuildSessionRows(ss)
			var t *ui.TableBuilder
			if verbose {
				// Full table with all columns.
				t = ui.NewTable(
					i18n.T("common.id"),
					i18n.T("common.title"),
					i18n.T("common.model"),
					i18n.T("common.mode"),
					i18n.T("common.project"),
					i18n.T("common.subAgents"),
					i18n.T("common.requests"),
					i18n.T("common.input"),
					i18n.T("common.output"),
					i18n.T("common.cacheRead"),
					i18n.T("common.duration"),
					i18n.T("common.cost"),
				).
					RightAlign(5, 6, 7, 8, 9, 10)
				for _, row := range rows {
					costStr := report.FormatCost(row.Cost, row.IsFree)
					if row.CostEstimated && row.Cost > 0 {
						costStr += " " + i18n.T("common.est")
					}
					subs := "-"
					if row.SubAgents > 0 {
						subs = fmt.Sprintf("%d", row.SubAgents)
					}
					t.Row(
						row.ID,
						row.Title,
						row.Model,
						row.Mode,
						row.Project,
						subs,
						fmt.Sprintf("%d", row.Requests),
						report.FormatTok(row.InputTok),
						report.FormatTok(row.OutputTok),
						report.FormatTok(row.CacheRead),
						report.FormatDur(row.Duration),
						costStr,
					)
				}
			} else {
				// Compact table: 7 core columns, fits 80-col terminals.
				t = ui.NewTable(
					i18n.T("common.id"),
					i18n.T("common.title"),
					i18n.T("common.model"),
					i18n.T("common.project"),
					i18n.T("common.requests"),
					i18n.T("common.input"),
					i18n.T("common.cost"),
				).
					RightAlign(4, 5)
				for _, row := range rows {
					costStr := report.FormatCost(row.Cost, row.IsFree)
					if row.CostEstimated && row.Cost > 0 {
						costStr += " " + i18n.T("common.est")
					}
					t.Row(
						row.ID,
						row.Title,
						row.Model,
						row.Project,
						fmt.Sprintf("%d", row.Requests),
						report.FormatTok(row.InputTok),
						costStr,
					)
				}
			}
			fmt.Println(t.String())
		},
	}
	c.Flags().BoolVar(&verbose, "verbose", false, "show all columns (mode, output, cache, duration)")
	return c
}

func cmdSession() *cobra.Command {
	c := &cobra.Command{
		Use:   "session <id>",
		Short: i18n.T("cmd.session"),
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			s, err := r.Session(args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			// Basic info
			modelName := s.LatestModel
			if modelName == "" {
				modelName = s.Model
			}
			dur := s.LastActivityAt.Sub(s.CreatedAt)
			fmt.Printf("%s: %s\n", i18n.T("common.id"), s.ID)
			fmt.Printf("%s: %s\n", i18n.T("common.title"), s.Title)
			fmt.Printf("%s: %s\n", i18n.T("common.model"), modelName)
			fmt.Printf("%s: %s\n", i18n.T("common.mode"), s.AgentMode)
			fmt.Printf("%s: %s\n", i18n.T("common.project"), s.WorkingDir)
			fmt.Printf("%s: %d\n", i18n.T("common.requests"), s.AssistantCount)
			fmt.Printf("%s: %s\n", i18n.T("common.duration"), report.FormatDur(dur))
			fmt.Printf("%s: %s / %s / %s / %s\n",
				i18n.T("common.tokens"),
				report.FormatTok(s.InputTokens),
				report.FormatTok(s.OutputTokens),
				report.FormatTok(s.CacheRead),
				report.FormatTok(s.CacheWrite),
			)
			// Sub-agent calls
			if len(s.SubAgentCalls) > 0 {
				fmt.Printf("\n%s (%d):\n", i18n.T("common.subAgents"), len(s.SubAgentCalls))
				for i, sa := range s.SubAgentCalls {
					bg := ""
					if sa.IsBackground {
						bg = " [" + i18n.T("common.bg") + "]"
					}
					title := sa.Title
					if title == "" {
						title = "-"
					}
					fmt.Printf("  %d. [%s] %s%s\n", i+1, sa.Profile, title, bg)
				}
			}
			// Tool calls summary
			if len(s.ToolCalls) > 0 {
				fmt.Printf("\n%s:\n", i18n.T("dash.tools.title"))
				type kv struct {
					k string
					v int
				}
				var kvs []kv
				for k, v := range s.ToolCalls {
					kvs = append(kvs, kv{k, v})
				}
				sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
				for _, e := range kvs {
					fmt.Printf("  %s  %d\n", e.k, e.v)
				}
			}
		},
	}
	return c
}

// ---- daily ----

// ---- weekly ----

func cmdWeekly() *cobra.Command {
	c := &cobra.Command{
		Use:   "weekly",
		Short: i18n.T("cmd.weekly"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			rows := lastPeriods(report.BuildWeekly(ss, report.ParseWeekday(flagStartDay)), flagLast)
			printTimeRows(rows, flagBreakdown, modeWeekly)
		},
	}
	c.Flags().BoolVar(&flagBreakdown, "breakdown", false, i18n.T("help.breakdown"))
	c.Flags().StringVar(&flagStartDay, "start-day", "monday", i18n.T("help.startDay"))
	c.Flags().IntVar(&flagLast, "last", 0, i18n.T("help.last"))
	return c
}

// ---- monthly ----

func cmdMonthly() *cobra.Command {
	c := &cobra.Command{
		Use:   "monthly",
		Short: i18n.T("cmd.monthly"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			rows := lastPeriods(report.BuildMonthly(ss), flagLast)
			printTimeRows(rows, flagBreakdown, modeMonthly)
		},
	}
	c.Flags().BoolVar(&flagBreakdown, "breakdown", false, i18n.T("help.breakdown"))
	c.Flags().IntVar(&flagLast, "last", 0, i18n.T("help.last"))
	return c
}

// timeRowMode controls how time rows are displayed.
type timeRowMode int

const (
	modeDaily timeRowMode = iota
	modeWeekly
	modeMonthly
)

// lastPeriods keeps only the most recent n time buckets.
//
// buildTimeBuckets returns buckets sorted by label ascending, so "most recent"
// is the tail of the slice. n <= 0 means no limit, which is the default and
// keeps the flag out of the way of existing invocations.
func lastPeriods(rows []report.TimeRow, n int) []report.TimeRow {
	if n <= 0 || n >= len(rows) {
		return rows
	}
	return rows[len(rows)-n:]
}

func printTimeRows(rows []report.TimeRow, breakdown bool, mode timeRowMode) {
	// Determine the label column header and a transform for the label.
	labelHeader := i18n.T("common.date")
	transformLabel := func(s string) string { return s }
	switch mode {
	case modeWeekly:
		labelHeader = i18n.T("common.week")
		transformLabel = func(s string) string { return report.WeekLabel(s) }
	case modeMonthly:
		labelHeader = i18n.T("common.month")
	}

	if breakdown {
		// Breakdown: per-model rows under each time bucket.
		// Columns: Label, Model, Sessions, Reqs, Input, Output, Cache R, Cache W, Cost
		t := ui.NewTable(
			labelHeader,
			i18n.T("common.model"),
			i18n.T("common.sessions"),
			i18n.T("common.requests"),
			i18n.T("common.input"),
			i18n.T("common.output"),
			i18n.T("common.cacheRead"),
			i18n.T("common.cacheWr"),
			i18n.T("common.cost"),
		).RightAlign(2, 3, 4, 5, 6, 7)
		// Accumulate totals across all time buckets.
		var totReq, totSess int
		var totIn, totOut, totCR, totCW int64
		var totCost float64
		for _, row := range rows {
			costStr := report.FormatCost(row.Cost, false)
			if row.CostEstimated && row.Cost > 0 {
				costStr += " " + i18n.T("common.est")
			}
			// Main row (all models combined).
			t.Row(transformLabel(row.Label), "(all)",
				fmt.Sprintf("%d", row.Sessions), fmt.Sprintf("%d", row.Requests),
				report.FormatTok(row.InputTok), report.FormatTok(row.OutputTok),
				report.FormatTok(row.CacheRead), report.FormatTok(row.CacheWrite),
				costStr)
			// Per-model rows.
			for _, mn := range sortedModelNames(row.ByModel) {
				ms := row.ByModel[mn]
				p := model.LookupPricing(mn)
				est := model.EstimateCost(p, ms.InputTokens, ms.OutputTokens, ms.CacheRead, ms.CacheWrite)
				t.Row("", mn, "", fmt.Sprintf("%d", ms.Requests),
					report.FormatTok(ms.InputTokens), report.FormatTok(ms.OutputTokens),
					report.FormatTok(ms.CacheRead), report.FormatTok(ms.CacheWrite),
					report.FormatCost(est, p.Free))
			}
			totReq += row.Requests
			totSess += row.Sessions
			totIn += row.InputTok
			totOut += row.OutputTok
			totCR += row.CacheRead
			totCW += row.CacheWrite
			totCost += row.Cost
		}
		// TOTALS row.
		t.TotalRow("TOTAL", "",
			fmt.Sprintf("%d", totSess), fmt.Sprintf("%d", totReq),
			report.FormatTok(totIn), report.FormatTok(totOut),
			report.FormatTok(totCR), report.FormatTok(totCW),
			report.FormatCost(totCost, false))
		fmt.Println(t.String())
		return
	}

	// Non-breakdown mode: unified columns for all modes.
	// Columns: Label, [DateRange for weekly], Sessions, Subs, Reqs, Input, Output, Cache R, Total, Cost, Models
	hasDateRange := mode == modeWeekly
	headers := []string{labelHeader}
	if hasDateRange {
		headers = append(headers, i18n.T("common.dateRange"))
	}
	headers = append(headers,
		i18n.T("common.sessions"),
		i18n.T("common.subAgents"),
		i18n.T("common.requests"),
		i18n.T("common.input"),
		i18n.T("common.output"),
		i18n.T("common.cacheRead"),
		i18n.T("common.total"),
		i18n.T("common.cost"),
		i18n.T("common.model"),
	)
	// Right-align numeric columns (account for dateRange offset).
	offset := 0
	if hasDateRange {
		offset = 1
	}
	rightCols := []int{1 + offset, 2 + offset, 3 + offset, 4 + offset, 5 + offset, 6 + offset, 7 + offset}
	t := ui.NewTable(headers...).RightAlign(rightCols...)
	// Accumulate totals.
	var totReq, totSess, totSubs int
	var totIn, totOut, totCR int64
	var totCost float64
	for _, row := range rows {
		costStr := report.FormatCost(row.Cost, false)
		if row.CostEstimated && row.Cost > 0 {
			costStr += " " + i18n.T("common.est")
		}
		subs := "-"
		if row.SubAgents > 0 {
			subs = fmt.Sprintf("%d", row.SubAgents)
		}
		values := []string{transformLabel(row.Label)}
		if hasDateRange {
			values = append(values, report.WeekDateRange(row.Label))
		}
		values = append(values,
			fmt.Sprintf("%d", row.Sessions),
			subs,
			fmt.Sprintf("%d", row.Requests),
			report.FormatTok(row.InputTok),
			report.FormatTok(row.OutputTok),
			report.FormatTok(row.CacheRead),
			report.FormatTok(row.InputTok+row.OutputTok+row.CacheRead+row.CacheWrite),
			costStr,
			compactModels(row.Models),
		)
		t.Row(values...)
		totReq += row.Requests
		totSess += row.Sessions
		totSubs += row.SubAgents
		totIn += row.InputTok
		totOut += row.OutputTok
		totCR += row.CacheRead
		totCost += row.Cost
	}
	// TOTALS row.
	totalsValues := []string{"TOTAL"}
	if hasDateRange {
		totalsValues = append(totalsValues, "")
	}
	totSubsStr := "-"
	if totSubs > 0 {
		totSubsStr = fmt.Sprintf("%d", totSubs)
	}
	totalsValues = append(totalsValues,
		fmt.Sprintf("%d", totSess),
		totSubsStr,
		fmt.Sprintf("%d", totReq),
		report.FormatTok(totIn),
		report.FormatTok(totOut),
		report.FormatTok(totCR),
		report.FormatTok(totIn+totOut+totCR),
		report.FormatCost(totCost, false),
		"",
	)
	t.TotalRow(totalsValues...)
	fmt.Println(t.String())
}

// compactModels formats a model list compactly, truncating if too long.
func compactModels(models []string) string {
	if len(models) == 0 {
		return i18n.T("common.na")
	}
	// Group by provider.
	groups := map[string][]string{}
	var bare []string
	for _, m := range models {
		if i := strings.Index(m, "/"); i >= 0 {
			prov := m[:i]
			mod := m[i+1:]
			groups[prov] = append(groups[prov], mod)
		} else {
			bare = append(bare, m)
		}
	}
	var parts []string
	for prov := range groups {
		mods := groups[prov]
		if len(mods) == 1 {
			parts = append(parts, prov+"/"+mods[0])
		} else {
			parts = append(parts, fmt.Sprintf("%s/{%s}", prov, strings.Join(mods, ",")))
		}
	}
	parts = append(parts, bare...)
	sort.Strings(parts)
	joined := strings.Join(parts, ", ")
	return joined
}

func sortedModelNames(m map[string]*model.ModelStats) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// sort by input tokens desc
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && m[out[j]].InputTokens > m[out[j-1]].InputTokens; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---- models ----

func cmdModels() *cobra.Command {
	var verbose bool
	c := &cobra.Command{
		Use:   "models",
		Short: i18n.T("cmd.models"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			rows := report.BuildModelRows(ss)
			// Accumulate totals across all models.
			var totSess, totReq int
			var totIn, totOut, totCR, totCW int64
			var totCost float64
			for _, row := range rows {
				totSess += row.Sessions
				totReq += row.Requests
				totIn += row.InputTok
				totOut += row.OutputTok
				totCR += row.CacheRead
				totCW += row.CacheWrite
				cost := row.CreditCost + row.ACUCost
				if cost == 0 {
					cost = row.EstCost
				}
				totCost += cost
			}
			var t *ui.TableBuilder
			if verbose {
				// Full table with latency columns.
				t = ui.NewTable(
					i18n.T("common.model"),
					i18n.T("common.sessions"),
					i18n.T("common.requests"),
					i18n.T("common.input"),
					i18n.T("common.output"),
					i18n.T("common.cacheRead"),
					i18n.T("common.cacheWr"),
					i18n.T("common.total"),
					i18n.T("common.cost"),
					i18n.T("common.costPct"),
					i18n.T("common.speed"),
					i18n.T("dash.latency.ttft")+" p50",
					i18n.T("dash.latency.total")+" p50",
					i18n.T("dash.latency.trunc"),
				).RightAlign(1, 2, 3, 4, 6, 7, 8, 9, 10, 11, 12)
				for _, row := range rows {
					costStr := report.FormatCost(row.CreditCost+row.ACUCost, row.IsFree)
					if row.CreditCost == 0 && row.ACUCost == 0 && row.EstCost > 0 {
						costStr = report.FormatCost(row.EstCost, row.IsFree) + " " + i18n.T("common.est")
					}
					t.Row(
						row.Name,
						fmt.Sprintf("%d", row.Sessions),
						fmt.Sprintf("%d", row.Requests),
						report.FormatTok(row.InputTok),
						report.FormatTok(row.OutputTok),
						report.FormatTok(row.CacheRead),
						report.FormatTok(row.CacheWrite),
						report.FormatTok(row.InputTok+row.OutputTok+row.CacheRead+row.CacheWrite),
						costStr,
						fmt.Sprintf("%.1f%%", row.CostPct),
						fmt.Sprintf("%.0f t/s", row.TokPerSecP50),
						fmt.Sprintf("%.1fs", row.TTFTP50/1000),
						fmt.Sprintf("%.1fs", row.TotalP50/1000),
						fmt.Sprintf("%.1f%%", row.TruncPct),
					)
				}
				t.TotalRow(
					"TOTAL",
					fmt.Sprintf("%d", totSess),
					fmt.Sprintf("%d", totReq),
					report.FormatTok(totIn),
					report.FormatTok(totOut),
					report.FormatTok(totCR),
					report.FormatTok(totCW),
					report.FormatTok(totIn+totOut+totCR+totCW),
					report.FormatCost(totCost, false),
					"100.0%",
					"", "", "", "",
				)
			} else {
				// Compact table: core columns matching ocmonitor.
				t = ui.NewTable(
					i18n.T("common.model"),
					i18n.T("common.sessions"),
					i18n.T("common.requests"),
					i18n.T("common.input"),
					i18n.T("common.output"),
					i18n.T("common.cacheRead"),
					i18n.T("common.cacheWr"),
					i18n.T("common.total"),
					i18n.T("common.cost"),
					i18n.T("common.costPct"),
					i18n.T("common.speed"),
				).RightAlign(1, 2, 3, 4, 6, 7, 8, 9, 10)
				for _, row := range rows {
					costStr := report.FormatCost(row.CreditCost+row.ACUCost, row.IsFree)
					if row.CreditCost == 0 && row.ACUCost == 0 && row.EstCost > 0 {
						costStr = report.FormatCost(row.EstCost, row.IsFree) + " " + i18n.T("common.est")
					}
					t.Row(
						row.Name,
						fmt.Sprintf("%d", row.Sessions),
						fmt.Sprintf("%d", row.Requests),
						report.FormatTok(row.InputTok),
						report.FormatTok(row.OutputTok),
						report.FormatTok(row.CacheRead),
						report.FormatTok(row.CacheWrite),
						report.FormatTok(row.InputTok+row.OutputTok+row.CacheRead+row.CacheWrite),
						costStr,
						fmt.Sprintf("%.1f%%", row.CostPct),
						fmt.Sprintf("%.0f t/s", row.TokPerSecP50),
					)
				}
				t.TotalRow(
					"TOTAL",
					fmt.Sprintf("%d", totSess),
					fmt.Sprintf("%d", totReq),
					report.FormatTok(totIn),
					report.FormatTok(totOut),
					report.FormatTok(totCR),
					report.FormatTok(totCW),
					report.FormatTok(totIn+totOut+totCR+totCW),
					report.FormatCost(totCost, false),
					"100.0%",
					"",
				)
			}
			fmt.Println(t.String())
		},
	}
	c.Flags().BoolVar(&verbose, "verbose", false, "show latency columns (TTFT, total time, truncation %)")
	return c
}

// ---- model <name> ----

func cmdModel() *cobra.Command {
	return &cobra.Command{
		Use:   "model <name>",
		Short: i18n.T("cmd.model"),
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			d, err := report.BuildModelDetail(ss, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}

			// Detail panel
			costStr := report.FormatCost(d.CreditCost+d.ACUCost, d.IsFree)
			if d.CreditCost == 0 && d.ACUCost == 0 && d.EstCost > 0 {
				costStr = report.FormatCost(d.EstCost, d.IsFree) + " " + i18n.T("common.est")
			}
			avgDay := 0.0
			if d.DaysUsed > 0 {
				avgDay = (d.CreditCost + d.ACUCost) / float64(d.DaysUsed)
				if avgDay == 0 && d.EstCost > 0 {
					avgDay = d.EstCost / float64(d.DaysUsed)
				}
			}
			avgSess := 0.0
			if d.Sessions > 0 {
				avgSess = (d.CreditCost + d.ACUCost) / float64(d.Sessions)
				if avgSess == 0 && d.EstCost > 0 {
					avgSess = d.EstCost / float64(d.Sessions)
				}
			}
			totalTok := d.InputTok + d.OutputTok + d.CacheRead + d.CacheWrite

			fmt.Printf("%s: %s\n", i18n.T("dash.model.detail"), d.Name)
			fmt.Printf("%s  %s\n", i18n.T("dash.model.firstUsed"), d.FirstUsed.Format("2006-01-02"))
			fmt.Printf("%s   %s\n", i18n.T("dash.model.lastUsed"), d.LastUsed.Format("2006-01-02"))
			fmt.Printf("%s    %d\n", i18n.T("common.sessions"), d.Sessions)
			fmt.Printf("%s    %d\n", i18n.T("dash.model.daysUsed"), d.DaysUsed)
			fmt.Printf("%s %d\n", i18n.T("common.requests"), d.Requests)
			fmt.Printf("%s  %s\n", i18n.T("common.input"), report.FormatTok(d.InputTok))
			fmt.Printf("%s %s\n", i18n.T("common.output"), report.FormatTok(d.OutputTok))
			fmt.Printf("%s  %s\n", i18n.T("common.cacheRead"), report.FormatTok(d.CacheRead))
			fmt.Printf("%s %s\n", i18n.T("common.cacheWr"), report.FormatTok(d.CacheWrite))
			fmt.Printf("%s   %s\n", i18n.T("common.total"), report.FormatTok(totalTok))
			fmt.Printf("%s    %s\n", i18n.T("common.cost"), costStr)
			fmt.Printf("%s   %s\n", i18n.T("dash.model.avgDay"), report.FormatCost(avgDay, d.IsFree))
			fmt.Printf("%s  %s\n", i18n.T("dash.model.avgSess"), report.FormatCost(avgSess, d.IsFree))
			fmt.Printf("%s   %.0f t/s (p50)\n", i18n.T("common.speed"), d.TokPerSecP50)
			fmt.Printf("%s   %.1fs (p50) / %.1fs (p95)\n", i18n.T("dash.latency.ttft"), d.TTFTP50/1000, d.TTFTP95/1000)
			fmt.Printf("%s  %.1fs (p50) / %.1fs (p95)\n", i18n.T("dash.latency.total"), d.TotalP50/1000, d.TotalP95/1000)
			fmt.Printf("%s   %.1f%%\n", i18n.T("dash.latency.trunc"), d.TruncPct)

			// Tool usage table
			if len(d.Tools) > 0 {
				fmt.Println()
				fmt.Printf("%s %s\n", i18n.T("dash.tools.title"), d.Name)
				t := ui.NewTable(
					i18n.T("dash.tools.tool"),
					i18n.T("dash.tools.calls"),
				).RightAlign(1)
				var totCalls int
				for _, tr := range d.Tools {
					t.Row(tr.Name, fmt.Sprintf("%d", tr.Calls))
					totCalls += tr.Calls
				}
				t.TotalRow("TOTAL", fmt.Sprintf("%d", totCalls))
				fmt.Println(t.String())
			}
		},
	}
}

// ---- projects ----

func cmdProjects() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: i18n.T("cmd.projects"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			rows := report.BuildProjectRows(ss)
			t := ui.NewTable(
				i18n.T("common.project"),
				i18n.T("common.sessions"),
				i18n.T("common.requests"),
				i18n.T("common.input"),
				i18n.T("common.output"),
				i18n.T("common.total"),
				i18n.T("common.cost"),
				i18n.T("common.model"),
			).RightAlign(1, 2, 3, 4, 5)
			for _, row := range rows {
				t.Row(
					row.Name,
					fmt.Sprintf("%d", row.Sessions),
					fmt.Sprintf("%d", row.Requests),
					report.FormatTok(row.InputTok),
					report.FormatTok(row.OutputTok),
					report.FormatTok(row.InputTok+row.OutputTok+row.CacheRead+row.CacheWrite),
					report.FormatCost(row.Cost, row.IsFree),
					compactModels(row.Models),
				)
			}
			fmt.Println(t.String())
		},
	}
}

// ---- agents ----

func cmdAgents() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: i18n.T("cmd.agents"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			// Chain heads (schema v17+) are optional metadata: Devin writes the
			// subagent_heads table only once it starts tracking them, and that
			// table is empty on every database observed so far.
			// reader.SubagentHeads reports "absent" and "empty" the same way —
			// an empty slice and a nil error — so `heads` stays nil here and the
			// column below is simply never added on current data.
			//
			// The read goes through a narrow inline facet instead of the
			// reader.Reader interface, which is how this codebase already
			// reaches the other v1Reader extension methods (see
			// internal/filterexport's filteringReader). A failed read of this
			// optional table warns and keeps going: losing one column is a
			// smaller problem than losing the whole agents report.
			var heads map[string][]report.AgentChainHead
			if hr, ok := r.(interface {
				SubagentHeads(sessionID string) ([]reader.SubagentHead, error)
			}); ok {
				for _, s := range ss {
					hs, err := hr.SubagentHeads(s.ID)
					if err != nil {
						fmt.Fprintf(os.Stderr, "chain heads for %s: %v\n", s.ID, err)
						continue
					}
					for _, h := range hs {
						if heads == nil {
							heads = map[string][]report.AgentChainHead{}
						}
						heads[s.ID] = append(heads[s.ID], report.AgentChainHead{
							AgentID:     h.AgentID,
							ChainNodeID: h.ChainNodeID,
						})
					}
				}
			}
			stats := report.BuildAgentStatsWithHeads(ss, heads)
			if len(stats) == 0 {
				fmt.Println(i18n.T("common.none"))
				return
			}
			// The "Chain heads" column appears ONLY for databases where Devin
			// has started recording chain heads. With none recorded it is left
			// off entirely, so a user never sees a column they can never
			// populate, and the table stays byte-for-byte what it was before
			// this feature existed — the tool must not imply that subagent
			// chain tracking is happening when Devin is not writing it.
			//
			// The header is a literal because the i18n catalogs are owned by
			// another change; every other header below goes through i18n.T.
			showHeads := report.HasChainHeads(stats)
			headers := []string{
				i18n.T("common.profile"),
				i18n.T("common.calls"),
				i18n.T("common.sessions"),
				i18n.T("common.bg"),
				i18n.T("common.fg"),
				i18n.T("common.done"),
				i18n.T("common.waits"),
				i18n.T("common.avgDur"),
				i18n.T("common.maxDur"),
				i18n.T("common.avgTask"),
				i18n.T("common.maxTask"),
				i18n.T("common.avgOut"),
				i18n.T("common.maxOut"),
			}
			align := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
			if showHeads {
				headers = append(headers, "Chain heads")
				align = append(align, len(headers)-1)
			}
			t := ui.NewTable(headers...).RightAlign(align...)
			// Accumulate totals.
			var totCalls, totSess, totBG, totFG, totDone, totWaits, totChainHeads int
			var totDur time.Duration
			var totTask, totOut int
			for _, st := range stats {
				avgDur := "-"
				maxDur := "-"
				if st.AvgDuration > 0 {
					avgDur = report.FormatDur(st.AvgDuration)
				}
				if st.MaxDuration > 0 {
					maxDur = report.FormatDur(st.MaxDuration)
				}
				avgTask := "-"
				maxTask := "-"
				if st.AvgTaskLen > 0 {
					avgTask = fmt.Sprintf("%d", st.AvgTaskLen)
				}
				if st.MaxTaskLen > 0 {
					maxTask = fmt.Sprintf("%d", st.MaxTaskLen)
				}
				avgOut := "-"
				maxOut := "-"
				if st.AvgOutputLen > 0 {
					avgOut = fmt.Sprintf("%d", st.AvgOutputLen)
				}
				if st.MaxOutputLen > 0 {
					maxOut = fmt.Sprintf("%d", st.MaxOutputLen)
				}
				row := []string{
					st.Profile,
					fmt.Sprintf("%d", st.Calls),
					fmt.Sprintf("%d", st.Sessions),
					fmt.Sprintf("%d", st.Background),
					fmt.Sprintf("%d", st.Foreground),
					fmt.Sprintf("%d", st.Completed),
					fmt.Sprintf("%d", st.ReadCalls),
					avgDur,
					maxDur,
					avgTask,
					maxTask,
					avgOut,
					maxOut,
				}
				if showHeads {
					// Count plus the recorded head node IDs, which are reported
					// as-is: chain_node_id is only the column's own name, and
					// there is no data anywhere to check a richer reading
					// against, so none is claimed.
					cell := "-"
					if st.ChainHeads > 0 {
						// One head per subagent, so cap the id list rather than
						// letting a busy session swamp the table.
						const maxIDs = 6
						ids := st.ChainHeadNodes
						extra := 0
						if len(ids) > maxIDs {
							extra = len(ids) - maxIDs
							ids = ids[:maxIDs]
						}
						parts := make([]string, 0, len(ids))
						for _, n := range ids {
							parts = append(parts, strconv.Itoa(n))
						}
						cell = fmt.Sprintf("%d (%s)", st.ChainHeads, strings.Join(parts, ","))
						if extra > 0 {
							cell += fmt.Sprintf(",+%d", extra)
						}
					}
					row = append(row, cell)
				}
				t.Row(row...)
				totCalls += st.Calls
				totSess += st.Sessions
				totBG += st.Background
				totFG += st.Foreground
				totDone += st.Completed
				totWaits += st.ReadCalls
				totChainHeads += st.ChainHeads
				totDur += st.AvgDuration * time.Duration(len(st.Durations))
				totTask += st.AvgTaskLen * len(st.TaskLens)
				totOut += st.AvgOutputLen * len(st.OutputLens)
			}
			// TOTALS row.
			totAvgDur := "-"
			if totDone > 0 {
				totAvgDur = report.FormatDur(totDur / time.Duration(totDone))
			}
			totAvgTask := "-"
			if totCalls > 0 && totTask > 0 {
				totAvgTask = fmt.Sprintf("%d", totTask/totCalls)
			}
			totAvgOut := "-"
			if totDone > 0 && totOut > 0 {
				totAvgOut = fmt.Sprintf("%d", totOut/totDone)
			}
			totRow := []string{
				"TOTAL",
				fmt.Sprintf("%d", totCalls),
				fmt.Sprintf("%d", totSess),
				fmt.Sprintf("%d", totBG),
				fmt.Sprintf("%d", totFG),
				fmt.Sprintf("%d", totDone),
				fmt.Sprintf("%d", totWaits),
				totAvgDur, "-",
				totAvgTask, "-",
				totAvgOut, "-",
			}
			if showHeads {
				// Every head is credited to exactly one session's dominant
				// profile, so this sum is the exact number of head records in
				// the report's sessions; the per-profile counts above are exact
				// only when a session used one profile.
				totRow = append(totRow, fmt.Sprintf("%d", totChainHeads))
			}
			t.TotalRow(totRow...)
			fmt.Println(t.String())
		},
	}
}

// ---- metrics ----

func cmdMetrics() *cobra.Command {
	var addr string
	c := &cobra.Command{
		Use:   "metrics",
		Short: i18n.T("cmd.metrics"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader()
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			exportMetrics(ss, addr)
		},
	}
	c.Flags().StringVar(&addr, "addr", ":9101", "listen address for Prometheus metrics server")
	return c
}

// exportMetrics starts a minimal HTTP server that exposes Prometheus-format
// metrics on /metrics. No external dependencies — we generate the text
// exposition format directly.
func exportMetrics(ss []model.Session, addr string) {
	// Pre-compute aggregates.
	modelRows := report.BuildModelRows(ss)
	projectRows := report.BuildProjectRows(ss)

	var totalReqs int
	var totalInput, totalOutput, totalCacheR, totalCacheW int64
	var totalCost float64
	for _, s := range ss {
		totalReqs += s.AssistantCount
		totalInput += s.InputTokens
		totalOutput += s.OutputTokens
		totalCacheR += s.CacheRead
		totalCacheW += s.CacheWrite
		cost, _ := report.SessionCost(&s)
		totalCost += cost
	}

	buildMetrics := func() string {
		var b strings.Builder
		b.WriteString("# HELP devinmonitor_sessions_total Total number of sessions.\n")
		b.WriteString("# TYPE devinmonitor_sessions_total gauge\n")
		fmt.Fprintf(&b, "devinmonitor_sessions_total %d\n", len(ss))

		b.WriteString("# HELP devinmonitor_requests_total Total assistant requests.\n")
		b.WriteString("# TYPE devinmonitor_requests_total gauge\n")
		fmt.Fprintf(&b, "devinmonitor_requests_total %d\n", totalReqs)

		b.WriteString("# HELP devinmonitor_input_tokens_total Total input tokens.\n")
		b.WriteString("# TYPE devinmonitor_input_tokens_total gauge\n")
		fmt.Fprintf(&b, "devinmonitor_input_tokens_total %d\n", totalInput)

		b.WriteString("# HELP devinmonitor_output_tokens_total Total output tokens.\n")
		b.WriteString("# TYPE devinmonitor_output_tokens_total gauge\n")
		fmt.Fprintf(&b, "devinmonitor_output_tokens_total %d\n", totalOutput)

		b.WriteString("# HELP devinmonitor_cache_read_tokens_total Total cache read tokens.\n")
		b.WriteString("# TYPE devinmonitor_cache_read_tokens_total gauge\n")
		fmt.Fprintf(&b, "devinmonitor_cache_read_tokens_total %d\n", totalCacheR)

		b.WriteString("# HELP devinmonitor_cache_write_tokens_total Total cache write tokens.\n")
		b.WriteString("# TYPE devinmonitor_cache_write_tokens_total gauge\n")
		fmt.Fprintf(&b, "devinmonitor_cache_write_tokens_total %d\n", totalCacheW)

		b.WriteString("# HELP devinmonitor_cost_total Total cost in USD.\n")
		b.WriteString("# TYPE devinmonitor_cost_total gauge\n")
		fmt.Fprintf(&b, "devinmonitor_cost_total %.4f\n", totalCost)

		// Per-model metrics.
		b.WriteString("# HELP devinmonitor_model_requests_total Total requests per model.\n")
		b.WriteString("# TYPE devinmonitor_model_requests_total gauge\n")
		for _, mr := range modelRows {
			fmt.Fprintf(&b, "devinmonitor_model_requests_total{model=%q} %d\n", mr.Name, mr.Requests)
		}

		b.WriteString("# HELP devinmonitor_model_input_tokens_total Input tokens per model.\n")
		b.WriteString("# TYPE devinmonitor_model_input_tokens_total gauge\n")
		for _, mr := range modelRows {
			fmt.Fprintf(&b, "devinmonitor_model_input_tokens_total{model=%q} %d\n", mr.Name, mr.InputTok)
		}

		b.WriteString("# HELP devinmonitor_model_output_tokens_total Output tokens per model.\n")
		b.WriteString("# TYPE devinmonitor_model_output_tokens_total gauge\n")
		for _, mr := range modelRows {
			fmt.Fprintf(&b, "devinmonitor_model_output_tokens_total{model=%q} %d\n", mr.Name, mr.OutputTok)
		}

		b.WriteString("# HELP devinmonitor_model_cost_total Cost per model in USD.\n")
		b.WriteString("# TYPE devinmonitor_model_cost_total gauge\n")
		for _, mr := range modelRows {
			cost := mr.CreditCost + mr.ACUCost
			if cost == 0 {
				cost = mr.EstCost
			}
			fmt.Fprintf(&b, "devinmonitor_model_cost_total{model=%q} %.4f\n", mr.Name, cost)
		}

		// Per-project metrics.
		b.WriteString("# HELP devinmonitor_project_sessions_total Sessions per project.\n")
		b.WriteString("# TYPE devinmonitor_project_sessions_total gauge\n")
		for _, pr := range projectRows {
			fmt.Fprintf(&b, "devinmonitor_project_sessions_total{project=%q} %d\n", pr.Name, pr.Sessions)
		}

		b.WriteString("# HELP devinmonitor_project_requests_total Requests per project.\n")
		b.WriteString("# TYPE devinmonitor_project_requests_total gauge\n")
		for _, pr := range projectRows {
			fmt.Fprintf(&b, "devinmonitor_project_requests_total{project=%q} %d\n", pr.Name, pr.Requests)
		}

		b.WriteString("# HELP devinmonitor_project_cost_total Cost per project in USD.\n")
		b.WriteString("# TYPE devinmonitor_project_cost_total gauge\n")
		for _, pr := range projectRows {
			fmt.Fprintf(&b, "devinmonitor_project_cost_total{project=%q} %.4f\n", pr.Name, pr.Cost)
		}

		return b.String()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, buildMetrics())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "DevinMonitor metrics server. Visit /metrics for Prometheus output.")
	})

	fmt.Fprintf(os.Stderr, "DevinMonitor metrics server listening on %s/metrics\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "metrics server error: %v\n", err)
		os.Exit(1)
	}
}

// ---- version ----

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: i18n.T("cmd.version"),
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("devinmonitor %s\n", version)
		},
	}
}
