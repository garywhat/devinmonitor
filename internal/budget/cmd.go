package budget

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
	"github.com/garywhat/devinmonitor/internal/ui"
)

func init() {
	cli.Register(cmdBudget)
	cli.Register(cmdBurnRate)
	cli.Register(cmdProjection)
	cli.Register(cmdPlan)
	cli.Register(cmdCurrency)
	cli.Register(cmdTopCost)
	cli.Register(cmdCost)
}

// openReader opens the Devin CLI session store. dataDir is optional; when
// empty, reader.Open auto-detects via DEVIN_DATA_DIR and platform defaults.
func openReader(dataDir string) reader.Reader {
	r, err := reader.Open(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open reader: %v\n", err)
		os.Exit(1)
	}
	return r
}

// ---- budget ----

func cmdBudget() *cobra.Command {
	c := &cobra.Command{
		Use:   "budget",
		Short: "Show budget status with guardrails and gauges",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader("")
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			cfg := config.Global()
			now := time.Now()

			fmt.Println(ui.Panel("Budget Guardrails", renderGuardrails(ss, cfg, now), 60))
			fmt.Println()
			fmt.Println(ui.Panel("Subscription Savings", renderSavings(ss, cfg, now), 60))
		},
	}
	return c
}

func renderGuardrails(ss []model.Session, cfg *config.Config, now time.Time) string {
	statuses := Guardrails(ss, cfg, now)
	var b strings.Builder
	for _, st := range statuses {
		b.WriteString(Gauge(st, 30))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderSavings(ss []model.Session, cfg *config.Config, now time.Time) string {
	s := ComputeSavings(ss, cfg, now)
	if cfg.Plan == "" || cfg.Plan == "none" || cfg.PlanMonthly <= 0 {
		return fmt.Sprintf("No Devin plan configured. Use `devinmonitor plan set` to track subscription savings.")
	}
	return fmt.Sprintf("Plan:            %s\nPlan monthly:    %s\nAPI equivalent:  %s\nSavings:         %s (%.1f%%)",
		s.Plan,
		report.FormatCost(s.PlanMonthly, false),
		report.FormatCost(s.APIEquivalent, false),
		report.FormatCost(s.Savings, false),
		s.SavingsPct,
	)
}

// ---- burn-rate ----

func cmdBurnRate() *cobra.Command {
	c := &cobra.Command{
		Use:   "burn-rate",
		Short: "Show spending velocity ($/hr, $/day, extrapolated)",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader("")
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			cfg := config.Global()
			br := ComputeBurnRate(ss, cfg, time.Now())
			fmt.Println(ui.Panel("Burn Rate", fmt.Sprintf(
				"Per hour:   %s\nPer day:    %s\nPer week:   %s\nPer month:  %s",
				report.FormatCost(br.PerHour, false),
				report.FormatCost(br.PerDay, false),
				report.FormatCost(br.PerWeek, false),
				report.FormatCost(br.PerMonth, false),
			), 40))
		},
	}
	return c
}

// ---- projection ----

func cmdProjection() *cobra.Command {
	c := &cobra.Command{
		Use:   "projection",
		Short: "Predict end-of-month spend and days to budget exhaustion",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader("")
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			cfg := config.Global()
			p := ComputeProjection(ss, cfg, time.Now())
			remaining := "n/a"
			if cfg.BudgetMonthly > 0 {
				remaining = report.FormatCost(p.RemainingBudget, false)
			}
			days := "n/a"
			if p.DaysToExhaust > 0 {
				days = fmt.Sprintf("%d days", p.DaysToExhaust)
			} else if cfg.BudgetMonthly > 0 {
				days = "over budget"
			}
			fmt.Println(ui.Panel("Cost Projection", fmt.Sprintf(
				"Predicted month-end:  %s\nRemaining budget:     %s\nDays to exhaustion:   %s\nConfidence:           %.0f%%",
				report.FormatCost(p.PredictedMonthEnd, false),
				remaining,
				days,
				p.Confidence*100,
			), 48))
		},
	}
	return c
}

// ---- plan ----

func cmdPlan() *cobra.Command {
	c := &cobra.Command{
		Use:   "plan",
		Short: "Show or configure your Devin subscription plan",
	}
	c.AddCommand(cmdPlanShow(), cmdPlanSet())
	return c
}

func cmdPlanShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current plan and ACU usage",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader("")
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			cfg := config.Global()
			st := PlanUsage(ss, cfg, time.Now())
			limit := "unlimited"
			if st.ACULimit > 0 {
				limit = fmt.Sprintf("%.0f", st.ACULimit)
			}
			over := ""
			if st.Overage > 0 {
				over = fmt.Sprintf("  (overage: %.0f ACU)", st.Overage)
			}
			fmt.Println(ui.Panel("Plan Tracking", fmt.Sprintf(
				"Plan:          %s\nMonthly cost:  %s\nACU limit:     %s\nACU used:      %.0f\nUsage:         %.1f%%%s",
				st.Plan,
				report.FormatCost(st.PlanMonthly, false),
				limit,
				st.ACUUsed,
				st.Pct,
				over,
			), 44))
		},
	}
}

