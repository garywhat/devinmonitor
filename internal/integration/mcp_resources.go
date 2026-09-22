package integration

// MCP resources. The server advertises these alongside its tools so a client
// can pull read-only views without calling a tool. Kept in its own file so the
// resource surface and the tool surface evolve independently.
//
// Interface contract (frozen):
//   - mcpResources() returns every advertised resource, in a stable order.
//   - readMCPResource() returns the plain-text body for one URI, or an
//     *rpcError carrying a JSON-RPC error when the URI is unknown.
//
// Both are called from mcp.go's request router, so their signatures must not
// change without updating that file.

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/limit"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// mcpResource is one advertised resource.
type mcpResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// Resource URIs. These are part of the public contract: clients may hard-code
// them, so treat a rename as a breaking change.
const (
	resourceSummary = "devinmonitor://summary"
	resourceModels  = "devinmonitor://models"
	resourceBlocks  = "devinmonitor://blocks"
	resourceAlerts  = "devinmonitor://alerts"
)

// resourceLineWidth caps every line a resource body emits. Resource contents
// are read by a language model, so a compact aligned table (not JSON) with
// short lines survives a client's re-wrapping; nothing here emits ANSI or tabs.
const resourceLineWidth = 96

// mcpResources lists the resources this server exposes.
func mcpResources() []mcpResource {
	return []mcpResource{
		{URI: resourceSummary, Name: "Usage Summary", Description: "Today, week, month and total cost, token and request totals, and the active session count.", MimeType: "text/plain"},
		{URI: resourceModels, Name: "Model Breakdown", Description: "Per-model token usage, request counts, cost and share of total tokens.", MimeType: "text/plain"},
		{URI: resourceBlocks, Name: "Current Billing Window", Description: "The open 5-hour billing window: tokens, burn rate, remaining time and projected totals.", MimeType: "text/plain"},
		{URI: resourceAlerts, Name: "Alerts", Description: "Current alerts such as budget thresholds and idle sessions.", MimeType: "text/plain"},
	}
}

// readMCPResource returns the text body for one resource URI.
func readMCPResource(cmd *cobra.Command, uri string) (string, *rpcError) {
	switch uri {
	case resourceSummary:
		return buildResourceSummary(cmd)
	case resourceModels:
		return buildResourceModels(cmd)
	case resourceBlocks:
		return buildResourceBlocks(cmd)
	case resourceAlerts:
		return buildResourceAlerts(cmd)
	default:
		return "", &rpcError{Code: -32602, Message: "Unknown resource: " + uri}
	}
}

// ---- resource bodies ----
// NOTE: these four builders are the implementation surface for the resources
// above. Each returns plain text (not JSON) — resource contents are read by a
// model, so a compact human-readable body beats a serialized struct.

// resourceSessions opens the session store through the inherited --data-dir
// flag and returns every visible session. A read failure becomes the JSON-RPC
// internal error (-32603) mandated for this transport; it never panics.
func resourceSessions(cmd *cobra.Command) ([]model.Session, *rpcError) {
	r := openReader(cmd)
	defer r.Close()
	ss, err := r.Sessions()
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	return ss, nil
}

