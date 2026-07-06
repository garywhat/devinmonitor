package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// ---- Snapshot Command (#81) ----

var cmdSnapshot = func() *cobra.Command {
	return &cobra.Command{
		Use:   "snapshot",
		Short: i18n.T("cmd.snapshot"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			sum := computeCostSummary(ss)
			cfg := config.Global()

			// Active sessions table.
			now := time.Now()
			var active []model.Session
			for _, s := range ss {
				if now.Sub(s.LastActivityAt) < 5*time.Minute {
					active = append(active, s)
				}
			}

			fmt.Println(ui.Panel("DevinMonitor Status", fmt.Sprintf(
				"Active sessions:  %d\nToday's cost:     $%.2f  [%s]\nWeek's cost:      $%.2f\nMonth's cost:     $%.2f\nTotal cost:       $%.2f\nTotal sessions:   %d\nTotal requests:   %d\nBudget daily:     $%.2f\nBudget monthly:   $%.2f\nACU rate:         %.2f USD/ACU\nPlan:             %s",
				sum.ActiveSess,
				sum.TodayCost, sum.Provenance,
				sum.WeekCost,
				sum.MonthCost,
				sum.TotalCost,
				sum.TotalSess,
				sum.TotalReqs,
				cfg.BudgetDaily,
				cfg.BudgetMonthly,
				cfg.ACURate,
				cfg.Plan,
			), 60))

			if len(active) > 0 {
				fmt.Println()
				t := ui.NewTable("ID", "Title", "Model", "Last Activity")
				for _, s := range active {
					ago := now.Sub(s.LastActivityAt).Round(time.Second)
					t.Row(s.ID, s.Title, s.Model, fmt.Sprintf("%s ago", ago))
				}
				fmt.Println(t.String())
			}
		},
	}
}

// ---- CLI Mode for Agents (#82) ----

var cmdLs = func() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "ls",
		Short: "List sessions (for agent consumption with --json)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			rows := report.BuildSessionRows(ss)
			// Build a lookup of active session IDs.
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
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(items)
			} else {
				t := ui.NewTable("ID", "Title", "Model", "Project", "Cost", "Tokens", "Duration", "Status")
				for _, item := range items {
					t.Row(item.ID, item.Title, item.Model, item.Project,
						fmt.Sprintf("$%.2f", item.Cost),
						report.FormatTok(item.Tokens),
						item.Duration, item.Status)
				}
				fmt.Println(t.String())
			}
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return c
}

var cmdWhoami = func() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "whoami",
		Short: "Show current user/config info",
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			info := map[string]interface{}{
				"locale":          cfg.Locale,
				"theme":           cfg.Theme,
				"currency":        cfg.Currency,
				"timezone":        cfg.Timezone,
				"resetHour":       cfg.ResetHour,
				"budgetDaily":     cfg.BudgetDaily,
				"budgetWeekly":    cfg.BudgetWeekly,
				"budgetMonthly":   cfg.BudgetMonthly,
				"plan":            cfg.Plan,
				"planMonthly":     cfg.PlanMonthly,
				"planACULimit":    cfg.PlanACULimit,
				"acuRate":         cfg.ACURate,
				"notifyDesktop":   cfg.NotifyDesktop,
				"notifyWebhook":   cfg.NotifyWebhook != "",
				"modelAliases":    cfg.ModelAliases,
				"customPricing":   cfg.CustomPricing,
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(info)
			} else {
				fmt.Println(ui.Panel("DevinMonitor Config", fmt.Sprintf(
					"Locale:         %s\nTheme:          %s\nCurrency:       %s\nTimezone:       %s\nReset hour:     %d\nBudget daily:   $%.2f\nBudget monthly: $%.2f\nPlan:           %s\nACU rate:       %.2f\nWebhook:        %v",
					cfg.Locale, cfg.Theme, cfg.Currency, cfg.Timezone, cfg.ResetHour,
					cfg.BudgetDaily, cfg.BudgetMonthly, cfg.Plan, cfg.ACURate,
					cfg.NotifyWebhook != "",
				), 50))
			}
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return c
}

var cmdAlerts = func() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "alerts",
		Short: i18n.T("cmd.alerts"),
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			alerts := detectAlerts(ss)
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(alerts)
			} else {
				if len(alerts) == 0 {
					fmt.Println("No alerts.")
					return
				}
				t := ui.NewTable("Kind", "Severity", "Message")
				for _, a := range alerts {
					t.Row(a.Kind, a.Severity, a.Message)
				}
				fmt.Println(t.String())
			}
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return c
}

// detectAlerts scans sessions for alert conditions.
func detectAlerts(ss []model.Session) []model.AlertItem {
	var alerts []model.AlertItem
	now := time.Now()
	cfg := config.Global()

	// Budget alerts.
	todayCost := 0.0
	monthCost := 0.0
	todayStart := model.DayStart(now)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	for _, s := range ss {
		cost, _ := report.SessionCost(&s)
		if s.LastActivityAt.After(todayStart) {
			todayCost += cost
		}
		if s.LastActivityAt.After(monthStart) {
			monthCost += cost
		}
	}
	if cfg.BudgetDaily > 0 && todayCost >= cfg.BudgetDaily {
		alerts = append(alerts, model.AlertItem{
			Kind:     "budget",
			Severity: "critical",
			Message:  fmt.Sprintf("Daily budget exceeded: $%.2f / $%.2f", todayCost, cfg.BudgetDaily),
		})
	} else if cfg.BudgetDaily > 0 && todayCost >= cfg.BudgetDaily*0.8 {
		alerts = append(alerts, model.AlertItem{
			Kind:     "budget",
			Severity: "warning",
			Message:  fmt.Sprintf("Approaching daily budget: $%.2f / $%.2f", todayCost, cfg.BudgetDaily),
		})
	}
	if cfg.BudgetMonthly > 0 && monthCost >= cfg.BudgetMonthly {
		alerts = append(alerts, model.AlertItem{
			Kind:     "budget",
			Severity: "critical",
			Message:  fmt.Sprintf("Monthly budget exceeded: $%.2f / $%.2f", monthCost, cfg.BudgetMonthly),
		})
	}

	// Idle/ghost session alerts.
	for _, s := range ss {
		age := now.Sub(s.LastActivityAt)
		if age > 2*time.Hour && s.AssistantCount > 0 {
			alerts = append(alerts, model.AlertItem{
				Kind:     "idle",
				Severity: "info",
				Message:  fmt.Sprintf("Session %s idle for %s: %s", s.ID, report.FormatDur(age), s.Title),
			})
		}
		if s.AssistantCount == 0 && age > 24*time.Hour {
			alerts = append(alerts, model.AlertItem{
				Kind:     "ghost",
				Severity: "info",
				Message:  fmt.Sprintf("Ghost session %s (no activity): %s", s.ID, s.Title),
			})
		}
	}

	return alerts
}
