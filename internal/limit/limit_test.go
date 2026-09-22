package limit

import (
	"math"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// All tests use a fixed "now": the package must never read the wall clock, so
// every time value flows in as a parameter.

func mustTime(s string) time.Time {
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic("bad test timestamp " + s + ": " + err.Error())
	}
	return v
}

func ptrF(v float64) *float64 { return &v }

func nearly(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// usageEntry builds an entry with a fixed token shape: 100 in / 50 out / 10 cache read.
func usageEntry(at, modelName string) Entry {
	return Entry{
		Time:      mustTime(at),
		Model:     modelName,
		Input:     100,
		Output:    50,
		CacheRead: 10,
	}
}

func TestTokenCounts(t *testing.T) {
	tc := TokenCounts{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40}
	if got, want := tc.Total(), int64(100); got != want {
		t.Fatalf("Total() = %d, want %d", got, want)
	}
	if got, want := tc.NonCache(), int64(30); got != want {
		t.Fatalf("NonCache() = %d, want %d", got, want)
	}
	var zero TokenCounts
	if zero.Total() != 0 || zero.NonCache() != 0 {
		t.Fatalf("zero TokenCounts should total 0, got %d/%d", zero.Total(), zero.NonCache())
	}
}

func TestConstants(t *testing.T) {
	if DefaultWindow != 5*time.Hour {
		t.Fatalf("DefaultWindow = %v, want 5h", DefaultWindow)
	}
	if WarningThreshold != 0.8 {
		t.Fatalf("WarningThreshold = %v, want 0.8", WarningThreshold)
	}
}

func TestIdentifyEmpty(t *testing.T) {
	if got := Identify(nil, DefaultWindow, mustTime("2025-01-02T12:00:00Z")); got != nil {
		t.Fatalf("Identify(nil) = %v, want nil", got)
	}
	if got := Identify([]Entry{}, DefaultWindow, mustTime("2025-01-02T12:00:00Z")); got != nil {
		t.Fatalf("Identify(empty) = %v, want nil", got)
	}
}

func TestIdentifySingleEntry(t *testing.T) {
	entries := []Entry{usageEntry("2025-01-02T13:47:12Z", "claude-sonnet-4-5")}

	// Long after: the single window has closed.
	blocks := Identify(entries, DefaultWindow, mustTime("2025-01-03T00:00:00Z"))
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	if blocks[0].IsActive {
		t.Fatalf("block should not be active when now is long after it")
	}
	if blocks[0].IsGap {
		t.Fatalf("single entry block must not be a gap block")
	}
	if got, want := blocks[0].ActualEndTime, entries[0].Time; !got.Equal(want) {
		t.Fatalf("ActualEndTime = %v, want %v", got, want)
	}

	// Inside the window.
	blocks = Identify(entries, DefaultWindow, mustTime("2025-01-02T14:00:00Z"))
	if len(blocks) != 1 || !blocks[0].IsActive {
		t.Fatalf("block should be active when now is inside the window, got %+v", blocks)
	}
	if got := Active(blocks); got == nil || got != &blocks[0] {
		t.Fatalf("Active() = %v, want &blocks[0]", got)
	}
}

// A whole-hour local offset still displays as an on-the-hour boundary because
// floorToHour works in UTC.
func TestIdentifyFloorsToHourInUTC(t *testing.T) {
	entries := []Entry{usageEntry("2025-01-02T13:47:12Z", "m")}
	now := mustTime("2025-01-02T14:00:00Z")
	blocks := Identify(entries, DefaultWindow, now)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	b := blocks[0]
	if got, want := b.StartTime, mustTime("2025-01-02T13:00:00Z"); !got.Equal(want) {
		t.Fatalf("StartTime = %v, want %v", got, want)
	}
	if b.StartTime.Location() != time.UTC {
		t.Fatalf("StartTime location = %v, want UTC", b.StartTime.Location())
	}
	if got, want := b.EndTime, mustTime("2025-01-02T18:00:00Z"); !got.Equal(want) {
		t.Fatalf("EndTime = %v, want %v", got, want)
	}
	if got, want := b.ID, "2025-01-02T13:00:00Z"; got != want {
		t.Fatalf("ID = %q, want %q", got, want)
	}

	// Same instant expressed in a +08:00 zone floors identically (UTC), which
	// is the point of flooring in UTC rather than the local zone.
	zoned := []Entry{{Time: mustTime("2025-01-02T21:47:12+08:00")}}
	blocksZ := Identify(zoned, DefaultWindow, now)
	if len(blocksZ) != 1 || !blocksZ[0].StartTime.Equal(mustTime("2025-01-02T13:00:00Z")) {
		t.Fatalf("zoned StartTime = %v, want 2025-01-02T13:00:00Z", blocksZ[0].StartTime)
	}
}

// Comparisons are strictly `>`: an entry exactly window after the block start
// does NOT split, one nanosecond more DOES.
func TestIdentifyStrictStartBoundary(t *testing.T) {
	t0 := mustTime("2025-01-02T10:00:00Z")

	exact := []Entry{
		{Time: t0, Input: 1},
		{Time: t0.Add(DefaultWindow), Input: 1},
	}
	blocks := Identify(exact, DefaultWindow, mustTime("2025-01-02T16:00:00Z"))
	if len(blocks) != 1 {
		t.Fatalf("entry exactly window after start split the block: got %d blocks, want 1", len(blocks))
	}
	if len(blocks[0].Entries) != 2 {
		t.Fatalf("block has %d entries, want 2", len(blocks[0].Entries))
	}

	plusOneNs := []Entry{
		{Time: t0, Input: 1},
		{Time: t0.Add(DefaultWindow + time.Nanosecond), Input: 1},
	}
	// Splits, and because the gap is also just over a window a (1ns) gap block
	// is emitted between the two windows.
	blocks = Identify(plusOneNs, DefaultWindow, mustTime("2025-01-02T16:00:00Z"))
	if len(blocks) != 3 {
		t.Fatalf("entry one nanosecond past the window: got %d blocks, want 3 (block/gap/block)", len(blocks))
	}
	if blocks[0].IsGap || !blocks[1].IsGap || blocks[2].IsGap {
		t.Fatalf("expected block/gap/block, got gaps %v/%v/%v", blocks[0].IsGap, blocks[1].IsGap, blocks[2].IsGap)
	}
	if got, want := blocks[1].StartTime, t0.Add(DefaultWindow); !got.Equal(want) {
		t.Fatalf("gap StartTime = %v, want %v", got, want)
	}
	if got, want := blocks[2].StartTime, mustTime("2025-01-02T15:00:00Z"); !got.Equal(want) {
		t.Fatalf("second block StartTime = %v, want %v", got, want)
	}
}

// A split can happen without a gap: the entries are close to each other but far
// from the window start.
func TestIdentifySplitWithoutGap(t *testing.T) {
	t0 := mustTime("2025-01-02T10:00:00Z")
	entries := []Entry{
		{Time: t0, Input: 1},
		{Time: t0.Add(4 * time.Hour), Input: 1},
		{Time: t0.Add(DefaultWindow + time.Nanosecond), Input: 1},
	}
	blocks := Identify(entries, DefaultWindow, mustTime("2025-01-02T16:00:00Z"))
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (no gap: entries are 1h+1ns apart)", len(blocks))
	}
	if blocks[0].IsGap || blocks[1].IsGap {
		t.Fatalf("neither block should be a gap block")
	}
	if len(blocks[0].Entries) != 2 || len(blocks[1].Entries) != 1 {
		t.Fatalf("entry split = %d/%d, want 2/1", len(blocks[0].Entries), len(blocks[1].Entries))
	}
}

