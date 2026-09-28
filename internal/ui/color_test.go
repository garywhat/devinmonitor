package ui

import (
	"strings"
	"testing"
)

// TestColorEnabledFor pins the whole decision table of the single choke point.
// It is a pure function of (mode, isTTY, environment) precisely so it can be
// tested without a terminal and without depending on how `go test`'s stdout is
// wired, which is what makes the rules below assertions rather than habits.
func TestColorEnabledFor(t *testing.T) {
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	none := env(nil)

	cases := []struct {
		name  string
		mode  string
		isTTY bool
		env   map[string]string
		want  bool
	}{
		{"auto on a terminal", ColorAuto, true, nil, true},
		{"auto on a pipe", ColorAuto, false, nil, false},
		{"auto on a dumb terminal", ColorAuto, true, map[string]string{"TERM": "dumb"}, false},
		{"auto is case-insensitive about TERM", ColorAuto, true, map[string]string{"TERM": "DUMB"}, false},
		{"NO_COLOR beats a terminal", ColorAuto, true, map[string]string{"NO_COLOR": "1"}, false},
		{"NO_COLOR beats a pipe too", ColorAuto, false, map[string]string{"NO_COLOR": "1"}, false},
		{"an empty NO_COLOR is not set", ColorAuto, true, map[string]string{"NO_COLOR": ""}, true},
		{"CLICOLOR=0 disables", ColorAuto, true, map[string]string{"CLICOLOR": "0"}, false},
		{"CLICOLOR_FORCE forces color on a pipe", ColorAuto, false, map[string]string{"CLICOLOR_FORCE": "1"}, true},
		{"CLICOLOR_FORCE=0 does not force", ColorAuto, false, map[string]string{"CLICOLOR_FORCE": "0"}, false},
		{"NO_COLOR outranks CLICOLOR_FORCE", ColorAuto, true,
			map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"}, false},
		{"always wins over a pipe", ColorAlways, false, nil, true},
		{"always wins over NO_COLOR", ColorAlways, false, map[string]string{"NO_COLOR": "1"}, true},
		{"never wins over a terminal", ColorNever, true, nil, false},
		{"never wins over CLICOLOR_FORCE", ColorNever, false, map[string]string{"CLICOLOR_FORCE": "1"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getenv := env(c.env)
			if c.env == nil {
				getenv = none
			}
			if got := colorEnabledFor(c.mode, c.isTTY, getenv); got != c.want {
				t.Errorf("colorEnabledFor(%q, tty=%v, env=%v) = %v, want %v",
					c.mode, c.isTTY, c.env, got, c.want)
			}
		})
	}
}

// TestTableHonoursColorMode is the property the whole design exists for: the
// renderer, not ~14 individual commands, decides whether escapes are emitted.
// A command that builds a table gets both behaviours for free.
func TestTableHonoursColorMode(t *testing.T) {
	newTable := func() *TableBuilder {
		return NewTable("ID", "成本").RightAlign(1).
			Row("session-a", "free").
			TotalRow("TOTAL", "free")
	}

	SetColorMode(ColorNever)
	t.Cleanup(func() { SetColorMode(ColorAuto) })
	plain := newTable().String()
	if strings.ContainsRune(plain, 0x1b) {
		t.Errorf("ColorNever table still contains ESC:\n%q", plain)
	}
	if !strings.Contains(plain, "成本") || !strings.Contains(plain, "TOTAL") {
		t.Errorf("plain table lost content:\n%s", plain)
	}

	SetColorMode(ColorAlways)
	colored := newTable().String()
	if !strings.ContainsRune(colored, 0x1b) {
		t.Errorf("ColorAlways table has no escapes:\n%q", colored)
	}
	// Same layout either way: the mode must change colour, not structure.
	stripANSI := func(s string) string { return ansiRe.ReplaceAllString(s, "") }
	if stripANSI(colored) != plain {
		t.Errorf("colour changed the layout\nwith:    %q\nwithout: %q", stripANSI(colored), plain)
	}
}

// TestPanelHonoursColorMode covers the other renderer in this package, because
// a panel is how most detail views are printed and it builds its escapes by
// hand rather than going through the table code.
func TestPanelHonoursColorMode(t *testing.T) {
	SetColorMode(ColorNever)
	t.Cleanup(func() { SetColorMode(ColorAuto) })
	if got := Panel("Status", "line one\nline two", 40); strings.ContainsRune(got, 0x1b) {
		t.Errorf("ColorNever panel still contains ESC:\n%q", got)
	}
	SetColorMode(ColorAlways)
	if got := Panel("Status", "line one", 40); !strings.ContainsRune(got, 0x1b) {
		t.Errorf("ColorAlways panel has no escapes:\n%q", got)
	}
}

// TestColorEnabledReadsTheEnvironment guards the wrapper (not just the pure
// decision): NO_COLOR in the real environment must turn colour off in auto mode,
// whatever stdout happens to be.
func TestColorEnabledReadsTheEnvironment(t *testing.T) {
	SetColorMode(ColorAuto)
	t.Cleanup(func() { SetColorMode(ColorAuto) })
	t.Setenv("NO_COLOR", "1")
	if ColorEnabled() {
		t.Error("ColorEnabled() = true with NO_COLOR set")
	}

	// An explicit mode outranks the environment, matching --color=always.
	SetColorMode(ColorAlways)
	if !ColorEnabled() {
		t.Error("ColorEnabled() = false in ColorAlways mode despite NO_COLOR")
	}
}

// TestSetColorModeNormalizes verifies an unknown mode degrades to auto instead
// of leaving the process in a state nobody can name.
func TestSetColorModeNormalizes(t *testing.T) {
	t.Cleanup(func() { SetColorMode(ColorAuto) })
	for _, in := range []string{" ALWAYS ", "Always", "always"} {
		SetColorMode(in)
		if got := ColorMode(); got != ColorAlways {
			t.Errorf("SetColorMode(%q) left mode %q, want %q", in, got, ColorAlways)
		}
	}
	SetColorMode("definitely-not-a-mode")
	if got := ColorMode(); got != ColorAuto {
		t.Errorf("unknown mode left %q, want %q", got, ColorAuto)
	}
}
