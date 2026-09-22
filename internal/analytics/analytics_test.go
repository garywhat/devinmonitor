package analytics

import (
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// fixedNow is the single wall-clock reference used throughout these tests.
// A Wednesday mid-month keeps day/week/month boundaries unambiguous.
var fixedNow = time.Date(2026, time.March, 18, 12, 0, 0, 0, time.UTC)

// ---- small helpers ----

func roughlyEqual(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

// allFinite fails the test for any NaN/Inf in the named values. Every ratio
// these builders compute divides by a count that can legitimately be zero, so
// this is the cheapest regression net for the classic 0/0 bug.
func allFinite(t *testing.T, vals map[string]float64) {
	t.Helper()
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if v := vals[n]; math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("%s = %v, want finite", n, v)
		}
	}
}

func tcArgsFile(path string) string { return `{"file_path":"` + path + `"}` }
func tcArgsCmd(cmd string) string   { return `{"command":"` + cmd + `"}` }

func tcEdit(id, path string) model.ToolCall {
	return model.ToolCall{ID: id, Name: "edit", Arguments: tcArgsFile(path)}
}
func tcRead(id, path string) model.ToolCall {
	return model.ToolCall{ID: id, Name: "read", Arguments: tcArgsFile(path)}
}
func tcExec(id, cmd string) model.ToolCall {
	return model.ToolCall{ID: id, Name: "exec", Arguments: tcArgsCmd(cmd)}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// fixtureSessions is the shared "several sessions" fixture: one coding-ish
// session with priced cache reads and one cheap exploration session.
func fixtureSessions() []model.Session {
	return []model.Session{
		{
			ID: "s1", Title: "fix the build", Model: "claude-sonnet-4-5", AgentMode: "normal",
			CreatedAt:      fixedNow.Add(-2 * time.Hour),
			LastActivityAt: fixedNow.Add(-time.Hour),
			CreditCost:     3.5,
			InputTokens:    10_000,
			OutputTokens:   2_000,
			CacheRead:      30_000,
			CacheWrite:     1_000,
			AssistantCount: 4,
			ToolCalls:      map[string]int{"edit": 3, "read": 2, "exec": 1},
			Messages: []model.Message{
				{NodeID: 1, Role: "user", Content: "fix the failing test", CreatedAt: fixedNow.Add(-2 * time.Hour)},
				{NodeID: 2, Role: "assistant", NumTokensPreceding: 100,
					GenerationModel: "claude-sonnet-4-5",
					Metrics:         &model.Metrics{InputTokens: 5000, OutputTokens: 1000, CacheReadTokens: 10000, TTFTMs: 200, TokensPerSec: 50},
					ToolCalls:       []model.ToolCall{tcRead("t1", "/repo/a.go")}},
				{NodeID: 3, Role: "tool", NumTokensPreceding: 400},
				{NodeID: 4, Role: "assistant", NumTokensPreceding: 500,
					GenerationModel: "claude-sonnet-4-5",
					Metrics:         &model.Metrics{InputTokens: 5000, OutputTokens: 1000, CacheReadTokens: 10000, TTFTMs: 400, TokensPerSec: 25},
					ToolCalls:       []model.ToolCall{tcEdit("t2", "/repo/a.go"), tcRead("t3", "/repo/a.go")}},
			},
		},
		{
			ID: "s2", Title: "explore", Model: "gemini-2.5-flash", AgentMode: "plan",
			CreatedAt:      fixedNow.Add(-90 * time.Minute),
			LastActivityAt: fixedNow.Add(-30 * time.Minute),
			ACUCost:        1.25,
			InputTokens:    2_000,
			OutputTokens:   500,
			AssistantCount: 2,
			ToolCalls:      map[string]int{"read": 2},
			Messages: []model.Message{
				{NodeID: 1, Role: "assistant", NumTokensPreceding: 1200,
					GenerationModel: "gemini-2.5-flash",
					Metrics:         &model.Metrics{InputTokens: 2000, OutputTokens: 500, TTFTMs: 100, TokensPerSec: 80}},
			},
		},
	}
}

// ---- smoke: empty / single / several sessions ----

func TestBuildersOnEmptyInput(t *testing.T) {
	var empty []model.Session

	cs := CacheStatsAggregate(empty)
	em := ComputeEfficiency(empty)
	oc := CompactionStats(empty)
	osr := OneShotRateAggregate(empty)
	avgRetries, topFiles := RetryRate(osr)
	cm := CompareModels(empty, nil)
	ca := AnalyzeContext(&model.Session{})
	cb := ContextBreakdownForSession(&model.Session{})

	allFinite(t, map[string]float64{
		"cache.hitRatio":             cs.HitRatio,
		"cache.leverage":             cs.Leverage,
		"cache.savingsUSD":           cs.SavingsUSD,
		"efficiency.tokensPerDollar": em.Score.TokensPerDollar,
		"efficiency.tokensPerReq":    em.Score.TokensPerRequest,
		"efficiency.verbosity":       em.OutputVerbosity,
		"efficiency.tokensPerMin":    em.TokensPerMin,
		"efficiency.codeRatio":       em.CodeRatio,
		"efficiency.cacheSavingsPct": em.Score.CacheSavingsPct,
		"efficiency.totalCost":       em.TotalCost,
		"compaction.avgDropPct":      oc.AvgDropPct,
		"oneshot.oneShotPct":         osr.OneShotPct,
		"oneshot.avgRetries":         avgRetries,
	})

	if len(cm.Models) != 0 {
		t.Errorf("CompareModels(empty) has %d rows, want 0", len(cm.Models))
	}
	if len(oc.Events) != 0 || oc.TotalEvents != 0 || oc.TotalTokensSaved != 0 {
		t.Errorf("CompactionStats(empty) = %+v, want zero", oc)
	}
	if n := len(WasteScan(empty)); n != 0 {
		t.Errorf("WasteScan(empty) returned %d findings, want 0", n)
	}
	if n := len(TaskCategories(empty)); n != 0 {
		t.Errorf("TaskCategories(empty) returned %d categories, want 0", n)
	}
	if len(topFiles) != 0 {
		t.Errorf("RetryRate(empty) topFiles = %v, want empty", topFiles)
	}
	if ca.TotalTokens != 0 || len(ca.ByTool) != 0 || len(ca.ByCategory) != 0 {
		t.Errorf("AnalyzeContext(zero session) = %+v, want zero", ca)
	}
	if cb.Total != 0 || len(cb.Tools) != 0 || len(cb.Categories) != 0 {
		t.Errorf("ContextBreakdownForSession(zero session) = %+v, want zero", cb)
	}
	if em.Grade == "" {
		t.Error("EfficiencyGrade returned an empty grade")
	}
}

func TestBuildersOnSingleSession(t *testing.T) {
	ss := fixtureSessions()[:1]
	s := &ss[0]

	cs := CacheStatsForSession(s)
	if !roughlyEqual(cs.HitRatio, 30000.0/40000.0, 1e-9) {
		t.Errorf("HitRatio = %v, want 0.75", cs.HitRatio)
	}
	em := ComputeEfficiency(ss)
	allFinite(t, map[string]float64{
		"efficiency.tokensPerDollar": em.Score.TokensPerDollar,
		"efficiency.tokensPerReq":    em.Score.TokensPerRequest,
		"efficiency.verbosity":       em.OutputVerbosity,
		"efficiency.tokensPerMin":    em.TokensPerMin,
		"efficiency.codeRatio":       em.CodeRatio,
	})
	if len(WasteScan(ss)) == 0 {
		t.Error("WasteScan(single session) returned no findings; want the cache_miss finding")
	}
	if n := len(CompareModels(ss, nil).Models); n != 1 {
		t.Errorf("CompareModels(single session) has %d rows, want 1", n)
	}
}

func TestBuildersOnSeveralSessions(t *testing.T) {
	ss := fixtureSessions()

	cs := CacheStatsAggregate(ss)
	if cs.CacheRead != 30_000 || cs.CacheWrite != 1_000 || cs.InputTokens != 12_000 {
		t.Errorf("cache totals = %+v, want read 30000 write 1000 input 12000", cs)
	}
	if !roughlyEqual(cs.HitRatio, 30000.0/42000.0, 1e-9) {
		t.Errorf("HitRatio = %v, want ~0.714", cs.HitRatio)
	}
	if cs.SavingsUSD <= 0 {
		t.Errorf("SavingsUSD = %v, want > 0 for a priced model with cache reads", cs.SavingsUSD)
	}

	em := ComputeEfficiency(ss)
	if em.TotalRequests != 6 || em.TotalOutput != 2_500 || em.TotalInput != 12_000 {
		t.Errorf("efficiency totals = requests %d output %d input %d, want 6/2500/12000",
			em.TotalRequests, em.TotalOutput, em.TotalInput)
	}
	if !roughlyEqual(em.OutputVerbosity, 2500.0/6.0, 1e-9) {
		t.Errorf("OutputVerbosity = %v, want %v", em.OutputVerbosity, 2500.0/6.0)
	}
	if !roughlyEqual(em.CodeRatio, 8.0/5.0, 1e-9) {
		t.Errorf("CodeRatio = %v, want 1.6 (8 tool calls / 5 messages)", em.CodeRatio)
	}
	if em.Score.CacheSavingsPct < 0 || em.Score.CacheSavingsPct > 100 {
		t.Errorf("CacheSavingsPct = %v, want within [0,100]", em.Score.CacheSavingsPct)
	}

	if n := len(DetectCompactionAll(ss)); n != 0 {
		t.Errorf("DetectCompactionAll(growing context) = %d events, want 0", n)
	}

	osr := OneShotRateAggregate(ss)
	if osr.TotalEdits != 1 || osr.Retries != 0 || !roughlyEqual(osr.OneShotPct, 100, 1e-9) {
		t.Errorf("OneShotRateAggregate = %+v, want 1 edit / 0 retries / 100%%", osr)
	}

	ca := AnalyzeContext(&ss[0])
	if ca.TotalTokens != 500 || ca.ByCategory["assistant"] != 200 || ca.ByCategory["tool"] != 300 {
		t.Errorf("AnalyzeContext(s1) = %+v, want total 500 assistant 200 tool 300", ca)
	}
	// read is 150, not 50: both assistant turns attribute their token delta to
	// the tool they called (turn 2 -> read alone, turn 4 -> edit and read
	// split), so read accumulates 100 + 50. The three values must also sum to
	// TotalTokens (300+50+150 = 500), which the assertion above pins.
	if ca.ByTool["tool_result"] != 300 || ca.ByTool["read"] != 150 || ca.ByTool["edit"] != 50 {
		t.Errorf("AnalyzeContext(s1).ByTool = %v, want tool_result 300 read 150 edit 50", ca.ByTool)
	}
}

// ---- cache ----

func TestCacheStatsRatiosAreInRangeAndFinite(t *testing.T) {
	cases := []struct {
		name         string
		cacheRead    int64
		input        int64
		wantHit      float64
		wantLeverage float64
	}{
		{"no tokens at all", 0, 0, 0, 0},
		{"input only has a zero hit ratio", 0, 1_000, 0, 0},
		{"cache only is a full hit", 1_000, 0, 1, 1},
		{"mixed", 3_000, 1_000, 0.75, 0.75},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ss := []model.Session{{ID: "s", CacheRead: tc.cacheRead, InputTokens: tc.input}}
			agg := CacheStatsAggregate(ss)
			per := CacheStatsForSession(&ss[0])
			allFinite(t, map[string]float64{"hit": agg.HitRatio, "leverage": agg.Leverage})
			if !roughlyEqual(agg.HitRatio, tc.wantHit, 1e-9) {
				t.Errorf("HitRatio = %v, want %v", agg.HitRatio, tc.wantHit)
			}
			if !roughlyEqual(agg.Leverage, tc.wantLeverage, 1e-9) {
				t.Errorf("Leverage = %v, want %v", agg.Leverage, tc.wantLeverage)
			}
			if agg.HitRatio < 0 || agg.HitRatio > 1 {
				t.Errorf("HitRatio = %v, want within [0,1]", agg.HitRatio)
			}
			if per.HitRatio != agg.HitRatio || per.Leverage != agg.Leverage {
				t.Errorf("per-session %+v != aggregate %+v for a one-session slice", per, agg)
			}
		})
	}
}

