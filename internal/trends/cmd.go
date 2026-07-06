package trends

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
)

func init() {
	cli.Register(cmdTrends)
	cli.Register(cmdHeatmap)
	cli.Register(cmdCalendar)
	cli.Register(cmdCompare)
	cli.Register(cmdToday)
}

// openReader opens a reader using the inherited --data-dir persistent flag.
func openReader(cmd *cobra.Command) reader.Reader {
	dir := ""
	if f := cmd.Flag("data-dir"); f != nil {
		dir = f.Value.String()
	}
	r, err := reader.Open(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open reader: %v\n", err)
		os.Exit(1)
	}
	return r
}

// ---- 12. Trend Range Toggle (#24) ----

// resolveDays maps a --range value to a day count. An explicit --days flag
// takes precedence over --range.
func resolveDays(days int, rng string) int {
	if days > 0 {
		return days
	}
	switch strings.ToLower(rng) {
	case "week":
		return 7
	case "month":
		return 30
	case "all":
		return 0 // 0 = no trimming, show all
	default:
		return 7
	}
}

// ---- trends (#13, #14, #17, #18, #21, #22, #24) ----

func cmdTrends() *cobra.Command {
	var days int
	var rng string
	c := &cobra.Command{
		Use:   "trends",
		Short: "Show cost and usage trends",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			n := resolveDays(days, rng)
			rows := report.BuildDaily(ss)

			// #17 Delta banner
			fmt.Println(RenderDeltaBanner(ss))
			fmt.Println()

			// #13 ASCII trend chart + #14 daily cost chart
			fmt.Println(RenderDailyCostChart(rows, n, chartHeight))
			fmt.Println()

			// #21 Cumulative cost trend
			fmt.Println(RenderCumulativeCost(rows, n, chartHeight))
			fmt.Println()

			// #22 Stacked area chart (per-model)
			fmt.Println(RenderStackedArea(rows, n, chartHeight))
			fmt.Println()

			// #18 Sparkline summary
			fmt.Println(RenderSparklineSummary(rows, n, 40))
			fmt.Println()

			// Range toggle hint for TUI mode.
			fmt.Printf("Range: %s (%d days). Use --range week|month|all to toggle.\n",
				rng, n)
		},
	}
	c.Flags().IntVar(&days, "days", 0, "Number of days to show (7, 30, 90); overrides --range")
	c.Flags().StringVar(&rng, "range", "week", "Range toggle: week, month, or all")
	return c
}

// ---- heatmap (#15) ----

func cmdHeatmap() *cobra.Command {
	return &cobra.Command{
		Use:   "heatmap",
		Short: "Show weekday × hour activity heatmap",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			cells := BuildHeatmap(ss)
			fmt.Println(RenderHeatmap(cells))
		},
	}
}

// ---- calendar (#16) ----

func cmdCalendar() *cobra.Command {
	var year int
	c := &cobra.Command{
		Use:   "calendar",
		Short: "Show full-year contribution calendar",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			if year == 0 {
				year = time.Now().Year()
			}
			days := BuildContributionCalendar(ss, year)
			fmt.Println(RenderContributionCalendar(days, year))
		},
	}
	c.Flags().IntVar(&year, "year", 0, "Year to display (default: current year)")
	return c
}

// ---- compare (#19, #20) ----

func cmdCompare() *cobra.Command {
	var current, previous, mode string
	c := &cobra.Command{
		Use:   "compare",
		Short: "Compare two time periods side by side",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}

			// Month-over-month shortcut (#20).
			if strings.EqualFold(mode, "mom") {
				pc := BuildMonthOverMonth(ss)
				fmt.Println(RenderMonthOverMonth(pc))
				return
			}

			curStart, curEnd, err := parsePeriod(current)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --current period: %v\n", err)
				os.Exit(1)
			}
			prevStart, prevEnd, err := parsePeriod(previous)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --previous period: %v\n", err)
				os.Exit(1)
			}
			pc := BuildPeriodComparison(ss, curStart, curEnd, prevStart, prevEnd)
			fmt.Println(RenderPeriodComparison(pc))
		},
	}
	c.Flags().StringVar(&current, "current", "", "Current period (YYYY-MM or YYYY-MM-DD..YYYY-MM-DD)")
	c.Flags().StringVar(&previous, "previous", "", "Previous period (YYYY-MM or YYYY-MM-DD..YYYY-MM-DD)")
	c.Flags().StringVar(&mode, "mode", "custom", "Comparison mode: custom or mom (month-over-month)")
	return c
}

// parsePeriod accepts either "YYYY-MM" (a full month) or
// "YYYY-MM-DD..YYYY-MM-DD" (an explicit date range).
func parsePeriod(s string) (time.Time, time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("empty period")
	}
	if strings.Contains(s, "..") {
		parts := strings.SplitN(s, "..", 2)
		start, err := time.Parse("2006-01-02", strings.TrimSpace(parts[0]))
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("bad start date: %w", err)
		}
		end, err := time.Parse("2006-01-02", strings.TrimSpace(parts[1]))
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("bad end date: %w", err)
		}
		end = end.AddDate(0, 0, 1) // make end exclusive
		return start, end, nil
	}
	// YYYY-MM → full month.
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("expected YYYY-MM or YYYY-MM-DD..YYYY-MM-DD: %w", err)
	}
	start := t
	end := t.AddDate(0, 1, 0)
	return start, end, nil
}

// ---- today / 24-hour usage (#23) ----

func cmdToday() *cobra.Command {
	return &cobra.Command{
		Use:   "today",
		Short: "Show activity over the last 24 hours (alias for 24h view)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			fmt.Println(Render24HourChart(ss, chartHeight))
		},
	}
}
