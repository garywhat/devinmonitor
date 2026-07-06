package analytics

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

func init() {
	cli.Register(cmdCache)
	cli.Register(cmdEfficiency)
	cli.Register(cmdTasks)
	cli.Register(cmdOptimize)
	cli.Register(cmdCompaction)
	cli.Register(cmdModelCompare)
	cli.Register(cmdYield)
	cli.Register(cmdContext)
	cli.Register(cmdAnalytics)
}

// openReader opens a reader using the --data-dir flag inherited from root.
func openReader(cmd *cobra.Command) reader.Reader {
	dataDir := ""
	if flag := cmd.Flag("data-dir"); flag != nil {
		dataDir = flag.Value.String()
	}
	r, err := reader.Open(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
		os.Exit(1)
	}
	return r
}

// ---- cache command (#25, #26, #27, #30) ----

func cmdCache() *cobra.Command {
	return &cobra.Command{
		Use:   "cache",
		Short: "Show cache hit ratio, efficiency donut, savings, and leverage",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading sessions: %v\n", err)
				os.Exit(1)
			}
			stats := CacheStatsAggregate(ss)

			fmt.Println(ui.Panel("Cache Statistics", cacheReport(stats, ss), 60))
		},
	}
}

func cacheReport(stats model.CacheStats, ss []model.Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cache Read Tokens:  %s\n", report.FormatTok(stats.CacheRead))
	fmt.Fprintf(&b, "Cache Write Tokens: %s\n", report.FormatTok(stats.CacheWrite))
	fmt.Fprintf(&b, "Input Tokens:       %s\n", report.FormatTok(stats.InputTokens))
	fmt.Fprintf(&b, "\nHit Ratio:   %.1f%%\n", stats.HitRatio*100)
	fmt.Fprintf(&b, "Leverage:    %.1f%%\n", stats.Leverage*100)
	fmt.Fprintf(&b, "Savings:     $%.2f\n", stats.SavingsUSD)
	fmt.Fprintf(&b, "\nCache Hit vs Miss:\n%s\n", CacheDonut(stats.HitRatio, 40))

	// Per-session breakdown (top 10).
	if len(ss) > 0 {
		type sessCache struct {
			id      string
			title   string
			hitPct  float64
			cacheR  int64
		}
		var rows []sessCache
		for i := range ss {
			s := &ss[i]
			cs := CacheStatsForSession(s)
			rows = append(rows, sessCache{s.ID, s.Title, cs.HitRatio * 100, s.CacheRead})
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].cacheR > rows[j].cacheR
		})
		max := 10
		if len(rows) < max {
			max = len(rows)
		}
		fmt.Fprintf(&b, "\nTop Sessions by Cache Read:\n")
		for i := 0; i < max; i++ {
			title := rows[i].title
			if title == "" {
				title = "(untitled)"
			}
			if len(title) > 30 {
				title = title[:30]
			}
			fmt.Fprintf(&b, "  %-12s %-30s hit:%5.1f%%  read:%s\n",
				rows[i].id, title, rows[i].hitPct, report.FormatTok(rows[i].cacheR))
		}
	}
	return b.String()
}

// ---- efficiency command (#28, #29, #31, #32, #36) ----

