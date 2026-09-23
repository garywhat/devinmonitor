# Design

DevinMonitor is a local-first token and cost monitor for the Devin CLI: it reads
Devin's own session database and projects it into terminal tables, a realtime
dashboard and machine-readable surfaces.

## Source of truth

**The local `sessions.db` is the only input** — an invariant, not a default: no
report, snapshot or export is assembled elsewhere, and no network call supplies data.

- Resolution is `--data-dir` > `DEVIN_DATA_DIR` > platform defaults, and an
  explicit `--data-dir` is authoritative (`internal/reader/reader.go`).
- The connection is read-only and WAL-safe
  (`mode=ro&_journal_mode=WAL&_query_only=1&_busy_timeout=5000`, one connection),
  so Devin CLI's writes are never blocked or rewritten (`internal/reader/v1.go`).
- Devin's SQLite schema is an internal detail: parsing sits behind the `reader`
  interface and a version-detected adapter, so reports never see raw rows.
- Writes touch only devinmonitor's own files, atomically (`state.WriteAtomic`), so a
  reader never sees a partial file.

Every surface is a **projection of that one source**:

| Surface | Projection | Code |
|---|---|---|
| CLI report tables | `sessions`, `daily`, `weekly`, `monthly`, `models`, `projects`, `agents` | `internal/report`, `internal/ui` |
| Realtime dashboard | 4-tier responsive TUI | `internal/live` |
| Machine snapshot | `snapshot --json` (`schemaVersion: 1`) | `internal/status` |
| MCP resources | `devinmonitor://summary`, `://models`, `://blocks`, `://alerts` | `internal/integration/mcp_resources.go` |
| State file | `<config dir>/state/latest.json` | `internal/state/state.go` |

## Brand

- **Name**: DevinMonitor; tagline *"Token & cost monitor for Devin CLI"*
  (`internal/i18n/en.toml`, `app.tagline`).
- **Position**: a companion instrument for a tool the user already runs, adding
  the Devin-specific metrics other monitors miss — TTFT, tokens/sec,
  finish-reason distribution, context growth, sub-agent usage (`README.md`).
- **Character**: dense and technical; it shows numbers and labels estimates.

## Product goals

| Goal | What it means | Where it shows |
|---|---|---|
| Local-first | Works offline against a database that never leaves the machine | `reader.ResolveDBPath`, read-only DSN |
| Zero runtime dependencies | One static binary; no interpreter, service or daemon | `CGO_ENABLED=0`, pure-Go SQLite |
| Realtime | Usage is visible while it happens, not after a batch job | `internal/live` 500 ms poll, incremental reload |
| Machine-consumable | Scripts, status bars, CI and MCP clients get a stable contract | `snapshot --json`, `RenderCompact`, MCP resources |
| Honest about estimates | A locally derived number is always visibly local | `confidence`, `(estimated)` suffixes |

## Personas and jobs

**Individual developer watching their spend.** Wants the current session's cost
and whether the five-hour window is closing. Hires the tool to watch a live rate
and context fill (`live`), see today's total, and correct a price that is plainly
wrong for a model they use.

**Team lead reviewing usage.** Needs an after-the-fact view across sessions and
projects without asking anyone for a dashboard. Hires the tool for
`daily`/`weekly`/`monthly --breakdown`, model comparison (`model-compare`),
project and tool attribution (`projects`, `tools`), and a shareable export.

**Automation / status-bar consumer.** A script, tmux statusline, CI job or
MCP-capable agent. Hires the tool for one stable machine document
(`snapshot --json`), one ANSI-free line for a prompt (`snapshot --compact`),
semantic exit codes `0/10/11/20/30` (`internal/status`), and read-only context
over MCP resources.

## Information architecture

Commands **self-register** through `cli.Register`, then are ordered explicitly by
`buildOrderedCommands()` with `cobra.EnableCommandSorting = false`, so `--help`
shows the intended grouping (`main.go`). 58 commands sit in 12 groups:

| Group | Commands |
|---|---|
| Live & TUI | `live`, `theme`, `replay`, `timeline` |
| Sessions | `session`, `sessions`, `filter`, `search` |
| Time reports | `weekly`, `monthly`, `daily`, `24h` |
| Cost & budget | `cost`, `budget`, `burn-rate`, `projection`, `top-cost`, `plan`, `currency`, `blocks` |
| Analytics | `cache`, `efficiency`, `tasks`, `optimize`, `compaction`, `context`, `analytics`, `model-compare`, `yield` |
| Trends & charts | `trends`, `heatmap`, `calendar`, `compare` |
| Projects & tools | `projects`, `project`, `tools`, `mcp-stats`, `shell-usage`, `activities`, `git` |
| Models | `models`, `model`, `agents` |
| Export & backup | `export`, `report`, `backup`, `status` |
| Integration | `mcp`, `web`, `notify`, `snapshot`, `alerts` |
| Config | `config`, `alias`, `pricing`, `warehouse` |
| System | `metrics`, `version` |

