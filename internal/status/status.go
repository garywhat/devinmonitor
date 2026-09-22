// Package status implements devinmonitor's automation protocol: a
// machine-readable snapshot of usage and limits, provenance grading for every
// number, and semantic exit codes for scripts, CI jobs and statuslines.
//
// The package is deliberately dependency-free (stdlib only, no other
// internal/ packages) and pure: every function is a deterministic transform of
// its inputs, which is what makes the protocol testable and stable.
package status

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Confidence grades how trustworthy a number is. The vocabulary matches the
// existing provenance labels in internal/integration ("official" / "estimated")
// so the whole tool keeps one language for this concept.
type Confidence string

const (
	ConfidenceOfficial Confidence = "official"  // from the tool's own accounting
	ConfidenceEstimate Confidence = "estimated" // derived from token pricing
	ConfidenceUnknown  Confidence = "unknown"
)

// Source records where a number came from.
type Source struct {
	Kind string `json:"kind"`
}

const (
	SourceDevinDB    = "devin_db"
	SourceStatusline = "statusline"
	SourceConfig     = "config"
)

// Semantic exit codes (a protocol for scripts/CI). 0/10/11/20/30.
const (
	CodeOK            = 0
	CodeNearLimit     = 10
	CodeLimitHit      = 11
	CodeIndeterminate = 20
	CodeNoData        = 30
)

// LimitWarningThreshold is the fraction of the limit at which we warn.
const LimitWarningThreshold = 0.8

// DefaultWindowSeconds is the default limit window (5 hours).
const DefaultWindowSeconds int64 = 5 * 60 * 60

// PaceTolerancePoints is the percentage-point tolerance for pace grading.
const PaceTolerancePoints = 10.0

// SanitizePercent cleans an upstream percentage.
//   - non-finite (NaN/±Inf) -> nil
//   - negative             -> nil
//   - 100 < v <= 101       -> 100 (rounding artifact; clamp)
//   - v > 101              -> nil (e.g. an epoch leaked into the field)
//   - otherwise            -> v
func SanitizePercent(v float64) *float64 {
	f := SanitizeFinite(v)
	if f == nil || *f < 0 {
		return nil
	}
	// A value a hair above 100 is an upstream rounding artifact: clamping keeps
	// the real "at the limit" information instead of discarding it.
	if *f > 100 && *f <= 101 {
		hundred := 100.0
		return &hundred
	}
	// Anything further out is not a percentage at all: upstream has been seen
	// writing an epoch into this field. Drop it rather than render nonsense.
	if *f > 101 {
		return nil
	}
	return f
}

// SanitizeFinite returns nil for non-finite input, else a pointer to the value.
func SanitizeFinite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	out := v
	return &out
}

// Pace compares consumption against elapsed time in the window.
type Pace struct {
	Label             string   `json:"label"` // "on track" | "slow down" | "speed up" | "unknown"
	UsedPercentage    *float64 `json:"usedPercentage"`
	ElapsedPercentage *float64 `json:"elapsedPercentage"`
}

// paceLabelUnknown is the label used when pace cannot be graded.
const paceLabelUnknown = "unknown"

// ComputePace compares consumption against elapsed time in the window.
//
// The spec asked for this function to be named Pace, but Go has a single
// namespace per package block: a type Pace and a func Pace cannot coexist. The
// data type keeps the name Pace (it is what Window embeds and what JSON emits),
// and the derivation gets the Compute* verb already used by ComputeExitCode.
//
// Returns a Pace with Label "unknown" when usedPct or resetEpoch is nil, or
// when windowSeconds is not positive (there is no window to grade against, and
// dividing by it would panic). Otherwise windowStart = resetAt - windowSeconds,
// the elapsed ratio is clamped to [0,1], elapsedPct is rounded to 1 decimal and
// delta = usedPct - elapsedPct drives the label. The comparisons are strict, so
// a delta of exactly ±PaceTolerancePoints is still "on track".
func ComputePace(usedPct *float64, resetEpoch *int64, windowSeconds int64, now time.Time) Pace {
	if usedPct == nil || resetEpoch == nil || windowSeconds <= 0 {
		return Pace{Label: paceLabelUnknown}
	}

	resetAt := time.Unix(*resetEpoch, 0)
	windowStart := resetAt.Add(-time.Duration(windowSeconds) * time.Second)

	// Clamp: before the window starts no time has elapsed; after the reset the
	// window is spent and must not produce a ratio above 1.
	ratio := now.Sub(windowStart).Seconds() / float64(windowSeconds)
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}

	elapsedPct := round1(ratio * 100)
	delta := *usedPct - elapsedPct

	label := "on track"
	switch {
	case delta > PaceTolerancePoints:
		label = "slow down"
	case delta < -PaceTolerancePoints:
		label = "speed up"
	}

	return Pace{Label: label, UsedPercentage: usedPct, ElapsedPercentage: &elapsedPct}
}

