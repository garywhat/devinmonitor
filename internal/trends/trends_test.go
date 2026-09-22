// Characterisation tests for the trends package.
//
// Everything here works on in-memory []model.Session fixtures — no database is
// opened and no real config directory is touched. All timestamps are fixed
// (UTC) so bucketing does not depend on the machine's timezone; the only
// wall-clock reads are the two APIs that read time.Now() internally
// (BuildMonthOverMonth, Build24HourBuckets) and those assertions are expressed
// relative to the clock, not against a fixed instant.
package trends

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// unpricedModel is deliberately absent from the built-in pricing table (and no
// built-in name is a prefix of it), so a session using it with no CreditCost/
// ACUCost estimates to exactly 0 — an isolated "free" session.
const unpricedModel = "zz-unpriced-model"

// ---- helpers ----

// at parses a fixed RFC3339 fixture timestamp. Panics only on a bad literal.
func at(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("trends_test: bad fixture time " + s + ": " + err.Error())
	}
	return v
}

// asst builds an assistant message with per-request metrics.
func asst(t time.Time, in, out, cacheRead, cacheWrite int64) model.Message {
	return model.Message{
		Role:      "assistant",
		CreatedAt: t,
		Metrics: &model.Metrics{
			InputTokens:      in,
			OutputTokens:     out,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
	}
}

// sess builds a session whose authoritative cost is credit (never estimated).
func sess(id string, credit float64, msgs ...model.Message) model.Session {
	return model.Session{ID: id, Model: unpricedModel, CreditCost: credit, Messages: msgs}
}

// freeSess builds a session with no credit cost and no token pricing, so its
// total cost is 0 while still carrying requests.
func freeSess(id string, msgs ...model.Message) model.Session {
	return model.Session{ID: id, Model: unpricedModel, Messages: msgs}
}

func wantFloat(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("%s is not a finite number: %v", label, got)
	}
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func wantFinite(t *testing.T, label string, got float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("%s is not a finite number: %v (NaN/+Inf regression)", label, got)
	}
}