func TestCacheSavingsDetailed(t *testing.T) {
	cases := []struct {
		name string
		s    model.Session
		want float64
		tol  float64
	}{
		{
			name: "priced model with cache reads",
			s:    model.Session{ID: "s", Model: "claude-sonnet-4-5", CacheRead: 1_000_000},
			want: 1_000_000 / 1e6 * (3.0 - 0.30),
			tol:  1e-9,
		},
		{
			name: "free model has no savings",
			s:    model.Session{ID: "s", Model: "glm-5-2", CacheRead: 1_000_000},
			want: 0,
			tol:  0,
		},
		{
			name: "unknown model has no pricing",
			s:    model.Session{ID: "s", Model: "definitely-not-a-model", CacheRead: 1_000_000},
			want: 0,
			tol:  0,
		},
		{
			name: "no cache reads",
			s:    model.Session{ID: "s", Model: "claude-sonnet-4-5", InputTokens: 5_000},
			want: 0,
			tol:  0,
		},
		{
			name: "LatestModel wins over Model",
			s:    model.Session{ID: "s", Model: "glm-5-2", LatestModel: "claude-sonnet-4-5", CacheRead: 1_000_000},
			want: 1_000_000 / 1e6 * (3.0 - 0.30),
			tol:  1e-9,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CacheSavingsDetailed([]model.Session{tc.s})
			if !roughlyEqual(got, tc.want, tc.tol) {
				t.Errorf("CacheSavingsDetailed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCacheDonutExactOutput(t *testing.T) {
	cases := []struct {
		name  string
		ratio float64
		width int
		want  string
	}{
		{"zero percent", 0, 10, "░░░░░░░░░░  0.0%"},
		{"half", 0.5, 10, "█████░░░░░  50.0%"},
		{"full", 1, 10, "██████████  100.0%"},
		{"negative ratio is clamped", -1, 10, "░░░░░░░░░░  0.0%"},
		{"ratio above one is clamped", 2, 10, "██████████  100.0%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CacheDonut(tc.ratio, tc.width); got != tc.want {
				t.Errorf("CacheDonut(%v, %d) = %q, want %q", tc.ratio, tc.width, got, tc.want)
			}
		})
	}

	t.Run("narrow width falls back to 40 cells", func(t *testing.T) {
		got := CacheDonut(0.5, 4)
		if n := strings.Count(got, "█"); n != 20 {
			t.Errorf("filled cells = %d, want 20", n)
		}
		if n := strings.Count(got, "░"); n != 20 {
			t.Errorf("empty cells = %d, want 20", n)
		}
		if !strings.HasSuffix(got, "  50.0%") {
			t.Errorf("CacheDonut = %q, want suffix %q", got, "  50.0%")
		}
	})
}

// ---- efficiency ----

func TestEfficiencyGradeBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		score model.EfficiencyScore
		want  string
	}{
		{"zero score", model.EfficiencyScore{}, "F"},
		{"cache savings only", model.EfficiencyScore{CacheSavingsPct: 100}, "F"},
		{"cache savings are capped at 40 points", model.EfficiencyScore{CacheSavingsPct: 250, TokensPerRequest: 2500}, "F"},
		{"tokens per dollar is capped at 30 points", model.EfficiencyScore{TokensPerDollar: 40000, TokensPerRequest: 5000}, "D"},
		{"tokens per request is capped at 30 points", model.EfficiencyScore{TokensPerDollar: 10000, TokensPerRequest: 20000}, "D"},
		{"exactly 60 is a D", model.EfficiencyScore{TokensPerDollar: 10000, TokensPerRequest: 5000}, "D"},
		{"exactly 70 is a C", model.EfficiencyScore{CacheSavingsPct: 100, TokensPerDollar: 5000, TokensPerRequest: 2500}, "C"},
		{"80 is a B", model.EfficiencyScore{CacheSavingsPct: 100, TokensPerDollar: 10000, TokensPerRequest: 5000 * 2 / 3}, "B"},
		// 40 (cache cap) + 30 (tpd cap) + tprPts; the tpr normalisation is
		// tpr/5000*30, so reaching exactly 90 needs tpr > 3333.33 and the
		// original fixture (3000) actually scored 88. Pin both sides of the
		// boundary instead of asserting a round number that is not reachable.
		{"just below the A boundary is a B", model.EfficiencyScore{CacheSavingsPct: 100, TokensPerDollar: 10000, TokensPerRequest: 3333}, "B"},
		{"just past the A boundary is an A", model.EfficiencyScore{CacheSavingsPct: 100, TokensPerDollar: 10000, TokensPerRequest: 3334}, "A"},
		{"comfortably an A", model.EfficiencyScore{CacheSavingsPct: 100, TokensPerDollar: 10000, TokensPerRequest: 3500}, "A"},
		{"36 is an F", model.EfficiencyScore{CacheSavingsPct: 25, TokensPerDollar: 2000}, "F"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EfficiencyGrade(tc.score); got != tc.want {
				t.Errorf("EfficiencyGrade(%+v) = %q, want %q", tc.score, got, tc.want)
			}
		})
	}
}

