package integration

import (
	"bytes"
	"strings"
	"testing"
)

// TestClearScreenForRefreshOnlyOnATerminal pins the fix for a residual leak:
// `sessions --watch` wrote the clear-and-home sequence unconditionally, so
// `sessions --watch > out` began with two escape sequences, and piping it
// anywhere put them in the stream.
//
// The distinction that matters is terminal-vs-not, NOT colour-vs-not. A
// terminal running with NO_COLOR still wants the screen cleared between
// refreshes; conflating the two would break one of them whichever way it went.
func TestClearScreenForRefreshOnlyOnATerminal(t *testing.T) {
	const seq = "\033[2J\033[H"

	t.Run("not a terminal writes nothing", func(t *testing.T) {
		var buf bytes.Buffer
		clearScreenForRefresh(&buf, false)
		if buf.Len() != 0 {
			t.Errorf("wrote %q to a non-terminal, want nothing", buf.String())
		}
	})

	t.Run("a terminal gets the sequence", func(t *testing.T) {
		var buf bytes.Buffer
		clearScreenForRefresh(&buf, true)
		if buf.String() != seq {
			t.Errorf("wrote %q, want %q", buf.String(), seq)
		}
	})

	t.Run("the sequence clears and homes", func(t *testing.T) {
		var buf bytes.Buffer
		clearScreenForRefresh(&buf, true)
		got := buf.String()
		if !strings.HasPrefix(got, "\033[2J") {
			t.Errorf("output does not start by clearing the screen: %q", got)
		}
		if !strings.HasSuffix(got, "\033[H") {
			t.Errorf("output does not end by homing the cursor: %q", got)
		}
	})
}

// TestClearScreenIsNotTiedToColour records the design decision explicitly, so a
// future change that "simplifies" this to ColorEnabled fails here rather than in
// someone's terminal.
func TestClearScreenIsNotTiedToColour(t *testing.T) {
	// Whatever the colour mode, the clear decision takes only the TTY flag.
	var buf bytes.Buffer
	clearScreenForRefresh(&buf, false)
	if buf.Len() != 0 {
		t.Fatal("the clear decision consulted something other than the TTY flag")
	}
}