// The gap rule is also strictly `>`: exactly window after the last entry does
// not create a gap block; more does.
func TestIdentifyStrictGapBoundary(t *testing.T) {
	now := mustTime("2025-01-02T17:00:00Z")
	first := mustTime("2025-01-02T10:30:00Z")

	// Exactly window after the last entry: the window splits (5.5h past the
	// 10:00 start) but no gap block appears.
	exact := []Entry{
		{Time: first, Input: 1},
		{Time: first.Add(DefaultWindow), Input: 1},
	}
	blocks := Identify(exact, DefaultWindow, now)
	if len(blocks) != 2 {
		t.Fatalf("exactly window after last entry: got %d blocks, want 2 (no gap)", len(blocks))
	}
	if blocks[0].IsGap || blocks[1].IsGap {
		t.Fatalf("no gap block expected at the exact boundary")
	}

	// One nanosecond more: a gap block is inserted, spanning
	// lastEntry+window .. nextEntry.
	next := first.Add(DefaultWindow + time.Nanosecond)
	over := []Entry{
		{Time: first, Input: 1},
		{Time: next, Input: 1},
	}
	blocks = Identify(over, DefaultWindow, now)
	if len(blocks) != 3 {
		t.Fatalf("past the gap boundary: got %d blocks, want 3 (block/gap/block)", len(blocks))
	}
	gap := blocks[1]
	if !gap.IsGap {
		t.Fatalf("blocks[1] should be the gap block")
	}
	if got, want := gap.StartTime, first.Add(DefaultWindow); !got.Equal(want) {
		t.Fatalf("gap StartTime = %v, want lastEntry+window = %v", got, want)
	}
	if got, want := gap.EndTime, next; !got.Equal(want) {
		t.Fatalf("gap EndTime = %v, want nextEntry = %v", got, want)
	}
	if got, want := gap.ID, "gap-"+mustTime("2025-01-02T15:30:00Z").Format(time.RFC3339); got != want {
		t.Fatalf("gap ID = %q, want %q", got, want)
	}
}

