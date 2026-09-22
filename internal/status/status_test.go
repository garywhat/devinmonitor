package status

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ptrF(v float64) *float64 { return &v }
func ptrI(v int64) *int64     { return &v }

func TestSanitizeFinite(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
		want *float64
	}{
		{"NaN", math.NaN(), nil},
		{"+Inf", math.Inf(1), nil},
		{"-Inf", math.Inf(-1), nil},
		{"negative is finite", -1, ptrF(-1)},
		{"zero", 0, ptrF(0)},
		{"fraction", 42.5, ptrF(42.5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeFinite(tc.in)
			if !equalFloatPtr(got, tc.want) {
				t.Fatalf("SanitizeFinite(%v) = %v, want %v", tc.in, fmtPtr(got), fmtPtr(tc.want))
			}
		})
	}
}

func TestSanitizePercent(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   float64
		want *float64
	}{
		{"NaN", math.NaN(), nil},
		{"+Inf", math.Inf(1), nil},
		{"-Inf", math.Inf(-1), nil},
		{"negative", -1, nil},
		{"zero", 0, ptrF(0)},
		{"fraction", 42.5, ptrF(42.5)},
		{"exactly 100", 100, ptrF(100)},
		{"rounding artifact clamps to 100", 100.5, ptrF(100)},
		{"clamp ceiling", 101, ptrF(100)},
		{"just past the clamp", 101.0001, nil},
		{"epoch leaked into the field", 1.79e9, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizePercent(tc.in)
			if !equalFloatPtr(got, tc.want) {
				t.Fatalf("SanitizePercent(%v) = %v, want %v", tc.in, fmtPtr(got), fmtPtr(tc.want))
			}
		})
	}
}

func TestComputeExitCode(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hasActive bool
		used      *float64
		limitHit  bool
		wantCode  int
		wantLabel string
	}{
		{"nil percentage without active session", false, nil, false, CodeIndeterminate, "no_active_session"},
		{"nil percentage with active session", true, nil, false, CodeIndeterminate, "indeterminate"},
		{"limitHit without any percentage still rule 1", true, nil, true, CodeIndeterminate, "indeterminate"},
		{"zero usage", true, ptrF(0), false, CodeOK, "ok"},
		{"just below the warning band", true, ptrF(79.9), false, CodeOK, "ok"},
		{"exactly at the warning threshold", true, ptrF(80), false, CodeNearLimit, "near_limit"},
		{"99.9 is near limit", true, ptrF(99.9), false, CodeNearLimit, "near_limit"},
		{"exactly 100 is limit hit", true, ptrF(100), false, CodeLimitHit, "limit_hit"},
		{"over 100 is limit hit", true, ptrF(180), false, CodeLimitHit, "limit_hit"},
		{"limitHit overrides low usage", true, ptrF(0), true, CodeLimitHit, "limit_hit"},
		{"limitHit overrides the warning band", true, ptrF(85), true, CodeLimitHit, "limit_hit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, label := ComputeExitCode(tc.hasActive, tc.used, tc.limitHit)
			if code != tc.wantCode || label != tc.wantLabel {
				t.Fatalf("ComputeExitCode(%v, %v, %v) = (%d, %q), want (%d, %q)",
					tc.hasActive, fmtPtr(tc.used), tc.limitHit, code, label, tc.wantCode, tc.wantLabel)
			}
		})
	}
}

func TestComputeExitCodeConstants(t *testing.T) {
	if CodeOK != 0 || CodeNearLimit != 10 || CodeLimitHit != 11 || CodeIndeterminate != 20 || CodeNoData != 30 {
		t.Fatalf("semantic exit codes drifted: %d/%d/%d/%d/%d",
			CodeOK, CodeNearLimit, CodeLimitHit, CodeIndeterminate, CodeNoData)
	}
	if LimitWarningThreshold != 0.8 || PaceTolerancePoints != 10.0 || DefaultWindowSeconds != 5*3600 {
		t.Fatalf("protocol constants drifted: %v/%v/%v", LimitWarningThreshold, PaceTolerancePoints, DefaultWindowSeconds)
	}
}

