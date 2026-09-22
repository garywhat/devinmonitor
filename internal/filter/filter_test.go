// Characterisation tests for the filter package.
//
// All fixtures are in-memory []model.Session values with fixed UTC timestamps
// and explicit CreditCost, so nothing here opens a database, reads the pricing
// table or depends on the wall clock.
package filter

import (
	"sort"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ---- fixtures ----

func at(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("filter_test: bad fixture time " + s + ": " + err.Error())
	}
	return v
}

// fixture returns a fresh session set:
//
//	alpha  normal mode, /work/alpha, created 2026-01-10 10:00Z, cost 1.0, 2h long
//	beta   plan mode,   /work/beta,  created 2026-02-15 09:00Z, cost 2.0, 4h long
//	hidden hidden flag, otherwise identical to alpha (must never survive Apply)
func fixture() []model.Session {
	return []model.Session{
		{
			ID: "alpha", WorkingDir: "/work/alpha", AgentMode: "normal",
			CreatedAt: at("2026-01-10T10:00:00Z"), LastActivityAt: at("2026-01-10T12:00:00Z"),
			CreditCost: 1.0, InputTokens: 100, OutputTokens: 200,
			Messages: []model.Message{
				{Role: "user", NumTokensPreceding: 400},
				{Role: "assistant", NumTokensPreceding: 500},
			},
		},
		{
			ID: "beta", WorkingDir: "/work/beta", AgentMode: "plan",
			CreatedAt: at("2026-02-15T09:00:00Z"), LastActivityAt: at("2026-02-15T13:00:00Z"),
			CreditCost: 2.0, InputTokens: 400, OutputTokens: 100, CacheRead: 300, CacheWrite: 50,
			Messages: []model.Message{
				{Role: "assistant", NumTokensPreceding: 900},
				{Role: "tool", NumTokensPreceding: 5000},
			},
		},
		{
			ID: "hidden", Hidden: true, WorkingDir: "/work/alpha", AgentMode: "normal",
			CreatedAt: at("2026-01-10T10:00:00Z"), LastActivityAt: at("2026-01-10T12:00:00Z"),
			CreditCost: 99.0,
		},
	}
}

func ids(ss []model.Session) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

func wantIDs(t *testing.T, got []model.Session, want ...string) {
	t.Helper()
	gotIDs := ids(got)
	if len(gotIDs) != len(want) {
		t.Fatalf("got %v, want %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("got %v, want %v (order matters: Apply must preserve input order)", gotIDs, want)
		}
	}
}

// visibleFixture is fixture() without the hidden session, for tests that
// exercise the raw sort/exclude helpers (which never look at Hidden).
func visibleFixture() []model.Session {
	var out []model.Session
	for _, s := range fixture() {
		if !s.Hidden {
			out = append(out, s)
		}
	}
	return out
}

// ---- Apply ----

func TestApply_EachCriterionSelectsExpectedSubset(t *testing.T) {
	cases := []struct {
		name string
		opts model.FilterOptions
		want []string
	}{
		{"no criteria keeps everything visible", model.FilterOptions{}, []string{"alpha", "beta"}},
		{"mode normal", model.FilterOptions{Mode: "normal"}, []string{"alpha"}},
		{"mode plan", model.FilterOptions{Mode: "plan"}, []string{"beta"}},
		{"mode that matches nothing", model.FilterOptions{Mode: "bypass"}, []string{}},
		{"project substring", model.FilterOptions{Project: "alpha"}, []string{"alpha"}},
		{"project substring shared by both", model.FilterOptions{Project: "work"}, []string{"alpha", "beta"}},
		{"from date inclusive of that instant", model.FilterOptions{FromDate: at("2026-02-01T00:00:00Z")}, []string{"beta"}},
		{"to date inclusive of that instant", model.FilterOptions{ToDate: at("2026-01-10T10:00:00Z")}, []string{"alpha"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantIDs(t, Apply(fixture(), c.opts), c.want...)
		})
	}
}

