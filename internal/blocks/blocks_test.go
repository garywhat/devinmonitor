package blocks

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/limit"
)

// fixedNow is the only clock used by these tests.
var fixedNow = time.Date(2026, 9, 10, 12, 0, 0, 0, time.Local)

// mkBlock builds a non-gap block starting at start with the given counts.
func mkBlock(start time.Time, active bool, in, out, cr, cw int64, cost float64) limit.Block {
	return limit.Block{
		ID:            start.Format(time.RFC3339),
		StartTime:     start,
		EndTime:       start.Add(limit.DefaultWindow),
		ActualEndTime: start.Add(time.Hour),
		IsActive:      active,
		Models:        []string{"model-a"},
		Tokens:        limit.TokenCounts{Input: in, Output: out, CacheRead: cr, CacheWrite: cw},
		Cost:          cost,
	}
}

// mkGap builds a gap (idle) block covering [from, to).
func mkGap(from, to time.Time) limit.Block {
	return limit.Block{
		ID:        "gap-" + from.Format(time.RFC3339),
		StartTime: from,
		EndTime:   to,
		IsGap:     true,
		Tokens:    limit.TokenCounts{Input: 999, Output: 999},
		Cost:      99.99,
	}
}

func localMidnight(t *testing.T, date string) time.Time {
	t.Helper()
	d, err := parseLocalDate(date)
	if err != nil {
		t.Fatalf("parseLocalDate(%q): %v", date, err)
	}
	return d
}

// ---- dates ----

func TestParseDateFlagInvalid(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"--since", "2026-13-01"},
		{"--until", "not-a-date"},
		{"--since", "2026/09/10"},
	} {
		_, err := parseDateFlag(tc.name, tc.in)
		if err == nil {
			t.Fatalf("parseDateFlag(%q, %q): expected error", tc.name, tc.in)
		}
		want := "invalid " + tc.name + ": "
		if !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("error = %q, want prefix %q", err.Error(), want)
		}
	}
}

func TestParseDateFlagEmptyIsUnbounded(t *testing.T) {
	for _, name := range []string{"--since", "--until"} {
		d, err := parseDateFlag(name, "")
		if err != nil {
			t.Fatalf("parseDateFlag(%q, \"\"): %v", name, err)
		}
		if !d.IsZero() {
			t.Fatalf("parseDateFlag(%q, \"\") = %v, want zero time", name, d)
		}
	}
}

func TestParseLocalDateIsLocalMidnight(t *testing.T) {
	d, err := parseLocalDate("2026-09-10")
	if err != nil {
		t.Fatalf("parseLocalDate: %v", err)
	}
	want := time.Date(2026, 9, 10, 0, 0, 0, 0, time.Local)
	if !d.Equal(want) {
		t.Fatalf("parsed %v, want local midnight %v", d, want)
	}
}

func TestFilterBlocksByDateBoundaries(t *testing.T) {
	since := localMidnight(t, "2026-09-10")
	until := localMidnight(t, "2026-09-12")

	beforeSince := since.Add(-time.Second) // 2026-09-09 23:59:59
	atSince := since                       // exactly midnight -> included (>=)
	beforeUntil := until.Add(-time.Second)
	atUntil := until // exactly midnight -> excluded (<)
	afterUntil := until.Add(time.Hour)

	blocks := []limit.Block{
		mkBlock(beforeSince, false, 1, 0, 0, 0, 0),
		mkBlock(atSince, false, 2, 0, 0, 0, 0),
		mkBlock(beforeUntil, false, 3, 0, 0, 0, 0),
		mkBlock(atUntil, false, 4, 0, 0, 0, 0),
		mkBlock(afterUntil, false, 5, 0, 0, 0, 0),
		mkGap(beforeSince, atSince),
	}

	// since only: inclusive lower bound. The gap starts before --since, so it
	// is dropped along with the block that precedes the bound.
	got := filterBlocksByDate(blocks, since, time.Time{})
	if len(got) != 4 {
		t.Fatalf("since-only kept %d blocks, want 4", len(got))
	}
	if !got[0].StartTime.Equal(atSince) {
		t.Fatalf("since-only first block starts %v, want %v", got[0].StartTime, atSince)
	}
	if got[len(got)-1].StartTime.Equal(beforeSince) {
		t.Fatal("since-only kept a block starting before --since")
	}

	// until only: exclusive upper bound.
	got = filterBlocksByDate(blocks, time.Time{}, until)
	if len(got) != 4 {
		t.Fatalf("until-only kept %d blocks, want 4", len(got))
	}
	for _, b := range got {
		if !b.StartTime.Before(until) {
			t.Fatalf("until-only kept block starting %v, want strictly before %v", b.StartTime, until)
		}
	}

	// both bounds: [since, until) keeps exactly the two blocks inside.
	got = filterBlocksByDate(blocks, since, until)
	if len(got) != 2 {
		t.Fatalf("both bounds kept %d blocks, want 2", len(got))
	}
	if !got[0].StartTime.Equal(atSince) || !got[1].StartTime.Equal(beforeUntil) {
		t.Fatalf("both bounds kept %v..%v, want %v..%v",
			got[0].StartTime, got[1].StartTime, atSince, beforeUntil)
	}

	// no bounds keeps everything, gap included.
	if got = filterBlocksByDate(blocks, time.Time{}, time.Time{}); len(got) != len(blocks) {
		t.Fatalf("no bounds kept %d blocks, want %d", len(got), len(blocks))
	}
}