func TestComputeEfficiencyContract(t *testing.T) {
	ss := fixtureSessions()
	em := ComputeEfficiency(ss)

	allFinite(t, map[string]float64{
		"tokensPerDollar":  em.Score.TokensPerDollar,
		"tokensPerRequest": em.Score.TokensPerRequest,
		"verbosity":        em.OutputVerbosity,
		"tokensPerMin":     em.TokensPerMin,
		"codeRatio":        em.CodeRatio,
		"cacheSavingsPct":  em.Score.CacheSavingsPct,
	})
	if !roughlyEqual(em.TotalCost, 4.75, 1e-9) {
		t.Errorf("TotalCost = %v, want 4.75 (3.5 credit + 1.25 ACU)", em.TotalCost)
	}
	if !roughlyEqual(em.Score.TokensPerRequest, 14500.0/6.0, 1e-9) {
		t.Errorf("TokensPerRequest = %v, want %v", em.Score.TokensPerRequest, 14500.0/6.0)
	}
	if !roughlyEqual(em.TokensPerMin, 2500.0/120.0, 1e-9) {
		t.Errorf("TokensPerMin = %v, want %v", em.TokensPerMin, 2500.0/120.0)
	}
	if em.Grade == "" {
		t.Error("Grade is empty")
	}
	if em.Score.OutputVerbosity != em.OutputVerbosity {
		t.Errorf("Score.OutputVerbosity %v != OutputVerbosity %v", em.Score.OutputVerbosity, em.OutputVerbosity)
	}
}

