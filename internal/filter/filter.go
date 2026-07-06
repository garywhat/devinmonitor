// Package filter provides session filtering, sorting, and pinned-session
// helpers for the filter/search/export feature group (WT4).
//
// These are pure functions over []model.Session so they can be reused by
// CLI commands, the TUI, and export routines without touching the reader.
package filter

import (
	"sort"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// Apply runs the in-memory portion of FilterOptions over a session slice:
// project substring (with optional exclude), mode, and date range.
// Model and search-text matching are handled by reader.FilteredSessions
// (which has DB-level access); this helper covers the post-load cases that
// the CLI sort/pinned pipeline needs.
func Apply(ss []model.Session, opts model.FilterOptions) []model.Session {
	var out []model.Session
	for _, s := range ss {
		if s.Hidden {
			continue
		}
		if opts.Mode != "" && s.AgentMode != opts.Mode {
			continue
		}
		if opts.Project != "" && !strings.Contains(s.WorkingDir, opts.Project) {
			continue
		}
		if !opts.FromDate.IsZero() && s.CreatedAt.Before(opts.FromDate) {
			continue
		}
		if !opts.ToDate.IsZero() && s.CreatedAt.After(opts.ToDate) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ExcludeProjects removes sessions whose working directory matches any of the
// comma-separated exclude patterns.
func ExcludeProjects(ss []model.Session, exclude string) []model.Session {
	if exclude == "" {
		return ss
	}
	patterns := splitCSV(exclude)
	var out []model.Session
	for _, s := range ss {
		drop := false
		for _, p := range patterns {
			if p == "" {
				continue
			}
			if strings.Contains(s.WorkingDir, p) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, s)
		}
	}
	return out
}

// SortBy reorders sessions by the given field. Supported values:
// "cost", "tokens", "context", "duration", "recent". Unknown values fall
// back to "recent". desc controls ascending/descending.
func SortBy(ss []model.Session, sortBy string, desc bool) {
	less := func(i, j int) bool { return false }
	switch sortBy {
	case "cost":
		less = func(i, j int) bool {
			return sessionCost(&ss[i]) < sessionCost(&ss[j])
		}
	case "tokens":
		less = func(i, j int) bool {
			return totalTokens(&ss[i]) < totalTokens(&ss[j])
		}
	case "context":
		less = func(i, j int) bool {
			return contextSize(&ss[i]) < contextSize(&ss[j])
		}
	case "duration":
		less = func(i, j int) bool {
			return ss[i].LastActivityAt.Sub(ss[i].CreatedAt) < ss[j].LastActivityAt.Sub(ss[j].CreatedAt)
		}
	case "recent", "":
		less = func(i, j int) bool {
			return ss[i].LastActivityAt.Before(ss[j].LastActivityAt)
		}
	}
	if desc {
		sort.SliceStable(ss, func(i, j int) bool { return less(j, i) })
	} else {
		sort.SliceStable(ss, less)
	}
}

// PinnedFirst partitions sessions into pinned (preserving config order) then
// the rest. pinnedIDs is the ordered list from config.Global().PinnedSessions.
func PinnedFirst(ss []model.Session, pinnedIDs []string) []model.Session {
	if len(pinnedIDs) == 0 {
		return ss
	}
	pinnedSet := map[string]int{}
	for i, id := range pinnedIDs {
		pinnedSet[id] = i
	}
	var pinned, rest []model.Session
	for _, s := range ss {
		if _, ok := pinnedSet[s.ID]; ok {
			pinned = append(pinned, s)
		} else {
			rest = append(rest, s)
		}
	}
	sort.SliceStable(pinned, func(i, j int) bool {
		return pinnedSet[pinned[i].ID] < pinnedSet[pinned[j].ID]
	})
	return append(pinned, rest...)
}

// IsPinned reports whether id is in the pinned list.
func IsPinned(id string, pinnedIDs []string) bool {
	for _, p := range pinnedIDs {
		if p == id {
			return true
		}
	}
	return false
}

// AddPin appends id to pinnedIDs if not already present. Returns the new list.
func AddPin(pinnedIDs []string, id string) []string {
	if IsPinned(id, pinnedIDs) {
		return pinnedIDs
	}
	return append(pinnedIDs, id)
}

// RemovePin removes id from pinnedIDs. Returns the new list.
func RemovePin(pinnedIDs []string, id string) []string {
	var out []string
	for _, p := range pinnedIDs {
		if p != id {
			out = append(out, p)
		}
	}
	return out
}

// ---- helpers ----

func sessionCost(s *model.Session) float64 {
	if s.CreditCost > 0 || s.ACUCost > 0 {
		return s.CreditCost + s.ACUCost
	}
	p := model.LookupPricing(s.Model)
	return model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
}

func totalTokens(s *model.Session) int64 {
	return s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
}

// contextSize returns the largest known context window size for the session,
// falling back to total tokens when per-message context is unavailable.
func contextSize(s *model.Session) int64 {
	var max int
	for _, m := range s.Messages {
		if m.NumTokensPreceding > max {
			max = m.NumTokensPreceding
		}
	}
	if max > 0 {
		return int64(max)
	}
	return totalTokens(s)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseDate parses a YYYY-MM-DD date string as a local midnight time.Time.
// Returns the zero time and an error if parsing fails.
func ParseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}