func cmdEfficiency() *cobra.Command {
	return &cobra.Command{
		Use:   "efficiency",
		Short: "Show token efficiency score, output verbosity, one-shot rate, and productivity",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading sessions: %v\n", err)
				os.Exit(1)
			}
			em := ComputeEfficiency(ss)
			osr := OneShotRateAggregate(ss)
			avgRetries, topFiles := RetryRate(osr)

			var b strings.Builder
			fmt.Fprintf(&b, "Efficiency Grade: %s\n\n", em.Grade)
			fmt.Fprintf(&b, "Tokens per Dollar:  %.0f\n", em.Score.TokensPerDollar)
			fmt.Fprintf(&b, "Tokens per Request: %.0f\n", em.Score.TokensPerRequest)
			fmt.Fprintf(&b, "Cache Savings:      %.1f%%\n", em.Score.CacheSavingsPct)
			fmt.Fprintf(&b, "\nOutput Verbosity:   %.0f tokens/request\n", em.OutputVerbosity)
			fmt.Fprintf(&b, "Tokens per Minute:  %.0f\n", em.TokensPerMin)
			fmt.Fprintf(&b, "Code Ratio:         %.2f (tool_calls/messages)\n", em.CodeRatio)
			fmt.Fprintf(&b, "\nOne-Shot Rate:      %.1f%% (%d/%d edits succeeded first try)\n",
				osr.OneShotPct, osr.TotalEdits-osr.Retries, osr.TotalEdits)
			fmt.Fprintf(&b, "Retry Rate:         %.2f retries/edit\n", avgRetries)
			fmt.Fprintf(&b, "Total Retries:      %d\n", osr.Retries)

			if len(topFiles) > 0 {
				fmt.Fprintf(&b, "\nFiles with Most Retries:\n")
				max := 10
				if len(topFiles) < max {
					max = len(topFiles)
				}
				for i := 0; i < max; i++ {
					fmt.Fprintf(&b, "  %3d  %s\n", topFiles[i].Retries, shortFile(topFiles[i].File))
				}
			}

			fmt.Println(ui.Panel("Efficiency & Productivity", b.String(), 60))
		},
	}
}

// ---- tasks command (#33) ----

func cmdTasks() *cobra.Command {
	return &cobra.Command{
		Use:   "tasks",
		Short: "Show task category breakdown (Coding, Debugging, Testing, etc.)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading sessions: %v\n", err)
				os.Exit(1)
			}
			cats := TaskCategories(ss)
			if len(cats) == 0 {
				fmt.Println("No sessions found.")
				return
			}
			var total int
			var totalCost float64
			for _, c := range cats {
				total += c.Count
				totalCost += c.Cost
			}
			t := ui.NewTable("Category", "Sessions", "%", "Cost", "Cost/Session").
				RightAlign(1, 2, 3, 4)
			for _, c := range cats {
				pct := 0.0
				if total > 0 {
					pct = float64(c.Count) / float64(total) * 100
				}
				cps := 0.0
				if c.Count > 0 {
					cps = c.Cost / float64(c.Count)
				}
				t.Row(c.Name, fmt.Sprintf("%d", c.Count), fmt.Sprintf("%.1f%%", pct),
					report.FormatCost(c.Cost, false), report.FormatCost(cps, false))
			}
			t.Row("TOTAL", fmt.Sprintf("%d", total), "100.0%",
				report.FormatCost(totalCost, false), report.FormatCost(totalCost/float64(total), false))
			fmt.Println(t.String())
		},
	}
}

// ---- optimize command (#34, #38) ----

func cmdOptimize() *cobra.Command {
	var days int
	c := &cobra.Command{
		Use:   "optimize",
		Short: "Scan for waste patterns and generate optimization suggestions",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading sessions: %v\n", err)
				os.Exit(1)
			}
			// Filter by days if specified.
			if days > 0 {
				cutoff := time.Now().AddDate(0, 0, -days)
				var filtered []model.Session
				for _, s := range ss {
					if s.CreatedAt.After(cutoff) {
						filtered = append(filtered, s)
					}
				}
				ss = filtered
			}
			findings := WasteScan(ss)
			if len(findings) == 0 {
				fmt.Println("No waste patterns detected. Your usage looks efficient!")
				return
			}
			fmt.Printf("Optimization Findings (%d):\n\n", len(findings))
			for i, f := range findings {
				fmt.Printf("%d. [%s] %s\n", i+1, f.Category, f.Description)
				fmt.Printf("   Impact:     %s\n", f.Impact)
				fmt.Printf("   Suggestion: %s\n\n", f.Suggestion)
			}
		},
	}
	c.Flags().IntVar(&days, "days", 30, "number of days to analyze (0 = all)")
	return c
}