func TestConfidenceVocabulary(t *testing.T) {
	// Pinned to the provenance labels already used by internal/integration
	// (provenanceLabel / provenanceTag) so the tool ships one vocabulary.
	if ConfidenceOfficial != "official" || ConfidenceEstimate != "estimated" || ConfidenceUnknown != "unknown" {
		t.Fatalf("confidence vocabulary drifted: %q/%q/%q", ConfidenceOfficial, ConfidenceEstimate, ConfidenceUnknown)
	}
	if SourceDevinDB != "devin_db" || SourceStatusline != "statusline" || SourceConfig != "config" {
		t.Fatalf("source vocabulary drifted: %q/%q/%q", SourceDevinDB, SourceStatusline, SourceConfig)
	}
}

func TestComputePace(t *testing.T) {
	const (
		windowSeconds int64 = 3600
		reset               = int64(1_800_000_000)
	)
	windowStart := time.Unix(reset, 0).Add(-time.Duration(windowSeconds) * time.Second)
	mid := windowStart.Add(1800 * time.Second) // half the window elapsed -> 50.0%

	for _, tc := range []struct {
		name        string
		used        float64
		now         time.Time
		wantLabel   string
		wantElapsed float64
	}{
		{"dead centre of the window", 50, mid, "on track", 50},
		{"exactly +10 points is on track", 60, mid, "on track", 50},
		{"just over +10 slows down", 60.1, mid, "slow down", 50},
		{"well over +10 slows down", 90, mid, "slow down", 50},
		{"exactly -10 points is on track", 40, mid, "on track", 50},
		{"just under -10 speeds up", 39.9, mid, "speed up", 50},
		{"elapsed rounds to one decimal, still ahead of pace", 20, windowStart.Add(1234 * time.Second), "speed up", 34.3},
		{"before the window starts clamps to 0", 5, windowStart.Add(-600 * time.Second), "on track", 0},
		{"after the reset clamps to 100", 95, time.Unix(reset, 0).Add(60 * time.Second), "on track", 100},
		{"after the reset and still high speeds up", 42.5, time.Unix(reset, 0).Add(60 * time.Second), "speed up", 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used := ptrF(tc.used)
			got := ComputePace(used, ptrI(reset), windowSeconds, tc.now)
			if got.Label != tc.wantLabel {
				t.Fatalf("ComputePace(...).Label = %q, want %q", got.Label, tc.wantLabel)
			}
			if got.ElapsedPercentage == nil || *got.ElapsedPercentage != tc.wantElapsed {
				t.Fatalf("ComputePace(...).ElapsedPercentage = %v, want %v", fmtPtr(got.ElapsedPercentage), tc.wantElapsed)
			}
			if got.UsedPercentage == nil || *got.UsedPercentage != tc.used {
				t.Fatalf("ComputePace(...).UsedPercentage = %v, want %v", fmtPtr(got.UsedPercentage), tc.used)
			}
		})
	}
}

func TestComputePaceUnknown(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name    string
		used    *float64
		reset   *int64
		window  int64
		wantWhy string
	}{
		{"no inputs at all", nil, nil, 3600, "missing usedPct and reset"},
		{"no percentage", nil, ptrI(1_800_000_000), 3600, "missing usedPct"},
		{"no reset", ptrF(42), nil, 3600, "missing resetEpoch"},
		{"no window length", ptrF(42), ptrI(1_800_000_000), 0, "no window to grade against"},
		{"negative window length", ptrF(42), ptrI(1_800_000_000), -60, "no window to grade against"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputePace(tc.used, tc.reset, tc.window, now)
			want := Pace{Label: "unknown"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ComputePace(%s) = %+v, want %+v (%s)", tc.name, got, want, tc.wantWhy)
			}
		})
	}
}