// round1 rounds to one decimal place, the precision used by the protocol.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// Forecast is a local projection of when the limit will be exhausted.
type Forecast struct {
	ExhaustedAt *string `json:"exhaustedAt"` // RFC3339, nil when it will not exhaust
	Display     *string `json:"display"`     // e.g. "Today 14:30 (estimated)"
}

// ForecastExhaustion projects when the limit will be hit.
//
// Returns nil when it cannot be projected: missing inputs, a non-positive or
// non-finite usedPct, a window that already elapsed, a window that has not
// started yet (no rate to extrapolate), or an exhaustion instant at/after the
// reset (the window clears first, so the limit is never actually reached).
// Otherwise the projection is linear on the percentage consumed so far within
// the window.
func ForecastExhaustion(usedPct *float64, resetEpoch *int64, windowSeconds int64, now time.Time) *Forecast {
	if usedPct == nil || resetEpoch == nil || windowSeconds <= 0 {
		return nil
	}
	used := *usedPct
	if used <= 0 || math.IsNaN(used) || math.IsInf(used, 0) {
		return nil
	}

	resetAt := time.Unix(*resetEpoch, 0)
	if !now.Before(resetAt) {
		return nil // window already elapsed: nothing left to project
	}

	windowStart := resetAt.Add(-time.Duration(windowSeconds) * time.Second)
	elapsed := now.Sub(windowStart).Seconds()
	if elapsed <= 0 {
		return nil // window has not started: there is no burn rate yet
	}

	remaining := 100.0 - used
	if remaining < 0 {
		remaining = 0 // already over the limit: exhaustion is "now"
	}

	secondsToExhaust := remaining * elapsed / used
	if secondsToExhaust >= resetAt.Sub(now).Seconds() {
		return nil // the window resets before the limit would be hit
	}

	exhaustedAt := now.Add(time.Duration(secondsToExhaust * float64(time.Second)))
	stamp := exhaustedAt.UTC().Format(time.RFC3339)
	display := forecastDisplay(exhaustedAt, now)
	return &Forecast{ExhaustedAt: &stamp, Display: &display}
}

// forecastDisplay renders the projection for humans, in the caller's location.
// The "(estimated)" suffix is ALWAYS present: this is a local projection, never
// official data.
func forecastDisplay(exhaustedAt, now time.Time) string {
	loc := now.Location()
	ex := exhaustedAt.In(loc)
	switch civilDays(ex) - civilDays(now.In(loc)) {
	case 0:
		return "Today " + ex.Format("15:04") + " (estimated)"
	case 1:
		return "Tomorrow " + ex.Format("15:04") + " (estimated)"
	default:
		return ex.Format("2006-01-02 15:04") + " (estimated)"
	}
}