// ---- compaction command (#37) ----

func cmdCompaction() *cobra.Command {
	return &cobra.Command{
		Use:   "compaction",
		Short: "Detect context compaction events from token count drops",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading sessions: %v\n", err)
				os.Exit(1)
			}
			summary := CompactionStats(ss)
			if summary.TotalEvents == 0 {
				fmt.Println("No compaction events detected.")
				return
			}
			fmt.Printf("Compaction Events: %d\n", summary.TotalEvents)
			fmt.Printf("Total Tokens Saved: %s\n", report.FormatTok(int64(summary.TotalTokensSaved)))
			fmt.Printf("Average Drop: %.1f%%\n\n", summary.AvgDropPct)

			t := ui.NewTable("Session", "Timestamp", "Before", "After", "Saved", "Drop%").
				RightAlign(2, 3, 4, 5)
			max := 50
			if len(summary.Events) < max {
				max = len(summary.Events)
			}
			for i := 0; i < max; i++ {
				e := summary.Events[i]
				saved := e.BeforeTokens - e.AfterTokens
				dropPct := 0.0
				if e.BeforeTokens > 0 {
					dropPct = float64(saved) / float64(e.BeforeTokens) * 100
				}
				t.Row(e.SessionID, e.Timestamp.Format("2006-01-02 15:04"),
					report.FormatTok(int64(e.BeforeTokens)),
					report.FormatTok(int64(e.AfterTokens)),
					report.FormatTok(int64(saved)),
					fmt.Sprintf("%.1f%%", dropPct))
			}
			fmt.Println(t.String())
		},
	}
}

// ---- model-compare command (#39) ----

func cmdModelCompare() *cobra.Command {
	return &cobra.Command{
		Use:   "model-compare [model1] [model2] ...",
		Short: "Compare models side by side (cost, tokens, latency, cache hit, speed)",
		Args:  cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading sessions: %v\n", err)
				os.Exit(1)
			}
			cmp := CompareModels(ss, args)
			if len(cmp.Models) == 0 {
				fmt.Println("No models found.")
				return
			}
			t := ui.NewTable("Model", "Requests", "Input", "Output", "Cost",
				"Avg Latency", "Tokens/sec", "Cache Hit%").
				RightAlign(1, 2, 3, 4, 5, 6, 7)
			for _, row := range cmp.Models {
				t.Row(row.Name,
					fmt.Sprintf("%d", row.Requests),
					report.FormatTok(row.InputTokens),
					report.FormatTok(row.OutputTokens),
					report.FormatCost(row.Cost, false),
					fmt.Sprintf("%.1fms", row.AvgLatency),
					fmt.Sprintf("%.0f", row.TokensPerSec),
					fmt.Sprintf("%.1f%%", row.CacheHitPct))
			}
			fmt.Println(t.String())
		},
	}
}

// ---- yield command (#40) ----

func cmdYield() *cobra.Command {
	var days int
	c := &cobra.Command{
		Use:   "yield",
		Short: "Show productive vs abandoned spend (correlates sessions with git commits)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading sessions: %v\n", err)
				os.Exit(1)
			}
			// Filter by days.
			if days > 0 {
				cutoff := time.Now().AddDate(0, 0, -days)
				var filtered []model.Session
				for _, s := range ss {
					if s.CreatedAt.After(cutoff) {
						filtered = append(filtered, s)
					}
				}
				ss = filtered
			}
			yieldReport(ss)
		},
	}
	c.Flags().IntVar(&days, "days", 30, "number of days to analyze (0 = all)")
	return c
}