func TestForecastExhaustion(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	epoch := func(t time.Time) *int64 { return ptrI(t.Unix()) }

	for _, tc := range []struct {
		name        string
		used        *float64
		reset       *int64
		window      int64
		wantNil     bool
		wantAt      time.Time
		wantDisplay string
	}{
		{
			name: "no percentage", used: nil, reset: epoch(now.Add(4 * time.Hour)), window: 3600,
			wantNil: true,
		},
		{
			name: "no reset", used: ptrF(50), reset: nil, window: 3600,
			wantNil: true,
		},
		{
			name: "no window length", used: ptrF(50), reset: epoch(now.Add(4 * time.Hour)), window: 0,
			wantNil: true,
		},
		{
			name: "zero usage has no rate", used: ptrF(0), reset: epoch(now.Add(4 * time.Hour)), window: 3600,
			wantNil: true,
		},
		{
			name: "negative usage", used: ptrF(-3), reset: epoch(now.Add(4 * time.Hour)), window: 3600,
			wantNil: true,
		},
		{
			name: "NaN usage", used: ptrF(math.NaN()), reset: epoch(now.Add(4 * time.Hour)), window: 3600,
			wantNil: true,
		},
		{
			name: "window already elapsed", used: ptrF(50), reset: epoch(now.Add(-time.Minute)), window: 3600,
			wantNil: true,
		},
		{
			name: "exhaustion lands after the reset", used: ptrF(10), reset: epoch(now.Add(3000 * time.Second)), window: 3600,
			wantNil: true,
		},
		{
			name: "exhaustion exactly at the reset does not count", used: ptrF(50), reset: epoch(now.Add(1800 * time.Second)), window: 3600,
			wantNil: true,
		},
		{
			name: "exhaustion today", used: ptrF(40), reset: epoch(now.Add(8 * time.Hour)), window: 36000,
			wantAt: now.Add(3 * time.Hour), wantDisplay: "Today 13:00 (estimated)",
		},
		{
			name: "exhaustion tomorrow", used: ptrF(20), reset: epoch(now.Add(20 * time.Hour)), window: 86400,
			wantAt: now.Add(16 * time.Hour), wantDisplay: "Tomorrow 02:00 (estimated)",
		},
		{
			name: "exhaustion on a later date", used: ptrF(60), reset: epoch(now.Add(100 * time.Hour)), window: 168 * 3600,
			wantAt: now.Add(45*time.Hour + 20*time.Minute), wantDisplay: "2026-09-24 07:20 (estimated)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ForecastExhaustion(tc.used, tc.reset, tc.window, now)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("ForecastExhaustion(...) = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("ForecastExhaustion(...) = nil, want a projection")
			}
			if got.ExhaustedAt == nil || got.Display == nil {
				t.Fatalf("ForecastExhaustion(...) = %+v, want both fields set", got)
			}
			at, err := time.Parse(time.RFC3339, *got.ExhaustedAt)
			if err != nil {
				t.Fatalf("ExhaustedAt = %q is not RFC3339: %v", *got.ExhaustedAt, err)
			}
			if !at.Equal(tc.wantAt) {
				t.Fatalf("ExhaustedAt = %s, want %s", at.UTC(), tc.wantAt.UTC())
			}
			if !strings.HasSuffix(*got.ExhaustedAt, "Z") {
				t.Fatalf("ExhaustedAt = %q, want a UTC stamp", *got.ExhaustedAt)
			}
			if *got.Display != tc.wantDisplay {
				t.Fatalf("Display = %q, want %q", *got.Display, tc.wantDisplay)
			}
			if !strings.Contains(*got.Display, "(estimated)") {
				t.Fatalf("Display = %q, want the (estimated) suffix", *got.Display)
			}
		})
	}
}

func TestForecastExhaustionOverTheLimit(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	// Reset in 30 minutes with a one-hour window: half the window has elapsed,
	// so there is a rate to extrapolate and the remaining budget is already 0.
	got := ForecastExhaustion(ptrF(120), ptrI(now.Add(30*time.Minute).Unix()), 3600, now)
	if got == nil {
		t.Fatal("ForecastExhaustion over the limit = nil, want an immediate projection")
	}
	if got.ExhaustedAt == nil || !strings.HasSuffix(*got.ExhaustedAt, "Z") {
		t.Fatalf("ExhaustedAt = %v, want a UTC RFC3339 stamp", fmtPtrStr(got.ExhaustedAt))
	}
}

func TestBuildSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	limits := map[string]*Window{
		"five_hour": {UsedPercentage: ptrF(85), Source: Source{Kind: SourceDevinDB}, Confidence: ConfidenceEstimate},
		"seven_day": {UsedPercentage: ptrF(10), Source: Source{Kind: SourceConfig}, Confidence: ConfidenceOfficial},
	}
	in := Input{
		Now:            now,
		TodayCost:      1.23,
		WeekCost:       4.56,
		MonthCost:      7.89,
		TotalCost:      12.34,
		TotalSessions:  19,
		TotalRequests:  29249,
		TotalTokens:    1234567,
		ActiveSessions: 2,
		CostProvenance: ConfidenceEstimate,
		ACU:            true,
		ByModel:        []ModelRow{{Model: "devin-1", Requests: 3, Cost: 0.5, Share: 66.7}},
		Limits:         limits,
		StatusWindow:   "five_hour",
	}

	s := BuildSnapshot(in)

	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", SchemaVersion)
	}
	if s.SchemaVersion != SchemaVersion {
		t.Fatalf("snapshot SchemaVersion = %d, want %d", s.SchemaVersion, SchemaVersion)
	}
	if s.GeneratedAt != "2026-09-22T10:00:00Z" {
		t.Fatalf("GeneratedAt = %q, want 2026-09-22T10:00:00Z", s.GeneratedAt)
	}
	if s.Limits == nil {
		t.Fatal("Limits = nil, want a non-nil map")
	}
	if len(s.Limits) != len(limits) {
		t.Fatalf("len(Limits) = %d, want %d", len(s.Limits), len(limits))
	}
	if s.Status.Code != CodeNearLimit || s.Status.Label != "near_limit" {
		t.Fatalf("Status = %+v, want near_limit", s.Status)
	}
	if s.Breakdown.TodayCost != 1.23 || s.Breakdown.TotalSessions != 19 || s.Breakdown.TotalRequests != 29249 {
		t.Fatalf("Breakdown not copied: %+v", s.Breakdown)
	}
	if s.Breakdown.ActiveSessions != 2 || !s.Breakdown.ACU || s.Breakdown.CostProvenance != ConfidenceEstimate {
		t.Fatalf("Breakdown flags not copied: %+v", s.Breakdown)
	}
	if len(s.Breakdown.ByModel) != 1 || s.Breakdown.ByModel[0].Model != "devin-1" {
		t.Fatalf("ByModel not copied: %+v", s.Breakdown.ByModel)
	}

	// The named window is what drives the status.
	in.StatusWindow = "seven_day"
	if got := BuildSnapshot(in).Status; got.Code != CodeOK || got.Label != "ok" {
		t.Fatalf("StatusWindow seven_day -> %+v, want ok", got)
	}
	in.StatusWindow = "five_hour"
	in.LimitHit = true
	if got := BuildSnapshot(in).Status; got.Code != CodeLimitHit || got.Label != "limit_hit" {
		t.Fatalf("LimitHit override -> %+v, want limit_hit", got)
	}
}

func TestBuildSnapshotCopiesLimits(t *testing.T) {
	caller := map[string]*Window{"five_hour": {UsedPercentage: ptrF(12)}}
	s := BuildSnapshot(Input{Now: time.Now(), ActiveSessions: 1, Limits: caller, StatusWindow: "five_hour"})

	delete(caller, "five_hour")
	if _, ok := s.Limits["five_hour"]; !ok {
		t.Fatal("BuildSnapshot aliased the caller's Limits map")
	}
	// The breakdown slice is copied too: mutating the caller's slice must not
	// change the snapshot.
	rows := []ModelRow{{Model: "devin-1"}}
	s = BuildSnapshot(Input{Now: time.Now(), ByModel: rows})
	rows[0].Model = "mutated"
	if s.Breakdown.ByModel[0].Model != "devin-1" {
		t.Fatalf("BuildSnapshot aliased ByModel: %+v", s.Breakdown.ByModel)
	}
}

