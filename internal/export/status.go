package export

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/state"
)

// StatusSnapshot is a compact usage snapshot used by status-bar integrations.
type StatusSnapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	TodayCost   float64   `json:"today_cost"`
	MonthCost   float64   `json:"month_cost"`
	Sessions    int       `json:"sessions"`
}

// BuildStatusSnapshot computes today's and this month's cost plus total
// non-hidden session count.
func BuildStatusSnapshot(ss []model.Session) StatusSnapshot {
	now := time.Now()
	todayStart := model.DayStart(now)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	snap := StatusSnapshot{GeneratedAt: now}
	for _, s := range ss {
		if s.Hidden {
			continue
		}
		snap.Sessions++
		c := sessionCostCSV(&s)
		if !s.LastActivityAt.Before(todayStart) {
			snap.TodayCost += c
		}
		if !s.LastActivityAt.Before(monthStart) {
			snap.MonthCost += c
		}
	}
	return snap
}

// CompactStatus renders a single-line status: "Today: $X | Month: $Y | Sessions: Z".
func CompactStatus(snap StatusSnapshot) string {
	return fmt.Sprintf("Today: $%.2f | Month: $%.2f | Sessions: %d", snap.TodayCost, snap.MonthCost, snap.Sessions)
}

// ShellStatus renders just the today cost number (for PS1 integration).
func ShellStatus(snap StatusSnapshot) string {
	return fmt.Sprintf("%.2f", snap.TodayCost)
}

// FormatTitle renders a terminal title from a template with {cost} and
// {sessions} placeholders. {cost} = today's cost, {sessions} = total count.
func FormatTitle(snap StatusSnapshot, format string) string {
	out := strings.ReplaceAll(format, "{cost}", fmt.Sprintf("$%.2f", snap.TodayCost))
	out = strings.ReplaceAll(out, "{sessions}", fmt.Sprintf("%d", snap.Sessions))
	out = strings.ReplaceAll(out, "{month}", fmt.Sprintf("$%.2f", snap.MonthCost))
	return out
}

// SetTerminalTitle writes the OSC escape sequence to set the terminal title
// to the given string on stdout.
func SetTerminalTitle(w io.Writer, title string) {
	fmt.Fprintf(w, "\x1b]2;%s\x07", title)
}

// WriteState writes a status snapshot atomically to a state file (JSON).
// The parent directory is created if missing.
//
// The write is delegated to state.WriteAtomic, which uses a pid- and
// random-suffixed temp file. A fixed "<path>.tmp" name (the previous
// implementation) let two concurrent writers clobber each other's temp file,
// so a reader could observe a torn document.
func WriteState(snap StatusSnapshot, path string) error {
	if path == "" {
		return fmt.Errorf("state file path is empty")
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteAtomic(path, data)
}