func yieldReport(ss []model.Session) {
	type result struct {
		session   model.Session
		commits   int
		lastCommit string
	}
	var results []result
	gitAvailable := false

	for i := range ss {
		s := &ss[i]
		if s.WorkingDir == "" {
			results = append(results, result{session: *s})
			continue
		}
		// Check if it's a git repo and get commit count.
		commits, lastHash := gitCommitsForSession(s)
		if commits >= 0 {
			gitAvailable = true
		}
		results = append(results, result{session: *s, commits: commits, lastCommit: lastHash})
	}

	if !gitAvailable {
		fmt.Println("Git integration requires a git repo in the session's working directory.")
		fmt.Println("No git commits were found for any session.")
		return
	}

	var productiveCost, abandonedCost float64
	var productiveSessions, abandonedSessions int

	t := ui.NewTable("Session", "Title", "Commits", "Last Commit", "Cost", "Yield").
		RightAlign(2, 4)
	for _, res := range results {
		cost, _ := report.SessionCost(&res.session)
		yield := "abandoned"
		if res.commits > 0 {
			yield = "productive"
			productiveCost += cost
			productiveSessions++
		} else {
			abandonedCost += cost
			abandonedSessions++
		}
		title := res.session.Title
		if title == "" {
			title = "(untitled)"
		}
		if len(title) > 30 {
			title = title[:30]
		}
		commitStr := fmt.Sprintf("%d", res.commits)
		if res.commits < 0 {
			commitStr = "n/a"
		}
		t.Row(res.session.ID, title, commitStr, res.lastCommit,
			report.FormatCost(cost, false), yield)
	}
	fmt.Println(t.String())

	totalCost := productiveCost + abandonedCost
	fmt.Printf("\nProductive:  %d sessions, %s (%.1f%% of spend)\n",
		productiveSessions, report.FormatCost(productiveCost, false),
		pctOf(productiveCost, totalCost))
	fmt.Printf("Abandoned:   %d sessions, %s (%.1f%% of spend)\n",
		abandonedSessions, report.FormatCost(abandonedCost, false),
		pctOf(abandonedCost, totalCost))
	fmt.Printf("Total:       %d sessions, %s\n", len(results), report.FormatCost(totalCost, false))
}

// gitCommitsForSession returns the number of commits and the last commit hash
// for the session's working directory. Returns -1, "" if git is not available.
func gitCommitsForSession(s *model.Session) (int, string) {
	dir := s.WorkingDir
	if dir == "" {
		return -1, ""
	}
	// Check if it's a git repo.
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		// Try git rev-parse for worktrees.
		cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
		if err := cmd.Run(); err != nil {
			return -1, ""
		}
	}
	// Get commit count since session start.
	cmd := exec.Command("git", "-C", dir, "rev-list", "--count", "--since",
		s.CreatedAt.Format("2006-01-02"), "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return -1, ""
	}
	count := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count)

	// Get last commit hash (short).
	cmd2 := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD")
	out2, err := cmd2.Output()
	if err != nil {
		return count, ""
	}
	return count, strings.TrimSpace(string(out2))
}

func pctOf(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return part / total * 100
}

// ---- context command (#41) ----

func cmdContext() *cobra.Command {
	return &cobra.Command{
		Use:   "context <session-id>",
		Short: "Analyze what fills a session's context window",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			s, err := r.Session(args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			cb := ContextBreakdownForSession(s)

			var b strings.Builder
			fmt.Fprintf(&b, "Session: %s\n", s.ID)
			fmt.Fprintf(&b, "Title:   %s\n", s.Title)
			fmt.Fprintf(&b, "Total Context Tokens: %s\n\n", report.FormatTok(cb.Total))

			fmt.Fprintf(&b, "By Message Category:\n")
			for _, e := range cb.Categories {
				fmt.Fprintf(&b, "  %-15s %s  (%.1f%%)\n", e.Label, report.FormatTok(e.Tokens), e.Pct)
			}

			if len(cb.Tools) > 0 {
				fmt.Fprintf(&b, "\nBy Tool:\n")
				for _, e := range cb.Tools {
					fmt.Fprintf(&b, "  %-20s %s  (%.1f%%)\n", e.Label, report.FormatTok(e.Tokens), e.Pct)
				}
			}

			fmt.Println(ui.Panel("Context Analysis", b.String(), 60))
		},
	}
}

