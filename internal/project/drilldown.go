// Package project implements project management features: per-project
// drill-down, cost attribution, tool/MCP/shell/activity breakdowns, and
// git integration for devinmonitor.
//
// All cobra commands are self-registered via cli.Register() in init().
package project

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// openReader opens a reader using the --data-dir persistent flag.
func openReader(cmd *cobra.Command) reader.Reader {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	r, err := reader.Open(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	return r
}

// ---- Per-Project Drill-Down (#94) & Project Detail (#95) ----

var cmdProject = func() *cobra.Command {
	var days int
	var detail bool
	c := &cobra.Command{
		Use:   "project <name> [--days 30] [--detail]",
		Short: i18n.T("cmd.project"),
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			name := args[0]
			var projSessions []model.Session
			for _, s := range ss {
				if baseProject(s.WorkingDir) == name || strings.Contains(s.WorkingDir, name) {
					projSessions = append(projSessions, s)
				}
			}
			if len(projSessions) == 0 {
				fmt.Printf("No sessions found for project: %s\n", name)
				return
			}
			if detail {
				printProjectDetail(name, projSessions, days)
			} else {
				printProjectDrilldown(name, projSessions, days)
			}
		},
	}
	c.Flags().IntVar(&days, "days", 30, "number of days to show")
	c.Flags().BoolVar(&detail, "detail", false, "show detailed project view with KPIs and charts")
	return c
}

func printProjectDrilldown(name string, ss []model.Session, days int) {
	now := time.Now()
	cutoff := now.AddDate(0, 0, -days)

	// Daily breakdown.
	dailyRows := report.BuildDaily(ss)
	var recentDaily []report.TimeRow
	for _, r := range dailyRows {
		if t, err := time.Parse("2006-01-02", r.Label); err == nil && t.After(cutoff) {
			recentDaily = append(recentDaily, r)
		}
	}

	// Model breakdown.
	modelStats := map[string]*model.ModelStats{}
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.GenerationModel == "" {
				continue
			}
			ms := modelStats[m.GenerationModel]
			if ms == nil {
				ms = &model.ModelStats{Name: m.GenerationModel, FinishReasons: map[string]int{}}
				modelStats[m.GenerationModel] = ms
			}
			ms.Requests++
			if m.Metrics != nil {
				ms.InputTokens += m.Metrics.InputTokens
				ms.OutputTokens += m.Metrics.OutputTokens
				ms.CacheRead += m.Metrics.CacheReadTokens
				ms.CacheWrite += m.Metrics.CacheWriteTokens
			}
		}
	}

	// Tool breakdown.
	toolCounts := map[string]int{}
	for _, s := range ss {
		for tool, count := range s.ToolCalls {
			toolCounts[tool] += count
		}
	}

	// Print daily breakdown.
	fmt.Printf("Project: %s (last %d days)\n\n", name, days)
	fmt.Println("Daily Breakdown:")
	t := ui.NewTable("Date", "Sessions", "Reqs", "Input", "Output", "Cost").RightAlign(1, 2, 3, 4, 5)
	for _, r := range recentDaily {
		costStr := report.FormatCost(r.Cost, false)
		if r.CostEstimated && r.Cost > 0 {
			costStr += " est"
		}
		t.Row(r.Label,
			fmt.Sprintf("%d", r.Sessions),
			fmt.Sprintf("%d", r.Requests),
			report.FormatTok(r.InputTok),
			report.FormatTok(r.OutputTok),
			costStr)
	}
	fmt.Println(t.String())

	// Print model breakdown.
	fmt.Println("\nModel Breakdown:")
	t2 := ui.NewTable("Model", "Requests", "Input", "Output", "Cache R").RightAlign(1, 2, 3, 4)
	modelNames := sortedModelKeys(modelStats)
	for _, mn := range modelNames {
		ms := modelStats[mn]
		t2.Row(mn,
			fmt.Sprintf("%d", ms.Requests),
			report.FormatTok(ms.InputTokens),
			report.FormatTok(ms.OutputTokens),
			report.FormatTok(ms.CacheRead))
	}
	fmt.Println(t2.String())

	// Print tool breakdown.
	if len(toolCounts) > 0 {
		fmt.Println("\nTool Breakdown:")
		t3 := ui.NewTable("Tool", "Calls").RightAlign(1)
		type kv struct {
			k string
			v int
		}
		var kvs []kv
		for k, v := range toolCounts {
			kvs = append(kvs, kv{k, v})
		}
		sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
		for _, e := range kvs {
			t3.Row(e.k, fmt.Sprintf("%d", e.v))
		}
		fmt.Println(t3.String())
	}
}