func TestFilterBlocksByDateFiltersGaps(t *testing.T) {
	since := localMidnight(t, "2026-09-10")
	gap := mkGap(since.Add(-2*time.Hour), since.Add(-time.Hour))
	got := filterBlocksByDate([]limit.Block{gap}, since, time.Time{})
	if len(got) != 0 {
		t.Fatalf("gap before --since was kept: %+v", got)
	}
}

// ---- remaining time ----

func TestFormatRemaining(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0m"},
		{-time.Minute, "0m"},
		{30 * time.Second, "0m"},
		{90 * time.Second, "1m"},
		{45 * time.Minute, "45m"},
		{2*time.Hour + 15*time.Minute, "2h15m"},
		{5 * time.Hour, "5h"},
		{2*time.Hour + 15*time.Minute + 59*time.Second, "2h15m"},
	}
	for _, tc := range cases {
		if got := formatRemaining(tc.in); got != tc.want {
			t.Errorf("formatRemaining(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatPercentAndNearLimit(t *testing.T) {
	if got := formatPercent(42.56); got != "42.6%" {
		t.Errorf("formatPercent(42.56) = %q, want %q", got, "42.6%")
	}
	if got := formatPercent(10); got != "10.0%" {
		t.Errorf("formatPercent(10) = %q, want %q", got, "10.0%")
	}
	if !nearLimit(limit.WarningThreshold * 100) {
		t.Errorf("nearLimit(%v) = false, want true at the threshold", limit.WarningThreshold*100)
	}
	if !nearLimit(95) {
		t.Error("nearLimit(95) = false, want true")
	}
	if nearLimit(10) {
		t.Error("nearLimit(10) = true, want false")
	}
}

// ---- labels / columns ----

func TestWindowLabelMarksGapAsIdle(t *testing.T) {
	start := time.Date(2026, 9, 10, 8, 30, 0, 0, time.Local)
	if got := windowLabel(mkBlock(start, false, 0, 0, 0, 0, 0)); got != "2026-09-10 08:30" {
		t.Fatalf("windowLabel = %q, want %q", got, "2026-09-10 08:30")
	}
	got := windowLabel(mkGap(start, start.Add(time.Hour)))
	if got != gapLabel {
		t.Fatalf("gap windowLabel = %q, want %q", got, gapLabel)
	}
	if strings.Contains(got, "2026-") || strings.Contains(got, ":") {
		t.Fatalf("gap windowLabel %q looks like a timestamp, want an idle marker", got)
	}
}

func TestModelsLabel(t *testing.T) {
	if got := modelsLabel(nil); got != "-" {
		t.Fatalf("modelsLabel(nil) = %q, want %q", got, "-")
	}
	if got := modelsLabel([]string{"a", "b"}); got != "a, b" {
		t.Fatalf("modelsLabel = %q, want %q", got, "a, b")
	}
}

func TestBlockColumnsCompactSelection(t *testing.T) {
	full := blockColumns(false)
	if len(full) != 9 {
		t.Fatalf("full column count = %d, want 9", len(full))
	}
	wantFull := []string{"Window", "Models", "Input", "Output", "Cache Read", "Cache Write", "Total Tokens", "Cost", "Status"}
	for i, h := range wantFull {
		if full[i].header != h {
			t.Fatalf("full column %d = %q, want %q", i, full[i].header, h)
		}
	}

	compact := blockColumns(true)
	wantCompact := []string{"Window", "Input", "Output", "Total Tokens", "Cost", "Status"}
	if len(compact) != len(wantCompact) {
		t.Fatalf("compact column count = %d, want %d", len(compact), len(wantCompact))
	}
	for i, h := range wantCompact {
		if compact[i].header != h {
			t.Fatalf("compact column %d = %q, want %q", i, compact[i].header, h)
		}
	}
	for _, c := range compact {
		if c.header == "Models" || strings.HasPrefix(c.header, "Cache") {
			t.Fatalf("compact columns still contain %q", c.header)
		}
	}
}

func TestShouldUseCompact(t *testing.T) {
	cases := []struct {
		explicit bool
		width    int
		want     bool
	}{
		{false, 200, false},
		{false, 120, false},
		{false, 119, true},
		{false, 80, true},
		{false, 0, false}, // unknown width (piped output) stays full
		{true, 200, true},
		{true, 0, true},
	}
	for _, tc := range cases {
		if got := shouldUseCompact(tc.explicit, tc.width); got != tc.want {
			t.Errorf("shouldUseCompact(%v, %d) = %v, want %v", tc.explicit, tc.width, got, tc.want)
		}
	}
}

// ---- totals / table ----

func TestNonGapTotalsExcludesGapBlocks(t *testing.T) {
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	real := mkBlock(start, false, 10, 20, 30, 40, 1.5)
	real2 := mkBlock(start.Add(6*time.Hour), false, 1, 2, 3, 4, 0.5)
	blocks := []limit.Block{real, mkGap(start.Add(time.Hour), start.Add(5*time.Hour)), real2}

	tot, cost := nonGapTotals(blocks)
	want := limit.TokenCounts{Input: 11, Output: 22, CacheRead: 33, CacheWrite: 44}
	if tot != want {
		t.Fatalf("totals = %+v, want %+v", tot, want)
	}
	if tot.Total() != 110 {
		t.Fatalf("total tokens = %d, want 110", tot.Total())
	}
	if cost != 2.0 {
		t.Fatalf("cost = %v, want 2.0 (gap cost excluded)", cost)
	}

	// A gap-only view totals to zero, not to the gap's placeholder values.
	onlyGap := []limit.Block{mkGap(start, start.Add(time.Hour))}
	if tot, cost := nonGapTotals(onlyGap); tot.Total() != 0 || cost != 0 {
		t.Fatalf("gap-only totals = %+v/%v, want zero", tot, cost)
	}
}

func TestRenderBlockTableHasTotalsRow(t *testing.T) {
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	blocks := []limit.Block{
		mkBlock(start.Add(6*time.Hour), true, 5, 5, 0, 0, 0.25),
		mkBlock(start, false, 10, 20, 30, 40, 1.5),
		mkGap(start.Add(time.Hour), start.Add(5*time.Hour)),
	}

	out := renderBlockTable(blocks, fixedNow, false)
	if !strings.Contains(out, "Total Tokens") || !strings.Contains(out, "Cache Write") {
		t.Fatalf("full table missing columns:\n%s", out)
	}
	if !strings.Contains(out, "110") { // 10+20+30+40, gap excluded
		t.Fatalf("totals row missing summed tokens:\n%s", out)
	}
	if !strings.Contains(out, gapLabel) {
		t.Fatalf("table missing idle marker for the gap block:\n%s", out)
	}

	compactOut := renderBlockTable(blocks, fixedNow, true)
	if strings.Contains(compactOut, "Cache Write") || strings.Contains(compactOut, "Models") {
		t.Fatalf("compact table still has model/cache columns:\n%s", compactOut)
	}
}

func TestSortNewestFirst(t *testing.T) {
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	older := mkBlock(start, false, 0, 0, 0, 0, 0)
	newer := mkBlock(start.Add(6*time.Hour), false, 0, 0, 0, 0, 0)
	in := []limit.Block{older, newer}

	got := sortNewestFirst(in)
	if !got[0].StartTime.Equal(newer.StartTime) || !got[1].StartTime.Equal(older.StartTime) {
		t.Fatalf("sortNewestFirst order = %v, %v", got[0].StartTime, got[1].StartTime)
	}
	if !in[0].StartTime.Equal(older.StartTime) {
		t.Fatal("sortNewestFirst mutated its input slice")
	}
}

// ---- JSON ----

// jsonWire is the test-side view of one encoded block, mirroring
// docs/blocks.schema.json.
type jsonWire struct {
	ID             string                `json:"id"`
	StartTime      string                `json:"startTime"`
	EndTime        string                `json:"endTime"`
	ActualEndTime  *string               `json:"actualEndTime"`
	IsActive       bool                  `json:"isActive"`
	IsGap          bool                  `json:"isGap"`
	Models         []string              `json:"models"`
	Cost           float64               `json:"cost"`
	Tokens         limit.WireTokens      `json:"tokens"`
	TotalTokens    int64                 `json:"totalTokens"`
	NonCacheTokens int64                 `json:"nonCacheTokens"`
	UsedPercent    *float64              `json:"usedPercent"`
	BurnRate       *limit.WireBurnRate   `json:"burnRate"`
	Projection     *limit.WireProjection `json:"projection"`
}

func decodeJSONBlocks(t *testing.T, out []byte) []map[string]json.RawMessage {
	t.Helper()
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	return raw
}

func TestBlocksJSONShape(t *testing.T) {
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	b := mkBlock(start, false, 10, 20, 30, 40, 1.25)
	b.Models = []string{"model-a", "model-b"}
	b.Entries = []limit.Entry{
		{Time: start, Input: 10, Output: 20},
		{Time: start.Add(10 * time.Minute), Input: 10, Output: 20},
	}

	out, err := blocksJSON([]limit.Block{b}, fixedNow, 0)
	if err != nil {
		t.Fatalf("blocksJSON: %v", err)
	}
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Fatal("blocksJSON output must end with a newline")
	}
	if !strings.Contains(string(out), "\n  {\n    \"id\": ") || strings.Contains(string(out), "\t") {
		t.Fatalf("blocksJSON is not 2-space indented:\n%s", out)
	}

	raw := decodeJSONBlocks(t, out)
	if len(raw) != 1 {
		t.Fatalf("decoded %d blocks, want 1", len(raw))
	}
	// Keys required by docs/blocks.schema.json.
	for _, key := range []string{
		"id", "startTime", "endTime", "actualEndTime", "isActive", "isGap",
		"models", "cost", "tokens", "totalTokens", "nonCacheTokens", "usedPercent",
	} {
		if _, ok := raw[0][key]; !ok {
			t.Fatalf("JSON block missing key %q:\n%s", key, out)
		}
	}
	if _, ok := raw[0]["entries"]; ok {
		t.Fatalf("JSON block must not include the raw entries array:\n%s", out)
	}

	// $defs/tokens allows exactly the four raw counters.
	var tokenKeys map[string]json.RawMessage
	if err := json.Unmarshal(raw[0]["tokens"], &tokenKeys); err != nil {
		t.Fatalf("unmarshal tokens: %v", err)
	}
	wantTokenKeys := map[string]bool{"input": true, "output": true, "cacheRead": true, "cacheWrite": true}
	if len(tokenKeys) != len(wantTokenKeys) {
		t.Fatalf("tokens has keys %v, want exactly input/output/cacheRead/cacheWrite", tokenKeys)
	}
	for k := range tokenKeys {
		if !wantTokenKeys[k] {
			t.Fatalf("tokens has unexpected key %q (the schema forbids extra properties)", k)
		}
	}

	var wire jsonWire
	if err := json.Unmarshal(out, &[]jsonWire{}); err != nil {
		t.Fatalf("unmarshal block list: %v", err)
	}
	var list []jsonWire
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("unmarshal block list: %v", err)
	}
	wire = list[0]

	if wire.ID != b.ID {
		t.Fatalf("id = %q, want %q", wire.ID, b.ID)
	}
	if wire.StartTime != start.Format(time.RFC3339) {
		t.Fatalf("startTime = %q, want %q", wire.StartTime, start.Format(time.RFC3339))
	}
	if wire.EndTime != b.EndTime.Format(time.RFC3339) {
		t.Fatalf("endTime = %q, want %q", wire.EndTime, b.EndTime.Format(time.RFC3339))
	}
	if wire.ActualEndTime == nil || *wire.ActualEndTime != b.ActualEndTime.Format(time.RFC3339) {
		t.Fatalf("actualEndTime = %v, want %q", wire.ActualEndTime, b.ActualEndTime.Format(time.RFC3339))
	}
	if wire.IsActive || wire.IsGap {
		t.Fatalf("flags = active:%v gap:%v, want false/false", wire.IsActive, wire.IsGap)
	}
	if len(wire.Models) != 2 || wire.Models[0] != "model-a" {
		t.Fatalf("models = %v, want [model-a model-b]", wire.Models)
	}
	if wire.Tokens.Input != 10 || wire.Tokens.Output != 20 || wire.Tokens.CacheRead != 30 || wire.Tokens.CacheWrite != 40 {
		t.Fatalf("tokens = %+v, want 10/20/30/40", wire.Tokens)
	}
	if wire.TotalTokens != 100 {
		t.Fatalf("totalTokens = %d, want 100", wire.TotalTokens)
	}
	if wire.NonCacheTokens != 30 {
		t.Fatalf("nonCacheTokens = %d, want 30 (input+output)", wire.NonCacheTokens)
	}
	if wire.Cost != 1.25 {
		t.Fatalf("cost = %v, want 1.25", wire.Cost)
	}
	// Without --limit-tokens the used percentage stays null.
	if wire.UsedPercent != nil {
		t.Fatalf("usedPercent = %v, want null when no token limit is set", *wire.UsedPercent)
	}
	// A non-active block has a burn rate but no projection.
	if wire.BurnRate == nil {
		t.Fatalf("active-usage block should carry a burnRate:\n%s", out)
	}
	if wire.BurnRate.TokensPerMinute != 10 {
		t.Fatalf("burnRate.tokensPerMinute = %v, want 10 (100 tokens over 10min)", wire.BurnRate.TokensPerMinute)
	}
	if wire.BurnRate.TokensPerMinuteDisplay != 3 {
		t.Fatalf("burnRate.tokensPerMinuteDisplay = %v, want 3 (30 non-cache over 10min)", wire.BurnRate.TokensPerMinuteDisplay)
	}
	if wire.Projection != nil {
		t.Fatalf("projection must be omitted for a non-active block:\n%s", out)
	}
}