func cmdPlanSet() *cobra.Command {
	var monthly, acu float64
	c := &cobra.Command{
		Use:   "set <name>",
		Short: "Set your Devin plan, monthly cost, and ACU limit",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			cfg.Plan = args[0]
			if cmd.Flags().Changed("monthly") {
				cfg.PlanMonthly = monthly
			}
			if cmd.Flags().Changed("acu") {
				cfg.PlanACULimit = acu
			}
			if err := config.SaveGlobal(); err != nil {
				fmt.Fprintf(os.Stderr, "save config: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Plan set: %s (monthly=%s, ACU limit=%.0f)\n",
				cfg.Plan, report.FormatCost(cfg.PlanMonthly, false), cfg.PlanACULimit)
		},
	}
	c.Flags().Float64Var(&monthly, "monthly", 0, "monthly plan cost in USD")
	c.Flags().Float64Var(&acu, "acu", 0, "monthly ACU limit")
	return c
}

// ---- currency ----

func cmdCurrency() *cobra.Command {
	c := &cobra.Command{
		Use:   "currency",
		Short: "Show or set the display currency",
	}
	c.AddCommand(cmdCurrencyShow(), cmdCurrencySet(), cmdCurrencyReset())
	return c
}

func cmdCurrencyShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the current display currency and rate",
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			cur := cfg.Currency
			if cur == "" {
				cur = "USD"
			}
			rate, ok := Rate(cur)
			fmt.Println(ui.Panel("Currency", fmt.Sprintf(
				"Currency:  %s\nSymbol:    %s\nRate:      1 USD = %.4f %s%s",
				cur,
				Symbol(cur),
				rate,
				cur,
				availNote(ok),
			), 40))
		},
	}
}

func availNote(ok bool) string {
	if ok {
		return ""
	}
	return "\n(hardcoded rate unavailable; USD used)"
}

func cmdCurrencySet() *cobra.Command {
	return &cobra.Command{
		Use:   "set <code>",
		Short: "Set the display currency (ISO 4217 code)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			code := strings.ToUpper(args[0])
			if !IsValidCurrency(code) {
				fmt.Fprintf(os.Stderr, "unknown ISO 4217 currency code: %s\n", code)
				os.Exit(1)
			}
			cfg := config.Global()
			cfg.Currency = code
			if err := config.SaveGlobal(); err != nil {
				fmt.Fprintf(os.Stderr, "save config: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Currency set to %s (%s)\n", code, Symbol(code))
		},
	}
}

func cmdCurrencyReset() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Reset the display currency to USD",
		Run: func(cmd *cobra.Command, args []string) {
			cfg := config.Global()
			cfg.Currency = "USD"
			if err := config.SaveGlobal(); err != nil {
				fmt.Fprintf(os.Stderr, "save config: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Currency reset to USD")
		},
	}
}

// ---- top-cost ----

func cmdTopCost() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "top-cost",
		Short: "Show the most expensive sessions",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader("")
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			summary := ComputeCostSummary(ss, time.Now())
			top := summary.TopN(limit)
			t := ui.NewTable("#", "ID", "Title", "Model", "Requests", "Cost").
				RightAlign(0, 4, 5)
			var totReq int
			var totCost float64
			for i, row := range top {
				costStr := report.FormatCost(row.Cost, false)
				if row.Estimated && row.Cost > 0 {
					costStr += " est"
				}
				t.Row(
					fmt.Sprintf("%d", i+1),
					row.ID,
					row.Title,
					row.Model,
					fmt.Sprintf("%d", row.Requests),
					costStr,
				)
				totReq += row.Requests
				totCost += row.Cost
			}
			t.TotalRow("TOTAL", "", "", "",
				fmt.Sprintf("%d", totReq),
				report.FormatCost(totCost, false))
			fmt.Println(t.String())
		},
	}
	c.Flags().IntVar(&limit, "limit", 10, "number of sessions to show")
	return c
}

// ---- cost ----

func cmdCost() *cobra.Command {
	c := &cobra.Command{
		Use:   "cost",
		Short: "Comprehensive cost overview with all metrics",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader("")
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			cfg := config.Global()
			now := time.Now()
			summary := ComputeCostSummary(ss, now)
			cur := cfg.Currency
			if cur == "" {
				cur = "USD"
			}

			fmt.Println(ui.Panel("Cost Summary", fmt.Sprintf(
				"Total cost:        %s  (%s)\nTotal sessions:    %d\nTotal requests:    %d\nTotal tokens:      %s\nActive days:       %d\n\nCost per request:  %s  (%s)\nCost per session:  %s  (%s)\nCost per token:    $%.6f\nCost per day:      %s  (%s)",
				report.FormatCost(summary.TotalCost, false), FormatMoney(summary.TotalCost, cur),
				summary.TotalSessions,
				summary.TotalRequests,
				report.FormatTok(summary.TotalTokens),
				summary.ActiveDays,
				report.FormatCost(summary.PerRequest, false), FormatMoney(summary.PerRequest, cur),
				report.FormatCost(summary.PerSession, false), FormatMoney(summary.PerSession, cur),
				summary.PerToken,
				report.FormatCost(summary.PerDay, false), FormatMoney(summary.PerDay, cur),
			), 56))
		},
	}
	return c
}
