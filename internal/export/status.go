package export

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/state"
	"github.com/garywhat/devinmonitor/internal/status"
)

// StatusSnapshot is a compact usage snapshot used by status-bar integrations.
//
// Each cost figure carries the basis it was produced with, and the snapshot as
// a whole carries the combined basis. The per-figure fields exist because the
// two periods need not agree: today can be pure ACU billing while the month
// total already mixes in token-estimated sessions, and a single basis field
// would have to pick one of them and be wrong about the other.
type StatusSnapshot struct {
	GeneratedAt time.Time `json:"generated_at"`
	TodayCost   float64   `json:"today_cost"`
	MonthCost   float64   `json:"month_cost"`
	Sessions    int       `json:"sessions"`
	// TodayBasis is the meter behind TodayCost, MonthBasis the meter behind
	// MonthCost, and CostBasis the combined basis of the figures in this
	// snapshot (mixed whenever the two differ).
	TodayBasis status.CostBasis `json:"today_basis"`
	MonthBasis status.CostBasis `json:"month_basis"`
	CostBasis  status.CostBasis `json:"cost_basis"`
}

// BuildStatusSnapshot computes today's and this month's cost plus total
// non-hidden session count, and labels each figure with its cost basis.
func BuildStatusSnapshot(ss []model.Session) StatusSnapshot {
	now := time.Now()
	todayStart := model.DayStart(now)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	snap := StatusSnapshot{GeneratedAt: now}

	var today, month, all []status.CostBasis
	visible := make([]model.Session, 0, len(ss))
	for _, s := range ss {
		if s.Hidden {
			continue
		}
		visible = append(visible, s)
		snap.Sessions++
		c := sessionCostCSV(&s)
		b := SessionCostBasis(&s)
		all = append(all, b)
		if !s.LastActivityAt.Before(todayStart) {
			snap.TodayCost += c
			today = append(today, b)
		}
		if !s.LastActivityAt.Before(monthStart) {
			snap.MonthCost += c
			month = append(month, b)
		}
	}
	snap.TodayBasis = status.CombineCostBasis(today...)
	snap.MonthBasis = status.CombineCostBasis(month...)
	snap.CostBasis = status.CombineCostBasis(append(append(today, month...), all...)...)
	return snap
}

// CompactStatus renders a single-line status:
// "Today: $X [acu] | Month: $Y [token est] | Sessions: Z".
//
// The tag rides on the figure rather than on the line so that a reader who
// copies one number out of it still has the unit; and when the bases differ or
// are mixed, the one line says so instead of presenting two incomparable
// quantities as a pair of peers.
func CompactStatus(snap StatusSnapshot) string {
	out := fmt.Sprintf("Today: $%.2f %s | Month: $%.2f %s | Sessions: %d",
		snap.TodayCost, status.NormalizeCostBasis(snap.TodayBasis).Tag(),
		snap.MonthCost, status.NormalizeCostBasis(snap.MonthBasis).Tag(),
		snap.Sessions)
	if warn := mixedBasisWarning(snap); warn != "" {
		out += " | " + warn
	}
	return out
}

// mixedBasisWarning is the short in-line warning for a one-line surface, and ""
// when the figures in the snapshot share a single basis.
//
// The rule is: warn when the reader could otherwise compare two numbers that
// are not the same kind of quantity — i.e. when the snapshot's figures span
// both bases, or when any of them is itself mixed.
func mixedBasisWarning(snap StatusSnapshot) string {
	t, m := status.NormalizeCostBasis(snap.TodayBasis), status.NormalizeCostBasis(snap.MonthBasis)
	if t == m && !t.IsMixed() {
		return ""
	}
	return "MIXED UNITS: acu and token-estimated costs are not comparable"
}

// FullStatus renders the default status block: the same three figures as
// CompactStatus, one per line, plus when the snapshot was taken and the legend
// that makes the basis tags readable.
//
// It exists so that --compact has something to be compact ABOUT. Before it, the
// flag's branch and the default branch called CompactStatus, so `status` and
// `status --compact` printed byte-identical output and the documented
// "single-line status" distinction did not exist.
func FullStatus(snap StatusSnapshot) string {
	lines := []string{
		fmt.Sprintf("Generated: %s", snap.GeneratedAt.Format("2006-01-02 15:04:05")),
		fmt.Sprintf("Today:     $%.2f %s", snap.TodayCost, status.NormalizeCostBasis(snap.TodayBasis).Tag()),
		fmt.Sprintf("Month:     $%.2f %s", snap.MonthCost, status.NormalizeCostBasis(snap.MonthBasis).Tag()),
		fmt.Sprintf("Sessions:  %d", snap.Sessions),
	}
	// The legend is the part that stops the comparison: the tag names the meter,
	// the sentence says the meters are not convertible. It is printed against
	// the COMBINED basis so a mixed snapshot gets the mixed warning.
	lines = append(lines, status.NormalizeCostBasis(snap.CostBasis).Legend())
	return strings.Join(lines, "\n")
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