// withinDir reports whether child is parent itself or nested inside it.
func withinDir(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// TestMain installs a throwaway config directory before ANY test in this
// package runs.
//
// config.Path() memoizes its result behind a sync.Once: if any test (in this
// file or a future one in the same package) triggers config.Path() before
// DEVINMONITOR_CONFIG_DIR is set, the developer's real ~/.devinmonitor would be
// frozen as the config dir and RenderDeltaBanner would write its delta snapshot
// there. Setting the variable here removes that hazard regardless of test
// order; TestRenderDeltaBanner still sets it per-test with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "devinmonitor-trends-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "trends_test: MkdirTemp:", err)
		os.Exit(1)
	}
	os.Setenv("DEVINMONITOR_CONFIG_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ---- period comparison ----

func TestBuildPeriodComparison_ComputesDeltasForKnownInput(t *testing.T) {
	curStart := at("2026-02-01T00:00:00Z")
	curEnd := at("2026-03-01T00:00:00Z")
	prevStart := at("2026-01-01T00:00:00Z")
	prevEnd := curStart

	jan := sess("jan", 2.0, asst(at("2026-01-15T10:00:00Z"), 100, 200, 300, 0))
	feb := sess("feb", 3.0, asst(at("2026-02-10T10:00:00Z"), 1000, 500, 0, 0))
	feb2 := sess("feb2", 5.0,
		asst(at("2026-02-11T10:00:00Z"), 100, 50, 10, 0),
		asst(at("2026-02-20T10:00:00Z"), 100, 50, 10, 0),
	)

	pc := BuildPeriodComparison([]model.Session{jan, feb, feb2}, curStart, curEnd, prevStart, prevEnd)

	// Current period: 2 sessions, 3 requests, in=1200 out=600 cacheRead=20, cost 8.
	if pc.Current.Requests != 3 {
		t.Errorf("Current.Requests = %d, want 3", pc.Current.Requests)
	}
	if pc.Current.InputTokens != 1200 || pc.Current.OutputTokens != 600 || pc.Current.CacheRead != 20 {
		t.Errorf("Current tokens = in %d / out %d / cacheRead %d, want 1200/600/20",
			pc.Current.InputTokens, pc.Current.OutputTokens, pc.Current.CacheRead)
	}
	wantFloat(t, "Current.CreditCost", pc.Current.CreditCost, 8)
	// Previous period: 1 session, 1 request, in=100 out=200 cacheRead=300, cost 2.
	if pc.Previous.Requests != 1 {
		t.Errorf("Previous.Requests = %d, want 1", pc.Previous.Requests)
	}
	wantFloat(t, "Previous.CreditCost", pc.Previous.CreditCost, 2)

	// Labels are inclusive at both ends (end is exclusive, rendered end-1s).
	if pc.Current.Label != "2026-02-01 ~ 2026-02-28" {
		t.Errorf("Current.Label = %q, want %q", pc.Current.Label, "2026-02-01 ~ 2026-02-28")
	}
	if pc.Previous.Label != "2026-01-01 ~ 2026-01-31" {
		t.Errorf("Previous.Label = %q, want %q", pc.Previous.Label, "2026-01-01 ~ 2026-01-31")
	}

	// NOTE: DeltaPct["Sessions"] is genuinely session-count based (2 vs 1), but
	// RenderPeriodComparison prints cur.Requests/prev.Requests in the "Sessions"
	// row and divides "Avg $/session" by requests — see the package report.
	// That rendering bug is deliberately NOT pinned as expected output here.
	want := map[string]float64{
		"Sessions":      100,           // (2-1)/1
		"Requests":      200,           // (3-1)/1
		"Input tokens":  1100,          // (1200-100)/100
		"Output tokens": 200,           // (600-200)/200
		"Cache read":    -93.333333333, // (20-300)/300
		"Total tokens":  203.333333333, // (1820-600)/600
		"Cost":          300,           // (8-2)/2
		"Avg $/session": 100,           // (4-2)/2
	}
	for _, name := range metricNames {
		got, ok := pc.DeltaPct[name]
		if !ok {
			t.Fatalf("DeltaPct is missing metric %q", name)
		}
		if math.Abs(got-want[name]) > 1e-6 {
			t.Errorf("DeltaPct[%q] = %v, want %v", name, got, want[name])
		}
	}
}

func TestBuildPeriodComparison_BoundariesAreHalfOpen(t *testing.T) {
	curStart := at("2026-02-01T00:00:00Z") // == prevEnd
	curEnd := at("2026-03-01T00:00:00Z")
	prevStart := at("2026-01-01T00:00:00Z")

	onBoundary := sess("on-boundary", 1.0, asst(curStart, 10, 10, 0, 0))
	atClose := sess("at-close", 100.0, asst(curEnd, 10, 10, 0, 0))

	pc := BuildPeriodComparison([]model.Session{onBoundary, atClose}, curStart, curEnd, prevStart, curStart)

	// [start, end): the start instant belongs to the current period...
	if pc.Current.Requests != 1 {
		t.Errorf("Current.Requests = %d, want 1 (message exactly at start)", pc.Current.Requests)
	}
	wantFloat(t, "Current.CreditCost", pc.Current.CreditCost, 1.0)
	// ...and the end instant belongs to neither period.
	if pc.Previous.Requests != 0 {
		t.Errorf("Previous.Requests = %d, want 0 (message at start is not in the previous period)", pc.Previous.Requests)
	}
	wantFloat(t, "Previous.CreditCost", pc.Previous.CreditCost, 0)
}

func TestBuildPeriodComparison_SessionWithoutAssistantMessagesIsIgnored(t *testing.T) {
	// computeBucketMetrics only marks a session in-range when it finds an
	// assistant message inside the window; tokens/cost alone do not qualify.
	curStart := at("2026-02-01T00:00:00Z")
	curEnd := at("2026-03-01T00:00:00Z")
	prevStart := at("2026-01-01T00:00:00Z")

	noMessages := model.Session{
		ID: "tokens-only", Model: unpricedModel, CreditCost: 42.0,
		InputTokens: 999, OutputTokens: 999,
		Messages: []model.Message{{Role: "user", CreatedAt: at("2026-02-05T10:00:00Z")}},
	}
	mixed := model.Session{
		ID: "mixed", Model: unpricedModel, CreditCost: 3.0,
		Messages: []model.Message{
			{Role: "assistant", CreatedAt: at("2026-01-20T10:00:00Z")},
			{Role: "assistant", CreatedAt: at("2026-02-05T10:00:00Z")},
		},
	}

	pc := BuildPeriodComparison([]model.Session{noMessages, mixed}, curStart, curEnd, prevStart, curStart)

	if pc.Current.Requests != 1 {
		t.Errorf("Current.Requests = %d, want 1 (only the mixed session counts)", pc.Current.Requests)
	}
	if pc.Previous.Requests != 1 {
		t.Errorf("Previous.Requests = %d, want 1", pc.Previous.Requests)
	}
	// The tokens-only session contributes nothing: no requests, no cost.
	wantFloat(t, "Current.CreditCost", pc.Current.CreditCost, 3.0)
	// A session spanning both periods has its full cost attributed to each
	// period it touches (deliberate simplification in computeBucketMetrics).
	wantFloat(t, "Previous.CreditCost", pc.Previous.CreditCost, 3.0)
}

func TestBuildPeriodComparison_EmptyInputHasZeroDeltas(t *testing.T) {
	pc := BuildPeriodComparison(nil,
		at("2026-02-01T00:00:00Z"), at("2026-03-01T00:00:00Z"),
		at("2026-01-01T00:00:00Z"), at("2026-02-01T00:00:00Z"))
	if pc == nil {
		t.Fatal("BuildPeriodComparison(nil) returned nil")
	}
	if pc.Current.Requests != 0 || pc.Previous.Requests != 0 {
		t.Fatalf("requests = %d/%d, want 0/0", pc.Current.Requests, pc.Previous.Requests)
	}
	for _, name := range metricNames {
		got, ok := pc.DeltaPct[name]
		if !ok {
			t.Fatalf("DeltaPct is missing metric %q", name)
		}
		wantFloat(t, "DeltaPct["+name+"]", got, 0)
	}
}

func TestBuildPeriodComparison_ZeroUsageHasFiniteDeltas(t *testing.T) {
	curStart := at("2026-02-01T00:00:00Z")
	curEnd := at("2026-03-01T00:00:00Z")
	prevStart := at("2026-01-01T00:00:00Z")

	paid := sess("paid", 4.0, asst(at("2026-02-10T10:00:00Z"), 1000, 500, 100, 50))
	freeCur := freeSess("free-cur", asst(at("2026-02-10T10:00:00Z"), 0, 0, 0, 0))
	freePrev := freeSess("free-prev", asst(at("2026-01-10T10:00:00Z"), 0, 0, 0, 0))
	// Every token metric is non-zero in the previous period, so every metric
	// has a non-zero denominator and must come out exactly -100.
	jan := sess("jan", 2.0, asst(at("2026-01-10T10:00:00Z"), 100, 100, 50, 10))

	t.Run("previous period empty", func(t *testing.T) {
		pc := BuildPeriodComparison([]model.Session{paid}, curStart, curEnd, prevStart, curStart)
		// Every metric grew from nothing: 100%, never NaN or +Inf.
		for _, name := range metricNames {
			got := pc.DeltaPct[name]
			wantFinite(t, "DeltaPct["+name+"]", got)
			wantFloat(t, "DeltaPct["+name+"]", got, 100)
		}
	})

	t.Run("current period empty", func(t *testing.T) {
		pc := BuildPeriodComparison([]model.Session{jan}, curStart, curEnd, prevStart, curStart)
		// Everything dropped to nothing: -100%, never NaN or -Inf.
		for _, name := range metricNames {
			got := pc.DeltaPct[name]
			wantFinite(t, "DeltaPct["+name+"]", got)
			wantFloat(t, "DeltaPct["+name+"]", got, -100)
		}
	})

	t.Run("both periods free (zero cost, equal requests)", func(t *testing.T) {
		// prev.Cost == 0 && cur.Cost == 0 is the case that a naive
		// (cur-prev)/prev*100 would turn into NaN, and 0/0 for the average
		// into NaN as well.
		pc := BuildPeriodComparison([]model.Session{freeCur, freePrev}, curStart, curEnd, prevStart, curStart)
		if pc.Current.Requests != 1 || pc.Previous.Requests != 1 {
			t.Fatalf("requests = %d/%d, want 1/1", pc.Current.Requests, pc.Previous.Requests)
		}
		wantFloat(t, "Current.CreditCost", pc.Current.CreditCost, 0)
		for _, name := range metricNames {
			got := pc.DeltaPct[name]
			wantFinite(t, "DeltaPct["+name+"]", got)
			wantFloat(t, "DeltaPct["+name+"]", got, 0)
		}
	})
}

func TestPctDelta_HandlesZeroPrevious(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur float64
		want      float64
	}{
		{"zero to zero", 0, 0, 0},
		{"zero to positive", 0, 5, 100},
		{"positive to zero", 4, 0, -100},
		{"halved", 2, 1, -50},
		{"grown", 4, 5, 25},
		{"unchanged", 3, 3, 0},
		{"fractional", 0.5, 0.25, -50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantFloat(t, "pctDelta", pctDelta(c.prev, c.cur), c.want)
		})
	}
}