func TestIdentifyMultiBlockAcrossDays(t *testing.T) {
	entries := []Entry{
		usageEntry("2025-01-02T10:00:00Z", "alpha"), // block 0
		usageEntry("2025-01-02T11:00:00Z", "beta"),
		usageEntry("2025-01-02T20:00:00Z", "alpha"), // gap 1, block 2
		usageEntry("2025-01-02T20:30:00Z", "beta"),
		usageEntry("2025-01-03T09:00:00Z", "gamma"), // gap 3, block 4
		usageEntry("2025-01-03T09:05:00Z", "gamma"),
		usageEntry("2025-01-04T12:00:00Z", "delta"), // gap 5, block 6
	}
	now := mustTime("2025-01-04T13:00:00Z")
	blocks := Identify(entries, DefaultWindow, now)

	if len(blocks) != 7 {
		t.Fatalf("got %d blocks, want 7", len(blocks))
	}
	wantGap := []bool{false, true, false, true, false, true, false}
	for i, want := range wantGap {
		if blocks[i].IsGap != want {
			t.Fatalf("blocks[%d].IsGap = %v, want %v", i, blocks[i].IsGap, want)
		}
	}
	wantEntries := []int{2, 0, 2, 0, 2, 0, 1}
	for i, want := range wantEntries {
		if got := len(blocks[i].Entries); got != want {
			t.Fatalf("blocks[%d] has %d entries, want %d", i, got, want)
		}
	}

	wantIDs := []string{
		"2025-01-02T10:00:00Z",
		"gap-2025-01-02T16:00:00Z",
		"2025-01-02T20:00:00Z",
		"gap-2025-01-03T01:30:00Z",
		"2025-01-03T09:00:00Z",
		"gap-2025-01-03T14:05:00Z",
		"2025-01-04T12:00:00Z",
	}
	for i, want := range wantIDs {
		if blocks[i].ID != want {
			t.Fatalf("blocks[%d].ID = %q, want %q", i, blocks[i].ID, want)
		}
	}

	// Gap blocks carry no usage at all.
	for _, i := range []int{1, 3, 5} {
		g := blocks[i]
		if g.Tokens.Total() != 0 || g.Cost != 0 || g.Models != nil {
			t.Fatalf("gap block %d has usage: tokens=%d cost=%v models=%v", i, g.Tokens.Total(), g.Cost, g.Models)
		}
		if !g.ActualEndTime.IsZero() {
			t.Fatalf("gap block %d ActualEndTime = %v, want zero", i, g.ActualEndTime)
		}
		if g.IsActive {
			t.Fatalf("gap block %d must never be active", i)
		}
		if g.Entries != nil {
			t.Fatalf("gap block %d has entries", i)
		}
	}

	// Per-entry shape is 160 tokens (100 in / 50 out / 10 cache read).
	wantTotals := []int64{320, 0, 320, 0, 320, 0, 160}
	for i, want := range wantTotals {
		if got := blocks[i].Tokens.Total(); got != want {
			t.Fatalf("blocks[%d].Tokens.Total() = %d, want %d", i, got, want)
		}
	}

	// Models are unique and in first-seen order per block.
	if got := blocks[0].Models; len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("blocks[0].Models = %v, want [alpha beta]", got)
	}
	if got := blocks[4].Models; len(got) != 1 || got[0] != "gamma" {
		t.Fatalf("blocks[4].Models = %v, want [gamma]", got)
	}
	if got := blocks[6].Models; len(got) != 1 || got[0] != "delta" {
		t.Fatalf("blocks[6].Models = %v, want [delta]", got)
	}

	// Only the newest window is live.
	live := Active(blocks)
	if live == nil || live != &blocks[6] {
		t.Fatalf("Active() = %v, want &blocks[6]", live)
	}
	for _, i := range []int{0, 2, 4} {
		if blocks[i].IsActive {
			t.Fatalf("blocks[%d] should be closed", i)
		}
	}
}