func printProjectDetail(name string, ss []model.Session, days int) {
	var totalCost float64
	var totalTokens int64
	var totalReqs int
	for _, s := range ss {
		cost, _ := report.SessionCost(&s)
		totalCost += cost
		totalTokens += s.InputTokens + s.OutputTokens
		totalReqs += s.AssistantCount
	}
	avgCost := 0.0
	if len(ss) > 0 {
		avgCost = totalCost / float64(len(ss))
	}

	fmt.Println(ui.Panel("Project Detail: "+name, fmt.Sprintf(
		"Sessions:       %d\nTotal Cost:     $%.2f\nTotal Tokens:   %s\nTotal Requests: %d\nAvg Cost/Sess:  $%.2f",
		len(ss), totalCost, report.FormatTok(totalTokens), totalReqs, avgCost,
	), 50))

	// Daily chart (sparkline).
	now := time.Now()
	cutoff := now.AddDate(0, 0, -days)
	dailyRows := report.BuildDaily(ss)
	var costs []float64
	var labels []string
	for _, r := range dailyRows {
		if t, err := time.Parse("2006-01-02", r.Label); err == nil && t.After(cutoff) {
			costs = append(costs, r.Cost)
			labels = append(labels, r.Label)
		}
	}
	if len(costs) > 0 {
		fmt.Printf("\nDaily Cost Chart (last %d days):\n", days)
		fmt.Println(ui.Sparkline(costs, 60))
		if len(labels) > 0 {
			fmt.Printf("%s ~ %s\n", labels[0], labels[len(labels)-1])
		}
	}

	// Model distribution.
	modelStats := map[string]*model.ModelStats{}
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.GenerationModel == "" {
				continue
			}
			ms := modelStats[m.GenerationModel]
			if ms == nil {
				ms = &model.ModelStats{Name: m.GenerationModel, FinishReasons: map[string]int{}}
				modelStats[m.GenerationModel] = ms
			}
			ms.Requests++
		}
	}
	if len(modelStats) > 0 {
		fmt.Println("\nModel Distribution:")
		t := ui.NewTable("Model", "Requests", "Share")
		totalModelReqs := 0
		for _, ms := range modelStats {
			totalModelReqs += ms.Requests
		}
		for _, mn := range sortedModelKeys(modelStats) {
			ms := modelStats[mn]
			share := 0.0
			if totalModelReqs > 0 {
				share = float64(ms.Requests) / float64(totalModelReqs) * 100
			}
			t.Row(mn, fmt.Sprintf("%d", ms.Requests), fmt.Sprintf("%.1f%%", share))
		}
		fmt.Println(t.String())
	}

	// Top tools.
	toolCounts := map[string]int{}
	for _, s := range ss {
		for tool, count := range s.ToolCalls {
			toolCounts[tool] += count
		}
	}
	if len(toolCounts) > 0 {
		fmt.Println("\nTop Tools:")
		t2 := ui.NewTable("Tool", "Calls").RightAlign(1)
		type kv struct {
			k string
			v int
		}
		var kvs []kv
		for k, v := range toolCounts {
			kvs = append(kvs, kv{k, v})
		}
		sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
		max := 10
		if len(kvs) < max {
			max = len(kvs)
		}
		for _, e := range kvs[:max] {
			t2.Row(e.k, fmt.Sprintf("%d", e.v))
		}
		fmt.Println(t2.String())
	}

	// Sessions list.
	fmt.Println("\nSessions:")
	t3 := ui.NewTable("ID", "Title", "Model", "Reqs", "Cost")
	for _, s := range ss {
		cost, _ := report.SessionCost(&s)
		t3.Row(s.ID, s.Title, s.Model,
			fmt.Sprintf("%d", s.AssistantCount),
			fmt.Sprintf("$%.2f", cost))
	}
	fmt.Println(t3.String())
}

func baseProject(dir string) string {
	if dir == "" {
		return "-"
	}
	dir = strings.TrimRight(dir, "/")
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		return dir[i+1:]
	}
	if i := strings.LastIndex(dir, "\\"); i >= 0 {
		return dir[i+1:]
	}
	return dir
}

func sortedModelKeys(m map[string]*model.ModelStats) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		return m[out[i]].Requests > m[out[j]].Requests
	})
	return out
}

func init() {
	cli.Register(cmdProject)
	cli.Register(cmdProjects)
	cli.Register(cmdTools)
	cli.Register(cmdMCPUsage)
	cli.Register(cmdShellUsage)
	cli.Register(cmdActivities)
	cli.Register(cmdGit)
}