func TestBlocksJSONActiveDerivedFields(t *testing.T) {
	start := fixedNow.Add(-time.Hour)
	b := mkBlock(start, true, 10, 20, 30, 40, 1.25)
	b.Entries = []limit.Entry{
		{Time: start, Input: 10, Output: 20},
		{Time: start.Add(10 * time.Minute), Input: 10, Output: 20},
	}

	out, err := blocksJSON([]limit.Block{b}, fixedNow, 200)
	if err != nil {
		t.Fatalf("blocksJSON: %v", err)
	}
	var list []jsonWire
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("decoded %d blocks, want 1", len(list))
	}
	got := list[0]
	if got.UsedPercent == nil || *got.UsedPercent != 50 {
		t.Fatalf("usedPercent = %v, want 50 (100 of 200 tokens)", got.UsedPercent)
	}
	if got.BurnRate == nil {
		t.Fatal("active block must carry a burnRate")
	}
	if got.Projection == nil {
		t.Fatal("active block must carry a projection")
	}
	if got.Projection.TotalTokens <= got.TotalTokens {
		t.Fatalf("projection.totalTokens = %d, want more than the current %d", got.Projection.TotalTokens, got.TotalTokens)
	}
	if got.Projection.RemainingMinutes != 240 {
		t.Fatalf("projection.remainingMinutes = %d, want 240 (4h left)", got.Projection.RemainingMinutes)
	}
}

