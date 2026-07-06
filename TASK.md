# Task: Efficiency & Analytics Features (WT3)

## Objective
Implement 17 efficiency and analytics features for DevinMonitor, a Go TUI monitoring tool for Devin CLI.

## CRITICAL RULES
1. Create ONLY new files. Do NOT edit main.go, internal/model/model.go, internal/reader/v1.go, or any existing file.
2. Register all cobra commands via `cli.Register()` in your package's `init()`.
3. Import: `"github.com/garywhat/devinmonitor/internal/cli"`
4. All code must compile with `go build ./...`
5. Use existing model types (extensions.go has CacheStats, EfficiencyScore, OneShotRate, TaskCategory, WasteFinding, CompactionEvent, ModelComparison, ModelCompareRow, ContextAnalysis).
6. Use existing reader: `reader.Open(dataDir)` → `r.Sessions()` returns `[]model.Session`.
7. Session messages have: m.Role, m.Content, m.ToolCalls[] (each has .Name, .Arguments as JSON string), m.Metrics (TTFTMs, InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, TokensPerSec), m.FinishReason, m.GenerationModel, m.NumTokensPreceding.
8. Tool call names include: exec, write, edit, read, grep, web_search, webfetch, run_subagent, read_subagent, mcp_call_tool, mcp_list_tools, skill, todo_write, ask_user_question, etc.
9. edit/write tool arguments contain "file_path" field (JSON). exec arguments contain "command" field.
10. Use existing report helpers: `report.BuildModelRows()`, `report.SessionCost()`.
11. Use existing UI helpers: `ui.NewTable()`, `ui.Panel()`, `ui.ProgressBar()`.

## Files to Create
- `internal/analytics/cache.go` — cache hit ratio, cache efficiency, cache savings, cache leverage
- `internal/analytics/efficiency.go` — token efficiency score, output verbosity, productivity metrics
- `internal/analytics/oneshot.go` — one-shot rate, retry rate (file-level retry detection)
- `internal/analytics/tasks.go` — task categories classification
- `internal/analytics/optimize.go` — waste scan, optimization findings
- `internal/analytics/compaction.go` — compaction detection from num_tokens_preceding drops
- `internal/analytics/modelcompare.go` — model comparison side by side
- `internal/analytics/context.go` — context analysis (what fills context window)
- `internal/analytics/cmd.go` — cobra commands via cli.Register()

## Features to Implement

### 1. Cache Hit Ratio (#25)
- Calculate: cache_read / (cache_read + input_tokens) per session and aggregate
- Show as percentage
- Command: `devinmonitor cache` — shows cache stats

### 2. Cache Efficiency Donut (#26)
- Visual donut chart of cache hit vs miss
- Use ASCII art donut (████ for hit, ░░░░ for miss)
- Part of `cache` command output

### 3. Cache Savings (#27)
- Calculate cost saved by caching: (cache_read × input_price) - (cache_read × cache_price)
- Use model.LookupPricing() for prices
- Show total savings in USD
- Part of `cache` command output

### 4. Cache Leverage (#30)
- Calculate: cache_read / total_input_tokens
- Show as ratio/percentage
- Part of `cache` command output

### 5. Token Efficiency Score (#28)
- Composite: tokens_per_dollar, tokens_per_request, cache_savings_pct
- Show as letter grade (A-F) or numeric score
- Command: `devinmonitor efficiency`

### 6. Output Verbosity (#29)
- Calculate: output_tokens / request_count
- Show average output length per request
- Part of `efficiency` command output

### 7. One-Shot Rate (#31)
- Parse edit/write tool call arguments JSON to extract file_path
- Detect retry: same file edited again after an exec/shell_command in between
- Calculate: % of edit turns that succeeded without retry
- Track per-file retry counts
- Part of `efficiency` command output

### 8. Retry Rate (#32)
- Average retries per edit operation
- Show files with most retries
- Part of `efficiency` command output

### 9. Task Categories (#33)
- Classify each session/turn into 13 categories based on tool usage and keywords:
  Coding (edit/write), Debugging (error/fix keywords), Testing (pytest/vitest/jest in exec),
  Exploration (read/grep without edits), Planning (todo_write/exit_plan_mode),
  Delegation (run_subagent), Git Ops (git in exec), Build/Deploy (npm build/docker),
  Conversation (no tools), General (uncategorized)
- Command: `devinmonitor tasks` — shows task category breakdown

### 10. Optimize / Waste Scan (#34)
- Scan for waste patterns:
  - Cache hit < 80% (unstable context)
  - High retry rate on edits
  - Excessive sub-agent fan-out
  - Long sessions with low output
  - Repeated read calls to same files
- Generate copy-pasteable fix suggestions
- Command: `devinmonitor optimize [--days 30]`

### 11. Productivity Metrics (#36) — partial
- Tokens/Min: output_tokens / session_duration_minutes
- Code Ratio: tool_calls / total_messages (code vs conversation ratio)
- Part of `efficiency` command output
- NOTE: Lines/Hour is NOT implementable (no line count data) — skip it

### 12. Compaction Detection (#37)
- Detect context compaction: num_tokens_preceding drops significantly between consecutive messages
- Show compaction events with before/after token counts
- Part of `analytics` command or standalone `devinmonitor compaction`

### 13. Rate Limit / Quota Detection (#38) — partial
- Devin uses ACU not 5h/7d limits; monitor ACU budget exhaustion instead
- Alert when ACU spend approaches configured plan limit
- Part of `optimize` or `analytics` command

### 14. Model Comparison (#39)
- Side-by-side comparison of models: cost, tokens, latency, cache hit, tokens/sec
- Command: `devinmonitor model-compare [model1] [model2]`

### 15. Yield (productive vs abandoned) (#40) — partial
- Need git integration: read git log in working_directory
- Correlate sessions with commits
- Show productive spend (led to commits) vs abandoned (no commits)
- Command: `devinmonitor yield [--days 30]`
- If git is not available, show a message saying git integration requires a git repo

### 16. Context Analysis (#41)
- Analyze what fills a session's context window
- Break down by tool type (system prompts, tool results, user messages, assistant messages)
- Use m.NumTokensPreceding to track context growth
- Command: `devinmonitor context <session-id>`

### 17. Analytics Summary Command
- Command: `devinmonitor analytics` — comprehensive analytics overview

## Implementation Pattern
```go
package analytics

import (
    "encoding/json"
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
    cli.Register(cmdModelCompare)
    cli.Register(cmdYield)
    cli.Register(cmdContext)
    cli.Register(cmdAnalytics)
}
```

## Verification
Run `go build ./...` from the worktree root. It must compile with zero errors.
Do NOT run `go mod tidy` — dependencies are already in go.mod.