// civilDays maps an instant to its calendar day number, so day arithmetic is
// immune to DST transitions (a 23-hour "day" would break duration division).
func civilDays(t time.Time) int64 {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

// Window is one limit window (e.g. a five-hour or seven-day quota).
type Window struct {
	UsedPercentage *float64   `json:"usedPercentage"`
	TokensUsed     *int64     `json:"tokensUsed"`
	TokenLimit     *int64     `json:"tokenLimit"`
	ACUUsed        *float64   `json:"acuUsed"`
	ACULimit       *float64   `json:"acuLimit"`
	ResetsAt       *string    `json:"resetsAt"` // RFC3339
	ResetsAtEpoch  *int64     `json:"resetsAtEpoch"`
	Source         Source     `json:"source"`
	Confidence     Confidence `json:"confidence"`
	Stale          bool       `json:"stale"`
	Pace           *Pace      `json:"pace,omitempty"`
	Forecast       *Forecast  `json:"forecast,omitempty"`
}

// StatusBlock carries the derived status code and its protocol label.
type StatusBlock struct {
	Code  int    `json:"code"`
	Label string `json:"label"` // "ok" | "near_limit" | "limit_hit" | "indeterminate" | "no_active_session" | "no_data"
}

// ModelRow is one per-model usage row.
type ModelRow struct {
	Model            string  `json:"model"`
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	Requests         int     `json:"requests"`
	Cost             float64 `json:"cost"`
	Share            float64 `json:"share"` // % of (input+output) tokens, 1 decimal
}

// Breakdown aggregates the usage numbers behind the status.
type Breakdown struct {
	ByModel        []ModelRow `json:"byModel"`
	ACU            bool       `json:"acu"`
	CostProvenance Confidence `json:"costProvenance"`
	TodayCost      float64    `json:"todayCost"`
	WeekCost       float64    `json:"weekCost"`
	MonthCost      float64    `json:"monthCost"`
	TotalCost      float64    `json:"totalCost"`
	TotalSessions  int        `json:"totalSessions"`
	TotalRequests  int        `json:"totalRequests"`
	TotalTokens    int64      `json:"totalTokens"`
	ActiveSessions int        `json:"activeSessions"`
}

// SchemaVersion is the snapshot schema version; bump on breaking changes.
const SchemaVersion = 1

// Snapshot is the complete machine-readable state of the tool.
type Snapshot struct {
	SchemaVersion int                `json:"schemaVersion"`
	GeneratedAt   string             `json:"generatedAt"` // RFC3339 UTC
	Status        StatusBlock        `json:"status"`
	Limits        map[string]*Window `json:"limits"`
	Breakdown     Breakdown          `json:"breakdown"`
}

// ComputeExitCode implements the ordered decision for the semantic exit code.
//
// The order matters and must not change: only the first matching rule applies,
// so an explicit upstream overflow (limitHit) outranks the numeric value, and a
// missing percentage can never be reported as "ok".
//
//  1. usedPct == nil                           -> (20, no_active_session|indeterminate)
//  2. limitHit true OR *usedPct >= 100         -> (11, limit_hit)
//  3. *usedPct >= LimitWarningThreshold*100    -> (10, near_limit)
//  4. otherwise                                -> (0, ok)
func ComputeExitCode(hasActive bool, usedPct *float64, limitHit bool) (int, string) {
	// Rule 1: without a percentage there is nothing to grade.
	if usedPct == nil {
		if !hasActive {
			return CodeIndeterminate, "no_active_session"
		}
		return CodeIndeterminate, "indeterminate"
	}
	// Rule 2: an explicit overflow marker wins over the numeric value.
	if limitHit || *usedPct >= 100 {
		return CodeLimitHit, "limit_hit"
	}
	// Rule 3: warning band.
	if *usedPct >= LimitWarningThreshold*100 {
		return CodeNearLimit, "near_limit"
	}
	// Rule 4: nominal.
	return CodeOK, "ok"
}

// Input carries everything needed to assemble a snapshot.
type Input struct {
	Now            time.Time
	TodayCost      float64
	WeekCost       float64
	MonthCost      float64
	TotalCost      float64
	TotalSessions  int
	TotalRequests  int
	TotalTokens    int64
	ActiveSessions int
	CostProvenance Confidence
	ACU            bool
	ByModel        []ModelRow
	Limits         map[string]*Window

	// StatusWindow names the key in Limits that drives the status code.
	// Empty means no window drives it: status becomes 20/"" when no data.
	StatusWindow string
	LimitHit     bool
}

// BuildSnapshot assembles a Snapshot: it stamps SchemaVersion and GeneratedAt
// (in.Now, formatted RFC3339 in UTC), copies the breakdown, copies Limits into
// a non-nil map, and derives Status via ComputeExitCode using
// Limits[StatusWindow] (hasActive = in.ActiveSessions > 0).
func BuildSnapshot(in Input) Snapshot {
	limits := make(map[string]*Window, len(in.Limits))
	for k, v := range in.Limits {
		limits[k] = v
	}

	// Only the named window drives the status; a missing key is the same as no
	// window at all (usedPct stays nil -> code 20).
	var usedPct *float64
	if in.StatusWindow != "" {
		if w := limits[in.StatusWindow]; w != nil {
			usedPct = w.UsedPercentage
		}
	}
	code, label := ComputeExitCode(in.ActiveSessions > 0, usedPct, in.LimitHit)

	// Copy rather than alias the caller's slice, and keep it non-nil so the
	// JSON stays "byModel": [] instead of flipping between [] and null.
	byModel := make([]ModelRow, len(in.ByModel))
	copy(byModel, in.ByModel)

	return Snapshot{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   in.Now.UTC().Format(time.RFC3339),
		Status:        StatusBlock{Code: code, Label: label},
		Limits:        limits,
		Breakdown: Breakdown{
			ByModel:        byModel,
			ACU:            in.ACU,
			CostProvenance: in.CostProvenance,
			TodayCost:      in.TodayCost,
			WeekCost:       in.WeekCost,
			MonthCost:      in.MonthCost,
			TotalCost:      in.TotalCost,
			TotalSessions:  in.TotalSessions,
			TotalRequests:  in.TotalRequests,
			TotalTokens:    in.TotalTokens,
			ActiveSessions: in.ActiveSessions,
		},
	}
}

// EncodeJSON renders the snapshot as indented JSON (2 spaces) with a trailing
// newline, so it is stable and diff-friendly.
func EncodeJSON(s Snapshot) ([]byte, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("status: encode snapshot: %w", err)
	}
	return append(b, '\n'), nil
}