func TestBuildMonthOverMonth_UsesCurrentCalendarMonth(t *testing.T) {
	now := time.Now()
	// The session's message timestamp is "now", which is always inside the
	// current calendar month, so the comparison must attribute it to Current.
	s := sess("now", 7.5, asst(now, 100, 50, 0, 0))

	pc := BuildMonthOverMonth([]model.Session{s})
	if pc == nil {
		t.Fatal("BuildMonthOverMonth returned nil")
	}
	if pc.Current.Requests != 1 {
		t.Errorf("Current.Requests = %d, want 1", pc.Current.Requests)
	}
	wantFloat(t, "Current.CreditCost", pc.Current.CreditCost, 7.5)
	wantFloat(t, "DeltaPct[Cost]", pc.DeltaPct["Cost"], 100)

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local).Format("2006-01-02")
	if !strings.HasPrefix(pc.Current.Label, monthStart) {
		t.Errorf("Current.Label = %q, want it to start at %q", pc.Current.Label, monthStart)
	}
	for _, name := range metricNames {
		wantFinite(t, "DeltaPct["+name+"]", pc.DeltaPct[name])
	}

	if out := RenderMonthOverMonth(pc); !strings.Contains(out, "Month-over-month") {
		t.Errorf("RenderMonthOverMonth output missing header: %q", out)
	}

	empty := BuildMonthOverMonth(nil)
	if empty == nil {
		t.Fatal("BuildMonthOverMonth(nil) returned nil")
	}
	for _, name := range metricNames {
		wantFloat(t, "empty DeltaPct["+name+"]", empty.DeltaPct[name], 0)
	}
}

