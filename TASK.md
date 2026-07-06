# Task: CLI Commands, Integration & Project Features (WT6)

## Objective
Implement 27 CLI command, integration, and project management features for DevinMonitor, a Go TUI monitoring tool for Devin CLI.

## CRITICAL RULES
1. Create ONLY new files. Do NOT edit main.go, internal/model/model.go, internal/reader/v1.go, or any existing file.
2. Register all cobra commands via `cli.Register()` in your package's `init()`.
3. Import: `"github.com/garywhat/devinmonitor/internal/cli"`
4. All code must compile with `go build ./...`
5. Use existing model types (extensions.go has ProjectDetail, ToolAttribution, SessionListItem, AlertItem, Notification).
6. Use existing reader: `reader.Open(dataDir)` → `r.Sessions()`, `r.Session(id)`.
7. Use existing report helpers: `report.BuildProjectRows()`, `report.BuildModelRows()`, `report.SessionCost()`.
8. Use existing UI helpers: `ui.NewTable()`, `ui.Panel()`.
9. Use config: `"github.com/garywhat/devinmonitor/internal/config"` for settings.
10. Use pricing: `model.LookupPricing()`, `model.EstimateCost()` for custom pricing.

## Files to Create
- `internal/integration/mcp.go` — MCP server exposing usage data
- `internal/integration/web.go` — local web dashboard server
- `internal/integration/notify.go` — desktop + webhook notifications
- `internal/integration/status.go` — status command, CLI mode for agents (ls/whoami/alerts)
- `internal/integration/shortcuts.go` — period shortcuts, command aliases, auto-refresh
- `internal/integration/settings.go` — save flags, model aliases, custom pricing, timezone, reset hour
- `internal/project/drilldown.go` — per-project drill-down, project detail
- `internal/project/attribution.go` — tool cost attribution, MCP server breakdown, shell commands breakdown, per-activity breakdown
- `internal/project/git.go` — git integration (read git log, correlate commits)
- `internal/integration/cmd.go` — cobra commands via cli.Register()

## Features to Implement

### Part A: CLI Commands & Integration (features #80-93)

#### 1. Period Shortcuts (#80)
- Aliases: `devinmonitor today`, `devinmonitor week`, `devinmonitor month`, `devinmonitor all`
- Each shows the appropriate time-bounded report
- These are thin wrappers around existing daily/weekly/monthly commands

#### 2. Status Command (#81)
- Show current snapshot: active sessions, today's cost, month's cost, ACU remaining
- Command: `devinmonitor status`

#### 3. CLI Mode for Agents (#82)
- `devinmonitor ls --json` — list sessions as JSON (SessionListItem[])
- `devinmonitor whoami --json` — show current user/config info as JSON
- `devinmonitor alerts --json` — show alerts as JSON (AlertItem[])
- Designed for other AI agents to consume

#### 4. MCP Server (#83)
- Expose usage data via MCP (Model Context Protocol) over stdio
- Tools: get_sessions, get_session, get_cost_summary, get_alerts
- Command: `devinmonitor mcp` — starts MCP server on stdio
- Use JSON-RPC 2.0 protocol

#### 5. Web Dashboard (#84)
- Local HTTP server with web UI
- SSE for real-time updates
- Charts via embedded HTML/JS (no external deps, use vanilla JS + canvas)
- Command: `devinmonitor web [--port 8080]`

#### 6. Webhook Notifications (#85)
- Send notifications to Discord, Slack, Telegram webhooks
- Trigger on: budget threshold, session complete, alert detected
- URL from config.Global().NotifyWebhook
- Auto-detect webhook type from URL (discord.com, hooks.slack.com, api.telegram.org)

#### 7. Desktop Notifications (#86)
- OS-native notifications (Windows toast, macOS notification center, Linux libnotify)
- Trigger on: budget threshold, session complete
- Command: `devinmonitor notify --test` to test notification

#### 8. Auto-Refresh Dashboard (#87)
- `devinmonitor live --refresh` already exists; add auto-refresh to report commands
- `devinmonitor daily --watch` — re-renders every N seconds
- `devinmonitor sessions --watch` — live updating session list

#### 9. Command Aliases (#88)
- Short aliases: `dm` → `devinmonitor`, `dm s` → `devinmonitor sessions`, etc.
- Configurable via config file
- Command: `devinmonitor alias [list|add <short> <long>|remove <short>]`

#### 10. Save Flags (#89)
- Persist frequently used flags to config
- `devinmonitor sessions --sort cost --save` — saves --sort cost as default
- Next `devinmonitor sessions` uses saved sort

#### 11. Model Aliases (#90)
- Map provider model names to canonical names for pricing
- `devinmonitor model-alias [list|add <alias> <canonical>|remove <alias>]`
- Stored in config.Global().ModelAliases