// buildResourceSummary answers "where am I": cost by period, session/request/
// token totals, and where the cost figure came from.
func buildResourceSummary(cmd *cobra.Command) (string, *rpcError) {
	ss, rerr := resourceSessions(cmd)
	if rerr != nil {
		return "", rerr
	}
	sum := computeCostSummary(ss)

	var tokens int64
	for i := range ss {
		tokens += ss[i].InputTokens + ss[i].OutputTokens + ss[i].CacheRead + ss[i].CacheWrite
	}

	// Labels and the continuation indent are the same width so a wrapped value
	// stays inside the same column as its label. 17 leaves a separating space
	// after the longest label ("Total sessions:" is 15 runes).
	const labelWidth = 17
	label := func(s string) string { return padRight(s, labelWidth) }
	indent := strings.Repeat(" ", labelWidth)

	var b strings.Builder
	b.WriteString("DevinMonitor usage summary (local Devin CLI sessions)\n")
	writeResourceLine(&b, label("Total cost:"), indent, report.FormatCost(sum.TotalCost, false), resourceLineWidth)
	writeResourceLine(&b, label("Today:"), indent, report.FormatCost(sum.TodayCost, false), resourceLineWidth)
	writeResourceLine(&b, label("Week:"), indent, report.FormatCost(sum.WeekCost, false), resourceLineWidth)
	writeResourceLine(&b, label("Month:"), indent, report.FormatCost(sum.MonthCost, false), resourceLineWidth)
	writeResourceLine(&b, label("Cost source:"), indent, sum.Provenance, resourceLineWidth)
	writeResourceLine(&b, indent, indent, provenanceExplain(sum.Provenance), resourceLineWidth)
	writeResourceLine(&b, label("Total sessions:"), indent, fmt.Sprintf("%d", sum.TotalSess), resourceLineWidth)
	writeResourceLine(&b, label("Total requests:"), indent, fmt.Sprintf("%d", sum.TotalReqs), resourceLineWidth)
	writeResourceLine(&b, label("Total tokens:"), indent,
		fmt.Sprintf("%s (%d)", report.FormatTok(tokens), tokens), resourceLineWidth)
	writeResourceLine(&b, label("Active sessions:"), indent,
		fmt.Sprintf("%d (activity within 5 minutes)", sum.ActiveSess), resourceLineWidth)
	b.WriteString("All data is local to this machine; nothing is uploaded.\n")
	return b.String(), nil
}

// provenanceExplain spells out where an aggregate cost figure came from, so a
// reader can tell a cost Devin reported from one estimated locally.
func provenanceExplain(p string) string {
	switch p {
	case "official":
		return "reported by Devin's own credit/ACU accounting"
	case "mixed":
		return "partly reported by Devin, partly estimated locally"
	case "estimated":
		return "not reported by Devin; derived from the local token pricing table"
	default:
		return "no cost data found in the local sessions"
	}
}

// buildResourceModels renders per-model usage as an aligned text table, ordered
// by share of all input+output tokens.
func buildResourceModels(cmd *cobra.Command) (string, *rpcError) {
	ss, rerr := resourceSessions(cmd)
	if rerr != nil {
		return "", rerr
	}
	rows := report.BuildModelRows(ss)
	if len(rows) == 0 {
		return "No per-model usage recorded: no assistant message carries a generation model.\n", nil
	}

	// BuildModelRows walks a map, so its order is unstable; sort by token share
	// (with deterministic tie-breaks) to make the body reproducible.
	type modelShare struct {
		row   report.ModelRow
		cost  float64
		share float64
	}
	var totalTokens int64
	for _, r := range rows {
		totalTokens += r.InputTok + r.OutputTok
	}
	items := make([]modelShare, 0, len(rows))
	for _, r := range rows {
		cost := r.CreditCost + r.ACUCost
		if cost == 0 {
			cost = r.EstCost
		}
		var share float64
		if totalTokens > 0 {
			share = float64(r.InputTok+r.OutputTok) / float64(totalTokens) * 100
		}
		items = append(items, modelShare{row: r, cost: cost, share: share})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].share != items[j].share {
			return items[i].share > items[j].share
		}
		ti := items[i].row.InputTok + items[i].row.OutputTok
		tj := items[j].row.InputTok + items[j].row.OutputTok
		if ti != tj {
			return ti > tj
		}
		return items[i].row.Name < items[j].row.Name
	})

	headers := []string{"MODEL", "REQS", "INPUT", "OUTPUT", "CACHE_READ", "COST", "SHARE"}
	alignRight := []bool{false, true, true, true, true, true, true}
	body := make([][]string, 0, len(items))
	for _, it := range items {
		body = append(body, []string{
			truncateRunes(it.row.Name, 28),
			fmt.Sprintf("%d", it.row.Requests),
			report.FormatTok(it.row.InputTok),
			report.FormatTok(it.row.OutputTok),
			report.FormatTok(it.row.CacheRead),
			report.FormatCost(it.cost, it.row.IsFree),
			fmt.Sprintf("%.1f%%", it.share),
		})
	}

	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, cells := range body {
		for i, c := range cells {
			if n := utf8.RuneCountInString(c); n > widths[i] {
				widths[i] = n
			}
		}
	}
	render := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			if alignRight[i] {
				parts[i] = padLeft(c, widths[i])
			} else {
				parts[i] = padRight(c, widths[i])
			}
		}
		return strings.TrimRight(strings.Join(parts, " "), " ")
	}

	dashes := make([]string, len(widths))
	for i, w := range widths {
		dashes[i] = strings.Repeat("-", w)
	}

	var b strings.Builder
	b.WriteString("Per-model usage (local sessions.db)\n")
	b.WriteString(render(headers) + "\n")
	b.WriteString(render(dashes) + "\n")
	for _, cells := range body {
		b.WriteString(render(cells) + "\n")
	}
	b.WriteString("Share is (input+output) as a percentage of every model's input+output.\n")
	return b.String(), nil
}

