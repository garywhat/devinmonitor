# Task: Filter, Search & Export Features (WT4)

## Objective
Implement 17 filter, search, and export features for DevinMonitor, a Go TUI monitoring tool for Devin CLI.

## CRITICAL RULES
1. Create ONLY new files. Do NOT edit main.go, internal/model/model.go, internal/reader/v1.go, or any existing file.
2. Register all cobra commands via `cli.Register()` in your package's `init()`.
3. Import: `"github.com/garywhat/devinmonitor/internal/cli"`
4. All code must compile with `go build ./...`
5. Use existing model types (extensions.go has FilterOptions, SearchResult).
6. Use existing reader: `reader.Open(dataDir)` → `r.Sessions()`, and v1Reader has `SearchMessages()`, `FilteredSessions()`, `PromptHistory()`, `RenderedCommits()`.
7. Use existing report helpers: `report.BuildSessionRows()`, `report.FormatCost()`, `report.FormatTok()`.
8. Use existing UI helpers: `ui.NewTable()`, `ui.Panel()`.
9. Use existing export: `"github.com/garywhat/devinmonitor/internal/export"` has `BuildDocument()`, `WriteJSON()`.
10. Use config: `"github.com/garywhat/devinmonitor/internal/config"` for pinned sessions and saved flags.

## Files to Create
- `internal/filter/filter.go` — filter sessions, sort sessions, pinned sessions
- `internal/filter/search.go` — full-text search, project search & merge
- `internal/export/csv.go` — CSV export
- `internal/export/markdown.go` — markdown table output
- `internal/export/html.go` — HTML export
- `internal/export/report.go` — shareable text/SVG report
- `internal/export/status.go` — compact one-liner, state file, terminal title
- `internal/export/backup.go` — database backup/download
- `internal/filterexport/cmd.go` — cobra commands via cli.Register()

## Features to Implement

### 1. Filter Sessions (#42)
- Filter by model, project, agent_mode
- Command: `devinmonitor filter --model <name> --project <name> --mode <mode>`
- Uses reader.FilteredSessions() from extensions.go

### 2. Filter by Date Range (#43)
- --from-date and --to-date flags
- Works on all list/report commands
- Part of `filter` command

### 3. Filter by Project (#44)
- --project flag with substring matching on working_directory
- --exclude flag to exclude projects
- Part of `filter` command

### 4. Sort Sessions (#45)
- Sort by: cost, tokens, context, duration, recent
- --sort flag: `devinmonitor sessions --sort cost`
- Implement as a sort wrapper around session list

### 5. Full-Text Search (#46)
- Search across all session message content
- Uses reader.SearchMessages() from extensions.go
- Command: `devinmonitor search "<query>" [--limit 50]`

### 6. Project Search & Merge (#47)
- Live substring filter over projects
- Merge projects that are the same codebase (renamed/moved)
- Command: `devinmonitor projects --search <query> --merge <name1,name2>`

### 7. Pinned Sessions (#48)
- Pin sessions to top of list
- Store pinned session IDs in config.Global().PinnedSessions
- Commands: `devinmonitor pin <session-id>`, `devinmonitor unpin <session-id>`
- Pinned sessions shown first in `sessions` output

### 8. CSV Export (#49)
- Export session data as CSV
- Command: `devinmonitor export --format csv [--output file.csv]`
- Include: id, title, model, project, cost, tokens, duration, created_at

### 9. Markdown Table Output (#50)
- Export as paste-friendly markdown table
- Command: `devinmonitor export --format markdown`

### 10. Shareable Usage Report (#51)
- Text-based shareable receipt (like toktrack report)
- Show last 7/30/N days summary
- Command: `devinmonitor report [--days 7|--month|--svg]`

### 11. HTML Export (#52)
- Interactive HTML snapshot with tables
- Command: `devinmonitor export --format html [--output file.html]`

### 12. Compact One-Liner Status (#53)
- Single-line status for status bars
- Format: "Today: $X | Month: $Y | Sessions: Z"
- Command: `devinmonitor status --compact`

### 13. Per-Command JSON Output (#54)
- --json flag on all report commands
- Structured JSON output to stdout
- Add to: sessions, daily, weekly, monthly, models, projects, agents

### 14. State File Writing (#55)
- Write snapshot to state file atomically
- Command: `devinmonitor status --write-state --state-file ~/.devinmonitor/state.json`

### 15. Terminal Title Setting (#56)
- Set terminal title from usage snapshot
- Command: `devinmonitor status --set-title --title-format "{cost} {sessions}"`

### 16. Shell Integration (#57)
- Output for shell prompt integration
- Command: `devinmonitor status --shell` (outputs just the cost number for PS1)

### 17. Database Backup (#58)
- Export normalized JSON of all data, or copy sessions.db
- Command: `devinmonitor backup [--output file.json|--db]`

## Implementation Pattern
```go
package filterexport

import (
    "encoding/csv"
    "os"
    "github.com/spf13/cobra"
    "github.com/garywhat/devinmonitor/internal/cli"
    "github.com/garywhat/devinmonitor/internal/config"
    "github.com/garywhat/devinmonitor/internal/export"
    "github.com/garywhat/devinmonitor/internal/model"
    "github.com/garywhat/devinmonitor/internal/reader"
    "github.com/garywhat/devinmonitor/internal/report"
    "github.com/garywhat/devinmonitor/internal/ui"
)

func init() {
    cli.Register(cmdFilter)
    cli.Register(cmdSearch)
    cli.Register(cmdPin)
    cli.Register(cmdUnpin)
    cli.Register(cmdReport)
    cli.Register(cmdStatus)
    cli.Register(cmdBackup)
}
```

NOTE: For export format expansion (CSV/markdown/HTML), add new files in `internal/export/` package (csv.go, markdown.go, html.go) with exported functions like `WriteCSV()`, `WriteMarkdown()`, `WriteHTML()`. These are new files in an existing package — no conflict with existing export.go.

## Verification
Run `go build ./...` from the worktree root. It must compile with zero errors.
Do NOT run `go mod tidy` — dependencies are already in go.mod.
