package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// ---- Period Shortcuts (#80) ----
// today/all/week/month removed as separate commands.
// daily --today, monthly --all, weekly/monthly cover these use cases.

// printPeriodRows prints time rows in a compact table.
func printPeriodRows(rows []report.TimeRow, labelHeader string) {
	t := ui.NewTable(
		labelHeader,
		"Sessions",
		"Reqs",
		"Input",
		"Output",
		"Cache R",
		"Total",
		"Cost",
	).RightAlign(1, 2, 3, 4, 5, 6)
	var totReq, totSess int
	var totIn, totOut, totCR int64
	var totCost float64
	for _, row := range rows {
		costStr := report.FormatCost(row.Cost, false)
		if row.CostEstimated && row.Cost > 0 {
			costStr += " est"
		}
		t.Row(
			row.Label,
			fmt.Sprintf("%d", row.Sessions),
			fmt.Sprintf("%d", row.Requests),
			report.FormatTok(row.InputTok),
			report.FormatTok(row.OutputTok),
			report.FormatTok(row.CacheRead),
			report.FormatTok(row.InputTok+row.OutputTok+row.CacheRead+row.CacheWrite),
			costStr,
		)
		totReq += row.Requests
		totSess += row.Sessions
		totIn += row.InputTok
		totOut += row.OutputTok
		totCR += row.CacheRead
		totCost += row.Cost
	}
	t.Row(
		"TOTALS",
		fmt.Sprintf("%d", totSess),
		fmt.Sprintf("%d", totReq),
		report.FormatTok(totIn),
		report.FormatTok(totOut),
		report.FormatTok(totCR),
		report.FormatTok(totIn+totOut+totCR),
		report.FormatCost(totCost, false),
	)
	fmt.Println(t.String())
}

// ---- Command Aliases (#88) ----

var cmdAlias = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "alias [list|add <short> <long>|remove <short>]",
		Short: "Manage command aliases",
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			if len(args) == 0 || args[0] == "list" {
				if cfg.SavedFlags == nil {
					fmt.Println("No aliases configured.")
					return
				}
				// Aliases are stored with "alias:" prefix in SavedFlags.
				type kv struct{ k, v string }
				var pairs []kv
				for k, v := range cfg.SavedFlags {
					if strings.HasPrefix(k, "alias:") {
						pairs = append(pairs, kv{strings.TrimPrefix(k, "alias:"), v})
					}
				}
				sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
				if len(pairs) == 0 {
					fmt.Println("No aliases configured.")
					return
				}
				t := ui.NewTable("Alias", "Command")
				for _, p := range pairs {
					t.Row(p.k, p.v)
				}
				fmt.Println(t.String())
				return
			}
			switch args[0] {
			case "add":
				if len(args) < 3 {
					fmt.Fprintln(os.Stderr, "usage: alias add <short> <long>")
					os.Exit(1)
				}
				if cfg.SavedFlags == nil {
					cfg.SavedFlags = map[string]string{}
				}
				cfg.SavedFlags["alias:"+args[1]] = args[2]
				if err := config.SaveGlobal(); err != nil {
					fmt.Fprintf(os.Stderr, "save: %v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Alias added: %s -> %s\n", args[1], args[2])
			case "remove":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: alias remove <short>")
					os.Exit(1)
				}
				if cfg.SavedFlags != nil {
					delete(cfg.SavedFlags, "alias:"+args[1])
					_ = config.SaveGlobal()
				}
				fmt.Printf("Alias removed: %s\n", args[1])
			default:
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
				os.Exit(1)
			}
		},
	}
	return c
}

// ---- Enhanced Sessions (#89 save flags, #87 watch, #82 json) ----