// ---- trend points / buckets ----

func TestBuildTrendPoints_KeepsLastNDaysAndReformatsLabels(t *testing.T) {
	rows := []report.TimeRow{
		{Label: "2026-01-01", Cost: 1, InputTok: 10, OutputTok: 20, CacheRead: 30, CacheWrite: 40},
		{Label: "2026-01-02", Cost: 2, InputTok: 1, OutputTok: 2, CacheRead: 3, CacheWrite: 4},
		{Label: "2026-01-03", Cost: 3, InputTok: 5, OutputTok: 5, CacheRead: 5, CacheWrite: 5},
		{Label: "2026-W01", Cost: 4, InputTok: 7},
	}

	pts := BuildTrendPoints(rows, 3)
	if len(pts) != 3 {
		t.Fatalf("BuildTrendPoints(rows, 3) returned %d points, want 3", len(pts))
	}
	// The last 3 rows survive, in order, labels reformatted to MM-DD.
	if pts[0].Label != "01-02" || pts[1].Label != "01-03" || pts[2].Label != "2026-W01" {
		t.Errorf("labels = %q/%q/%q, want 01-02/01-03/2026-W01", pts[0].Label, pts[1].Label, pts[2].Label)
	}
	wantFloat(t, "pts[0].Cost", pts[0].Cost, 2)
	wantFloat(t, "pts[0].Tokens", float64(pts[0].Tokens), 10) // 1+2+3+4
	wantFloat(t, "pts[2].Tokens", float64(pts[2].Tokens), 7)
	// A label that is not a plain date is passed through untouched.
	if pts[2].Label != "2026-W01" {
		t.Errorf("non-date label was rewritten: %q", pts[2].Label)
	}

	if got := BuildTrendPoints(rows, 0); len(got) != 4 {
		t.Errorf("days=0 returned %d points, want all 4", len(got))
	}
	if got := BuildTrendPoints(rows, 99); len(got) != 4 {
		t.Errorf("days=99 returned %d points, want all 4", len(got))
	}
	if got := BuildTrendPoints(nil, 7); len(got) != 0 {
		t.Errorf("BuildTrendPoints(nil, 7) returned %d points, want 0", len(got))
	}
}

