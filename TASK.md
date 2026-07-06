# Task: Budget & Cost Management Features (WT1)

## Objective
Implement 12 budget and cost management features for DevinMonitor, a Go TUI monitoring tool for Devin CLI.

## CRITICAL RULES
1. Create ONLY new files. Do NOT edit main.go, internal/model/model.go, internal/reader/v1.go, or any existing file.
2. Register all cobra commands via `cli.Register()` in your package's `init()`.
3. Import the command registry: `"github.com/garywhat/devinmonitor/internal/cli"`
4. All code must compile with `go build ./...`
5. Use existing model types from `internal/model/` (see extensions.go for new types like Budget, BurnRate, CostProjection, CostBreakdown).
6. Use existing reader: `reader.Open(dataDir)` → `r.Sessions()` returns `[]model.Session`.
7. Use existing config: `"github.com/garywhat/devinmonitor/internal/config"` → `config.Global()` for user settings.
8. Use existing report helpers: `"github.com/garywhat/devinmonitor/internal/report"` → `report.SessionCost()`, `report.FormatCost()`.
9. Use existing UI helpers: `"github.com/garywhat/devinmonitor/internal/ui"` → `ui.NewTable()`, `ui.Panel()`, `ui.ProgressBar()`.

## Files to Create
- `internal/budget/budget.go` — budget guardrails, budget gauge, plan tracking
- `internal/budget/burnrate.go` — burn rate ($/hr, $/day), cost projection
- `internal/budget/currency.go` — multi-currency conversion
- `internal/budget/costmetrics.go` — cost-per-request, cost-per-session, most expensive, avg cost
- `internal/budget/cmd.go` — cobra command definitions, registered via cli.Register()

## Features to Implement

### 1. Budget Guardrails (#1)
- Read budget limits from config.Global() (BudgetDaily, BudgetWeekly, BudgetMonthly)
- Calculate current spend per period from sessions
- Show warning colors when approaching limits (80% yellow, 100% red)
- Command: `devinmonitor budget` — shows budget status with progress bars

### 2. Burn Rate (#2)
- Calculate $/hr from recent session activity (last hour, last 24h)
- Extrapolate to $/day, $/week, $/month
- Use ACU cost from sessions (s.ACUCost) + config.Global().ACURate for USD conversion
- Command: `devinmonitor burn-rate`

### 3. Budget Gauge (#3)
- Visual progress bar with color thresholds (green <60%, yellow 60-80%, red >80%)
- Show for daily, weekly, monthly budgets
- Part of the `budget` command output

### 4. Cost Projection (#4)
- Based on historical daily spend, predict end-of-month total
- Calculate days to budget exhaustion
- Confidence based on data volume (more days = higher confidence)
- Command: `devinmonitor projection`

### 5. Subscription Savings (#5) — partial
- User configures plan in config (Plan, PlanMonthly, PlanACULimit)
- Compare API-equivalent cost vs plan cost
- Show savings amount and percentage
- Part of `budget` command output

### 6. Plan Tracking (#6) — partial
- User configures Devin plan (Plan, PlanMonthly, PlanACULimit in config)
- Track ACU usage against plan limit
- Show overage if exceeded
- Command: `devinmonitor plan` (show), `devinmonitor plan set <name> --monthly <usd> --acu <limit>`

### 7. Currency Multi-currency (#7)
- Support 162 ISO 4217 currencies
- Read config.Global().Currency for target currency
- Hardcoded common rates (USD, EUR, GBP, JPY, CNY, KRW, TWD, HKD, INR, AUD, CAD)
- Command: `devinmonitor currency [set <code>|show|reset]`

### 8. Cost-per-request (#8)
- Total cost / total assistant messages (requests)
- Show in budget/cost summary

### 9. Cost-per-session (#9)
- Total cost / total sessions
- Show in budget/cost summary

### 10. Most Expensive Sessions (#11)
- Sort sessions by cost descending, show top N
- Command: `devinmonitor top-cost [--limit 10]`

### 11. Avg Cost per Session (#12)
- Simple aggregate, show in cost summary

### 12. Cost Summary Command
- Command: `devinmonitor cost` — comprehensive cost overview with all metrics above

## Implementation Pattern (follow exactly)
```go
package budget

import (
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

func cmdBudget() *cobra.Command {
    return &cobra.Command{
        Use:   "budget",
        Short: "Show budget status with guardrails",
        Run: func(cmd *cobra.Command, args []string) {
            // implementation
        },
    }
}
```

## Verification
Run `go build ./...` from the worktree root. It must compile with zero errors.
Do NOT run `go mod tidy` — dependencies are already in go.mod.