// buildResourceBlocks reports the open 5-hour billing window plus a short
// history of the windows that already closed.
func buildResourceBlocks(cmd *cobra.Command) (string, *rpcError) {
	ss, rerr := resourceSessions(cmd)
	if rerr != nil {
		return "", rerr
	}

	now := time.Now()
	blocks := limit.Identify(limit.FromSessions(ss), limit.DefaultWindow, now)
	active := limit.Active(blocks)

	const labelWidth = 17 // see buildResourceSummary: keeps a space after the label
	label := func(s string) string { return padRight(s, labelWidth) }
	indent := strings.Repeat(" ", labelWidth)
	const timeLayout = "2006-01-02 15:04"

	var b strings.Builder
	b.WriteString("DevinMonitor billing blocks (5h windows, local time)\n")

	if active == nil {
		b.WriteString(label("Window:") + "none - no 5h billing window is currently active\n")
	} else {
		elapsed := now.Sub(active.StartTime)
		if elapsed < 0 {
			elapsed = 0
		}
		remaining := active.EndTime.Sub(now)
		if remaining < 0 {
			remaining = 0
		}

		models := strings.Join(active.Models, ", ")
		if models == "" {
			models = "(none)"
		}

		burn := "unavailable (tokens/min needs two distinct message times)"
		if rate := limit.BurnRateOf(active); rate != nil {
			burn = fmt.Sprintf("%.1f tokens/min, %.1f non-cache tokens/min, %s/hour",
				rate.TokensPerMinute, rate.TokensPerMinuteDisplay, report.FormatCost(rate.CostPerHour, false))
		}
		projected := "unavailable (needs a usable burn rate)"
		if p := limit.Project(active, now); p != nil {
			projected = fmt.Sprintf("%s tokens, %s (in %s)",
				report.FormatTok(p.TotalTokens), report.FormatCost(p.TotalCost, false),
				report.FormatDur(time.Duration(p.RemainingMinutes)*time.Minute))
		}

		writeResourceLine(&b, label("Window:"), indent, "active", resourceLineWidth)
		writeResourceLine(&b, label("Start:"), indent, active.StartTime.Local().Format(timeLayout), resourceLineWidth)
		writeResourceLine(&b, label("End:"), indent, active.EndTime.Local().Format(timeLayout), resourceLineWidth)
		writeResourceLine(&b, label("Elapsed:"), indent, report.FormatDur(elapsed), resourceLineWidth)
		writeResourceLine(&b, label("Remaining:"), indent, report.FormatDur(remaining), resourceLineWidth)
		writeResourceLine(&b, label("Input:"), indent, fmt.Sprintf("%d", active.Tokens.Input), resourceLineWidth)
		writeResourceLine(&b, label("Output:"), indent, fmt.Sprintf("%d", active.Tokens.Output), resourceLineWidth)
		writeResourceLine(&b, label("Cache read:"), indent, fmt.Sprintf("%d", active.Tokens.CacheRead), resourceLineWidth)
		writeResourceLine(&b, label("Cache write:"), indent, fmt.Sprintf("%d", active.Tokens.CacheWrite), resourceLineWidth)
		writeResourceLine(&b, label("Total tokens:"), indent,
			fmt.Sprintf("%d", active.Tokens.Total()), resourceLineWidth)
		writeResourceLine(&b, label("Models:"), indent, models, resourceLineWidth)
		writeResourceLine(&b, label("Cost:"), indent, report.FormatCost(active.Cost, false), resourceLineWidth)
		writeResourceLine(&b, label("Burn rate:"), indent, burn, resourceLineWidth)
		writeResourceLine(&b, label("Projected end:"), indent, projected, resourceLineWidth)
	}

	// Finished windows: blocks arrive in time order, so the tail holds the most
	// recent ones. Gap blocks carry no usage and are skipped.
	var finished []limit.Block
	for _, blk := range blocks {
		if blk.IsGap || blk.IsActive {
			continue
		}
		finished = append(finished, blk)
	}
	if len(finished) > 5 {
		finished = finished[len(finished)-5:]
	}
	b.WriteString("Recent finished windows (newest first):\n")
	if len(finished) == 0 {
		b.WriteString("  none yet\n")
	} else {
		for i := len(finished) - 1; i >= 0; i-- {
			blk := finished[i]
			fmt.Fprintf(&b, "  %s  tokens %d  cost %s\n",
				blk.StartTime.Local().Format(timeLayout),
				blk.Tokens.Total(),
				report.FormatCost(blk.Cost, false))
		}
	}
	return b.String(), nil
}

