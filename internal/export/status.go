package export

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
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
func WriteState(snap StatusSnapshot, path string) error {
	if path == "" {
		return fmt.Errorf("state file path is empty")
	}
	if err := os.MkdirAll(parentDir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parentDir(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[:i]
	}
	return "."
}