func TestBlocksJSONGapAndEmptyList(t *testing.T) {
	start := time.Date(2026, 9, 10, 8, 0, 0, 0, time.Local)
	gap := limit.Block{
		ID:        "gap-" + start.Format(time.RFC3339),
		StartTime: start,
		EndTime:   start.Add(time.Hour),
		IsGap:     true,
	}
	out, err := blocksJSON([]limit.Block{gap}, fixedNow, 1000)
	if err != nil {
		t.Fatalf("blocksJSON: %v", err)
	}
	var got []jsonWire
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || !got[0].IsGap || got[0].IsActive {
		t.Fatalf("gap block JSON = %+v", got)
	}
	if got[0].ActualEndTime != nil {
		t.Fatalf("zero actualEndTime should encode as null, got %q", *got[0].ActualEndTime)
	}
	if got[0].Models == nil {
		t.Fatal("models must encode as [] rather than null")
	}
	if got[0].NonCacheTokens != 0 || got[0].TotalTokens != 0 {
		t.Fatalf("gap totals = %d/%d, want 0/0", got[0].TotalTokens, got[0].NonCacheTokens)
	}
	if got[0].BurnRate != nil || got[0].Projection != nil {
		t.Fatalf("gap block must omit burnRate/projection: %+v", got[0])
	}

	empty, err := blocksJSON(nil, fixedNow, 0)
	if err != nil {
		t.Fatalf("blocksJSON(nil): %v", err)
	}
	if string(empty) != "[]\n" {
		t.Fatalf("blocksJSON(nil) = %q, want %q", empty, "[]\n")
	}
}

