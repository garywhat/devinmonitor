// Package blocks implements the `devinmonitor blocks` command: local Devin CLI
// usage grouped into billing windows (blocks), with burn rate and
// end-of-window projection for the active window.
//
// The command self-registers with internal/cli in init(), so main.go only has
// to import this package (blank or otherwise) for the command to appear.
//
// Block identification, burn rate, projection and limit arithmetic all live in
// internal/limit; this package only filters, formats and renders.
package blocks

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/limit"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/ui"
)

// compactWidthThreshold is the terminal width below which the table degrades to
// the compact column set (model + cache columns dropped). Mirrors ccusage's
// BLOCKS_COMPACT_WIDTH_THRESHOLD.
const compactWidthThreshold = 120

// windowTimeLayout is the local-time layout used by the Window column.
const windowTimeLayout = "2006-01-02 15:04"

const (
	// noActiveWindowMsg is the single clear line printed by `blocks --active`
	// when no window is currently live (exit 0).
	noActiveWindowMsg = "No active window."
	// gapLabel marks a gap block as idle instead of showing a start time.
	gapLabel = "idle"
	// activePanelTitle is the ui.Panel title for the --active detail view.
	activePanelTitle = "Active Window"
	// nearLimitMarker is appended to the used-percent line at/over the warning
	// threshold.
	nearLimitMarker = "near limit"
)

// cmdBlocks is the package-level command factory, registered in init().
var cmdBlocks = func() *cobra.Command {
	c := &cobra.Command{
		Use:   "blocks",
		Short: i18n.T("cmd.blocks"),
		Run: func(cmd *cobra.Command, args []string) {
			window, _ := cmd.Flags().GetDuration("window")
			activeOnly, _ := cmd.Flags().GetBool("active")
			tokenLimit, _ := cmd.Flags().GetInt64("limit-tokens")
			asJSON, _ := cmd.Flags().GetBool("json")
			sinceStr, _ := cmd.Flags().GetString("since")
			untilStr, _ := cmd.Flags().GetString("until")
			compact, _ := cmd.Flags().GetBool("compact")

			// Validate dates before touching the database, so a typo fails fast.
			since, err := parseDateFlag("--since", sinceStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			until, err := parseDateFlag("--until", untilStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}

			if window <= 0 {
				window = limit.DefaultWindow
			}

			dataDir, _ := cmd.Flags().GetString("data-dir")
			r, err := reader.Open(dataDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "open reader: %v\n", err)
				os.Exit(1)
			}
			defer r.Close()

			ss, err := r.Sessions()
			if err != nil {
				fmt.Fprintf(os.Stderr, "read sessions: %v\n", err)
				os.Exit(1)
			}

			now := time.Now()
			blocks := limit.Identify(limit.FromSessions(ss), window, now)

			// The active window is a property of "now", not of the date filter,
			// so --active is resolved against the full block list.
			if activeOnly {
				fmt.Println(activeDetail(blocks, now, tokenLimit, panelWidth()))
				return
			}

			blocks = sortNewestFirst(filterBlocksByDate(blocks, since, until))

			if asJSON {
				out, err := blocksJSON(blocks, now, tokenLimit)
				if err != nil {
					fmt.Fprintf(os.Stderr, "encode json: %v\n", err)
					os.Exit(1)
				}
				_, _ = os.Stdout.Write(out)
				return
			}

			fmt.Println(renderBlockTable(blocks, now, shouldUseCompact(compact, terminalWidth())))
		},
	}
	c.Flags().Duration("window", limit.DefaultWindow, i18n.T("help.blockWindow"))
	c.Flags().Bool("active", false, i18n.T("help.blockActive"))
	c.Flags().Int64("limit-tokens", 0, i18n.T("help.blockLimitTokens"))
	c.Flags().Bool("json", false, i18n.T("help.blockJSON"))
	c.Flags().String("since", "", i18n.T("help.blockSince"))
	c.Flags().String("until", "", i18n.T("help.blockUntil"))
	c.Flags().Bool("compact", false, i18n.T("help.blockCompact"))
	return c
}