func TestApply_ToDateIsAnExactTimestampNotAnEndOfDay(t *testing.T) {
	// ToDate is compared with After(), so it bounds the exact timestamp, not
	// the calendar day: a midnight ToDate (what ParseDate returns) drops every
	// session later that same day.
	midnight := Apply(fixture(), model.FilterOptions{ToDate: at("2026-01-10T00:00:00Z")})
	wantIDs(t, midnight) // alpha at 10:00 on the 10th is excluded

	endOfDay := Apply(fixture(), model.FilterOptions{ToDate: at("2026-01-10T23:59:59Z")})
	wantIDs(t, endOfDay, "alpha")

	from := Apply(fixture(), model.FilterOptions{FromDate: at("2026-01-10T00:00:00Z")})
	wantIDs(t, from, "alpha", "beta")
}

func TestApply_IgnoresHiddenSessionsAndModelSearchText(t *testing.T) {
	// Hidden sessions are dropped by every combination...
	wantIDs(t, Apply(fixture(), model.FilterOptions{Project: "alpha"}), "alpha")
	// ...and Apply does not implement Model/SearchText matching at all (those
	// live in reader.FilteredSessions), so a bogus model must not filter.
	got := Apply(fixture(), model.FilterOptions{Model: "no-such-model", SearchText: "no-such-text"})
	wantIDs(t, got, "alpha", "beta")
}

func TestApply_CombinesCriteriaWithAND(t *testing.T) {
	ss := fixture()
	opts := model.FilterOptions{
		Mode:     "plan",
		Project:  "work",
		FromDate: at("2026-02-01T00:00:00Z"),
		ToDate:   at("2026-02-28T23:59:59Z"),
	}
	wantIDs(t, Apply(ss, opts), "beta")

	// Flip one criterion so the intersection is empty.
	opts.Mode = "normal"
	wantIDs(t, Apply(ss, opts))
}

func TestApply_NoMatchReturnsEmptySafeResult(t *testing.T) {
	out := Apply(fixture(), model.FilterOptions{Project: "does-not-exist"})
	if len(out) != 0 {
		t.Fatalf("got %v, want no matches", ids(out))
	}
	// Apply returns a nil slice (not an empty non-nil one) when nothing
	// matches; pin that so a change to either shape is noticed.
	if out != nil {
		t.Logf("Apply now returns a non-nil empty slice (%#v) instead of nil", out)
	}
	// Either shape must stay safe to range over and append to.
	total := 0
	for range out {
		total++
	}
	if total != 0 {
		t.Fatalf("ranging over the empty result yielded %d entries", total)
	}
	if appended := append(out, model.Session{ID: "appended"}); len(appended) != 1 {
		t.Fatalf("append to empty result gave len %d, want 1", len(appended))
	}
}

// ---- ExcludeProjects ----

func TestExcludeProjects(t *testing.T) {
	t.Run("empty pattern returns the input slice unchanged (aliased)", func(t *testing.T) {
		ss := fixture()
		out := ExcludeProjects(ss, "")
		if len(out) != len(ss) {
			t.Fatalf("got %v, want the input back", ids(out))
		}
		if len(out) > 0 && &out[0] != &ss[0] {
			t.Fatal("empty exclude must return the same slice, not a copy")
		}
	})

	cases := []struct {
		name    string
		exclude string
		want    []string
	}{
		{"single pattern", "beta", []string{"alpha"}},
		{"comma separated patterns", "alpha,beta", []string{}},
		{"whitespace around patterns is trimmed", " alpha , beta ", []string{}},
		{"empty segments are skipped", "beta,, ", []string{"alpha"}},
		{"no match keeps everything", "nothing-here", []string{"alpha", "beta"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantIDs(t, ExcludeProjects(visibleFixture(), c.exclude), c.want...)
		})
	}

	// ExcludeProjects does not look at Hidden, unlike Apply: the hidden
	// session survives even though it shares alpha's working directory.
	out := ExcludeProjects(fixture(), "beta")
	wantIDs(t, out, "alpha", "hidden")
}

// ---- SortBy ----