// Identify must not assume the caller sorted its input, and must not reorder
// the caller's slice.
func TestIdentifySortsInput(t *testing.T) {
	a := usageEntry("2025-01-02T10:00:00Z", "m")
	b := usageEntry("2025-01-02T09:00:00Z", "m")
	entries := []Entry{a, b}
	blocks := Identify(entries, DefaultWindow, mustTime("2025-01-02T12:00:00Z"))
	if len(blocks) != 1 || len(blocks[0].Entries) != 2 {
		t.Fatalf("got %d blocks, want 1 with 2 entries", len(blocks))
	}
	if got := blocks[0].Entries[0].Time; !got.Equal(b.Time) {
		t.Fatalf("first entry = %v, want the earliest (%v)", got, b.Time)
	}
	if !entries[0].Time.Equal(a.Time) || !entries[1].Time.Equal(b.Time) {
		t.Fatalf("Identify mutated the caller's slice order")
	}
}

func TestIdentifyNonPositiveWindowUsesDefault(t *testing.T) {
	entries := []Entry{
		usageEntry("2025-01-02T10:00:00Z", "m"),
		usageEntry("2025-01-02T15:00:00Z", "m"), // exactly DefaultWindow after start
	}
	fixedNow := mustTime("2025-01-02T16:00:00Z")
	for _, w := range []time.Duration{0, -time.Second, -DefaultWindow} {
		blocks := Identify(entries, w, fixedNow)
		if len(blocks) != 1 {
			t.Fatalf("window=%v: got %d blocks, want 1 (DefaultWindow behavior)", w, len(blocks))
		}
		if got, want := blocks[0].EndTime, mustTime("2025-01-02T15:00:00Z"); !got.Equal(want) {
			t.Fatalf("window=%v: EndTime = %v, want %v", w, got, want)
		}
	}
}

func TestIdentifyExplicitShortWindow(t *testing.T) {
	entries := []Entry{
		{Time: mustTime("2025-01-02T10:00:00Z"), Input: 1},
		{Time: mustTime("2025-01-02T11:00:00Z"), Input: 1}, // exactly 1h after start
	}
	blocks := Identify(entries, time.Hour, mustTime("2025-01-02T12:00:00Z"))
	if len(blocks) != 1 {
		t.Fatalf("exactly window after start must not split: got %d blocks", len(blocks))
	}
	entries[1].Time = mustTime("2025-01-02T11:00:00Z").Add(time.Nanosecond)
	blocks = Identify(entries, time.Hour, mustTime("2025-01-02T12:00:00Z"))
	if len(blocks) != 3 {
		t.Fatalf("past the window: got %d blocks, want 3", len(blocks))
	}
}

// IsActive uses strict `<` on both halves of the condition.
func TestIsActiveBoundaries(t *testing.T) {
	entries := []Entry{
		{Time: mustTime("2025-01-02T10:00:00Z"), Input: 1},
		{Time: mustTime("2025-01-02T10:30:00Z"), Input: 1},
	}
	// Start 10:00, EndTime 15:00, ActualEndTime 10:30.
	cases := []struct {
		name string
		now  string
		want bool
	}{
		{"now == ActualEndTime", "2025-01-02T10:30:00Z", true},
		{"now just inside", "2025-01-02T14:59:59.999999999Z", true},
		{"now == EndTime is not active", "2025-01-02T15:00:00Z", false},
		{"now just past EndTime", "2025-01-02T15:00:00.000000001Z", false},
		{"now == ActualEndTime+window", "2025-01-02T15:30:00Z", false},
		{"now just before ActualEndTime+window", "2025-01-02T15:29:59.999999999Z", false},
		{"now == StartTime", "2025-01-02T10:00:00Z", true},
		{"now before StartTime", "2025-01-02T09:00:00Z", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks := Identify(entries, DefaultWindow, mustTime(tc.now))
			if len(blocks) != 1 {
				t.Fatalf("got %d blocks, want 1", len(blocks))
			}
			if blocks[0].IsActive != tc.want {
				t.Fatalf("IsActive = %v, want %v (now=%s, EndTime=%v, ActualEndTime=%v)",
					blocks[0].IsActive, tc.want, tc.now, blocks[0].EndTime, blocks[0].ActualEndTime)
			}
			if got := Active(blocks); (got != nil) != tc.want {
				t.Fatalf("Active() = %v, want active=%v", got, tc.want)
			}
		})
	}
}

