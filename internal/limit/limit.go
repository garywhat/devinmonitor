// Package limit groups token usage into billing windows ("blocks") — the
// rolling 5-hour windows popularized by Claude Code — so the CLI can answer
// "how much have I burned this window, at what rate, and will I hit the limit".
//
// This is a faithful port of the reference implementation cross-checked with
// ccusage (rust/crates/ccusage/src/blocks.rs). Behavior, including the
// deliberately strict comparisons used to split windows, is kept identical.
//
// The package never reads the wall clock: every function that needs "now"
// takes it as a parameter, which keeps callers (and tests) deterministic.
package limit

import (
	"math"
	"sort"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// DefaultWindow is the conventional billing window length.
const DefaultWindow = 5 * time.Hour

// WarningThreshold is the share of a token limit at which a window is "near".
const WarningThreshold = 0.8

// TokenCounts holds a token breakdown.
type TokenCounts struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// Total is the cache-inclusive total.
func (t TokenCounts) Total() int64 {
	return t.Input + t.Output + t.CacheRead + t.CacheWrite
}

// NonCache is input+output, the figure used for the burn-rate indicator
// (cache reads dominate raw totals and are not new work).
func (t TokenCounts) NonCache() int64 {
	return t.Input + t.Output
}

// Entry is one usage-bearing event: an assistant message that carried metrics.
type Entry struct {
	Time       time.Time
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Cost       float64
}

// FromSessions flattens sessions into usage-bearing entries sorted ascending by
// Time.
//
// Only messages with a non-nil Metrics contribute: system/user/tool messages
// carry no token usage of their own, and in real data only ~57% of messages have
// metrics (exactly the assistant turns). Cost is the pricing estimate for that
// message, using the message's generation model falling back to the session's
// LatestModel and then its Model when GenerationModel is empty.
func FromSessions(ss []model.Session) []Entry {
	var out []Entry
	for _, s := range ss {
		for i := range s.Messages {
			msg := &s.Messages[i]
			if msg.Metrics == nil {
				continue
			}
			msgModel := msg.GenerationModel
			if msgModel == "" {
				msgModel = s.LatestModel
			}
			if msgModel == "" {
				msgModel = s.Model
			}
			in := msg.Metrics.InputTokens
			o := msg.Metrics.OutputTokens
			cr := msg.Metrics.CacheReadTokens
			cw := msg.Metrics.CacheWriteTokens
			out = append(out, Entry{
				Time:       msg.CreatedAt,
				Model:      msgModel,
				Input:      in,
				Output:     o,
				CacheRead:  cr,
				CacheWrite: cw,
				Cost:       model.EstimateCost(model.LookupPricing(msgModel), in, o, cr, cw),
			})
		}
	}
	// Callers may hand us sessions in any order (and a session's messages in
	// any order), so sort here rather than assuming it upstream.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Time.Before(out[j].Time)
	})
	return out
}

// Block is one billing window.
type Block struct {
	ID            string    // RFC3339 of StartTime; gap blocks are "gap-<RFC3339>"
	StartTime     time.Time // window start, floored to the hour
	EndTime       time.Time // StartTime + window
	ActualEndTime time.Time // last entry time; zero for gap blocks
	IsActive      bool
	IsGap         bool
	Entries       []Entry
	Tokens        TokenCounts
	Cost          float64
	Models        []string // unique, in first-seen order
}

// Identify groups entries into consecutive billing windows.
//
// A new window starts when an entry is more than window past the window start
// OR more than window past the previous entry. Both comparisons are strictly
// greater-than, so an entry landing exactly on a boundary stays in the current
// window. When the entry is more than window past the previous entry, the idle
// span between them is itself reported as a gap block.
//
// window <= 0 is treated as DefaultWindow. An empty input yields nil.
func Identify(entries []Entry, window time.Duration, now time.Time) []Block {
	if len(entries) == 0 {
		return nil
	}
	if window <= 0 {
		window = DefaultWindow
	}
	// Stable sort: entries sharing a timestamp keep their input order.
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Time.Before(sorted[j].Time)
	})

	var blocks []Block
	var currentStart time.Time
	haveStart := false
	var current []Entry

	for _, e := range sorted {
		if !haveStart {
			currentStart = floorToHour(e.Time)
			haveStart = true
		} else {
			sinceStart := e.Time.Sub(currentStart)
			lastTime := current[len(current)-1].Time
			sinceLast := e.Time.Sub(lastTime)
			if sinceStart > window || sinceLast > window {
				blocks = append(blocks, createBlock(currentStart, current, now, window))
				if sinceLast > window {
					blocks = append(blocks, createGapBlock(lastTime, e.Time, window))
				}
				currentStart = floorToHour(e.Time)
				current = nil
			}
		}
		current = append(current, e)
	}
	if haveStart && len(current) > 0 {
		blocks = append(blocks, createBlock(currentStart, current, now, window))
	}
	return blocks
}