func init() { cli.Register(cmdBlocks) }

// tr returns the translation for key, or fallback when the key is absent.
// i18n.T echoes the key back when a lookup misses, which is used here as the
// existence probe: status words are only translated once the catalogs actually
// define block.status* keys.
func tr(key, fallback string) string {
	if v := i18n.T(key); v != "" && v != key {
		return v
	}
	return fallback
}

// ---- dates ----

// parseLocalDate parses a YYYY-MM-DD date as local midnight. An empty string
// yields the zero time (meaning "no bound"). Mirrors filter.ParseDate, but is
// local to this package so the command does not depend on the filter feature.
func parseLocalDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

// parseDateFlag parses a date flag value and wraps failures in the message the
// command prints: "invalid --since: <err>" / "invalid --until: <err>".
func parseDateFlag(name, s string) (time.Time, error) {
	d, err := parseLocalDate(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s: %w", name, err)
	}
	return d, nil
}

// filterBlocksByDate keeps blocks whose StartTime is on or after the local
// midnight of --since (inclusive) and strictly before the local midnight of
// --until (exclusive). A zero bound is ignored. Gap blocks follow the same rule.
func filterBlocksByDate(bs []limit.Block, since, until time.Time) []limit.Block {
	out := make([]limit.Block, 0, len(bs))
	for _, b := range bs {
		if !since.IsZero() && b.StartTime.Before(since) {
			continue
		}
		if !until.IsZero() && !b.StartTime.Before(until) {
			continue
		}
		out = append(out, b)
	}
	return out
}

// ---- ordering / aggregation ----

// sortNewestFirst returns a copy of bs ordered by StartTime descending.
func sortNewestFirst(bs []limit.Block) []limit.Block {
	out := make([]limit.Block, len(bs))
	copy(out, bs)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartTime.After(out[j].StartTime)
	})
	return out
}

// nonGapTotals sums token counts and cost over the real (non-gap) blocks in
// view. Gap blocks are idle intervals and never contribute.
func nonGapTotals(bs []limit.Block) (limit.TokenCounts, float64) {
	var tot limit.TokenCounts
	var cost float64
	for _, b := range bs {
		if b.IsGap {
			continue
		}
		tot.Input += b.Tokens.Input
		tot.Output += b.Tokens.Output
		tot.CacheRead += b.Tokens.CacheRead
		tot.CacheWrite += b.Tokens.CacheWrite
		cost += b.Cost
	}
	return tot, cost
}

// ---- formatting ----

// formatRemaining renders a duration as a compact "2h15m" / "45m" / "5h"
// string. Seconds are truncated; non-positive durations render as "0m".
func formatRemaining(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	total := int(d.Minutes())
	h := total / 60
	m := total % 60
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dh%dm", h, m)
	}
}

// formatCount renders a token count as a plain integer (repo convention).
func formatCount(n int64) string { return strconv.FormatInt(n, 10) }

// formatCost renders USD with two decimals.
func formatCost(v float64) string { return fmt.Sprintf("$%.2f", v) }

// formatPercent renders a percentage with exactly one decimal.
func formatPercent(pct float64) string { return fmt.Sprintf("%.1f%%", pct) }

// nearLimit reports whether a used percentage (0-100) has reached the limit
// package's warning threshold.
func nearLimit(usedPct float64) bool { return usedPct >= limit.WarningThreshold*100 }

// windowLabel renders the Window column: the block start in local time, or an
// explicit idle marker for gap blocks.
func windowLabel(b limit.Block) string {
	if b.IsGap {
		return gapLabel
	}
	return b.StartTime.Local().Format(windowTimeLayout)
}

// modelsLabel renders the comma-joined model list, "-" when empty.
func modelsLabel(models []string) string {
	if len(models) == 0 {
		return "-"
	}
	return strings.Join(models, ", ")
}

