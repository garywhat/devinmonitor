package ui

import (
	"regexp"
	"strings"
	"testing"
)

// TestHideColumnsDropsTheColumn covers the --no-cost mechanism: a global flag
// cannot reach a dozen independent table builders, so the suppression happens
// in the one place every table passes through, keyed by header text.
func TestHideColumnsDropsTheColumn(t *testing.T) {
	defer HideColumns()

	build := func() string {
		return NewTable("Model", "Requests", "Cost").
			Row("swe-1-7", "3", "$1.05").
			TotalRow("TOTAL", "3", "$1.05").
			String()
	}

	t.Run("nothing hidden renders every column", func(t *testing.T) {
		HideColumns()
		out := build()
		for _, want := range []string{"Model", "Requests", "Cost", "$1.05"} {
			if !strings.Contains(out, want) {
				t.Errorf("output is missing %q\n%s", want, out)
			}
		}
	})

	t.Run("the hidden column and its values disappear", func(t *testing.T) {
		HideColumns("Cost")
		out := build()
		if strings.Contains(out, "Cost") {
			t.Errorf("the Cost header survived:\n%s", out)
		}
		// The VALUES must go too, not just the header — a column left with its
		// data and no heading would be worse than not hiding anything.
		if strings.Contains(out, "$1.05") {
			t.Errorf("a cost value survived:\n%s", out)
		}
		if !strings.Contains(out, "Model") || !strings.Contains(out, "swe-1-7") {
			t.Errorf("other columns were removed as well:\n%s", out)
		}
	})

	t.Run("the header must match exactly", func(t *testing.T) {
		HideColumns("Cost")
		out := NewTable("Cost Center", "Cost").Row("a", "b").String()
		// Compare header CELLS rather than substrings: "│ Cost Center │"
		// contains "│ Cost ", so a substring assertion here tests the test, not
		// the behaviour.
		cells := headerCells(out)
		if len(cells) != 1 {
			t.Fatalf("got %d header cells %v, want 1 (only 'Cost Center')", len(cells), cells)
		}
		if cells[0] != "Cost Center" {
			t.Errorf("surviving header = %q, want %q", cells[0], "Cost Center")
		}
	})

	t.Run("hiding everything yields nothing rather than an empty box", func(t *testing.T) {
		HideColumns("Model", "Requests", "Cost")
		if got := build(); got != "" {
			t.Errorf("a table with no visible columns rendered %q, want empty", got)
		}
	})

	t.Run("passing no names clears the suppression", func(t *testing.T) {
		HideColumns("Cost")
		HideColumns()
		if got := HiddenColumns(); len(got) != 0 {
			t.Errorf("HiddenColumns() = %v after clearing, want empty", got)
		}
		if got := build(); !strings.Contains(got, "Cost") {
			t.Errorf("the column did not come back after clearing:\n%s", got)
		}
	})

	t.Run("empty names are ignored", func(t *testing.T) {
		HideColumns("", "Cost", "")
		if got := HiddenColumns(); len(got) != 1 || got[0] != "Cost" {
			t.Errorf("HiddenColumns() = %v, want [Cost]", got)
		}
	})

	t.Run("the original table is not modified", func(t *testing.T) {
		HideColumns()
		tb := NewTable("Model", "Cost").Row("a", "b")
		HideColumns("Cost")
		_ = tb.String()
		// A second render with the suppression cleared must show both columns,
		// which proves the projection used a copy rather than mutating the
		// builder in place.
		HideColumns()
		if out := tb.String(); !strings.Contains(out, "Cost") || !strings.Contains(out, "b") {
			t.Errorf("the table was mutated by a suppressed render:\n%s", out)
		}
	})
}

// TestHideColumnsLeavesAlignmentSane guards the projected right-align slice: if
// it were not projected in step with the headers, the remaining numeric columns
// would flip to left alignment.
func TestHideColumnsLeavesAlignmentSane(t *testing.T) {
	defer HideColumns()
	HideColumns()
	full := NewTable("Model", "Cost", "Requests").RightAlign(1, 2).Row("a", "1", "2").String()
	HideColumns("Cost")
	trimmed := NewTable("Model", "Cost", "Requests").RightAlign(1, 2).Row("a", "1", "2").String()
	if full == trimmed {
		t.Fatal("hiding a column changed nothing; the test is not exercising the path")
	}
	// Requests becomes the last column; it is still right-aligned, so the row
	// must end with padding before the border rather than hugging it.
	for _, line := range strings.Split(trimmed, "\n") {
		if strings.Contains(line, "2 │") && !strings.Contains(line, "  2 │") {
			t.Errorf("the right alignment was lost for the column after the hidden one:\n%s", line)
		}
	}
}

// headerCells returns the trimmed cell values of a rendered table's header row,
// so an assertion can compare columns rather than substrings.
func headerCells(rendered string) []string {
	for _, line := range strings.Split(rendered, "\n") {
		if !strings.HasPrefix(line, "│") {
			continue
		}
		inner := strings.Trim(line, "│")
		parts := strings.Split(inner, "│")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			out = append(out, strings.TrimSpace(strings.ReplaceAll(p, "\x1b", "")))
		}
		// Strip any residual ANSI sequences before comparing.
		for i, c := range out {
			out[i] = ansiStrip(c)
		}
		return out
	}
	return nil
}

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

func ansiStrip(s string) string { return ansiSeq.ReplaceAllString(s, "") }