var cmdSessionsEnhanced = func() *cobra.Command {
	var verbose, watch, jsonOut bool
	var sortKey string
	var save bool
	c := &cobra.Command{
		Use:   "sessions",
		Short: "List sessions (with --sort, --save, --watch, --json)",
		Run: func(cmd *cobra.Command, args []string) {
			// Apply saved sort if no explicit --sort.
			cfg := config.Global()
			if !cmd.Flags().Changed("sort") && cfg.SavedFlags != nil {
				if v, ok := cfg.SavedFlags["sessions.sort"]; ok && v != "" {
					sortKey = v
				}
			}
			// Persist flag if --save.
			if save && sortKey != "" {
				if cfg.SavedFlags == nil {
					cfg.SavedFlags = map[string]string{}
				}
				cfg.SavedFlags["sessions.sort"] = sortKey
				_ = config.SaveGlobal()
				fmt.Fprintf(os.Stderr, "Saved default sort: %s\n", sortKey)
			}

			render := func() error {
				r := openReader(cmd)
				defer r.Close()
				ss, err := r.Sessions()
				if err != nil {
					return err
				}
				rows := report.BuildSessionRows(ss)
				sortSessionRows(rows, sortKey)

				if jsonOut {
					// JSON output with active/completed status (replaces ls --json).
					now := time.Now()
					activeIDs := map[string]bool{}
					for _, s := range ss {
						if now.Sub(s.LastActivityAt) < 5*time.Minute {
							activeIDs[s.ID] = true
						}
					}
					items := make([]model.SessionListItem, 0, len(rows))
					for _, row := range rows {
						status := "completed"
						if activeIDs[row.ID] {
							status = "active"
						}
						items = append(items, model.SessionListItem{
							ID:       row.ID,
							Title:    row.Title,
							Model:    row.Model,
							Project:  row.Project,
							Cost:     row.Cost,
							Tokens:   row.InputTok + row.OutputTok,
							Duration: report.FormatDur(row.Duration),
							Status:   status,
						})
					}
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					_ = enc.Encode(items)
					return nil
				}

				t := buildSessionsTable(rows, verbose)
				fmt.Println(t.String())
				return nil
			}
			if watch {
				interval, _ := cmd.Flags().GetInt("interval")
				if interval <= 0 {
					interval = 3
				}
				watchLoop(time.Duration(interval)*time.Second, func() error {
					return render()
				})
			} else {
				if err := render(); err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}
			}
		},
	}
	c.Flags().BoolVar(&verbose, "verbose", false, "show all columns")
	c.Flags().BoolVar(&watch, "watch", false, "auto-refresh (live updating)")
	c.Flags().BoolVar(&jsonOut, "json", false, "output as JSON (with active/completed status)")
	c.Flags().StringVar(&sortKey, "sort", "", "sort by: cost, tokens, requests, recent")
	c.Flags().BoolVar(&save, "save", false, "save current flags as default")
	c.Flags().Int("interval", 3, "refresh interval in seconds (for --watch)")
	return c
}

func sortSessionRows(rows []report.SessionRow, key string) {
	switch strings.ToLower(key) {
	case "cost":
		sort.Slice(rows, func(i, j int) bool { return rows[i].Cost > rows[j].Cost })
	case "tokens":
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].InputTok+rows[i].OutputTok > rows[j].InputTok+rows[j].OutputTok
		})
	case "requests":
		sort.Slice(rows, func(i, j int) bool { return rows[i].Requests > rows[j].Requests })
	case "recent":
		// BuildSessionRows already sorts by activity; keep as-is.
	default:
		// Default: by requests (existing behavior).
	}
}

func buildSessionsTable(rows []report.SessionRow, verbose bool) *ui.TableBuilder {
	var t *ui.TableBuilder
	if verbose {
		t = ui.NewTable(
			"ID", "Title", "Model", "Mode", "Project",
			"Subs", "Reqs", "Input", "Output", "Cache R",
			"Duration", "Cost",
		).RightAlign(5, 6, 7, 8, 9, 10)
		for _, row := range rows {
			costStr := report.FormatCost(row.Cost, row.IsFree)
			if row.CostEstimated && row.Cost > 0 {
				costStr += " est"
			}
			subs := "-"
			if row.SubAgents > 0 {
				subs = fmt.Sprintf("%d", row.SubAgents)
			}
			t.Row(
				row.ID, row.Title, row.Model, row.Mode, row.Project,
				subs, fmt.Sprintf("%d", row.Requests),
				report.FormatTok(row.InputTok), report.FormatTok(row.OutputTok),
				report.FormatTok(row.CacheRead), report.FormatDur(row.Duration),
				costStr,
			)
		}
	} else {
		t = ui.NewTable(
			"ID", "Title", "Model", "Project", "Reqs", "Input", "Cost",
		).RightAlign(4, 5)
		for _, row := range rows {
			costStr := report.FormatCost(row.Cost, row.IsFree)
			if row.CostEstimated && row.Cost > 0 {
				costStr += " est"
			}
			t.Row(
				row.ID, row.Title, row.Model, row.Project,
				fmt.Sprintf("%d", row.Requests),
				report.FormatTok(row.InputTok), costStr,
			)
		}
	}
	return t
}

// ---- Enhanced Daily (#87 watch, #80 today shortcut) ----