#### 12. Custom Pricing (#91)
- User-defined pricing overrides
- `devinmonitor pricing [list|set <model> --input <usd> --output <usd> --cache-read <usd>|remove <model>]`
- Stored in config.Global().CustomPricing
- Merged with builtin pricing at lookup time

#### 13. Auto-Detect Timezone (#92)
- Auto-detect system timezone
- `devinmonitor config timezone [show|set <tz>|auto]`
- Used for daily reset boundaries

#### 14. Custom Reset Hour (#93)
- Set custom daily reset hour (0-23)
- `devinmonitor config reset-hour <hour>`
- Stored in config.Global().ResetHour
- Affects daily aggregation boundaries

### Part B: Project Management (features #94-102)

#### 15. Per-Project Drill-Down (#94)
- Drill from project summary to day-by-day, per-model usage
- Command: `devinmonitor project <name> [--days 30]`
- Shows: daily breakdown, model breakdown, tool breakdown

#### 16. Project Detail Dialog (#95)
- Detailed project view with KPIs, charts, session list
- Command: `devinmonitor project <name> --detail`
- Shows: total cost, total tokens, session count, avg cost, daily chart, model distribution, top tools, sessions list

#### 17. Project Cost Attribution (#96)
- Enhanced project cost breakdown
- Show cost per project as percentage of total
- Command: `devinmonitor projects --attribution`

#### 18. Tool Cost Attribution (#98)
- Distribute session cost across tools proportionally
- Based on tool call count or estimated token contribution
- Command: `devinmonitor tools [--session <id>|--all]`

#### 19. MCP Server Breakdown (#99)
- Break down usage by MCP server (mcp_call_tool, mcp_list_tools, mcp_read_resource)
- Parse tool call arguments JSON to extract server_name
- Command: `devinmonitor mcp-usage`

#### 20. Shell Commands Breakdown (#100)
- Break down exec/shell_command usage
- Parse command from tool call arguments JSON
- Categorize: git, npm, go, python, docker, make, etc.
- Command: `devinmonitor shell-usage`

#### 21. Per-Activity Breakdown (#101)
- Break down by activity type (coding, debugging, testing, etc.)
- Uses task categorization logic
- Command: `devinmonitor activities`

#### 22. Git Integration (#102)
- Read git log in session's working_directory
- Correlate sessions with commits by timestamp overlap
- Show: commits per session, productive vs abandoned sessions
- Command: `devinmonitor git [--session <id>|--days 30]`
- If working_directory is not a git repo, show message

### Part C: Data Persistence (features #73, #75, #77-79)

#### 23. Persistent Cache (#73)
- Cache aggregated results to local file
- `~/.devinmonitor/cache/` directory
- Store daily/weekly/monthly summaries
- Invalidate on new sessions

#### 24. Usage Warehouse (#75)
- Local time-series snapshot store
- Take periodic snapshots of usage data
- Store in `~/.devinmonitor/warehouse/` as JSON files
- Command: `devinmonitor warehouse [snapshot|list|show <id>]`

#### 25. File Watcher (#77)
- Use fs.watch (or polling on Windows) to detect sessions.db changes
- Alternative to interval-based polling
- `devinmonitor live --watch` flag

#### 26. Config File (#78)
- Already implemented in internal/config/config.go
- Command: `devinmonitor config [show|set <key> <value>|reset]`

#### 27. Provenance Labels (#79)
- Mark data sources: "official" (from sessions.metadata), "estimated" (from token pricing)
- Show labels in output
- Part of all cost displays

## Implementation Pattern
```go
package integration

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
    cli.Register(cmdToday)
    cli.Register(cmdWeek)
    cli.Register(cmdMonth)
    cli.Register(cmdAll)
    cli.Register(cmdStatus)
    cli.Register(cmdLs)
    cli.Register(cmdWhoami)
    cli.Register(cmdAlerts)
    cli.Register(cmdMCP)
    cli.Register(cmdWeb)
    cli.Register(cmdNotify)
    cli.Register(cmdAlias)
    cli.Register(cmdModelAlias)
    cli.Register(cmdPricing)
    cli.Register(cmdConfig)
    cli.Register(cmdWarehouse)
}
```

For project features, create a separate package:
```go
package project

func init() {
    cli.Register(cmdProject)
    cli.Register(cmdProjectDetail)
    cli.Register(cmdTools)
    cli.Register(cmdMCPUsage)
    cli.Register(cmdShellUsage)
    cli.Register(cmdActivities)
    cli.Register(cmdGit)
}
```

## Verification
Run `go build ./...` from the worktree root. It must compile with zero errors.
Do NOT run `go mod tidy` — dependencies are already in go.mod.
