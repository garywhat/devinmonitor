# DevinMonitor 98-Feature Implementation Plan

## Architecture: Command Registry Pattern

To enable parallel development across worktrees without merge conflicts:
- Each feature group creates **new files only** (new packages + new `cmd_*.go` files)
- A shared `internal/cli/registry.go` lets packages self-register commands via `init()`
- `main.go` only needs `_ import` lines added during integration
- **Zero conflicts** because each worktree creates different files

## Worktree Task Breakdown

### WT0: Foundation (sequential, prerequisite)
- `internal/cli/registry.go` — command registry
- `internal/model/extensions.go` — new model types (Budget, CacheStats, TrendPoint, Filter, etc.)
- `internal/reader/extensions.go` — new reader methods (SearchMessages, WatchDB, PromptHistory, RenderedCommits)
- `internal/config/config.go` — config file load/save (~/.devinmonitor/config.json)

### WT1: Budget & Cost Management (features #1-12) → `internal/budget/`
- Budget guardrails, burn rate, budget gauge, cost projection
- Subscription savings, plan tracking, currency conversion
- Cost-per-request, cost-per-session, most expensive sessions, avg cost
- Files: `internal/budget/budget.go`, `internal/budget/burnrate.go`, `internal/budget/projection.go`, `cmd_budget.go`

### WT2: Trends & Charts (features #13-24) → `internal/trends/`
- ASCII trend charts, daily cost chart, activity heatmap, contribution calendar
- Delta banner, sparklines in tables, period comparison, month-over-month
- Cumulative cost trend, stacked area chart, 24h usage chart, trend range toggle
- Files: `internal/trends/charts.go`, `internal/trends/heatmap.go`, `internal/trends/comparison.go`, `cmd_trends.go`

### WT3: Efficiency & Analytics (features #25-41) → `internal/analytics/`
- Cache hit ratio, cache efficiency donut, cache savings, cache leverage
- Token efficiency score, output verbosity, one-shot rate, retry rate
- Task categories, optimize/waste scan, productivity metrics
- Compaction detection, rate limit detection, model comparison, yield, context analysis
- Files: `internal/analytics/cache.go`, `internal/analytics/efficiency.go`, `internal/analytics/tasks.go`, `internal/analytics/optimize.go`, `cmd_analytics.go`

### WT4: Filter/Sort/Search + Export (features #42-58) → `internal/filter/` + `internal/export/` expansion
- Filter sessions, filter by date/project, sort sessions, full-text search
- Project search & merge, pinned sessions
- CSV export, markdown table, shareable report, HTML export
- Compact one-liner, per-command JSON, state file, terminal title, shell integration, DB backup
- Files: `internal/filter/filter.go`, `internal/filter/search.go`, `internal/export/csv.go`, `internal/export/markdown.go`, `internal/export/html.go`, `internal/export/report.go`, `cmd_filter.go`, `cmd_export2.go`

### WT5: TUI Interaction (features #59-72) → `internal/live/` expansion
- Multi-theme, command palette, settings panel, help overlay
- Session detail popup, model breakdown popup, split pane
- List/pane view toggle, time window cycling, session replay
- Live log tailing, timeline view, demo mode, quick non-interactive
- Files: `internal/live/themes.go`, `internal/live/popup.go`, `internal/live/replay.go`, `internal/live/timeline.go`, `internal/live/demo.go`, `cmd_live2.go`

### WT6: CLI Commands & Integration (features #80-93, 94-102) → `internal/integration/` + `internal/project/`
- Period shortcuts (today/week/month/all), status command, CLI mode for agents
- MCP server, web dashboard, webhook notifications, desktop notifications
- Auto-refresh, command aliases, save flags, model aliases, custom pricing
- Auto-detect timezone, custom reset hour
- Per-project drill-down, project detail, project cost attribution
- Tool cost attribution, MCP server breakdown, shell commands breakdown
- Per-activity breakdown, git integration
- Files: `internal/integration/mcp.go`, `internal/integration/web.go`, `internal/integration/notify.go`, `internal/project/drilldown.go`, `internal/project/attribution.go`, `internal/project/git.go`, `cmd_integration.go`, `cmd_project.go`

## Execution Order
1. WT0 (foundation) — sequential, blocks all others
2. WT1-WT6 — parallel (6 worktrees, 6 Claude agents)
3. Integration — merge all, add imports to main.go, build, test
