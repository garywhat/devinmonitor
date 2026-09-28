package main

import (
	"sort"
	"testing"

	"github.com/garywhat/devinmonitor/internal/cli"
)

// TestEveryRegisteredCommandIsExplicitlyOrdered pins the audit finding behind
// cli.Remaining(): because buildOrderedCommands() lists every registered
// command by name, the "safety net" in main() appends nothing, and `--help`
// order is fully determined by that explicit list.
//
// The assertion matters in both directions. If someone adds a command to a
// feature package but forgets buildOrderedCommands(), this test fails and names
// the command instead of letting it silently land at the bottom of --help (the
// safety net would still add it, so nothing else would complain). If someone
// removes a command from the list, same. And if the net is ever deleted, this
// test is the thing that proves the deletion was safe.
func TestEveryRegisteredCommandIsExplicitlyOrdered(t *testing.T) {
	t.Logf("registered commands: %d", len(cli.All()))

	ordered := buildOrderedCommands()

	seen := make(map[string]bool, len(ordered))
	for _, e := range ordered {
		if e.Name != "" {
			seen[e.Name] = true
		}
	}

	var leaked []string
	for _, fn := range cli.All() {
		name := fn().Name()
		if !seen[name] {
			leaked = append(leaked, name)
		}
	}
	sort.Strings(leaked)

	if len(leaked) != 0 {
		t.Errorf("cli.Remaining() would append %d command(s) not in buildOrderedCommands(): %v\n"+
			"Add them to the explicit ordered list so --help order stays intentional.", len(leaked), leaked)
	}

	// A registry that failed to load (all init()s skipped) would also pass the
	// check above, so guard the size of the surface we are claiming to cover.
	if got := len(cli.All()); got < 40 {
		t.Errorf("only %d commands are registered; the registry looks broken, not ordered", got)
	}
}

// TestOrderedListHasNoUnresolvableFeatureNames catches a stale name in
// buildOrderedCommands(): feat() returns a placeholder with no Cmd pointer when
// cli.Get() misses, and main() silently skips those. A typo would therefore make
// a whole command disappear from --help with no error anywhere.
func TestOrderedListHasNoUnresolvableFeatureNames(t *testing.T) {
	for _, e := range buildOrderedCommands() {
		if e.Name != "" && e.Cmd == nil {
			t.Errorf("buildOrderedCommands() names %q but no package registers it "+
				"(cli.Get returned nil); the command will be silently missing", e.Name)
		}
	}
}