func TestBuildSnapshotStatusWindowMissing(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	inactive := BuildSnapshot(Input{Now: now, Limits: map[string]*Window{"five_hour": {UsedPercentage: ptrF(85)}}})
	if inactive.Status.Code != CodeIndeterminate {
		t.Fatalf("no StatusWindow -> code %d, want %d", inactive.Status.Code, CodeIndeterminate)
	}
	if inactive.Status.Label != "no_active_session" {
		t.Fatalf("no StatusWindow without active sessions -> label %q, want no_active_session", inactive.Status.Label)
	}

	active := BuildSnapshot(Input{Now: now, ActiveSessions: 1})
	if active.Status.Code != CodeIndeterminate || active.Status.Label != "indeterminate" {
		t.Fatalf("no StatusWindow with an active session -> %+v, want indeterminate", active.Status)
	}

	// A StatusWindow key that is not present in Limits behaves like no window.
	missing := BuildSnapshot(Input{Now: now, ActiveSessions: 1, StatusWindow: "five_hour"})
	if missing.Status.Code != CodeIndeterminate {
		t.Fatalf("unknown StatusWindow -> code %d, want %d", missing.Status.Code, CodeIndeterminate)
	}
}

func TestBuildSnapshotLimitsNeverNil(t *testing.T) {
	s := BuildSnapshot(Input{Now: time.Now()})
	if s.Limits == nil {
		t.Fatal("Limits = nil, want an empty non-nil map")
	}
	if len(s.Limits) != 0 {
		t.Fatalf("len(Limits) = %d, want 0", len(s.Limits))
	}
	if s.Breakdown.ByModel == nil {
		t.Fatal("ByModel = nil, want an empty slice so JSON stays []")
	}
}

func TestBuildSnapshotGeneratedAtIsUTC(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	local := time.Date(2026, 9, 22, 18, 30, 0, 0, loc)
	s := BuildSnapshot(Input{Now: local})

	if s.GeneratedAt != "2026-09-22T10:30:00Z" {
		t.Fatalf("GeneratedAt = %q, want 2026-09-22T10:30:00Z", s.GeneratedAt)
	}
	parsed, err := time.Parse(time.RFC3339, s.GeneratedAt)
	if err != nil {
		t.Fatalf("GeneratedAt = %q is not RFC3339: %v", s.GeneratedAt, err)
	}
	if !parsed.Equal(local) {
		t.Fatalf("GeneratedAt = %s, want the same instant as %s", parsed, local)
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("GeneratedAt location = %v, want UTC", parsed.Location())
	}
}

func TestEncodeJSON(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour).Unix()
	used := ptrF(42.5)
	w := &Window{
		UsedPercentage: used,
		TokensUsed:     ptrI(183000),
		TokenLimit:     ptrI(430000),
		ACUUsed:        ptrF(12.3),
		ACULimit:       ptrF(50),
		ResetsAt:       ptrS(now.Add(2 * time.Hour).UTC().Format(time.RFC3339)),
		ResetsAtEpoch:  ptrI(reset),
		Source:         Source{Kind: SourceDevinDB},
		Confidence:     ConfidenceEstimate,
		Stale:          false,
		Pace:           pacePtr(ComputePace(used, ptrI(reset), DefaultWindowSeconds, now)),
		Forecast:       ForecastExhaustion(used, ptrI(reset), DefaultWindowSeconds, now),
	}
	s := BuildSnapshot(Input{
		Now:            now,
		TodayCost:      1.23,
		WeekCost:       4.56,
		MonthCost:      7.89,
		TotalCost:      12.34,
		TotalSessions:  19,
		TotalRequests:  29249,
		TotalTokens:    1234567,
		ActiveSessions: 1,
		CostProvenance: ConfidenceEstimate,
		ACU:            true,
		ByModel:        []ModelRow{{Model: "devin-1", InputTokens: 10, OutputTokens: 5, Requests: 3, Cost: 0.5, Share: 66.7}},
		Limits:         map[string]*Window{"five_hour": w},
		StatusWindow:   "five_hour",
	})

	b, err := EncodeJSON(s)
	if err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}
	if !json.Valid(b) {
		t.Fatalf("EncodeJSON produced invalid JSON:\n%s", b)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Fatal("EncodeJSON output does not end with a newline")
	}
	if !strings.Contains(string(b), "\n  \"schemaVersion\": 1") {
		t.Fatalf("EncodeJSON is not indented with two spaces:\n%s", b)
	}

	var back Snapshot
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, s) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", back, s)
	}
}