To find a command, pick the **unit** first — session, time bucket, model, project,
tool or billing window — then the group whose name matches. Reports are nouns;
verbs modify a unit, and the `cli.Remaining` safety net appends anything unlisted.

## Design principles

1. **Estimates are always labelled.** A token × price figure is never a reported
   one: the protocol carries a `confidence` (`official` / `estimated` / `unknown`)
   and display code appends `(estimated)` unless it is `official`
   (`internal/status/status.go`, `internal/integration/warehouse.go`). Free-tier
   costs are `0.0` and render as `free`.
2. **A wrong input fails loudly.** A `--data-dir` without `sessions.db` is an
   error, not a silent fallback; unknown `compare --mode` values are rejected
   (`internal/trends/cmd.go`); `--interval` below the floor is refused by the flag
   itself (`minIntValue`), so requested and effective intervals cannot diverge.
3. **Panic-free on malformed upstream data.** Non-finite, negative and
   epoch-sized percentages become `null` rather than being rendered and a
   percentage just over 100 is clamped (`status.SanitizePercent`); window keys
   match through a normalised spelling, unknown shapes decode to `nil` instead of
   erroring, and the statusline strips control characters
   (`internal/state/state.go`).
4. **Narrow terminals stay usable.** Every layout has a defined tier down to a
   six-row ticker, and tables truncate rather than wrap.
5. **One vocabulary for provenance.** `official` / `estimated` / `unknown` is used
   identically by `internal/status`, `internal/integration` and the schemas.

## Visual language

**Tables** (`ui.TableBuilder`, `internal/ui/ui.go`): rounded borders (`╭╮╰╯─│`), a
bold header in colour 99, right-aligned numeric columns, and a bold `TOTALS` row
behind its own separator. Borders are literal characters plus manual ANSI (238)
rather than lipgloss, so a non-TTY gets a clean plain table rather than none.

**Panels** (`ui.Panel`): a rounded frame, bold title, body clipped to `width - 4`.

**Colour semantics** (`internal/ui/ui.go`, `internal/live/live.go`): primary and
accent `39`/`41` for titles and token figures; header `99` for table headers and
panel titles; `252`/`245`/`240` for the value → label → dim hierarchy; warn `220`
for cost; error `203` for failures; free/success `42` for free models. Percentage
colour is banded (`ui.PctColor`): `>= 90` error, `>= 70` warn, else success.

**Themes** (`internal/live/themes.go`): 15 named 7-colour palettes (background,
foreground, accent, warning, success, error, muted) — `auto`, `dark`, `light`,
`dracula`, `nord`, `solarized-dark`, `solarized-light`, `gruvbox`, `monokai`,
`tokyo-night`, `catppuccin`, `everforest`, `gruvbox-light`, `rose-pine`, `github`.

## Components

| Component | Behaviour | Code |
|---|---|---|
| Table | Auto-fits terminal width; truncates text columns first | `ui.TableBuilder` |
| Panel | Titled rounded frame; content clipped to inner width | `ui.Panel` |
| Tabs | Compact tier tabs stream / context / latency / tools; `1-4` or `Tab` | `internal/live/live.go` |
| Card grid | Session cards, card width 36; toggled with `v` | `internal/live/settings.go` |
| Split pane | List + detail side by side; needs `width >= 60`; vertical-bar key | `internal/live/settings.go` |
| Overlays | Session-detail popup, model-breakdown popup, `?` help, `Ctrl+P` palette | `internal/live/popup.go` |
| Settings panel | Theme, refresh interval, budgets, currency; `s` | `internal/live/settings.go` |
| Sparkline | 8-step unicode ramp `▁▂▃▄▅▆▇█`, needs width >= 4 | `ui.Sparkline` |
| Charts | ASCII bars, 10-row default height, stacked-area fills, heatmap, calendar | `internal/trends` |
| Progress bar | `█` / `░` fill, width floored at 4 | `ui.ProgressBar` |
| Status bar | Current time window and active toggles | `internal/live/settings.go` |

## Accessibility

- **No-colour operation.** Borders and tables are unicode characters with explicit
  ANSI, so content stays legible without colour; values also carry text labels
  (`Cost`, `Free`, `Input`) and provenance is spelled out as `(estimated)`.
- **Wide-character handling.** All width maths go through `go-runewidth`; the
  East-Asian *ambiguous* width is pinned to 1 cell so `…` cannot misalign tables,
  and `truncateCJK` cuts on display width while preserving ANSI
  (`internal/ui/ui.go`).