func TestBuild24HourBuckets_RelativeToNow(t *testing.T) {
	// Build24HourBuckets reads time.Now() internally, so the fixture is
	// relative to the clock and offsets sit mid-bucket (never on a boundary).
	now := time.Now()
	ss := []model.Session{{
		ID: "recent",
		Messages: []model.Message{
			{Role: "assistant", CreatedAt: now.Add(-30 * time.Minute)},              // idx 23
			{Role: "assistant", CreatedAt: now.Add(-90 * time.Minute)},              // idx 22
			{Role: "assistant", CreatedAt: now.Add(-23*time.Hour - 30*time.Minute)}, // idx 0
			{Role: "assistant", CreatedAt: now.Add(-25 * time.Hour)},                // too old
			{Role: "assistant", CreatedAt: now.Add(1 * time.Minute)},                // in the future
			{Role: "user", CreatedAt: now.Add(-30 * time.Minute)},                   // not an assistant turn
		},
	}}

	buckets := Build24HourBuckets(ss)
	if buckets[23] != 1 {
		t.Errorf("buckets[23] = %d, want 1 (message 30m ago)", buckets[23])
	}
	if buckets[22] != 1 {
		t.Errorf("buckets[22] = %d, want 1 (message 90m ago)", buckets[22])
	}
	if buckets[0] != 1 {
		t.Errorf("buckets[0] = %d, want 1 (message 23.5h ago)", buckets[0])
	}
	total := 0
	for _, n := range buckets {
		total += n
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (future, >24h-old and non-assistant messages must be dropped)", total)
	}

	empty := Build24HourBuckets(nil)
	for i, n := range empty {
		if n != 0 {
			t.Fatalf("Build24HourBuckets(nil)[%d] = %d, want 0", i, n)
		}
	}
}

func TestBuildHeatmap_BucketsByWeekdayAndHour(t *testing.T) {
	// 2026-01-04 is a Sunday, 2026-01-05 a Monday.
	mon := sess("mon", 1.0,
		asst(at("2026-01-05T09:30:00Z"), 1, 1, 0, 0),
		asst(at("2026-01-05T09:45:00Z"), 1, 1, 0, 0),
		model.Message{Role: "user", CreatedAt: at("2026-01-05T09:50:00Z")},
	)
	sun := sess("sun", 1.0, asst(at("2026-01-04T23:15:00Z"), 1, 1, 0, 0))

	cells := BuildHeatmap([]model.Session{mon, sun})
	if len(cells) != 7*24 {
		t.Fatalf("BuildHeatmap returned %d cells, want 168", len(cells))
	}
	idx := func(wd, hr int) model.HeatmapCell { return cells[wd*24+hr] }
	if got := idx(1, 9); got.Count != 2 || got.Weekday != 1 || got.Hour != 9 {
		t.Errorf("cell(Mon 09) = %+v, want Weekday 1 Hour 9 Count 2", got)
	}
	if got := idx(0, 23); got.Count != 1 {
		t.Errorf("cell(Sun 23) = %+v, want Count 1", got)
	}
	if got := idx(2, 9); got.Count != 0 {
		t.Errorf("cell(Tue 09) = %+v, want Count 0", got)
	}
	for i, c := range cells {
		if c.Weekday != i/24 || c.Hour != i%24 {
			t.Fatalf("cell %d is %+v, want Weekday %d Hour %d", i, c, i/24, i%24)
		}
	}

	empty := BuildHeatmap(nil)
	if len(empty) != 168 {
		t.Fatalf("BuildHeatmap(nil) returned %d cells, want 168 zero cells", len(empty))
	}
	for _, c := range empty {
		if c.Count != 0 {
			t.Fatalf("BuildHeatmap(nil) has non-zero cell: %+v", c)
		}
	}

	if out := RenderHeatmap(cells); !strings.Contains(out, "Peak: 2 msgs/hr") {
		t.Errorf("RenderHeatmap output missing peak: %q", out)
	}
}