func TestSortBy_KeysProduceDeterministicOrder(t *testing.T) {
	// alpha: cost 1.0, tokens 600, max context 500 (max over all messages),
	//        duration 2h, last activity 2026-01-10 12:00Z
	// beta:  cost 2.0, tokens 850, max context 5000 (a tool message counts),
	//        duration 4h, last activity 2026-02-15 13:00Z
	cases := []struct {
		key  string
		desc bool
		want []string
	}{
		{"cost", false, []string{"alpha", "beta"}},
		{"cost", true, []string{"beta", "alpha"}},
		{"tokens", false, []string{"alpha", "beta"}},
		{"tokens", true, []string{"beta", "alpha"}},
		{"context", false, []string{"alpha", "beta"}},
		{"context", true, []string{"beta", "alpha"}},
		{"duration", false, []string{"alpha", "beta"}},
		{"duration", true, []string{"beta", "alpha"}},
		{"recent", false, []string{"alpha", "beta"}},
		{"recent", true, []string{"beta", "alpha"}},
	}
	for _, c := range cases {
		name := c.key
		if c.desc {
			name += " desc"
		}
		t.Run(name, func(t *testing.T) {
			ss := visibleFixture()
			SortBy(ss, c.key, c.desc)
			wantIDs(t, ss, c.want...)
		})
	}
}

func TestSortBy_UnknownKeyFallsBackToRecency(t *testing.T) {
	for _, key := range []string{"", "bogus", "ReCent"} {
		ss := visibleFixture()
		SortBy(ss, key, false)
		wantIDs(t, ss, "alpha", "beta")
		ss = visibleFixture()
		SortBy(ss, key, true)
		wantIDs(t, ss, "beta", "alpha")
	}
}

func TestSortBy_TiesKeepInputOrder(t *testing.T) {
	// Two sessions tie on every key under test; sort.SliceStable keeps the
	// input order for ties in *both* directions.
	tie := func() []model.Session {
		return []model.Session{
			{ID: "first", CreditCost: 5.0, InputTokens: 500,
				CreatedAt: at("2026-03-01T00:00:00Z"), LastActivityAt: at("2026-03-01T05:00:00Z")},
			{ID: "second", CreditCost: 5.0, InputTokens: 500,
				CreatedAt: at("2026-03-01T00:00:00Z"), LastActivityAt: at("2026-03-01T05:00:00Z")},
			{ID: "third", CreditCost: 1.0, InputTokens: 100,
				CreatedAt: at("2026-03-02T00:00:00Z"), LastActivityAt: at("2026-03-02T05:00:00Z")},
		}
	}
	ss := tie()
	SortBy(ss, "cost", false)
	wantIDs(t, ss, "third", "first", "second")

	ss = tie()
	SortBy(ss, "cost", true)
	wantIDs(t, ss, "first", "second", "third")

	ss = tie()
	SortBy(ss, "tokens", false)
	wantIDs(t, ss, "third", "first", "second")

	ss = tie()
	SortBy(ss, "recent", true)
	wantIDs(t, ss, "third", "first", "second")

	// A stable sort also means the caller's slice is reordered in place.
	ss = tie()
	SortBy(ss, "cost", false)
	if ss[0].ID != "third" {
		t.Fatalf("SortBy must sort the caller's slice in place; got %v", ids(ss))
	}
}

// ---- ParseDate ----

func TestParseDate(t *testing.T) {
	if got, err := ParseDate(""); err != nil || !got.IsZero() {
		t.Fatalf("ParseDate(\"\") = %v, %v; want zero time, nil error", got, err)
	}
	got, err := ParseDate("2026-02-15")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	want := time.Date(2026, 2, 15, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("ParseDate = %v, want local midnight %v", got, want)
	}
	if _, err := ParseDate("15/02/2026"); err == nil {
		t.Fatal("ParseDate accepted a non-ISO date, want an error")
	}
	if got, err := ParseDate("not-a-date"); err == nil || !got.IsZero() {
		t.Fatalf("ParseDate(bad) = %v, %v; want zero time and an error", got, err)
	}
}

// ---- ProjectSearch ----