// buildResourceAlerts groups the current alerts by kind.
func buildResourceAlerts(cmd *cobra.Command) (string, *rpcError) {
	ss, rerr := resourceSessions(cmd)
	if rerr != nil {
		return "", rerr
	}
	alerts := detectAlerts(ss)
	if len(alerts) == 0 {
		return "No alerts: no budget threshold crossed, no idle or ghost sessions.\n", nil
	}

	byKind := map[string][]model.AlertItem{}
	var kinds []string
	for _, a := range alerts {
		if _, ok := byKind[a.Kind]; !ok {
			kinds = append(kinds, a.Kind)
		}
		byKind[a.Kind] = append(byKind[a.Kind], a)
	}
	// detectAlerts' own order depends on the session ordering; sorting the kind
	// labels keeps the body reproducible run to run.
	sort.Strings(kinds)

	var b strings.Builder
	alertWord, kindWord := "alerts", "kinds"
	if len(alerts) == 1 {
		alertWord = "alert"
	}
	if len(kinds) == 1 {
		kindWord = "kind"
	}
	fmt.Fprintf(&b, "Alerts: %d %s across %d %s\n", len(alerts), alertWord, len(kinds), kindWord)
	for _, kind := range kinds {
		items := byKind[kind]
		fmt.Fprintf(&b, "[%s] %d\n", kind, len(items))
		for _, a := range items {
			severity := a.Severity
			if severity == "" {
				severity = "info"
			}
			prefix := "  - " + severity + ": "
			writeResourceLine(&b, prefix, strings.Repeat(" ", len(prefix)), a.Message, resourceLineWidth)
		}
	}
	return b.String(), nil
}

// ---- plain-text layout helpers ----

// padRight pads s with spaces to w runes (a no-op when s is already wider).
func padRight(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// padLeft pads s with leading spaces to w runes.
func padLeft(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// truncateRunes shortens s to at most w runes, marking the cut with "...".
func truncateRunes(s string, w int) string {
	if utf8.RuneCountInString(s) <= w {
		return s
	}
	r := []rune(s)
	if w <= 3 {
		return string(r[:w])
	}
	return string(r[:w-3]) + "..."
}

// wrapText greedily wraps text into lines of at most width runes. A single word
// wider than width gets its own over-long line rather than being broken, so a
// long session ID stays copy-pasteable.
func wrapText(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	if width < 8 {
		width = 8
	}
	lines := []string{words[0]}
	for _, w := range words[1:] {
		cur := lines[len(lines)-1]
		if utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(w) <= width {
			lines[len(lines)-1] = cur + " " + w
			continue
		}
		lines = append(lines, w)
	}
	return lines
}

// writeResourceLine writes one labelled line, wrapping the value so no emitted
// line exceeds width. firstPrefix and contPrefix are expected to be the same
// width, which keeps the wrapped value in the same column as the first line.
func writeResourceLine(b *strings.Builder, firstPrefix, contPrefix, value string, width int) {
	avail := width - utf8.RuneCountInString(contPrefix)
	if avail < 16 {
		avail = 16
	}
	for i, line := range wrapText(value, avail) {
		if i == 0 {
			b.WriteString(firstPrefix + line + "\n")
			continue
		}
		b.WriteString(contPrefix + line + "\n")
	}
}