func TestActiveNone(t *testing.T) {
	entries := []Entry{usageEntry("2025-01-02T10:00:00Z", "m")}
	blocks := Identify(entries, DefaultWindow, mustTime("2025-02-01T00:00:00Z"))
	if got := Active(blocks); got != nil {
		t.Fatalf("Active() = %v, want nil", got)
	}
	if got := Active(nil); got != nil {
		t.Fatalf("Active(nil) = %v, want nil", got)
	}
	// A hand-built gap block must never be reported as active.
	gapOnly := []Block{{ID: "gap-x", IsGap: true, IsActive: true}}
	if got := Active(gapOnly); got != nil {
		t.Fatalf("Active() over gap block = %v, want nil", got)
	}
}

func TestBurnRateOfNilCases(t *testing.T) {
	if got := BurnRateOf(nil); got != nil {
		t.Fatalf("BurnRateOf(nil) = %v, want nil", got)
	}
	if got := BurnRateOf(&Block{IsGap: true, Entries: []Entry{{}, {}}}); got != nil {
		t.Fatalf("BurnRateOf(gap) = %v, want nil", got)
	}
	if got := BurnRateOf(&Block{Entries: nil}); got != nil {
		t.Fatalf("BurnRateOf(empty) = %v, want nil", got)
	}
	if got := BurnRateOf(&Block{Entries: []Entry{{Time: mustTime("2025-01-02T10:00:00Z")}}}); got != nil {
		t.Fatalf("BurnRateOf(single entry) = %v, want nil", got)
	}
	same := mustTime("2025-01-02T10:00:00Z")
	if got := BurnRateOf(&Block{Entries: []Entry{{Time: same}, {Time: same}}}); got != nil {
		t.Fatalf("BurnRateOf(single instant) = %v, want nil", got)
	}
	// An empty block that nevertheless carries tokens has no elapsed time.
	if got := BurnRateOf(&Block{Tokens: TokenCounts{Input: 500}}); got != nil {
		t.Fatalf("BurnRateOf(empty entries, non-zero tokens) = %v, want nil", got)
	}
}

func TestBurnRateOfValues(t *testing.T) {
	start := mustTime("2025-01-02T10:00:00Z")
	b := &Block{
		Entries: []Entry{
			{Time: start, Model: "m"},
			{Time: start.Add(time.Hour), Model: "m"},
		},
		Tokens: TokenCounts{Input: 100, Output: 100, CacheRead: 800},
		Cost:   6.0,
	}
	rate := BurnRateOf(b)
	if rate == nil {
		t.Fatalf("BurnRateOf returned nil")
	}
	// 1000 tokens over 60 minutes.
	if !nearly(rate.TokensPerMinute, 1000.0/60.0) {
		t.Fatalf("TokensPerMinute = %v, want %v", rate.TokensPerMinute, 1000.0/60.0)
	}
	// Non-cache indicator: (100+100)/60.
	if !nearly(rate.TokensPerMinuteDisplay, 200.0/60.0) {
		t.Fatalf("TokensPerMinuteDisplay = %v, want %v", rate.TokensPerMinuteDisplay, 200.0/60.0)
	}
	if !nearly(rate.CostPerHour, 6.0) {
		t.Fatalf("CostPerHour = %v, want 6", rate.CostPerHour)
	}

	// A 30-minute span doubles both per-minute rates.
	short := &Block{
		Entries: []Entry{
			{Time: start},
			{Time: start.Add(30 * time.Minute)},
		},
		Tokens: TokenCounts{Input: 100, Output: 100, CacheRead: 800},
		Cost:   3.0,
	}
	r2 := BurnRateOf(short)
	if r2 == nil || !nearly(r2.TokensPerMinute, 1000.0/30.0) || !nearly(r2.TokensPerMinuteDisplay, 200.0/30.0) || !nearly(r2.CostPerHour, 6.0) {
		t.Fatalf("30-minute burn rate = %+v", r2)
	}

	// Entries out of order must not flip the sign of the elapsed time: the
	// caller (Identify) always sorts, and BurnRateOf reads first/last as given.
	reversed := &Block{
		Entries: []Entry{
			{Time: start.Add(time.Hour)},
			{Time: start},
		},
		Tokens: TokenCounts{Input: 600},
	}
	if got := BurnRateOf(reversed); got != nil {
		t.Fatalf("reversed entries should yield no rate (negative span), got %+v", got)
	}
}

func TestBurnRateGapNeverReported(t *testing.T) {
	entries := []Entry{
		usageEntry("2025-01-02T10:00:00Z", "m"),
		usageEntry("2025-01-02T11:00:00Z", "m"),
		usageEntry("2025-01-02T20:00:00Z", "m"),
	}
	now := mustTime("2025-01-02T21:00:00Z")
	blocks := Identify(entries, DefaultWindow, now)
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	gap := blocks[1]
	if !gap.IsGap {
		t.Fatalf("blocks[1] should be a gap")
	}
	if got := BurnRateOf(&gap); got != nil {
		t.Fatalf("BurnRateOf(gap) = %+v, want nil", got)
	}
	if got := Project(&gap, now); got != nil {
		t.Fatalf("Project(gap) = %+v, want nil", got)
	}
	if got := UsedPercent(&gap, 1000); got == nil || *got != 0 {
		t.Fatalf("UsedPercent(gap) = %v, want 0", got)
	}
}

