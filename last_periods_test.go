package main

import (
	"testing"

	"github.com/garywhat/devinmonitor/internal/report"
)

// TestLastPeriods pins the --last semantics. buildTimeBuckets sorts buckets by
// label ascending, so "the most recent N" is the tail of the slice — reversing
// that would silently report the OLDEST periods while looking plausible.
func TestLastPeriods(t *testing.T) {
	rows := []report.TimeRow{
		{Label: "2026-09-01"}, {Label: "2026-09-02"}, {Label: "2026-09-03"},
		{Label: "2026-09-04"}, {Label: "2026-09-05"},
	}
	labels := func(rs []report.TimeRow) string {
		out := ""
		for i, r := range rs {
			if i > 0 {
				out += ","
			}
			out += r.Label
		}
		return out
	}

	cases := []struct {
		name string
		n    int
		want string
	}{
		{"zero means no limit", 0, "2026-09-01,2026-09-02,2026-09-03,2026-09-04,2026-09-05"},
		{"negative means no limit", -1, "2026-09-01,2026-09-02,2026-09-03,2026-09-04,2026-09-05"},
		{"more than available is no limit", 99, "2026-09-01,2026-09-02,2026-09-03,2026-09-04,2026-09-05"},
		{"exactly the length is no limit", 5, "2026-09-01,2026-09-02,2026-09-03,2026-09-04,2026-09-05"},
		// The tail, i.e. the NEWEST buckets, not the oldest.
		{"one keeps the newest", 1, "2026-09-05"},
		{"three keeps the newest three", 3, "2026-09-03,2026-09-04,2026-09-05"},
		{"four keeps the newest four", 4, "2026-09-02,2026-09-03,2026-09-04,2026-09-05"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := labels(lastPeriods(rows, c.n)); got != c.want {
				t.Errorf("lastPeriods(n=%d) = %q, want %q", c.n, got, c.want)
			}
		})
	}

	t.Run("empty input", func(t *testing.T) {
		if got := lastPeriods(nil, 3); len(got) != 0 {
			t.Errorf("lastPeriods(nil, 3) returned %d rows", len(got))
		}
	})

	t.Run("does not mutate or alias the input beyond the tail", func(t *testing.T) {
		// The result shares the backing array with the input, which is fine for
		// a read-only report path, but the input's own length must not change.
		before := len(rows)
		_ = lastPeriods(rows, 2)
		if len(rows) != before {
			t.Errorf("lastPeriods changed the input length from %d to %d", before, len(rows))
		}
	})
}
