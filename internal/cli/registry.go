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