func TestProject(t *testing.T) {
	start := mustTime("2025-01-02T10:00:00Z")
	b := &Block{
		ID:            start.UTC().Format(time.RFC3339),
		StartTime:     start,
		EndTime:       start.Add(DefaultWindow),
		ActualEndTime: start.Add(time.Hour),
		IsActive:      true,
		Entries: []Entry{
			{Time: start},
			{Time: start.Add(time.Hour)},
		},
		Tokens: TokenCounts{Input: 3000, Output: 3000}, // 6000 total, 100/min over 60min
		Cost:   30.0,                                   // 30/hour
	}

	// now = 12:00, EndTime = 15:00 -> 180 minutes remaining.
	p := Project(b, mustTime("2025-01-02T12:00:00Z"))
	if p == nil {
		t.Fatalf("Project returned nil for an active block")
	}
	if got, want := p.RemainingMinutes, int64(180); got != want {
		t.Fatalf("RemainingMinutes = %d, want %d", got, want)
	}
	if got, want := p.TotalTokens, int64(6000+100*180); got != want {
		t.Fatalf("TotalTokens = %d, want %d", got, want)
	}
	if !nearly(p.TotalCost, 30.0+30.0*3) {
		t.Fatalf("TotalCost = %v, want %v", p.TotalCost, 30.0+30.0*3)
	}

	// now = ActualEndTime -> 240 minutes remaining.
	p = Project(b, start.Add(time.Hour))
	if p == nil || p.RemainingMinutes != 240 {
		t.Fatalf("RemainingMinutes = %v, want 240", p)
	}
	if want := int64(6000 + 100*240); p.TotalTokens != want {
		t.Fatalf("TotalTokens = %d, want %d", p.TotalTokens, want)
	}

	// Inactive blocks project nothing.
	inactive := *b
	inactive.IsActive = false
	if got := Project(&inactive, mustTime("2025-01-02T12:00:00Z")); got != nil {
		t.Fatalf("Project(inactive) = %+v, want nil", got)
	}
	if got := Project(nil, mustTime("2025-01-02T12:00:00Z")); got != nil {
		t.Fatalf("Project(nil) = %+v, want nil", got)
	}

	// Active but without a usable burn rate (single entry) projects nothing.
	noRate := *b
	noRate.Entries = noRate.Entries[:1]
	if got := Project(&noRate, mustTime("2025-01-02T12:00:00Z")); got != nil {
		t.Fatalf("Project(no burn rate) = %+v, want nil", got)
	}
}

// A block flagged active whose window has already elapsed must clamp to 0
// rather than extrapolating a negative span.
func TestProjectClampsRemainingMinutes(t *testing.T) {
	start := mustTime("2025-01-02T09:00:00Z")
	b := &Block{
		StartTime:     start,
		EndTime:       start.Add(time.Hour), // already past
		ActualEndTime: start.Add(30 * time.Minute),
		IsActive:      true, // deliberately stale flag
		Entries: []Entry{
			{Time: start},
			{Time: start.Add(30 * time.Minute)},
		},
		Tokens: TokenCounts{Input: 300}, // 10/min over 30min
		Cost:   3.0,                     // 6/hour
	}
	p := Project(b, mustTime("2025-01-02T12:00:00Z"))
	if p == nil {
		t.Fatalf("Project returned nil")
	}
	if p.RemainingMinutes != 0 {
		t.Fatalf("RemainingMinutes = %d, want 0 (clamped, never negative)", p.RemainingMinutes)
	}
	if p.RemainingMinutes < 0 {
		t.Fatalf("RemainingMinutes must never be negative")
	}
	if got, want := p.TotalTokens, b.Tokens.Total(); got != want {
		t.Fatalf("TotalTokens = %d, want the current total %d", got, want)
	}
	if !nearly(p.TotalCost, b.Cost) {
		t.Fatalf("TotalCost = %v, want %v", p.TotalCost, b.Cost)
	}

	// Exactly at EndTime: also 0 remaining, no negative.
	p = Project(b, b.EndTime)
	if p == nil || p.RemainingMinutes != 0 {
		t.Fatalf("Project(now == EndTime).RemainingMinutes = %v, want 0", p)
	}
}

