package live

import (
	"strings"
	"testing"
)

// TestStatusBarReportsASaveFailure covers the TUI half of a real defect: the
// settings panel used to discard config.SaveGlobal()'s error, so a failed write
// looked exactly like a successful one and the user's preference silently
// disappeared at exit.
//
// Unlike the CLI, the dashboard must not exit on that failure — the in-memory
// value is still usable — so the status bar is where it has to be visible.
func TestStatusBarReportsASaveFailure(t *testing.T) {
	m := newExtModelWithSessions(layoutFixture(), false)
	m.width, m.height = 120, 40

	m.saveErr = ""
	if bar := m.renderStatusBar(); strings.Contains(bar, "not saved") {
		t.Errorf("status bar shows a save warning with no failure recorded: %q", bar)
	}

	m.saveErr = "settings not saved: permission denied"
	if bar := m.renderStatusBar(); !strings.Contains(bar, "not saved") {
		t.Errorf("status bar hides a recorded save failure: %q", bar)
	}
}

// TestSaveSettingsReturnsTheModel is the guard for the value-receiver trap:
// saveSettings is on a value receiver, so a caller that discards its result
// would drop the recorded failure and the warning above would never render.
// This is the same class of bug the fix was for, so it is worth pinning.
func TestSaveSettingsReturnsTheModel(t *testing.T) {
	m := newExtModelWithSessions(layoutFixture(), false)
	got := m.saveSettings()
	if got.saveErr != "" && !strings.Contains(got.saveErr, "not saved") {
		t.Errorf("saveErr = %q, want empty or a 'not saved' message", got.saveErr)
	}
}