func TestBuildContributionCalendar_CountsFixedDays(t *testing.T) {
	ss := []model.Session{
		sess("two-on-mar-5", 0,
			asst(at("2026-03-05T12:00:00Z"), 1, 1, 0, 0),
			asst(at("2026-03-05T13:00:00Z"), 1, 1, 0, 0),
		),
		sess("one-on-jul-4", 0, asst(at("2026-07-04T12:00:00Z"), 1, 1, 0, 0)),
	}

	days := BuildContributionCalendar(ss, 2026)
	if len(days) != 365 {
		t.Fatalf("BuildContributionCalendar(2026) returned %d days, want 365", len(days))
	}
	byDate := map[string]model.ContributionDay{}
	for _, d := range days {
		byDate[d.Date.Format("2006-01-02")] = d
	}
	mar5, ok := byDate["2026-03-05"]
	if !ok {
		t.Fatal("no entry for 2026-03-05")
	}
	if mar5.Count != 2 {
		t.Errorf("2026-03-05 count = %d, want 2", mar5.Count)
	}
	if mar5.Level != 4 {
		t.Errorf("2026-03-05 level = %d, want 4 (the day with max activity)", mar5.Level)
	}
	jul4 := byDate["2026-07-04"]
	if jul4.Count != 1 || jul4.Level != 3 {
		t.Errorf("2026-07-04 = count %d level %d, want 1/3 (half of the max)", jul4.Count, jul4.Level)
	}
	quiet := byDate["2026-06-01"]
	if quiet.Count != 0 || quiet.Level != 0 {
		t.Errorf("2026-06-01 = count %d level %d, want 0/0", quiet.Count, quiet.Level)
	}
	// Unpriced fixture model: no estimated cost, and definitely no NaN.
	for _, d := range days {
		wantFinite(t, "day cost "+d.Date.Format("2006-01-02"), d.Cost)
	}
	if mar5.Cost != 0 {
		t.Errorf("2026-03-05 cost = %v, want 0 for an unpriced model", mar5.Cost)
	}

	if got := BuildContributionCalendar(nil, 2026); len(got) != 365 {
		t.Errorf("BuildContributionCalendar(nil, 2026) returned %d days, want 365", len(got))
	}
}

// ---- renderers ----

func TestRenderers_EmptyInputPlaceholder(t *testing.T) {
	cases := []struct {
		name string
		got  string
	}{
		{"RenderPeriodComparison(nil)", RenderPeriodComparison(nil)},
		{"RenderMonthOverMonth(nil)", RenderMonthOverMonth(nil)},
		{"RenderHeatmap(nil)", RenderHeatmap(nil)},
		{"RenderContributionCalendar(nil, 2026)", RenderContributionCalendar(nil, 2026)},
		{"RenderASCIIChart(nil, 10)", RenderASCIIChart(nil, 10)},
		{"RenderDailyCostChart(nil, 7, 10)", RenderDailyCostChart(nil, 7, 10)},
		{"RenderCumulativeCost(nil, 7, 10)", RenderCumulativeCost(nil, 7, 10)},
		{"RenderStackedArea(nil, 7, 10)", RenderStackedArea(nil, 7, 10)},
	}
	for _, c := range cases {
		if c.got != "(no data)" {
			t.Errorf("%s = %q, want \"(no data)\"", c.name, c.got)
		}
	}

	// Built-but-empty inputs must render instead of collapsing to a placeholder.
	if out := RenderHeatmap(BuildHeatmap(nil)); !strings.Contains(out, "Peak: 0 msgs/hr") {
		t.Errorf("RenderHeatmap(empty grid) missing zero peak: %q", out)
	}
	if out := Render24HourChart(nil, 0); !strings.Contains(out, "Activity over last 24 hours") {
		t.Errorf("Render24HourChart(nil) missing header: %q", out)
	}
	if out := RenderContributionCalendar(BuildContributionCalendar(nil, 2026), 2026); !strings.Contains(out, "0 contributions") {
		t.Errorf("RenderContributionCalendar(empty year) missing zero count: %q", out)
	}
}