func TestRenderCompact(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	base := BuildSnapshot(Input{
		Now:            now,
		TodayCost:      1.23,
		TotalSessions:  19,
		TotalRequests:  29249,
		ActiveSessions: 1,
		Limits:         map[string]*Window{"five_hour": {UsedPercentage: ptrF(12)}},
		StatusWindow:   "five_hour",
	})

	t.Run("without a window", func(t *testing.T) {
		s := base
		s.Limits = nil
		got := RenderCompact(s)
		want := "devinmonitor ok · today $1.23 · 19 sess · 29249 req"
		if got != want {
			t.Fatalf("RenderCompact = %q, want %q", got, want)
		}
		assertSingleLine(t, got)
	})

	t.Run("with a window", func(t *testing.T) {
		// Build the reset instant in the local zone so the expected string is
		// stable no matter which timezone the test host runs in.
		reset := time.Date(2026, 9, 22, 14, 30, 0, 0, time.Local)
		s := base
		s.Status = StatusBlock{Code: CodeNearLimit, Label: "near_limit"}
		s.Limits = map[string]*Window{
			"five_hour": {
				UsedPercentage: ptrF(42),
				ResetsAtEpoch:  ptrI(reset.Unix()),
				Pace:           &Pace{Label: "on track"},
			},
		}
		got := RenderCompact(s)
		want := "devinmonitor near_limit · today $1.23 · 19 sess · 29249 req · 5h 42% on track (resets " +
			reset.Format("15:04") + ")"
		if got != want {
			t.Fatalf("RenderCompact = %q, want %q", got, want)
		}
		assertSingleLine(t, got)
	})

	t.Run("fraction, unknown pace and fallback reset string", func(t *testing.T) {
		s := base
		s.Limits = map[string]*Window{
			"custom_bucket": {
				UsedPercentage: ptrF(42.5),
				ResetsAt:       ptrS(time.Date(2026, 9, 22, 23, 5, 0, 0, time.Local).Format(time.RFC3339)),
				Pace:           &Pace{Label: "unknown"},
			},
		}
		got := RenderCompact(s)
		want := "devinmonitor ok · today $1.23 · 19 sess · 29249 req · custom_bucket 42.5% (resets 23:05)"
		if got != want {
			t.Fatalf("RenderCompact = %q, want %q", got, want)
		}
		assertSingleLine(t, got)
	})

	t.Run("window without a percentage is skipped", func(t *testing.T) {
		s := base
		s.Limits = map[string]*Window{"five_hour": {ResetsAtEpoch: ptrI(now.Add(time.Hour).Unix())}}
		if got := RenderCompact(s); strings.Contains(got, "resets") {
			t.Fatalf("RenderCompact = %q, want no window section", got)
		}
	})

	t.Run("hostile label cannot break the one-line contract", func(t *testing.T) {
		s := base
		s.Status.Label = "ok\n\x1b[31mred\rmore"
		got := RenderCompact(s)
		assertSingleLine(t, got)
		if !strings.HasPrefix(got, "devinmonitor ok") || !strings.Contains(got, "red") {
			t.Fatalf("RenderCompact = %q, want the printable remainder kept", got)
		}
	})
}

func assertSingleLine(t *testing.T, s string) {
	t.Helper()
	if strings.ContainsAny(s, "\n\r") {
		t.Fatalf("RenderCompact returned a newline: %q", s)
	}
	if strings.ContainsRune(s, 0x1b) {
		t.Fatalf("RenderCompact returned an ANSI escape: %q", s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("RenderCompact returned control character %q in %q", r, s)
		}
	}
}

func ptrS(v string) *string { return &v }

func pacePtr(p Pace) *Pace { return &p }

func equalFloatPtr(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func fmtPtr(p *float64) string {
	if p == nil {
		return "nil"
	}
	return strconvFloat(*p)
}

func fmtPtrStr(p *string) string {
	if p == nil {
		return "nil"
	}
	return strconvQuote(*p)
}

func strconvFloat(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func strconvQuote(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}