// ---- model comparison ----

func TestCompareModels(t *testing.T) {
	t.Run("empty input yields no rows", func(t *testing.T) {
		if got := CompareModels(nil, nil); len(got.Models) != 0 {
			t.Errorf("Models = %+v, want empty", got.Models)
		}
	})

	rows := CompareModels(fixtureSessions(), nil).Models
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	// Sorted by total tokens descending: claude 12000 > gemini 2500.
	if rows[0].Name != "claude-sonnet-4-5" || rows[1].Name != "gemini-2.5-flash" {
		t.Errorf("row order = [%s, %s], want claude first", rows[0].Name, rows[1].Name)
	}
	claude := rows[0]
	if claude.Requests != 2 || claude.InputTokens != 10_000 || claude.OutputTokens != 2_000 {
		t.Errorf("claude row = %+v, want 2 requests / 10000 in / 2000 out", claude)
	}
	if !roughlyEqual(claude.AvgLatency, 300, 1e-9) || !roughlyEqual(claude.TokensPerSec, 37.5, 1e-9) {
		t.Errorf("claude latency = %v tok/s = %v, want 300 / 37.5", claude.AvgLatency, claude.TokensPerSec)
	}
	if !roughlyEqual(claude.CacheHitPct, 20000.0/30000.0*100, 1e-9) {
		t.Errorf("claude CacheHitPct = %v, want 66.67", claude.CacheHitPct)
	}
	if rows[1].CacheHitPct != 0 {
		t.Errorf("gemini CacheHitPct = %v, want 0 (no metrics cache reads)", rows[1].CacheHitPct)
	}
	for _, r := range rows {
		if r.CacheHitPct < 0 || r.CacheHitPct > 100 {
			t.Errorf("%s CacheHitPct = %v, want within [0,100]", r.Name, r.CacheHitPct)
		}
		if r.Cost < 0 {
			t.Errorf("%s Cost = %v, want >= 0", r.Name, r.Cost)
		}
	}
}