var cmdDailyEnhanced = func() *cobra.Command {
	var breakdown, watch, today bool
	c := &cobra.Command{
		Use:   "daily",
		Short: "Daily usage report (with --watch, --today)",
		Run: func(cmd *cobra.Command, args []string) {
			render := func() error {
				r := openReader(cmd)
				defer r.Close()
				ss, err := r.Sessions()
				if err != nil {
					return err
				}
				if today {
					todayStart := model.DayStart(time.Now())
					var filtered []model.Session
					for _, s := range ss {
						if s.LastActivityAt.After(todayStart) || s.LastActivityAt.Equal(todayStart) {
							filtered = append(filtered, s)
						}
					}
					ss = filtered
				}
				rows := report.BuildDaily(ss)
				printDailyRows(rows, breakdown)
				return nil
			}
			if watch {
				interval, _ := cmd.Flags().GetInt("interval")
				if interval <= 0 {
					interval = 3
				}
				watchLoop(time.Duration(interval)*time.Second, func() error {
					return render()
				})
			} else {
				if err := render(); err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}
			}
		},
	}
	c.Flags().BoolVar(&breakdown, "breakdown", false, "show per-model breakdown")
	c.Flags().BoolVar(&watch, "watch", false, "auto-refresh every N seconds")
	c.Flags().BoolVar(&today, "today", false, "show only today's data")
	c.Flags().Int("interval", 3, "refresh interval in seconds (for --watch)")
	return c
}

func printDailyRows(rows []report.TimeRow, breakdown bool) {
	if breakdown {
		t := ui.NewTable(
			"Date", "Model", "Sessions", "Reqs", "Input", "Output",
			"Cache R", "Cache W", "Cost",
		).RightAlign(2, 3, 4, 5, 6, 7)
		for _, row := range rows {
			costStr := report.FormatCost(row.Cost, false)
			if row.CostEstimated && row.Cost > 0 {
				costStr += " est"
			}
			t.Row(row.Label, "(all)",
				fmt.Sprintf("%d", row.Sessions), fmt.Sprintf("%d", row.Requests),
				report.FormatTok(row.InputTok), report.FormatTok(row.OutputTok),
				report.FormatTok(row.CacheRead), report.FormatTok(row.CacheWrite),
				costStr)
			for _, mn := range sortedModelNames(row.ByModel) {
				ms := row.ByModel[mn]
				p := model.LookupPricing(mn)
				est := model.EstimateCost(p, ms.InputTokens, ms.OutputTokens, ms.CacheRead, ms.CacheWrite)
				t.Row("", mn, "", fmt.Sprintf("%d", ms.Requests),
					report.FormatTok(ms.InputTokens), report.FormatTok(ms.OutputTokens),
					report.FormatTok(ms.CacheRead), report.FormatTok(ms.CacheWrite),
					report.FormatCost(est, p.Free))
			}
		}
		fmt.Println(t.String())
		return
	}
	t := ui.NewTable(
		"Date", "Sessions", "Subs", "Reqs", "Input", "Output",
		"Cache R", "Total", "Cost", "Model",
	).RightAlign(1, 2, 3, 4, 5, 6, 7)
	var totReq, totSess, totSubs int
	var totIn, totOut, totCR int64
	var totCost float64
	for _, row := range rows {
		costStr := report.FormatCost(row.Cost, false)
		if row.CostEstimated && row.Cost > 0 {
			costStr += " est"
		}
		subs := "-"
		if row.SubAgents > 0 {
			subs = fmt.Sprintf("%d", row.SubAgents)
		}
		t.Row(
			row.Label,
			fmt.Sprintf("%d", row.Sessions),
			subs,
			fmt.Sprintf("%d", row.Requests),
			report.FormatTok(row.InputTok),
			report.FormatTok(row.OutputTok),
			report.FormatTok(row.CacheRead),
			report.FormatTok(row.InputTok+row.OutputTok+row.CacheRead+row.CacheWrite),
			costStr,
			compactModelsList(row.Models),
		)
		totReq += row.Requests
		totSess += row.Sessions
		totSubs += row.SubAgents
		totIn += row.InputTok
		totOut += row.OutputTok
		totCR += row.CacheRead
		totCost += row.Cost
	}
	totSubsStr := "-"
	if totSubs > 0 {
		totSubsStr = fmt.Sprintf("%d", totSubs)
	}
	t.Row(
		"TOTALS",
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
	fmt.Println(t.String())
}

func sortedModelNames(m map[string]*model.ModelStats) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && m[out[j]].InputTokens > m[out[j-1]].InputTokens; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func compactModelsList(models []string) string {
	if len(models) == 0 {
		return "-"
	}
	var parts []string
	for _, m := range models {
		parts = append(parts, m)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