- **A machine-readable surface exists for screen-reader and script users.** The
  snapshot is one stable JSON document, the statusline form is one ANSI-free line
  (`status.RenderCompact`), and MCP resources carry the same data without layout.

## Responsive behavior

The live dashboard picks a tier from width and height (`internal/live/live.go`):

| Tier | Condition | Layout |
|---|---|---|
| **Full** | `width >= 120` and `height >= 28` | 4 rows: tokens/context/status, request stream, context growth + latency, tools |
| **Compact** | `width >= 80` and `height >= 12` | Tokens + status row, tab bar, one tab panel, controls |
| **Tiny** | `height < 6` | Single-line ticker: model · tokens · context · cost · requests |
| **Mini** | otherwise (width < 80, height >= 6) | Single-column flow: header, recent requests, sparkline, tool summary |

The width breakpoints are `bpFull = 120` and `bpCompact = 80`; the height
thresholds are literals in `View()`. Full mode reserves one column (`w--`) to
avoid autowrap overwriting the last column and splits remaining height 35/30/35.

Elsewhere: the split pane needs `width >= 60`; the card grid fits as many
36-column cards as the width allows; popups clamp to 40×10; table columns truncate
text to 10 then 4 cells and numeric columns to 6 then 3, so an 80-column terminal
still shows every header.

## Interaction states

| State | Behaviour |
|---|---|
| **Loading** | `Loading… (polling sessions.db)` until the first poll completes (`internal/live/live.go`) |
| **Error** | `error: <err>` plus a quit hint; the TUI never dies mid-frame |
| **Empty** | The `err.noSessions` catalog string, same quit hint |
| **Stale** | An upstream capture older than the 600 s TTL (`state.OfficialTTLSeconds`) is marked `stale: true` and **downgraded to a local estimate**, never silently reused as current (`internal/state/state.go`) |

Staleness is a snapshot field, so a consumer decides whether to trust a window;
consumption is additionally paced against elapsed time (`PaceTolerancePoints`).

## Content voice

All user-facing strings live in two embedded TOML catalogs, `en.toml` and
`zh.toml` (`internal/i18n`), read through `i18n.T(key)`; a string written inline
in Go is a bug, not a shortcut.

- **Structure**: nested tables flatten at load into dotted keys, so `[dash.tokens]`
  + `title = "Tokens"` becomes `dash.tokens.title`.
- **Namespaces**: `app.*`, `cmd.*`, `common.*`, `dash.*`, `err.*`, `help.*`,
  `block.*`; multiword keys are camelCase (`burnRate`, `modelCompare`).
- **Lookup** falls back locale → English → the key itself, so a missing
  translation degrades to English and a missing key is visible, not blank. Both
  catalogs define the same 254 keys.
- **Technical terms stay English** — token, cost, ttft, sparkline are deliberately
  not translated (`internal/i18n/i18n.go`). Tone is labels and units, not
  sentences; errors name the offending value.

## Implementation constraints

| Constraint | Value |
|---|---|
| Go version | `go 1.26.4` (`go.mod`) |
| SQLite / build | `modernc.org/sqlite` (pure Go); `CGO_ENABLED=0`, single static binary (`.goreleaser.yaml`) |
| Version / targets | `-ldflags "-s -w -X main.version=<v>"` (default `dev`); linux / darwin / windows × amd64 / arm64 |
| TUI / CLI | `bubbletea` v1.3.10 + `lipgloss` v1.1.0; `go-runewidth`; `cobra` v1.10.2; i18n catalogs `go:embed`-ed, so no locale files at runtime |
| Release path | tag `v*` → Actions runs GoReleaser v2 (`release --clean`): draft GitHub release, `tar.gz` (zip on Windows), `checksums.txt` (`release.yml`) |
| CI | `go vet ./...`, linux build, `--help` / `version` smoke tests, then a 6-cell cross-compile matrix (`ci.yml`) |

## Open questions

Several items below were found while writing this document; those that have
since been fixed are kept with their resolution so the reasoning is not lost.

1. ~~**The Full tier's height threshold was documented as 24 but implemented as
   28.**~~ **Fixed.** Both READMEs and the comment above `viewFull` now state
   `>= 28 rows`.
2. ~~**`internal/pricing` was implemented but wired to nothing.**~~ **Fixed.**
   `model.LookupPricing` now consults an override table installed at startup, so
   every cost path honours `pricing.json`. The legacy `config.customPricing` map
   is still read for compatibility and the file wins. Model aliases were dead in
   exactly the same way and are now applied in the same place.
3. ~~**Narrow-terminal truncation existed in four rune-counting copies.**~~
   **Fixed.** `internal/ui` measures *display* width (go-runewidth); the MCP
   resources' own `padRight`/`padLeft`/`truncateRunes` were rune-count based, so
   any CJK label shifted every column after it. They now delegate to
   `ui.PadRight`/`ui.PadLeft`/`ui.Truncate`.