func TestUsedPercent(t *testing.T) {
	total := func(n int64) *Block { return &Block{Tokens: TokenCounts{Input: n}} }

	if got := UsedPercent(total(500), 0); got != nil {
		t.Fatalf("UsedPercent(limit=0) = %v, want nil", *got)
	}
	if got := UsedPercent(total(500), -100); got != nil {
		t.Fatalf("UsedPercent(limit<0) = %v, want nil", *got)
	}
	if got := UsedPercent(nil, 1000); got != nil {
		t.Fatalf("UsedPercent(nil block) = %v, want nil", *got)
	}

	cases := []struct {
		name  string
		block *Block
		limit int64
		want  float64
	}{
		{"empty block is 0", &Block{}, 1000, 0},
		{"exact half", total(500), 1000, 50},
		{"rounds to one decimal (up)", total(2), 3, 66.7},
		{"rounds to one decimal (down)", total(1), 3, 33.3},
		{"one eighth", total(1), 8, 12.5},
		{"clamps above 100", total(3), 1, 100},
		{"clamps far above 100", total(1_000_000), 1, 100},
		{"tiny share rounds to 0", total(1), 100000, 0},
		{"cache-inclusive total", &Block{Tokens: TokenCounts{Input: 250, CacheRead: 250}}, 1000, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UsedPercent(tc.block, tc.limit)
			if got == nil {
				t.Fatalf("UsedPercent returned nil")
			}
			if *got != tc.want {
				t.Fatalf("UsedPercent = %v, want %v", *got, tc.want)
			}
		})
	}

	// The result is clamped into [0,100] for any sane input.
	for _, n := range []int64{0, 1, 7, 100, 999, 1000, 1001, 1 << 40} {
		got := UsedPercent(total(n), 1000)
		if got == nil || *got < 0 || *got > 100 {
			t.Fatalf("UsedPercent(%d/1000) = %v, want a value in [0,100]", n, got)
		}
	}

	// The pointer is a fresh value, not aliased shared state.
	a := UsedPercent(total(2), 3)
	b := UsedPercent(total(1), 3)
	if a == b || *a == *b {
		t.Fatalf("UsedPercent results should be independent: %v vs %v", *a, *b)
	}
}

func TestFromSessions(t *testing.T) {
	sessions := []model.Session{
		{
			ID:          "s1",
			Model:       "ignored-model", // overridden by LatestModel
			LatestModel: "claude-sonnet-4-5",
			Messages: []model.Message{
				{NodeID: 1, Role: "system", Content: "hi", CreatedAt: mustTime("2025-01-02T12:00:00Z")},
				{
					NodeID:          2,
					Role:            "assistant",
					CreatedAt:       mustTime("2025-01-02T11:00:00Z"),
					GenerationModel: "", // falls back to session LatestModel
					Metrics: &model.Metrics{
						InputTokens: 1000, OutputTokens: 500,
						CacheReadTokens: 2000, CacheWriteTokens: 0,
					},
				},
				{NodeID: 3, Role: "user", Content: "no metrics", CreatedAt: mustTime("2025-01-02T10:00:00Z")},
			},
		},
		{
			ID:          "s2",
			Model:       "totally-unknown-model", // LatestModel empty -> session Model
			LatestModel: "",
			Messages: []model.Message{
				{
					NodeID:          1,
					Role:            "assistant",
					CreatedAt:       mustTime("2025-01-02T09:00:00Z"),
					GenerationModel: "gpt-4o",
					Metrics: &model.Metrics{
						InputTokens: 1000, OutputTokens: 100,
						CacheReadTokens: 0, CacheWriteTokens: 0,
					},
				},
				{NodeID: 2, Role: "tool", Content: "result", CreatedAt: mustTime("2025-01-02T09:30:00Z")},
				{
					NodeID:          3,
					Role:            "assistant",
					CreatedAt:       mustTime("2025-01-02T13:00:00Z"),
					GenerationModel: "gpt-4o",
					Metrics: &model.Metrics{
						InputTokens: 10, OutputTokens: 20,
						CacheReadTokens: 0, CacheWriteTokens: 0,
					},
				},
			},
		},
		{ID: "s3", Model: "empty-session"},
	}

	entries := FromSessions(sessions)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (only messages with Metrics)", len(entries))
	}

	// Sorted ascending across sessions.
	wantTimes := []string{"2025-01-02T09:00:00Z", "2025-01-02T11:00:00Z", "2025-01-02T13:00:00Z"}
	for i, want := range wantTimes {
		if got := entries[i].Time; !got.Equal(mustTime(want)) {
			t.Fatalf("entries[%d].Time = %v, want %v", i, got, want)
		}
	}

	// Model fallback: GenerationModel, then session LatestModel, then Model.
	wantModels := []string{"gpt-4o", "claude-sonnet-4-5", "gpt-4o"}
	for i, want := range wantModels {
		if entries[i].Model != want {
			t.Fatalf("entries[%d].Model = %q, want %q", i, entries[i].Model, want)
		}
	}

	// Token fields are copied verbatim.
	if got := entries[1]; got.Input != 1000 || got.Output != 500 || got.CacheRead != 2000 || got.CacheWrite != 0 {
		t.Fatalf("entries[1] tokens = %+v", got)
	}

	// Cost: gpt-4o is 2.50 in / 10.0 out per Mtok.
	// 1000/1e6*2.50 + 100/1e6*10.0 = 0.0025 + 0.001 = 0.0035
	if !nearly(entries[0].Cost, 0.0035) {
		t.Fatalf("entries[0].Cost = %v, want 0.0035", entries[0].Cost)
	}
	// claude-sonnet-4-5 is 3.0 in / 15.0 out / 0.30 cache read per Mtok.
	// 1000/1e6*3.0 + 500/1e6*15.0 + 2000/1e6*0.30 = 0.003 + 0.0075 + 0.0006 = 0.0111
	if !nearly(entries[1].Cost, 0.0111) {
		t.Fatalf("entries[1].Cost = %v, want 0.0111", entries[1].Cost)
	}
	if entries[2].Cost <= 0 {
		t.Fatalf("entries[2].Cost = %v, want > 0 for gpt-4o", entries[2].Cost)
	}

	// Unknown models price at zero rather than blowing up.
	unknown := FromSessions([]model.Session{{
		ID:    "u",
		Model: "totally-unknown-model",
		Messages: []model.Message{{
			Role:      "assistant",
			CreatedAt: mustTime("2025-01-02T09:00:00Z"),
			Metrics:   &model.Metrics{InputTokens: 5000, OutputTokens: 5000},
		}},
	}})
	if len(unknown) != 1 || unknown[0].Model != "totally-unknown-model" || unknown[0].Cost != 0 {
		t.Fatalf("unknown model entry = %+v, want zero cost", unknown)
	}
}

