// Package main — feature command wiring.
//
// This file imports the integration and project packages so their init()
// functions register cobra commands via cli.Register(). It then builds a
// root command that includes both the existing main.go commands and all
// feature commands, replacing main()'s Execute call.
//
// We use init() + os.Exit() so that feature commands are wired in without
// editing main.go. main()'s own Execute() never runs because init() exits
// first.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/i18n"
	_ "github.com/garywhat/devinmonitor/internal/analytics"
	_ "github.com/garywhat/devinmonitor/internal/budget"
	_ "github.com/garywhat/devinmonitor/internal/filterexport"
	_ "github.com/garywhat/devinmonitor/internal/integration"
	_ "github.com/garywhat/devinmonitor/internal/project"
	_ "github.com/garywhat/devinmonitor/internal/trends"
	_ "github.com/garywhat/devinmonitor/internal/tuiext"
)

func init() {
	if err := i18n.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "i18n init: %v\n", err)
	}
	root := buildFeatureRoot()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// buildFeatureRoot creates the root command with both main.go's commands
// and all feature commands registered via cli.Register().
//
// We skip cmdSessions, cmdDaily, and cmdProjects from main.go because
// the integration and project packages provide enhanced versions with
// additional flags (--watch, --save, --sort, --attribution).
func buildFeatureRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "devinmonitor",
		Short: i18n.T("app.tagline"),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if flagLocale != "" {
				i18n.SetLocale(flagLocale)
			}
		},
	}
	root.PersistentFlags().StringVar(&flagDataDir, "data-dir", "", i18n.T("help.dataDir"))
	root.PersistentFlags().StringVar(&flagLocale, "locale", "", i18n.T("help.locale"))

	cobra.EnableCommandSorting = false

	// Add existing commands from main.go, EXCEPT those replaced by
	// enhanced versions in the integration/project packages:
	//   - cmdSessions → integration.cmdSessionsEnhanced (--sort, --save, --watch)
	//   - cmdDaily    → integration.cmdDailyEnhanced (--watch)
	//   - cmdProjects → project.cmdProjects (--attribution)
	root.AddCommand(
		cmdLive(),
		// cmdSessions() replaced by integration.cmdSessionsEnhanced
		cmdSession(),
		// cmdDaily() replaced by integration.cmdDailyEnhanced
		cmdWeekly(),
		cmdMonthly(),
		cmdModels(),
		cmdModel(),
		// cmdProjects() replaced by project.cmdProjects
		cmdAgents(),
		cmdMetrics(),
		cmdExport(),
		cmdVersion(),
	)

	// Add all feature commands registered via cli.Register().
	for _, fn := range cli.All() {
		root.AddCommand(fn())
	}

	return root
}