// ---- sparklines / delta banner ----

func TestHumanizeAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{-time.Minute, "just now"},
		{5 * time.Minute, "5m ago"},
		{59 * time.Minute, "59m ago"},
		{90 * time.Minute, "1h ago"},
		{25 * time.Hour, "1d ago"},
		{50 * time.Hour, "2d ago"},
	}
	for _, c := range cases {
		if got := humanizeAge(c.d); got != c.want {
			t.Errorf("humanizeAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestCurrentTotals_SumsCostAndTokens(t *testing.T) {
	ss := []model.Session{
		{ID: "a", CreditCost: 1.5, InputTokens: 100, OutputTokens: 200, CacheRead: 30, CacheWrite: 4},
		{ID: "b", CreditCost: 0.25, InputTokens: 1, OutputTokens: 2, CacheRead: 3, CacheWrite: 4},
		// No authoritative cost and an unpriced model: tokens still count,
		// cost stays 0.
		{ID: "c", Model: unpricedModel, InputTokens: 10},
	}
	cost, tokens := currentTotals(ss)
	wantFloat(t, "cost", cost, 1.75)
	if tokens != 354 {
		t.Errorf("tokens = %d, want 354", tokens)
	}
	if c, tk := currentTotals(nil); c != 0 || tk != 0 {
		t.Errorf("currentTotals(nil) = %v/%d, want 0/0", c, tk)
	}
}

func TestRenderDeltaBanner_FirstCheckThenSignedDelta(t *testing.T) {
	// A test touching the persisted snapshot path MUST redirect the config
	// directory first, or it writes into the developer's real ~/.devinmonitor.
	t.Setenv("DEVINMONITOR_CONFIG_DIR", t.TempDir())

	snap := snapshotPath()
	if snap != filepath.Join(filepath.Dir(config.Path()), "state", "trends-delta.json") {
		t.Fatalf("snapshotPath() = %q, want it under the config dir", snap)
	}
	if !withinDir(os.TempDir(), filepath.Dir(snap)) {
		t.Fatalf("snapshot path %q is not under the temp dir — refusing to write into the real config dir", snap)
	}
	if err := os.Remove(snap); err != nil && !os.IsNotExist(err) {
		t.Fatalf("cannot reset snapshot: %v", err)
	}

	// 1st run: no prior snapshot, zero usage. Cost 0 renders as "free".
	first := RenderDeltaBanner(nil)
	if !strings.Contains(first, "first check (no prior snapshot)") {
		t.Errorf("first banner = %q, want the first-check label", first)
	}
	if !strings.Contains(first, "free cost") || !strings.Contains(first, "0 tokens") {
		t.Errorf("first banner = %q, want zero cost/token deltas", first)
	}

	// The snapshot must have been persisted for the next run.
	data, err := os.ReadFile(snap)
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v (%q)", err, data)
	}
	if saved.Cost != 0 || saved.Tokens != 0 || saved.When == "" {
		t.Errorf("saved snapshot = %+v, want zero totals and a timestamp", saved)
	}
	if _, err := time.Parse(time.RFC3339, saved.When); err != nil {
		t.Errorf("saved When = %q, not RFC3339: %v", saved.When, err)
	}

	// 2nd run: usage appeared, so the delta is signed and positive.
	ss := []model.Session{{ID: "s1", Model: unpricedModel, CreditCost: 1.25, InputTokens: 1000, OutputTokens: 500}}
	second := RenderDeltaBanner(ss)
	if !strings.Contains(second, "since last check") {
		t.Errorf("second banner = %q, want the since-last-check label", second)
	}
	if !strings.Contains(second, "+$1.25") || !strings.Contains(second, "+1.5k tokens") {
		t.Errorf("second banner = %q, want +$1.25 / +1.5k tokens", second)
	}

	// 3rd run: totals dropped back to zero, so the deltas are negative.
	third := RenderDeltaBanner(nil)
	if !strings.Contains(third, "-$1.25") || !strings.Contains(third, "-1.5k tokens") {
		t.Errorf("third banner = %q, want -$1.25 / -1.5k tokens", third)
	}
}