// ---- analytics summary command (#17) ----

func cmdAnalytics() *cobra.Command {
	var days int
	c := &cobra.Command{
		Use:   "analytics",
		Short: "Comprehensive analytics overview (cache, efficiency, tasks, waste, compaction)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading sessions: %v\n", err)
				os.Exit(1)
			}
			if days > 0 {
				cutoff := time.Now().AddDate(0, 0, -days)
				var filtered []model.Session
				for _, s := range ss {
					if s.CreatedAt.After(cutoff) {
						filtered = append(filtered, s)
					}
				}
				ss = filtered
			}

			// Cache summary.
			cacheStats := CacheStatsAggregate(ss)
			fmt.Println(ui.Panel("Cache", cacheSummary(cacheStats), 60))
			fmt.Println()

			// Efficiency summary.
			em := ComputeEfficiency(ss)
			fmt.Println(ui.Panel("Efficiency", efficiencySummary(em), 60))
			fmt.Println()

			// Task categories.
			cats := TaskCategories(ss)
			fmt.Println(ui.Panel("Task Categories", taskSummary(cats), 60))
			fmt.Println()

			// Waste findings.
			findings := WasteScan(ss)
			fmt.Println(ui.Panel("Optimization Findings", wasteSummary(findings), 60))
			fmt.Println()

			// Compaction.
			comp := CompactionStats(ss)
			fmt.Println(ui.Panel("Compaction", compactionSummary(comp), 60))
		},
	}
	c.Flags().IntVar(&days, "days", 0, "number of days to analyze (0 = all)")
	return c
}

func cacheSummary(stats model.CacheStats) string {
	return fmt.Sprintf("Hit Ratio:  %.1f%%\nLeverage:   %.1f%%\nSavings:    $%.2f\nCache Read: %s\nInput:      %s",
		stats.HitRatio*100, stats.Leverage*100, stats.SavingsUSD,
		report.FormatTok(stats.CacheRead), report.FormatTok(stats.InputTokens))
}

func efficiencySummary(em EfficiencyMetrics) string {
	return fmt.Sprintf("Grade:             %s\nTokens/Dollar:      %.0f\nTokens/Request:     %.0f\nOutput Verbosity:   %.0f tok/req\nTokens/Min:         %.0f\nCode Ratio:         %.2f",
		em.Grade, em.Score.TokensPerDollar, em.Score.TokensPerRequest,
		em.OutputVerbosity, em.TokensPerMin, em.CodeRatio)
}

func taskSummary(cats []model.TaskCategory) string {
	if len(cats) == 0 {
		return "No sessions found."
	}
	var b strings.Builder
	var total int
	for _, c := range cats {
		total += c.Count
	}
	for _, c := range cats {
		pct := 0.0
		if total > 0 {
			pct = float64(c.Count) / float64(total) * 100
		}
		fmt.Fprintf(&b, "%-15s %3d  (%.1f%%)  %s\n", c.Name, c.Count, pct, report.FormatCost(c.Cost, false))
	}
	return b.String()
}

func wasteSummary(findings []model.WasteFinding) string {
	if len(findings) == 0 {
		return "No waste patterns detected."
	}
	var b strings.Builder
	for i, f := range findings {
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, f.Category, f.Description)
	}
	return b.String()
}

func compactionSummary(comp CompactionSummary) string {
	if comp.TotalEvents == 0 {
		return "No compaction events detected."
	}
	return fmt.Sprintf("Events:          %d\nTokens Saved:    %s\nAverage Drop:    %.1f%%",
		comp.TotalEvents, report.FormatTok(int64(comp.TotalTokensSaved)), comp.AvgDropPct)
}