// statusLabel renders the Status column: "active (2h15m)", "done" or "gap".
func statusLabel(b limit.Block, now time.Time) string {
	switch {
	case b.IsGap:
		return tr("block.statusGap", "gap")
	case b.IsActive:
		return fmt.Sprintf("%s (%s)", tr("block.statusActive", "active"),
			formatRemaining(b.EndTime.Sub(now)))
	default:
		return tr("block.statusDone", "done")
	}
}

// ---- responsive width ----

// terminalWidth returns the terminal width in columns. It follows the same
// approach as internal/ui (ioctl first, then COLUMNS) but returns 0 when the
// width is unknown, so piped output is never silently degraded.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if w, err := strconv.Atoi(cols); err == nil && w > 0 {
			return w
		}
	}
	return 0
}

// shouldUseCompact reports whether the compact column set applies: always for
// an explicit --compact, otherwise when the terminal is narrower than the
// compact threshold. An unknown width (0) is treated as wide.
func shouldUseCompact(explicit bool, width int) bool {
	return explicit || (width > 0 && width < compactWidthThreshold)
}

// panelWidth is the ui.Panel width: the detected terminal width, else 80.
func panelWidth() int {
	if w := terminalWidth(); w > 0 {
		return w
	}
	return 80
}

// ---- table ----

// blockColumn describes one table column: its header, whether its values are
// numeric (right-aligned), how to render a cell, and how to render its cell in
// the TOTALS row (nil = blank).
type blockColumn struct {
	header  string
	numeric bool
	value   func(b limit.Block, now time.Time) string
	total   func(tot limit.TokenCounts, cost float64) string
}

// blockColumns returns the full column set, or the compact one (models and both
// cache columns dropped) when compact is set.
func blockColumns(compact bool) []blockColumn {
	cols := []blockColumn{
		{
			header: "Window",
			value:  func(b limit.Block, _ time.Time) string { return windowLabel(b) },
			total:  func(limit.TokenCounts, float64) string { return tr("common.totals", "TOTALS") },
		},
		{
			header: "Models",
			value:  func(b limit.Block, _ time.Time) string { return modelsLabel(b.Models) },
		},
		{
			header:  "Input",
			numeric: true,
			value:   func(b limit.Block, _ time.Time) string { return formatCount(b.Tokens.Input) },
			total:   func(tot limit.TokenCounts, _ float64) string { return formatCount(tot.Input) },
		},
		{
			header:  "Output",
			numeric: true,
			value:   func(b limit.Block, _ time.Time) string { return formatCount(b.Tokens.Output) },
			total:   func(tot limit.TokenCounts, _ float64) string { return formatCount(tot.Output) },
		},
		{
			header:  "Cache Read",
			numeric: true,
			value:   func(b limit.Block, _ time.Time) string { return formatCount(b.Tokens.CacheRead) },
			total:   func(tot limit.TokenCounts, _ float64) string { return formatCount(tot.CacheRead) },
		},
		{
			header:  "Cache Write",
			numeric: true,
			value:   func(b limit.Block, _ time.Time) string { return formatCount(b.Tokens.CacheWrite) },
			total:   func(tot limit.TokenCounts, _ float64) string { return formatCount(tot.CacheWrite) },
		},
		{
			header:  "Total Tokens",
			numeric: true,
			value:   func(b limit.Block, _ time.Time) string { return formatCount(b.Tokens.Total()) },
			total:   func(tot limit.TokenCounts, _ float64) string { return formatCount(tot.Total()) },
		},
		{
			header:  "Cost",
			numeric: true,
			value:   func(b limit.Block, _ time.Time) string { return formatCost(b.Cost) },
			total:   func(_ limit.TokenCounts, cost float64) string { return formatCost(cost) },
		},
		{
			header: "Status",
			value:  func(b limit.Block, now time.Time) string { return statusLabel(b, now) },
		},
	}
	if !compact {
		return cols
	}
	out := make([]blockColumn, 0, len(cols))
	for _, c := range cols {
		switch c.header {
		case "Models", "Cache Read", "Cache Write":
			continue
		}
		out = append(out, c)
	}
	return out
}