func TestFromSessionsEmpty(t *testing.T) {
	if got := FromSessions(nil); len(got) != 0 {
		t.Fatalf("FromSessions(nil) = %v, want empty", got)
	}
	noMetrics := []model.Session{{
		ID: "s",
		Messages: []model.Message{
			{Role: "user", Content: "a"},
			{Role: "assistant", Content: "b"},
		},
	}}
	if got := FromSessions(noMetrics); len(got) != 0 {
		t.Fatalf("FromSessions(sessions without metrics) = %v, want empty", got)
	}
}

// FromSessions feeds Identify end-to-end: the pieces must fit together.
func TestFromSessionsIntoIdentify(t *testing.T) {
	sessions := []model.Session{{
		ID:          "s1",
		LatestModel: "claude-sonnet-4-5",
		Messages: []model.Message{
			{
				Role:      "assistant",
				CreatedAt: mustTime("2025-01-02T10:00:00Z"),
				Metrics:   &model.Metrics{InputTokens: 1000, OutputTokens: 500},
			},
			{
				Role:      "assistant",
				CreatedAt: mustTime("2025-01-02T11:00:00Z"),
				Metrics:   &model.Metrics{InputTokens: 2000, OutputTokens: 500},
			},
		},
	}}
	entries := FromSessions(sessions)
	now := mustTime("2025-01-02T12:00:00Z")
	blocks := Identify(entries, DefaultWindow, now)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	b := blocks[0]
	if got, want := b.Tokens.Total(), int64(4000); got != want {
		t.Fatalf("block total = %d, want %d", got, want)
	}
	if got, want := b.Models, []string{"claude-sonnet-4-5"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("block models = %v, want %v", got, want)
	}
	if !b.IsActive {
		t.Fatalf("block should be active at %v", now)
	}
	// 4000 tokens over 60 minutes -> 4000 + (4000/60)*180 projected at 12:00.
	p := Project(&b, now)
	if p == nil || p.RemainingMinutes != 180 {
		t.Fatalf("projection = %+v, want RemainingMinutes 180", p)
	}
	if want := int64(4000 + 4000.0/60.0*180); p.TotalTokens != want {
		t.Fatalf("projected tokens = %d, want %d", p.TotalTokens, want)
	}
	if used := UsedPercent(&b, 8000); used == nil || *used != 50 {
		t.Fatalf("UsedPercent = %v, want 50", used)
	}
}