func TestCompareModelsFiltering(t *testing.T) {
	ss := fixtureSessions()
	cases := []struct {
		name   string
		filter []string
		want   []string
	}{
		{"substring match is case-insensitive", []string{"GEMINI"}, []string{"gemini-2.5-flash"}},
		{"partial name matches", []string{"sonnet-4-5"}, []string{"claude-sonnet-4-5"}},
		{"multiple filters union", []string{"gemini", "claude"}, []string{"claude-sonnet-4-5", "gemini-2.5-flash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := CompareModels(ss, tc.filter).Models
			got := make([]string, 0, len(rows))
			for _, r := range rows {
				got = append(got, r.Name)
			}
			if !reflect.DeepEqual(sortedStrings(got), sortedStrings(tc.want)) {
				t.Errorf("filtered rows = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCompareModelsUnmatchedFilterReturnsEverything pins current behaviour:
// an unmatched filter is silently ignored because the `want` set stays empty
// and `len(want) > 0` then skips all filtering. Arguably a BUG — reported, not
// fixed here.
func TestCompareModelsUnmatchedFilterReturnsEverything(t *testing.T) {
	rows := CompareModels(fixtureSessions(), []string{"no-such-model-anywhere"}).Models
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (an unmatched filter currently filters nothing)", len(rows))
	}
}

func TestCompareModelsIgnoresNonAssistantAndUnattributedMessages(t *testing.T) {
	ss := []model.Session{{
		ID: "s",
		Messages: []model.Message{
			{Role: "user", GenerationModel: "claude-sonnet-4-5"},
			{Role: "assistant"},                                  // no generation model
			{Role: "assistant", GenerationModel: "gpt-4o"},       // counted
			{Role: "tool", GenerationModel: "claude-sonnet-4-5"}, // ignored
			{Role: "assistant", GenerationModel: "gpt-4o"},       // counted
		},
	}}
	rows := CompareModels(ss, nil).Models
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Name != "gpt-4o" || rows[0].Requests != 2 {
		t.Errorf("row = %+v, want gpt-4o with 2 requests", rows[0])
	}
	if rows[0].AvgLatency != 0 || rows[0].TokensPerSec != 0 {
		t.Errorf("row = %+v, want zero latency metrics when Metrics is nil", rows[0])
	}
}

// ---- one-shot / retries ----

func TestDetectRetries(t *testing.T) {
	cases := []struct {
		name        string
		messages    []model.Message
		wantEdits   int
		wantRetries int
		wantPct     float64
		wantFiles   map[string]int
	}{
		{
			name:      "no messages",
			wantFiles: map[string]int{},
		},
		{
			name: "edit then exec then edit the same file is a retry",
			messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go build ./...")}},
				{NodeID: 3, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e2", "/repo/a.go")}},
			},
			wantEdits: 2, wantRetries: 1, wantPct: 50,
			wantFiles: map[string]int{"/repo/a.go": 1},
		},
		{
			name: "edit then edit without an exec in between is not a retry",
			messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e2", "/repo/a.go")}},
			},
			wantEdits: 2, wantRetries: 0, wantPct: 100,
			wantFiles: map[string]int{},
		},
		{
			name: "edit then exec then edit a different file is not a retry",
			messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go test ./...")}},
				{NodeID: 3, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e2", "/repo/b.go")}},
			},
			wantEdits: 2, wantRetries: 0, wantPct: 100,
			wantFiles: map[string]int{},
		},
		{
			name: "a later exec re-arms the retry detector",
			messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go test ./...")}},
				{NodeID: 3, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e2", "/repo/a.go")}},
				{NodeID: 4, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x2", "go test ./...")}},
				{NodeID: 5, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e3", "/repo/a.go")}},
			},
			wantEdits: 3, wantRetries: 2, wantPct: 100.0 / 3.0,
			wantFiles: map[string]int{"/repo/a.go": 2},
		},
		{
			name: "non-assistant messages are ignored",
			messages: []model.Message{
				{NodeID: 1, Role: "user", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
				{NodeID: 2, Role: "tool", ToolCalls: []model.ToolCall{tcEdit("e2", "/repo/a.go")}},
			},
			wantFiles: map[string]int{},
		},
		{
			name: "edits without a file_path are ignored",
			messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "")}},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{{ID: "e2", Name: "write", Arguments: "not json"}}},
			},
			wantFiles: map[string]int{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRetries(tc.messages)
			if got.TotalEdits != tc.wantEdits {
				t.Errorf("TotalEdits = %d, want %d", got.TotalEdits, tc.wantEdits)
			}
			if got.Retries != tc.wantRetries {
				t.Errorf("Retries = %d, want %d", got.Retries, tc.wantRetries)
			}
			if !roughlyEqual(got.OneShotPct, tc.wantPct, 1e-9) {
				t.Errorf("OneShotPct = %v, want %v", got.OneShotPct, tc.wantPct)
			}
			if !reflect.DeepEqual(got.FileRetries, tc.wantFiles) {
				t.Errorf("FileRetries = %v, want %v", got.FileRetries, tc.wantFiles)
			}
			allFinite(t, map[string]float64{"oneShotPct": got.OneShotPct})
		})
	}
}

// TestDetectRetriesZeroNodeID pins an off-by-one: the guard
// `lastEdit[fp] > 0` treats a first edit recorded at NodeID 0 as "never
// edited", so a genuine retry is not counted. SUSPECTED BUG — reported, not
// fixed here; the same fixture with NodeIDs starting at 1 does count it.
func TestDetectRetriesZeroNodeID(t *testing.T) {
	build := func(firstNodeID int) model.OneShotRate {
		return detectRetries([]model.Message{
			{NodeID: firstNodeID, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
			{NodeID: firstNodeID + 1, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go build")}},
			{NodeID: firstNodeID + 2, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e2", "/repo/a.go")}},
		})
	}

	withZero := build(0)
	if withZero.TotalEdits != 2 {
		t.Errorf("TotalEdits = %d, want 2", withZero.TotalEdits)
	}
	if withZero.Retries != 0 {
		t.Errorf("Retries = %d, want 0 (current behaviour: NodeID 0 disables retry detection)", withZero.Retries)
	}

	withOne := build(1)
	if withOne.Retries != 1 {
		t.Errorf("Retries = %d, want 1 once the first edit has a non-zero NodeID", withOne.Retries)
	}
}

func TestOneShotRateAggregate(t *testing.T) {
	t.Run("empty input stays zero", func(t *testing.T) {
		got := OneShotRateAggregate(nil)
		if got.TotalEdits != 0 || got.Retries != 0 || got.OneShotPct != 0 || len(got.FileRetries) != 0 {
			t.Errorf("OneShotRateAggregate(nil) = %+v, want zero", got)
		}
		allFinite(t, map[string]float64{"oneShotPct": got.OneShotPct})
	})

	t.Run("sums edits and merges per-file retries", func(t *testing.T) {
		ss := []model.Session{
			{Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("a1", "/repo/a.go")}},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go build")}},
				{NodeID: 3, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("a2", "/repo/a.go")}},
			}},
			{Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("b1", "/repo/a.go")}},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x2", "go test")}},
				{NodeID: 3, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("b2", "/repo/a.go")}},
			}},
		}
		got := OneShotRateAggregate(ss)
		if got.TotalEdits != 4 || got.Retries != 2 {
			t.Errorf("aggregate = %+v, want 4 edits / 2 retries", got)
		}
		if !roughlyEqual(got.OneShotPct, 50, 1e-9) {
			t.Errorf("OneShotPct = %v, want 50", got.OneShotPct)
		}
		if got.FileRetries["/repo/a.go"] != 2 {
			t.Errorf("FileRetries = %v, want /repo/a.go -> 2", got.FileRetries)
		}
	})
}