// RenderCompact renders a strict ONE-LINE, ANSI-free summary suitable for a
// shell/tmux statusline:
//
//	devinmonitor ok · today $1.23 · 19 sess · 29249 req
//
// and when a usable window exists it appends " · 5h 42% on track (resets 14:30)".
// The returned string contains no escape characters and no newline.
func RenderCompact(s Snapshot) string {
	var b strings.Builder
	b.WriteString("devinmonitor")
	if s.Status.Label != "" {
		b.WriteString(" ")
		b.WriteString(s.Status.Label)
	}
	fmt.Fprintf(&b, " · today $%.2f · %d sess · %d req",
		s.Breakdown.TodayCost, s.Breakdown.TotalSessions, s.Breakdown.TotalRequests)

	if key, w := compactWindow(s.Limits); w != nil {
		b.WriteString(" · ")
		b.WriteString(compactWindowText(key, w))
	}

	return sanitizeSingleLine(b.String())
}

// compactWindow picks the window shown on the statusline: a window with an
// actual percentage, preferring the short (5h) quota over the long ones, then
// the lexicographically smallest key so the choice is deterministic despite Go's
// randomized map iteration.
func compactWindow(limits map[string]*Window) (string, *Window) {
	bestKey := ""
	bestScore := -1
	var best *Window
	for k, w := range limits {
		if w == nil || w.UsedPercentage == nil {
			continue
		}
		score := windowKeyScore(k)
		if score > bestScore || (score == bestScore && (bestKey == "" || k < bestKey)) {
			bestKey, bestScore, best = k, score, w
		}
	}
	return bestKey, best
}

// windowKeyScore ranks known window keys so the most relevant one wins.
func windowKeyScore(key string) int {
	switch key {
	case "five_hour", "five_hours", "5h":
		return 4
	case "daily", "day", "1d":
		return 3
	case "seven_day", "seven_days", "weekly", "week", "7d":
		return 2
	case "monthly", "month", "30d":
		return 1
	default:
		return 0
	}
}

// shortWindowLabel keeps the statusline narrow for the well-known windows.
func shortWindowLabel(key string) string {
	switch key {
	case "five_hour", "five_hours", "5h":
		return "5h"
	case "daily", "day", "1d":
		return "1d"
	case "seven_day", "seven_days", "weekly", "week", "7d":
		return "7d"
	case "monthly", "month", "30d":
		return "30d"
	case "":
		return "limit"
	default:
		return key
	}
}

// compactWindowText renders one window as e.g. "5h 42.5% on track (resets 14:30)".
func compactWindowText(key string, w *Window) string {
	var b strings.Builder
	b.WriteString(shortWindowLabel(key))
	if w.UsedPercentage != nil {
		// -1 precision drops the trailing ".0" of whole percentages.
		b.WriteString(" " + strconv.FormatFloat(*w.UsedPercentage, 'f', -1, 64) + "%")
	}
	if w.Pace != nil && w.Pace.Label != "" && w.Pace.Label != paceLabelUnknown {
		b.WriteString(" " + w.Pace.Label)
	}
	if t, ok := windowResetTime(w); ok {
		b.WriteString(" (resets " + t.Format("15:04") + ")")
	}
	return b.String()
}

// windowResetTime returns the window reset instant in the local timezone, from
// the epoch when present and falling back to the RFC3339 string.
func windowResetTime(w *Window) (time.Time, bool) {
	if w.ResetsAtEpoch != nil {
		return time.Unix(*w.ResetsAtEpoch, 0).Local(), true
	}
	if w.ResetsAt != nil {
		if t, err := time.Parse(time.RFC3339, *w.ResetsAt); err == nil {
			return t.Local(), true
		}
	}
	return time.Time{}, false
}

// sanitizeSingleLine enforces RenderCompact's contract at any cost: a
// statusline is embedded into a shell prompt, so a stray newline or escape
// sequence from upstream data must never be able to corrupt the terminal.
func sanitizeSingleLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
