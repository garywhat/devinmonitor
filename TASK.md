# Task: TUI Interaction Features (WT5)

## Objective
Implement 14 TUI interaction features for DevinMonitor, a Go TUI monitoring tool for Devin CLI.

## CRITICAL RULES
1. Create ONLY new files. Do NOT edit main.go, internal/model/model.go, internal/reader/v1.go, or any existing file.
2. Register all cobra commands via `cli.Register()` in your package's `init()`.
3. Import: `"github.com/garywhat/devinmonitor/internal/cli"`
4. All code must compile with `go build ./...`
5. Use existing model types from `internal/model/`.
6. Use existing reader: `reader.Open(dataDir)` → `r.Sessions()`, `r.Session(id)`.
7. The existing live package (`internal/live/live.go`) uses bubbletea (`"github.com/charmbracelet/bubbletea"`) and lipgloss (`"github.com/charmbracelet/lipgloss"`). Follow the same pattern.
8. Use config: `"github.com/garywhat/devinmonitor/internal/config"` for theme, settings.
9. Do NOT modify live.go. Create new files in `internal/live/` package (themes.go, popup.go, etc.) — these are new files in an existing package, no conflict.

## Files to Create
- `internal/live/themes.go` — multi-theme support (15 themes)
- `internal/live/popup.go` — session detail popup, model breakdown popup
- `internal/live/replay.go` — session replay mode
- `internal/live/timeline.go` — timeline view, live log tailing
- `internal/live/demo.go` — demo mode with synthetic data
- `internal/live/settings.go` — settings panel, help overlay, command palette
- `internal/tuiext/cmd.go` — cobra commands via cli.Register()

## Features to Implement

### 1. Multi-Theme Support (#59)
- 15 built-in themes: auto, dark, light, dracula, nord, solarized-dark, solarized-light, gruvbox, monokai, tokyo-night, catppuccin, everforest, gruvbox-light, rose-pine, github
- Theme definitions with color palettes (background, foreground, accent, warning, success, error)
- Load from config.Global().Theme
- Command: `devinmonitor theme [list|set <name>|show]`
- Apply to all TUI rendering via lipgloss styles

### 2. Command Palette (#60)
- Ctrl+P opens a fuzzy-search command palette overlay
- Lists all available devinmonitor commands
- Type to filter, Enter to execute
- Integrate into live TUI model

### 3. Settings Panel (#61)
- Interactive settings form in TUI
- Edit: theme, refresh interval, currency, budget limits, timezone
- Save to config file on change
- Accessible via 's' key in live mode

### 4. Help Overlay (#62)
- '?' key shows help overlay with all keyboard shortcuts
- Categorized: Navigation, Filtering, Views, Actions
- Press '?' or Esc to dismiss

### 5. Session Detail Popup (#63)
- Press Enter on a session in live view to see full details
- Shows: title, model, cost breakdown, token breakdown, tool usage, duration, messages count
- Esc to close

### 6. Model Breakdown Popup (#64)
- Press 'm' in live view to see model distribution popup
- Shows per-model: requests, tokens, cost, latency, cache hit
- Pie chart (ASCII) of cost distribution

### 7. Split Pane (#65)
- Split view: sessions list on left, details on right
- Toggle with '|' key
- Both panes scroll independently

### 8. List/Pane View Toggle (#66)
- 'v' key toggles between list view (table) and pane view (card grid)
- List: compact rows, Pane: larger cards with more info
- Remember preference in config

### 9. Time Window Cycling (#67)
- 't' key cycles through time windows: today → week → month → all
- Updates all displayed metrics accordingly
- Show current window in status bar

### 10. Session Replay (#68)
- `devinmonitor replay <session-id>` — plays back a session message by message
- Shows messages in chronological order with timestamps
- Space to pause/resume, Left/Right to step, 'q' to quit
- Uses r.Session(id) to get full message list

### 11. Live Log Tailing (#69)
- Stream new messages as they arrive in real-time
- Poll sessions.db for new message_nodes
- Show in a split pane below session list
- 'l' key to toggle log tailing

### 12. Timeline View (#70)
- Visual timeline of session activity
- Shows message events on a horizontal timeline
- Color-coded by message type (user=blue, assistant=green, tool=yellow)
- Command: `devinmonitor timeline <session-id>`

### 13. Demo Mode (#71)
- `devinmonitor live --demo` — runs with synthetic data
- Generate 5-10 fake sessions with realistic data
- Useful for screenshots, testing, exploration
- No real database needed

### 14. Quick Non-Interactive Mode (#72)
- `devinmonitor live --once` — render one frame and exit
- `devinmonitor live --light` — minimal rendering for slow terminals
- Useful for scripts and CI

## Implementation Pattern
```go
package tuiext

import (
    "github.com/spf13/cobra"
    "github.com/garywhat/devinmonitor/internal/cli"
    "github.com/garywhat/devinmonitor/internal/config"
    "github.com/garywhat/devinmonitor/internal/live"
    "github.com/garywhat/devinmonitor/internal/reader"
)

func init() {
    cli.Register(cmdTheme)
    cli.Register(cmdReplay)
    cli.Register(cmdTimeline)
    // live --demo, --once, --light are flags on existing live command
    // but since we can't edit live.go, create a new command
    cli.Register(cmdLiveExt)
}
```

For theme definitions, create a `Theme` struct and a `Themes` map in themes.go:
```go
type Theme struct {
    Name, Bg, Fg, Accent, Warning, Success, Error, Muted string
}
var Themes = map[string]Theme{
    "dracula": {Bg: "#282a36", Fg: "#f8f8f2", ...},
    ...
}
```

## Verification
Run `go build ./...` from the worktree root. It must compile with zero errors.
Do NOT run `go mod tidy` — dependencies are already in go.mod.
