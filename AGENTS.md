# DevinMonitor - Agent Guidelines

## Tool Preferences

### Serena MCP (preferred for code navigation and editing)

**Always prefer Serena MCP tools over native grep/read/edit for the following operations:**

1. **Understanding file structure**: Use `get_symbols_overview` instead of `read` to get function/class/variable layout
2. **Finding symbol implementations**: Use `find_symbol` with `name_path_pattern` instead of `grep "func.*Name"`
3. **Finding references across files**: Use `find_references` instead of `grep "Name("` — semantic accuracy, no false matches in comments/strings
4. **Replacing large function bodies**: Use `replace_symbol_body` instead of `edit` — no need to paste the original 200-line function as old_string
5. **Replacing code sections with wildcards**: Use `replace_content` in regex mode (`beginning.*?end`) instead of `edit` — saves tokens, more tolerant of whitespace differences
6. **Inserting new functions/methods**: Use `insert_after_symbol` / `insert_before_symbol` instead of `edit` — only needs the anchor symbol name

**Use native tools when:**
- Quick one-line edits (native `edit` is faster for small changes)
- Parallel reads of multiple files (native `read` can be batched, Serena is serial)
- Shell commands (`exec` has no Serena equivalent)
- File searches by glob pattern (`find_file_by_name` has no Serena equivalent)

### Serena usage notes
- Parameter is `name_path_pattern` (not `name_path`) for `find_symbol`
- For Go methods, use just the method name (e.g. `String` not `TableBuilder.String`) if the full path doesn't match
- Call `initial_instructions` first when starting a new coding task

## Build & Verify

```bash
go build ./...      # compile
go vet ./...        # lint
go build -o devinmonitor.exe .  # build binary
```

## Project Structure

- `main.go` — CLI entry point, core commands (weekly, monthly, models, etc.)
- `internal/ui/` — TableBuilder, Panel, ProgressBar, Sparkline
- `internal/i18n/` — locale system (en.toml, zh.toml, locale_windows.go, locale_other.go)
- `internal/integration/` — feature commands (sessions, daily, config, web, mcp, etc.)
- `internal/filterexport/` — filter, search, export, report, backup, status commands
- `internal/analytics/` — cache, efficiency, tasks, compaction, model-compare, yield
- `internal/budget/` — budget, burn-rate, projection, top-cost, plan, currency
- `internal/project/` — projects, tools, mcp-stats, shell-usage, activities, git
- `internal/trends/` — trends, heatmap, calendar, compare, 24h, sparklines
- `internal/tuiext/` — theme, replay, timeline
- `internal/cli/` — command registry (Register, Get, GetMany, Remaining)
- `internal/config/` — config struct and persistence
- `internal/report/` — SessionRow, TimeRow, formatting helpers (FormatTok, FormatCost, FormatDur)

## i18n

- All user-facing strings (command Short, table headers, totals labels) must use `i18n.T("key")`
- Add keys to both `en.toml` and `zh.toml`
- Locale detection priority: DEVINMONITOR_LOCALE > LC_ALL > LC_MESSAGES > LANG > OS API
- `--locale` flag is pre-scanned in main() before command registration

## Commands

- 58 top-level commands, no duplicates
- `config` has 6 subcommands: show, set, reset, timezone, reset-hour, model-alias
- Help output is logically grouped (not alphabetical) via `buildOrderedCommands()` in main.go