func TestProjectSearch(t *testing.T) {
	rows := []report.ProjectRow{
		{Name: "DevinMonitor", Requests: 9},
		{Name: "alpha", Requests: 3},
		{Name: "ALPHAbet", Requests: 1},
	}
	t.Run("empty query returns the input slice unchanged", func(t *testing.T) {
		out := ProjectSearch(rows, "")
		if len(out) != len(rows) || &out[0] != &rows[0] {
			t.Fatal("empty query must return the same slice")
		}
	})
	t.Run("case insensitive substring", func(t *testing.T) {
		out := ProjectSearch(rows, "alpha")
		if len(out) != 2 || out[0].Name != "alpha" || out[1].Name != "ALPHAbet" {
			t.Fatalf("got %v, want alpha and ALPHAbet", out)
		}
	})
	t.Run("no match returns empty", func(t *testing.T) {
		if out := ProjectSearch(rows, "zzz"); len(out) != 0 {
			t.Fatalf("got %v, want no matches", out)
		}
	})
	t.Run("input is not mutated", func(t *testing.T) {
		before := make([]string, len(rows))
		for i, r := range rows {
			before[i] = r.Name
		}
		_ = ProjectSearch(rows, "alpha")
		for i, r := range rows {
			if r.Name != before[i] {
				t.Fatalf("input row %d changed: %q -> %q", i, before[i], r.Name)
			}
		}
	})
}

// ---- MergeProjects ----

func TestMergeProjects(t *testing.T) {
	rows := func() []report.ProjectRow {
		return []report.ProjectRow{
			{Name: "alpha", Sessions: 1, Requests: 3, InputTok: 10, OutputTok: 20,
				CacheRead: 30, CacheWrite: 40, Cost: 0.5, Models: []string{"m1"}},
			{Name: "beta", Sessions: 2, Requests: 5, InputTok: 100, OutputTok: 200,
				CacheRead: 300, CacheWrite: 400, Cost: 1.5, Models: []string{"m2", "m1"}},
			{Name: "gamma", Sessions: 1, Requests: 9, InputTok: 1, OutputTok: 1, Cost: 0.1},
		}
	}

	t.Run("fewer than two names returns the input unchanged", func(t *testing.T) {
		rs := rows()
		if out := MergeProjects(rs, "alpha"); len(out) != 3 || &out[0] != &rs[0] {
			t.Fatal("a single name must be a no-op")
		}
		if out := MergeProjects(rs, ""); len(out) != 3 {
			t.Fatal("an empty name list must be a no-op")
		}
	})

	t.Run("unknown names are a no-op", func(t *testing.T) {
		if out := MergeProjects(rows(), "nope,other"); len(out) != 3 {
			t.Fatalf("got %d rows, want the 3 input rows", len(out))
		}
	})

	t.Run("sums stats and keeps the first name as canonical", func(t *testing.T) {
		out := MergeProjects(rows(), "alpha,beta")
		if len(out) != 2 {
			t.Fatalf("got %d rows, want 2 (alpha+beta merged, gamma)", len(out))
		}
		// gamma has more requests, so it sorts first.
		merged := out[1]
		if merged.Name != "alpha" {
			t.Errorf("merged name = %q, want the first name %q", merged.Name, "alpha")
		}
		if merged.Sessions != 3 || merged.Requests != 8 {
			t.Errorf("merged sessions/requests = %d/%d, want 3/8", merged.Sessions, merged.Requests)
		}
		if merged.InputTok != 110 || merged.OutputTok != 220 || merged.CacheRead != 330 || merged.CacheWrite != 440 {
			t.Errorf("merged tokens = %d/%d/%d/%d, want 110/220/330/440",
				merged.InputTok, merged.OutputTok, merged.CacheRead, merged.CacheWrite)
		}
		if merged.Cost != 2.0 {
			t.Errorf("merged cost = %v, want 2.0", merged.Cost)
		}
		models := append([]string(nil), merged.Models...)
		sort.Strings(models)
		if len(models) != 2 || models[0] != "m1" || models[1] != "m2" {
			t.Errorf("merged models = %v, want [m1 m2] (union, no duplicates)", merged.Models)
		}
		if out[0].Name != "gamma" {
			t.Errorf("out[0] = %q, want gamma (sorted by requests desc)", out[0].Name)
		}
	})

	t.Run("name matching is exact and case sensitive", func(t *testing.T) {
		out := MergeProjects(rows(), "ALPHA,beta")
		if len(out) != 3 {
			t.Fatalf("got %d rows, want 3 (ALPHA does not match alpha)", len(out))
		}
	})
}