// renderBlockTable renders one row per block (already ordered), plus a TOTALS
// row summing the non-gap rows in view.
func renderBlockTable(bs []limit.Block, now time.Time, compact bool) string {
	cols := blockColumns(compact)
	headers := make([]string, len(cols))
	var right []int
	for i, c := range cols {
		headers[i] = c.header
		if c.numeric {
			right = append(right, i)
		}
	}

	t := ui.NewTable(headers...)
	t.RightAlign(right...)
	for _, b := range bs {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = c.value(b, now)
		}
		t.Row(row...)
	}

	tot, cost := nonGapTotals(bs)
	totalRow := make([]string, len(cols))
	for i, c := range cols {
		if c.total != nil {
			totalRow[i] = c.total(tot, cost)
		}
	}
	t.TotalRow(totalRow...)
	return t.String()
}

// ---- active window detail ----

// activeDetail renders the --active view: the detailed panel for the live
// window, or a single line when nothing is active.
func activeDetail(bs []limit.Block, now time.Time, tokenLimit int64, width int) string {
	b := limit.Active(bs)
	if b == nil {
		return noActiveWindowMsg
	}
	return renderActivePanel(b, now, tokenLimit, width)
}

// renderActivePanel builds the ui.Panel body for one active block.
func renderActivePanel(b *limit.Block, now time.Time, tokenLimit int64, width int) string {
	var sb strings.Builder
	line := func(label, value string) {
		fmt.Fprintf(&sb, "%s: %s\n", label, value)
	}

	elapsed := now.Sub(b.StartTime)
	if elapsed < 0 {
		elapsed = 0
	}

	line("Start", b.StartTime.Local().Format(windowTimeLayout))
	line("End", b.EndTime.Local().Format(windowTimeLayout))
	line("Elapsed", formatRemaining(elapsed))
	line("Remaining", formatRemaining(b.EndTime.Sub(now)))
	line(tr("common.input", "Input"), formatCount(b.Tokens.Input))
	line(tr("common.output", "Output"), formatCount(b.Tokens.Output))
	line(tr("common.cacheRead", "Cache Read"), formatCount(b.Tokens.CacheRead))
	line(tr("common.cacheWr", "Cache Write"), formatCount(b.Tokens.CacheWrite))
	line("Total Tokens", formatCount(b.Tokens.Total()))
	line(tr("common.model", "Models"), modelsLabel(b.Models))
	line(tr("common.cost", "Cost"), formatCost(b.Cost))
	line("Burn Rate", formatBurnRate(limit.BurnRateOf(b)))
	line("Projected", formatProjection(limit.Project(b, now)))

	if tokenLimit > 0 {
		if pct := limit.UsedPercent(b, tokenLimit); pct != nil {
			value := fmt.Sprintf("%s of %s tokens", formatPercent(*pct), formatCount(tokenLimit))
			if nearLimit(*pct) {
				value += " (" + nearLimitMarker + ")"
			}
			line("Used", value)
		}
	}

	return ui.Panel(activePanelTitle, strings.TrimRight(sb.String(), "\n"), width)
}

// formatBurnRate renders tokens/min, non-cache tokens/min and cost/hour.
func formatBurnRate(rate *limit.BurnRate) string {
	if rate == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f tok/min (non-cache %.1f/min), %s/hour",
		rate.TokensPerMinute, rate.TokensPerMinuteDisplay, formatCost(rate.CostPerHour))
}

// formatProjection renders projected end-of-window totals.
func formatProjection(p *limit.Projection) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%s tokens, %s in %s", formatCount(p.TotalTokens),
		formatCost(p.TotalCost), formatRemaining(time.Duration(p.RemainingMinutes)*time.Minute))
}

// ---- JSON ----

// blocksJSON marshals the block list as indented JSON (2 spaces) with a
// trailing newline, preserving the given order. An empty list marshals as "[]".
func blocksJSON(bs []limit.Block, now time.Time, tokenLimit int64) ([]byte, error) {
	out := limit.WireBlocks(bs, now, tokenLimit)
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