func TestRetryRate(t *testing.T) {
	t.Run("no edits", func(t *testing.T) {
		avg, top := RetryRate(model.OneShotRate{FileRetries: map[string]int{}})
		if avg != 0 || len(top) != 0 {
			t.Errorf("RetryRate(empty) = %v, %v, want 0, empty", avg, top)
		}
	})
	t.Run("average and descending top files", func(t *testing.T) {
		avg, top := RetryRate(model.OneShotRate{
			TotalEdits:  4,
			Retries:     3,
			FileRetries: map[string]int{"/repo/a.go": 2, "/repo/b.go": 1, "/repo/c.go": 0},
		})
		if !roughlyEqual(avg, 0.75, 1e-9) {
			t.Errorf("avg = %v, want 0.75", avg)
		}
		if len(top) != 2 {
			t.Fatalf("topFiles = %v, want 2 entries (zero counts dropped)", top)
		}
		if top[0].File != "/repo/a.go" || top[0].Retries != 2 || top[1].File != "/repo/b.go" {
			t.Errorf("topFiles = %v, want a.go(2) then b.go(1)", top)
		}
	})
}

// ---- task classification ----

func TestClassifySession(t *testing.T) {
	cases := []struct {
		name string
		sess model.Session
		want string
	}{
		{
			name: "no tools at all is a conversation",
			sess: model.Session{ID: "s"},
			want: CatConversation,
		},
		{
			name: "tool-call map without messages falls through to general",
			sess: model.Session{ID: "s", ToolCalls: map[string]int{"edit": 1}},
			want: CatGeneral,
		},
		{
			name: "edit only is coding",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
			}},
			want: CatCoding,
		},
		{
			name: "edit plus a debug keyword is debugging",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "user", Content: "please fix the crash"},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
			}},
			want: CatDebugging,
		},
		{
			name: "a test command is testing",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go test ./internal/...")}},
			}},
			want: CatTesting,
		},
		{
			name: "test plus edit plus debug keyword is debugging",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "user", Content: "the test fails with an error"},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go test ./..."), tcEdit("e1", "/repo/a.go")}},
			}},
			want: CatDebugging,
		},
		{
			name: "git command without edits is git ops",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "git commit -m 'wip'")}},
			}},
			want: CatGitOps,
		},
		{
			name: "build command without edits is build/deploy",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go build ./...")}},
			}},
			want: CatBuildDeploy,
		},
		{
			name: "build command with edits is coding",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go build ./..."), tcEdit("e1", "/repo/a.go")}},
			}},
			want: CatCoding,
		},
		{
			name: "subagent without edits is delegation",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{{ID: "r1", Name: "run_subagent"}}},
			}},
			want: CatDelegation,
		},
		{
			name: "read only is exploration",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcRead("r1", "/repo/a.go")}},
			}},
			want: CatExploration,
		},
		{
			name: "read plus exec falls through to general",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcRead("r1", "/repo/a.go"), tcExec("x1", "echo hi")}},
			}},
			want: CatGeneral,
		},
		{
			name: "plan mode without edits is planning",
			sess: model.Session{ID: "s", AgentMode: "plan", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcRead("r1", "/repo/a.go")}},
			}},
			want: CatPlanning,
		},
		{
			name: "todo_write without edits is planning",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{{ID: "t1", Name: "todo_write"}}},
			}},
			want: CatPlanning,
		},
		{
			name: "plan mode with edits is coding",
			sess: model.Session{ID: "s", AgentMode: "plan", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
			}},
			want: CatCoding,
		},
		{
			name: "debug keywords are substring matched, so 'prefix' counts as 'fix'",
			sess: model.Session{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "user", Content: "Change the output prefix"},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
			}},
			want: CatDebugging,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifySession(&tc.sess); got != tc.want {
				t.Errorf("ClassifySession = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTaskCategories(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		if got := TaskCategories(nil); len(got) != 0 {
			t.Errorf("TaskCategories(nil) = %+v, want empty", got)
		}
	})

	t.Run("counts and costs are aggregated, sorted by count", func(t *testing.T) {
		coding := func(id string, cost float64) model.Session {
			return model.Session{ID: id, CreditCost: cost, Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
			}}
		}
		git := model.Session{ID: "g", CreditCost: 4, Messages: []model.Message{
			{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "git push origin main")}},
		}}
		got := TaskCategories([]model.Session{coding("a", 1), coding("b", 2), git})
		if len(got) != 2 {
			t.Fatalf("got %d categories, want 2: %+v", len(got), got)
		}
		if got[0].Name != CatCoding || got[0].Count != 2 || !roughlyEqual(got[0].Cost, 3, 1e-9) {
			t.Errorf("first category = %+v, want Coding count 2 cost 3", got[0])
		}
		if got[1].Name != CatGitOps || got[1].Count != 1 || !roughlyEqual(got[1].Cost, 4, 1e-9) {
			t.Errorf("second category = %+v, want Git Ops count 1 cost 4", got[1])
		}
	})
}

// ---- waste scan ----

func wasteCategories(findings []model.WasteFinding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Category)
	}
	return sortedStrings(out)
}