// createBlock materializes one window from its entries. entries must be
// non-empty.
func createBlock(start time.Time, entries []Entry, now time.Time, window time.Duration) Block {
	end := start.Add(window)
	actualEnd := entries[len(entries)-1].Time

	var tokens TokenCounts
	var cost float64
	var models []string
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		tokens.Input += e.Input
		tokens.Output += e.Output
		tokens.CacheRead += e.CacheRead
		tokens.CacheWrite += e.CacheWrite
		cost += e.Cost
		if e.Model != "" && !seen[e.Model] {
			seen[e.Model] = true
			models = append(models, e.Model)
		}
	}

	return Block{
		ID:        start.UTC().Format(time.RFC3339),
		StartTime: start,
		EndTime:   end,
		// ActualEndTime is the last entry time, not the window end: it is what
		// the activity check below measures against.
		ActualEndTime: actualEnd,
		// Both halves matter: the elapsed-since-last-activity check keeps a
		// window from staying "active" forever when the clock merely hasn't
		// passed its end, and the EndTime check pins the hard ceiling.
		IsActive: now.Sub(actualEnd) < window && now.Before(end),
		IsGap:    false,
		Entries:  entries,
		Tokens:   tokens,
		Cost:     cost,
		Models:   models,
	}
}

// createGapBlock describes the idle span between two windows: it runs from one
// window length after the last entry of the previous block up to the first
// entry of the next one. Gap blocks carry no usage.
func createGapBlock(last, next time.Time, window time.Duration) Block {
	// Normalise to UTC exactly like createBlock does. Entry times arrive in the
	// database's own zone (time.Unix yields Local), so without this a gap's
	// StartTime would serialize with a different offset than its own ID and
	// than every real block's StartTime.
	start := last.Add(window).UTC()
	end := next.UTC()
	return Block{
		ID:            "gap-" + start.Format(time.RFC3339),
		StartTime:     start,
		EndTime:       end,
		ActualEndTime: time.Time{},
		IsActive:      false,
		IsGap:         true,
	}
}

// floorToHour truncates t to the top of its hour in UTC, and the block times it
// produces stay in UTC: window boundaries must be deterministic regardless of
// the machine's time zone. A zone whose whole-hour offset differs from UTC
// still displays as an on-the-hour local boundary (e.g. UTC+8 21:00 for the
// 13:00Z block), so this costs no readability in practice.
func floorToHour(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), 0, 0, 0, time.UTC)
}

// Active returns a pointer to the active, non-gap block, or nil when none is
// active. The pointer aliases the slice element, so callers can read it without
// copying the block.
func Active(blocks []Block) *Block {
	for i := range blocks {
		if blocks[i].IsActive && !blocks[i].IsGap {
			return &blocks[i]
		}
	}
	return nil
}

// BurnRate is the rate of consumption inside a window.
type BurnRate struct {
	// TokensPerMinute is the cache-inclusive rate.
	TokensPerMinute float64
	// TokensPerMinuteDisplay is the non-cache (input+output) rate, the figure
	// the indicator shows: cache reads dominate raw totals without being new
	// work, so the display deliberately excludes them.
	TokensPerMinuteDisplay float64
	CostPerHour            float64
}

// BurnRateOf returns nil for gap blocks, empty blocks, or when the first and
// last entries are the same instant (no elapsed time to divide by).
func BurnRateOf(b *Block) *BurnRate {
	if b == nil || b.IsGap || len(b.Entries) == 0 {
		return nil
	}
	first := b.Entries[0].Time
	last := b.Entries[len(b.Entries)-1].Time
	minutes := last.Sub(first).Minutes()
	if minutes <= 0 {
		return nil
	}
	return &BurnRate{
		TokensPerMinute:        float64(b.Tokens.Total()) / minutes,
		TokensPerMinuteDisplay: float64(b.Tokens.NonCache()) / minutes,
		CostPerHour:            b.Cost / minutes * 60,
	}
}

// Projection estimates the final usage of an active window.
type Projection struct {
	TotalTokens      int64
	TotalCost        float64
	RemainingMinutes int64
}

// Project returns nil unless the block is active and non-gap AND has a usable
// burn rate. It extrapolates linearly: current totals plus burn rate times the
// minutes remaining until EndTime. RemainingMinutes is clamped at 0.
func Project(b *Block, now time.Time) *Projection {
	if b == nil || b.IsGap || !b.IsActive {
		return nil
	}
	rate := BurnRateOf(b)
	if rate == nil {
		return nil
	}
	// Clamped so that a block flagged active but already past its end (or a
	// caller-supplied "now" ahead of the window) yields 0, never a negative.
	remaining := b.EndTime.Sub(now).Minutes()
	if remaining < 0 {
		remaining = 0
	}
	return &Projection{
		TotalTokens:      b.Tokens.Total() + int64(math.Round(rate.TokensPerMinute*remaining)),
		TotalCost:        b.Cost + rate.CostPerHour*remaining/60,
		RemainingMinutes: int64(math.Round(remaining)),
	}
}

// UsedPercent is the share of a token limit the block has consumed, rounded to
// one decimal and clamped to [0,100]. Returns nil when tokenLimit <= 0.
func UsedPercent(b *Block, tokenLimit int64) *float64 {
	if b == nil || tokenLimit <= 0 {
		return nil
	}
	pct := float64(b.Tokens.Total()) / float64(tokenLimit) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	pct = math.Round(pct*10) / 10
	return &pct
}
