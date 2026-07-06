// Package cli provides a command registry so feature packages can
// self-register cobra commands via init() without touching main.go.
// This enables parallel development across worktrees with zero merge conflicts.
package cli

import "github.com/spf13/cobra"

var registered []func() *cobra.Command

// Register adds a command factory to the registry.
// Feature packages call this in their init() functions.
func Register(fn func() *cobra.Command) {
	registered = append(registered, fn)
}

// All returns all registered command factories.
// main.go calls this after all packages are imported.
func All() []func() *cobra.Command {
	return registered
}

// Get returns the command factory registered with the given Use name.
// Returns nil if not found. Use this to pick specific commands in a
// desired order rather than relying on import/init order.
func Get(name string) func() *cobra.Command {
	for _, fn := range registered {
		cmd := fn()
		if cmd.Name() == name {
			return func() *cobra.Command { return cmd }
		}
	}
	return nil
}

// GetMany returns command factories for the given names in order.
// Unknown names are skipped silently.
func GetMany(names ...string) []func() *cobra.Command {
	var out []func() *cobra.Command
	for _, name := range names {
		if fn := Get(name); fn != nil {
			out = append(out, fn)
		}
	}
	return out
}

// Remaining returns command factories whose Use name is NOT in the
// provided set. Useful for adding any not-yet-added commands after
// an explicit ordered list.
func Remaining(exclude ...string) []func() *cobra.Command {
	ex := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		ex[n] = true
	}
	var out []func() *cobra.Command
	for _, fn := range registered {
		cmd := fn()
		if !ex[cmd.Name()] {
			out = append(out, func() *cobra.Command { return cmd })
		}
	}
	return out
}