func TestWasteScan(t *testing.T) {
	cases := []struct {
		name string
		ss   []model.Session
		want []string
	}{
		{"no sessions", nil, nil},
		{
			name: "cache miss",
			ss:   []model.Session{{ID: "s", InputTokens: 1_000}},
			want: []string{"cache_miss"},
		},
		{
			name: "retry loop",
			ss: []model.Session{{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e1", "/repo/a.go")}},
				{NodeID: 2, Role: "assistant", ToolCalls: []model.ToolCall{tcExec("x1", "go build")}},
				{NodeID: 3, Role: "assistant", ToolCalls: []model.ToolCall{tcEdit("e2", "/repo/a.go")}},
			}}},
			want: []string{"retry_loop"},
		},
		{
			name: "subagent fan-out",
			ss:   []model.Session{{ID: "s", SubAgentCalls: make([]model.SubAgentCall, 11)}},
			want: []string{"subagent_fanout"},
		},
		{
			name: "repeated reads",
			ss: []model.Session{{ID: "s", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", ToolCalls: []model.ToolCall{
					tcRead("r1", "/repo/x.go"), tcRead("r2", "/repo/x.go"),
					tcRead("r3", "/repo/x.go"), tcRead("r4", "/repo/x.go"),
				}},
			}}},
			want: []string{"repeated_reads"},
		},
		{
			name: "long unproductive session",
			ss: []model.Session{{
				ID: "s", OutputTokens: 10, AssistantCount: 21,
				CreatedAt: fixedNow.Add(-2 * time.Hour), LastActivityAt: fixedNow.Add(-time.Hour),
			}},
			want: []string{"low_output_session"},
		},
		{
			name: "ACU spend",
			ss:   []model.Session{{ID: "s", ACUCost: 2}},
			want: []string{"acu_budget"},
		},
		{
			name: "several findings at once",
			ss: []model.Session{{
				ID: "s", InputTokens: 1_000, ACUCost: 2,
			}},
			want: []string{"acu_budget", "cache_miss"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wasteCategories(WasteScan(tc.ss))
			if !reflect.DeepEqual(got, sortedStrings(tc.want)) {
				t.Errorf("categories = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("every finding carries a suggestion", func(t *testing.T) {
		for _, f := range WasteScan(fixtureSessions()) {
			if f.Category == "" || f.Description == "" || f.Suggestion == "" {
				t.Errorf("finding %+v has an empty field", f)
			}
		}
	})
}

// ---- context analysis ----

func TestAnalyzeContext(t *testing.T) {
	t.Run("empty session", func(t *testing.T) {
		got := AnalyzeContext(&model.Session{ID: "s"})
		if got.SessionID != "s" || got.TotalTokens != 0 {
			t.Errorf("AnalyzeContext = %+v, want session s and zero tokens", got)
		}
	})

	t.Run("deltas are attributed to the preceding role and tools", func(t *testing.T) {
		s := &model.Session{ID: "s", Messages: []model.Message{
			{NodeID: 1, Role: "assistant", NumTokensPreceding: 100},
			{NodeID: 2, Role: "tool", NumTokensPreceding: 400},
			{NodeID: 3, Role: "assistant", NumTokensPreceding: 500, ToolCalls: []model.ToolCall{
				tcRead("r1", "/repo/a.go"), tcEdit("e1", "/repo/a.go"),
			}},
		}}
		got := AnalyzeContext(s)
		if got.TotalTokens != 500 {
			t.Errorf("TotalTokens = %d, want 500", got.TotalTokens)
		}
		if got.ByCategory["assistant"] != 200 || got.ByCategory["tool"] != 300 {
			t.Errorf("ByCategory = %v, want assistant 200 tool 300", got.ByCategory)
		}
		if got.ByTool["tool_result"] != 300 || got.ByTool["read"] != 50 || got.ByTool["edit"] != 50 {
			t.Errorf("ByTool = %v, want tool_result 300 read 50 edit 50", got.ByTool)
		}
	})

	t.Run("negative deltas from compaction are clamped to zero", func(t *testing.T) {
		s := &model.Session{ID: "s", Messages: []model.Message{
			{NodeID: 1, Role: "assistant", NumTokensPreceding: 500},
			{NodeID: 2, Role: "assistant", NumTokensPreceding: 100},
		}}
		got := AnalyzeContext(s)
		if got.TotalTokens != 500 {
			t.Errorf("TotalTokens = %d, want 500 (drop is not subtracted)", got.TotalTokens)
		}
		if got.ByCategory["assistant"] != 500 {
			t.Errorf("ByCategory = %v, want assistant 500", got.ByCategory)
		}
	})

	t.Run("messages without a token count are skipped", func(t *testing.T) {
		s := &model.Session{ID: "s", Messages: []model.Message{
			{NodeID: 1, Role: "user", Content: "hi"},
			{NodeID: 2, Role: "assistant", NumTokensPreceding: 250},
		}}
		got := AnalyzeContext(s)
		if got.TotalTokens != 250 {
			t.Errorf("TotalTokens = %d, want 250", got.TotalTokens)
		}
		if _, ok := got.ByCategory["user"]; ok {
			t.Errorf("ByCategory = %v, want no user entry", got.ByCategory)
		}
	})
}

func TestContextBreakdownForSession(t *testing.T) {
	t.Run("empty session yields no entries", func(t *testing.T) {
		got := ContextBreakdownForSession(&model.Session{ID: "s"})
		if got.Total != 0 || len(got.Categories) != 0 || len(got.Tools) != 0 {
			t.Errorf("breakdown = %+v, want empty", got)
		}
	})

	t.Run("percentages stay in range and entries are sorted descending", func(t *testing.T) {
		s := &model.Session{ID: "s", Messages: []model.Message{
			{NodeID: 1, Role: "assistant", NumTokensPreceding: 100},
			{NodeID: 2, Role: "tool", NumTokensPreceding: 400},
			{NodeID: 3, Role: "assistant", NumTokensPreceding: 500, ToolCalls: []model.ToolCall{
				tcRead("r1", "/repo/a.go"), tcEdit("e1", "/repo/a.go"),
			}},
		}}
		got := ContextBreakdownForSession(s)
		if got.Total != 500 {
			t.Fatalf("Total = %d, want 500", got.Total)
		}
		pct := map[string]float64{}
		for _, e := range got.Categories {
			pct[e.Label] = e.Pct
			if e.Pct < 0 || e.Pct > 100 {
				t.Errorf("category %q Pct = %v, want within [0,100]", e.Label, e.Pct)
			}
		}
		if !roughlyEqual(pct["tool"], 60, 1e-9) || !roughlyEqual(pct["assistant"], 40, 1e-9) {
			t.Errorf("category pcts = %v, want tool 60 / assistant 40", pct)
		}
		for i := 1; i < len(got.Categories); i++ {
			if got.Categories[i-1].Tokens < got.Categories[i].Tokens {
				t.Errorf("Categories not sorted descending: %+v", got.Categories)
			}
		}

		toolTokens := map[string]int64{}
		for _, e := range got.Tools {
			toolTokens[e.Label] = e.Tokens
			if e.Pct < 0 || e.Pct > 100 {
				t.Errorf("tool %q Pct = %v, want within [0,100]", e.Label, e.Pct)
			}
		}
		if toolTokens["tool_result"] != 300 || toolTokens["read"] != 50 || toolTokens["edit"] != 50 {
			t.Errorf("tool tokens = %v, want tool_result 300 read 50 edit 50", toolTokens)
		}
		for i := 1; i < len(got.Tools); i++ {
			if got.Tools[i-1].Tokens < got.Tools[i].Tokens {
				t.Errorf("Tools not sorted descending: %+v", got.Tools)
			}
		}
	})
}

// ---- compaction ----

func TestDetectCompaction(t *testing.T) {
	assistant := func(nodeID, tokens int) model.Message {
		return model.Message{NodeID: nodeID, Role: "assistant", NumTokensPreceding: tokens, CreatedAt: fixedNow}
	}
	cases := []struct {
		name       string
		messages   []model.Message
		wantEvents int
		wantBefore int
		wantAfter  int
	}{
		{"no messages", nil, 0, 0, 0},
		{"monotonic growth is not compaction", []model.Message{assistant(1, 100), assistant(2, 200), assistant(3, 300)}, 0, 0, 0},
		{"exactly 30% drop is below the threshold", []model.Message{assistant(1, 100), assistant(2, 70)}, 0, 0, 0},
		{"31% drop is compaction", []model.Message{assistant(1, 100), assistant(2, 69)}, 1, 100, 69},
		{
			name:       "non-assistant messages do not reset the baseline",
			messages:   []model.Message{assistant(1, 1000), {NodeID: 2, Role: "tool", NumTokensPreceding: 5000}, assistant(3, 100)},
			wantEvents: 1, wantBefore: 1000, wantAfter: 100,
		},
		{
			name:       "messages without a token count are skipped",
			messages:   []model.Message{assistant(1, 1000), {NodeID: 2, Role: "assistant"}, assistant(3, 100)},
			wantEvents: 1, wantBefore: 1000, wantAfter: 100,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectCompaction(&model.Session{ID: "s", Messages: tc.messages})
			if len(got) != tc.wantEvents {
				t.Fatalf("got %d events, want %d: %+v", len(got), tc.wantEvents, got)
			}
			if tc.wantEvents == 0 {
				return
			}
			if got[0].SessionID != "s" || got[0].BeforeTokens != tc.wantBefore || got[0].AfterTokens != tc.wantAfter {
				t.Errorf("event = %+v, want session s %d -> %d", got[0], tc.wantBefore, tc.wantAfter)
			}
			if !got[0].Timestamp.Equal(fixedNow) {
				t.Errorf("event timestamp = %v, want %v", got[0].Timestamp, fixedNow)
			}
		})
	}
}