// ---- active window ----

func TestActiveDetailNoActiveWindow(t *testing.T) {
	if got := activeDetail(nil, fixedNow, 0, 80); got != noActiveWindowMsg {
		t.Fatalf("activeDetail(nil) = %q, want %q", got, noActiveWindowMsg)
	}

	// A finished block is not active, so the same single line is printed.
	finished := mkBlock(fixedNow.Add(-24*time.Hour), false, 10, 10, 0, 0, 0)
	if got := activeDetail([]limit.Block{finished}, fixedNow, 0, 80); got != noActiveWindowMsg {
		t.Fatalf("activeDetail(finished) = %q, want %q", got, noActiveWindowMsg)
	}
	if strings.Count(noActiveWindowMsg, "\n") != 0 {
		t.Fatal("the no-active-window message must be a single line")
	}
}

func TestActiveDetailRendersPanel(t *testing.T) {
	start := fixedNow.Add(-time.Hour)
	b := mkBlock(start, true, 10, 20, 30, 40, 1.25)
	b.Models = []string{"model-a", "model-b"}

	out := activeDetail([]limit.Block{b}, fixedNow, 0, 80)
	if out == noActiveWindowMsg {
		t.Fatal("activeDetail returned the no-active message for an active block")
	}
	for _, want := range []string{"Start", "End", "Elapsed", "Remaining", "1h", "4h", "100", "$1.25", "model-a, model-b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("active panel missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "%") {
		t.Fatalf("panel must not show a percentage without --limit-tokens:\n%s", out)
	}
}

