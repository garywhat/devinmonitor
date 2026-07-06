# Task: Trends & Charts Features (WT2)

## Objective
Implement 12 trend and chart features for DevinMonitor, a Go TUI monitoring tool for Devin CLI.

## CRITICAL RULES
1. Create ONLY new files. Do NOT edit main.go, internal/model/model.go, internal/reader/v1.go, or any existing file.
2. Register all cobra commands via `cli.Register()` in your package's `init()`.
3. Import: `"github.com/garywhat/devinmonitor/internal/cli"`
4. All code must compile with `go build ./...`
5. Use existing model types from `internal/model/` (extensions.go has TrendPoint, HeatmapCell, ContributionDay, PeriodComparison).
6. Use existing reader: `reader.Open(dataDir)` → `r.Sessions()` returns `[]model.Session`.
7. Use existing report helpers: `report.BuildDaily()`, `report.BuildWeekly()`, `report.BuildMonthly()` return `[]report.TimeRow`.
8. Use existing UI helpers: `ui.NewTable()`, `ui.Panel()`, `ui.ProgressBar()`, `ui.Sparkline()`.
9. Use lipgloss for styling: `"github.com/charmbracelet/lipgloss"`.

## Files to Create
- `internal/trends/charts.go` — ASCII trend charts, daily cost chart, cumulative cost, stacked area
- `internal/trends/heatmap.go` — activity heatmap, contribution calendar
- `internal/trends/comparison.go` — period comparison, month-over-month, delta calculation
- `internal/trends/sparklines.go` — sparklines in tables, delta banner
- `internal/trends/cmd.go` — cobra commands via cli.Register()

## Features to Implement

### 1. ASCII Trend Charts (#13)
- ASCII step chart showing cost over 7/30/90 days
- Use `report.BuildDaily()` to get daily cost data
- Render as ASCII bar chart or step chart in terminal
- Command: `devinmonitor trends [--days 7|30|90]`

### 2. Daily Cost Chart (#14)
- Bar chart of daily cost for selected period
- Show date labels on x-axis, cost on y-axis
- Part of `trends` command output

### 3. Activity Heatmap (#15)
- GitHub-style weekday × hour grid showing activity intensity
- Use message timestamps from sessions (s.Messages[].CreatedAt)
- Color intensity based on activity count
- Command: `devinmonitor heatmap`

### 4. Contribution Calendar (#16)
- Full-year GitHub-style contribution calendar
- Each day colored by activity level (0-4)
- Command: `devinmonitor calendar [--year 2026]`

### 5. Delta Banner (#17)
- Show "since last check: +$X, +Y tokens" banner
- Compare current poll vs previous (store last snapshot in config or temp file)
- Part of `trends` command output

### 6. Sparklines in Tables (#18)
- Add inline sparkline columns to session tables
- Show token/cost trend per session as mini sparkline
- Use `ui.Sparkline()` helper

### 7. Period Comparison (#19)
- Compare two time periods side by side
- Show 8 metrics with delta % and color-coded indicators
- Command: `devinmonitor compare --current <period> --previous <period>`

### 8. Month-over-Month Comparison (#20)
- Compare current month vs previous month
- Show delta calculations for cost, tokens, sessions
- Part of `trends` command or `compare` command

### 9. Cumulative Cost Trend (#21)
- Running total of cost over time
- Show as ASCII line/step chart
- Part of `trends` command output

### 10. Stacked Area Chart (#22)
- Per-model stacked history chart
- Show how different models contribute to total cost over time
- ASCII rendering with different characters per model

### 11. 24-hour Usage Chart (#23)
- Show activity over last 24 hours in hourly buckets
- Use message timestamps to bucket by hour
- Command: `devinmonitor today` (alias for 24h view)

### 12. Trend Range Toggle (#24)
- Support week/month/all range toggling
- `--range week|month|all` flag on trends command
- Keyboard shortcut info for TUI mode

## Implementation Pattern
```go
package trends

import (
    "github.com/spf13/cobra"
    "github.com/garywhat/devinmonitor/internal/cli"
    "github.com/garywhat/devinmonitor/internal/model"
    "github.com/garywhat/devinmonitor/internal/reader"
    "github.com/garywhat/devinmonitor/internal/report"
    "github.com/garywhat/devinmonitor/internal/ui"
)

func init() {
    cli.Register(cmdTrends)
    cli.Register(cmdHeatmap)
    cli.Register(cmdCalendar)
    cli.Register(cmdCompare)
    cli.Register(cmdToday)
}

func cmdTrends() *cobra.Command {
    var days int
    c := &cobra.Command{
        Use:   "trends",
        Short: "Show cost and usage trends",
        Run: func(cmd *cobra.Command, args []string) {
            // implementation
        },
    }
    c.Flags().IntVar(&days, "days", 7, "Number of days to show (7, 30, 90)")
    return c
}
```

## Verification
Run `go build ./...` from the worktree root. It must compile with zero errors.
Do NOT run `go mod tidy` — dependencies are already in go.mod.