func TestCompactionStats(t *testing.T) {
	t.Run("no events stays zero and finite", func(t *testing.T) {
		got := CompactionStats(nil)
		if got.TotalEvents != 0 || got.TotalTokensSaved != 0 || got.AvgDropPct != 0 {
			t.Errorf("CompactionStats(nil) = %+v, want zero", got)
		}
		allFinite(t, map[string]float64{"avgDropPct": got.AvgDropPct})
	})

	t.Run("aggregates saved tokens and average drop", func(t *testing.T) {
		ss := []model.Session{
			{ID: "a", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", NumTokensPreceding: 1000},
				{NodeID: 2, Role: "assistant", NumTokensPreceding: 500},
			}},
			{ID: "b", Messages: []model.Message{
				{NodeID: 1, Role: "assistant", NumTokensPreceding: 200},
				{NodeID: 2, Role: "assistant", NumTokensPreceding: 100},
			}},
		}
		got := CompactionStats(ss)
		if got.TotalEvents != 2 || got.TotalTokensSaved != 600 {
			t.Errorf("summary = %+v, want 2 events / 600 tokens saved", got)
		}
		if !roughlyEqual(got.AvgDropPct, 50, 1e-9) {
			t.Errorf("AvgDropPct = %v, want 50", got.AvgDropPct)
		}
		if len(got.Events) != 2 {
			t.Errorf("Events = %d, want 2", len(got.Events))
		}
	})
}