4. ~~**Layout geometry is re-derived per surface.**~~ **Corrected and fixed.**
   The original claim was wrong. It came from a static audit that counted
   anything named width/height in three packages, but `internal/trends`
   computes bar heights inside an ASCII chart (`v / max * height`) — data-to-
   glyph scaling, not layout — and the extended dashboard does not re-derive
   the breakpoints at all; it delegates to the same `View()`. Unifying those
   would have been a forced abstraction in two different problems.

   What the audit had noticed, vaguely, was real: measuring the renderers
   across every tier showed the **tiers do not fit the terminal they are given**.
   At 80x12 the compact tier rendered 20 lines in 93 columns, and the tiny tier
   rendered 54 columns into 40. Height overflow merely loses lines, because
   bubbletea drops them from the top of an over-tall frame, but width overflow
   wraps and tears the box drawing apart — and 80x12 is an ordinary tmux split.
   `fitToTerminal` now clamps every tier in one place as `View()` returns,
   reserving one column for the autowrap guard and truncating by display width
   so a CJK glyph is never cut in half. A matrix test asserts, for every tier and
   both sides of both breakpoints, that no frame exceeds its terminal.

5. **Error classification recall, and why keywords do not solve it.**
   Measured against a real database (621 tool-role, error-looking, short
   messages), the table classified 58.8% of them; categories for file reads,
   tool validation and structured tool errors raised that to 71.8%, and an
   anchored `Unclassified Error` tier took the real corpus from 1292 counted
   errors to 1549.
   Two findings are worth recording. First, **an unanchored "contains the word
   error" fallback was measured and rejected**: it classified `<file-view ...>`
   listings, `Found 30 match(es)` grep output and `✓ ... started` success
   messages as errors, because file contents and match results routinely
   contain those words. Recall would have reached 100% at the cost of a
   meaningless error rate. Second, the remaining gap is dominated by bodies with
   no error wording at all, which cannot be recovered without an error flag on
   the message; `model.Message` carries none, so precision is chosen over recall
   deliberately. Localized (non-English) error text is only partly covered.

6. ~~**`MaxSupportedSchema` used to be 999**~~, making `ErrSchemaUnsupported`
   unreachable while the real database already shipped version 16. **Fixed and
   then immediately vindicated:** the ceiling became the version actually
   validated, and Devin shipped **version 17** while this release was being
   prepared. Instead of quietly misreading it under the old constant, the tool
   refused with an actionable message. v17 turned out to be **additive** — it
   adds a `subagent_heads` table and leaves every column we read intact (checked
   column by column against `sessions` and `message_nodes`) — so the ceiling
   moved to 17 rather than the guard being relaxed.
   Detection is exact but a compatible bump still needs a release; the
   `DEVINMONITOR_ALLOW_UNKNOWN_SCHEMA` escape hatch is what covers the gap, and
   `subagent_heads` is not read yet.

7. **Remote pricing is implemented, as a write-through cache only.**
   `pricing fetch` (plus an opt-in `pricingAutoFetch`) downloads a public model
   catalogue into `<config dir>/pricing.cache.json`. Three boundaries keep the
   local-first invariant intact: it is **off unless the user turns it on**; the
   request is a bare `GET` for a public price list, so **nothing about the local
   machine is transmitted**; and it **writes to a separate cache file**, never
   `pricing.json`, so a refresh cannot clobber a hand-written override.
   Precedence is user override > fetched cache > built-in table, refresh is
   bounded by a 24h TTL and a 3s timeout, `DEVINMONITOR_OFFLINE=1` forbids
   fetching outright, and a failed refresh warns instead of failing a report.
   Read-time lookups still never touch the network.

8. ~~**The period-comparison "Sessions" row printed request counts.**~~
   **Fixed.** `model.TimeBucket` had no session field, so the renderer
   substituted `Requests` (showing 19145 where the real figure was 10) and
   `Avg $/session` divided by the request count. The bucket now carries
   `Sessions` and both uses are corrected.
9. ~~**Two basename helpers disagreed on Windows paths.**~~ **Fixed.**
   `report.baseProject` split only on `/` while `project.baseProject` also
   split on `\`, so the `projects` listing named a directory `C:\work\proj`
   while `project` and `git` called it `proj`. Both split on either separator
   now, and a test asserts they agree.
10. ~~**`--to-date` compensated with a literal +24h.**~~ **Fixed.** An hour is
   lost or duplicated across a DST transition, which shifted the boundary, and
   `filter.Apply` compares with a strict `After`, so the bound now uses
   `AddDate(0, 0, 1)` and next-midnight is correctly excluded.
11. **`other`-bucket semantics for error analysis** are a deliberate trade-off,
   described in item 5. If recall matters more than precision, the pattern table
   is the lever — not the bucket.