// ---- command wiring ----

func TestCommandFlags(t *testing.T) {
	c := cmdBlocks()
	if c.Name() != "blocks" {
		t.Fatalf("command name = %q, want %q", c.Name(), "blocks")
	}
	for _, name := range []string{"window", "active", "limit-tokens", "json", "since", "until", "compact"} {
		if c.Flags().Lookup(name) == nil {
			t.Fatalf("flag --%s is not defined", name)
		}
	}

	window, err := c.Flags().GetDuration("window")
	if err != nil || window != limit.DefaultWindow {
		t.Fatalf("--window default = %v (%v), want %v", window, err, limit.DefaultWindow)
	}
	for _, name := range []string{"active", "json", "compact"} {
		if v, err := c.Flags().GetBool(name); err != nil || v {
			t.Fatalf("--%s default = %v (%v), want false", name, v, err)
		}
	}
	if v, err := c.Flags().GetInt64("limit-tokens"); err != nil || v != 0 {
		t.Fatalf("--limit-tokens default = %v (%v), want 0", v, err)
	}
	for _, name := range []string{"since", "until"} {
		if v, err := c.Flags().GetString(name); err != nil || v != "" {
			t.Fatalf("--%s default = %q (%v), want empty", name, v, err)
		}
	}
	for _, key := range []string{"help.blockWindow", "help.blockActive", "help.blockLimitTokens", "help.blockJSON", "help.blockSince", "help.blockUntil", "help.blockCompact"} {
		flagName := map[string]string{
			"help.blockWindow":      "window",
			"help.blockActive":      "active",
			"help.blockLimitTokens": "limit-tokens",
			"help.blockJSON":        "json",
			"help.blockSince":       "since",
			"help.blockUntil":       "until",
			"help.blockCompact":     "compact",
		}[key]
		if got := c.Flags().Lookup(flagName).Usage; got == "" {
			t.Fatalf("flag --%s has an empty usage string (i18n key %s)", flagName, key)
		}
	}
}
