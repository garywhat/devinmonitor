package live

import (
	"strings"
	"testing"

	"github.com/garywhat/devinmonitor/internal/ui"
)

// The panel border is drawn with hand-written escapes rather than through
// lipgloss, because lipgloss strips ANSI from a pure-symbol string when stdout
// is not a TTY and the border would lose its colour. That workaround is
// precisely why the escapes used to leak: emitting them unconditionally
// bypassed the shared colour decision, so `live --once | cat` carried 28 escape
// sequences and NO_COLOR had no effect on them at all.
//
// These tests pin both halves: silent when colour is off, unchanged when it is
// on. ui.ColorEnabled reads DEVINMONITOR_COLOR, so the mode is controllable
// without a terminal.

func TestPanelEmitsNoEscapesWhenColourIsDisabled(t *testing.T) {
	t.Setenv("DEVINMONITOR_COLOR", "never")
	if ui.ColorEnabled() {
		t.Fatal("DEVINMONITOR_COLOR=never did not disable colour; the test cannot prove anything")
	}
	out := renderPanelFor(t)
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("panel emitted an escape with colour disabled:\n%q", out)
	}
	// The border itself must still be drawn -- switching colour off must not
	// cost the layout.
	for _, want := range []string{"╭", "╮", "╰", "╯", "│"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel lost its %q border with colour disabled:\n%s", want, out)
		}
	}
}

func TestPanelKeepsEscapesWhenColourIsEnabled(t *testing.T) {
	t.Setenv("DEVINMONITOR_COLOR", "always")
	if !ui.ColorEnabled() {
		t.Fatal("DEVINMONITOR_COLOR=always did not enable colour")
	}
	out := renderPanelFor(t)
	if !strings.ContainsRune(out, 0x1b) {
		t.Errorf("panel emitted no escape with colour enabled; the border lost its colour:\n%q", out)
	}
	// The escape hatch is not just "some escape" -- it is the border colour.
	if !strings.Contains(out, "\x1b[38;5;238m") {
		t.Errorf("panel did not use the border colour with colour enabled:\n%q", out)
	}
}

// TestPanelLayoutIsIdenticalWithAndWithoutColour is the property that makes the
// gate safe: turning colour off removes bytes, it does not move anything.
func TestPanelLayoutIsIdenticalWithAndWithoutColour(t *testing.T) {
	t.Setenv("DEVINMONITOR_COLOR", "never")
	plain := renderPanelFor(t)

	t.Setenv("DEVINMONITOR_COLOR", "always")
	coloured := renderPanelFor(t)

	if stripANSI(coloured) != plain {
		t.Errorf("stripping colour from the coloured panel does not reproduce the plain one:\n coloured: %q\n plain:    %q",
			stripANSI(coloured), plain)
	}
}

// renderPanelFor draws one panel through the same entry point the dashboard
// uses, with a fixed model so nothing depends on the clock or the database.
func renderPanelFor(t *testing.T) string {
	t.Helper()
	m := model_{sessions: layoutFixture(), width: 200, height: 40}
	return m.panel("Panel Title", "first line\nsecond line", 40)
}

// TestLayoutFixtureHasNoSessionsNeeded keeps the helper above honest: the panel
// is a pure function of its arguments, so it must work with no sessions at all.
func TestPanelWorksWithoutSessions(t *testing.T) {
	t.Setenv("DEVINMONITOR_COLOR", "never")
	m := model_{}
	if got := m.panel("Empty", "", 30); got == "" {
		t.Error("panel returned nothing for an empty body")
	}
}
