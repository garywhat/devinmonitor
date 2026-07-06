// Package tuiext registers TUI interaction cobra commands via the cli
// registry. Each command factory is registered in init() so that main.go
// can discover them through cli.All() without any file edits.
package tuiext

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/cli"
	"github.com/garywhat/devinmonitor/internal/live"
)

func init() {
	cli.Register(cmdTheme)
	cli.Register(cmdReplay)
	cli.Register(cmdTimeline)
	cli.Register(cmdLiveExt)
}

// ---- theme command ----

// cmdTheme implements: devinmonitor theme [list|set <name>|show]
func cmdTheme() *cobra.Command {
	c := &cobra.Command{
		Use:   "theme [list|set <name>|show]",
		Short: "Manage TUI color themes",
		Long:  "Manage TUI color themes. Subcommands: list (default), set <name>, show.\n15 built-in themes: auto, dark, light, dracula, nord, solarized-dark, solarized-light, gruvbox, monokai, tokyo-night, catppuccin, everforest, gruvbox-light, rose-pine, github",
		Args:  cobra.MaximumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			sub := "list"
			if len(args) > 0 {
				sub = args[0]
			}
			switch sub {
			case "list":
				fmt.Print(live.FormatThemeList())
			case "show":
				fmt.Print(live.FormatThemeShow())
			case "set":
				if len(args) < 2 {
					fmt.Fprintln(os.Stderr, "usage: devinmonitor theme set <name>")
					os.Exit(1)
				}
				name := args[1]
				if err := live.SetGlobalTheme(name); err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}
				fmt.Printf("Theme set to: %s\n", name)
			default:
				fmt.Fprintf(os.Stderr, "unknown subcommand: %s (use list, set, or show)\n", sub)
				os.Exit(1)
			}
		},
	}
	return c
}

// ---- replay command ----

// cmdReplay implements: devinmonitor replay <session-id>
func cmdReplay() *cobra.Command {
	dataDir := ""
	c := &cobra.Command{
		Use:   "replay <session-id>",
		Short: "Replay a session message by message",
		Long:  "Plays back a session's messages in chronological order with timestamps.\nControls: Space=pause/resume, Left/Right=step, q=quit.",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if err := live.RunReplay(dataDir, args[0]); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		},
	}
	c.Flags().StringVar(&dataDir, "data-dir", "", "override Devin data directory")
	return c
}

// ---- timeline command ----

// cmdTimeline implements: devinmonitor timeline <session-id>
func cmdTimeline() *cobra.Command {
	dataDir := ""
	c := &cobra.Command{
		Use:   "timeline <session-id>",
		Short: "Show a visual timeline of session activity",
		Long:  "Displays a horizontal timeline of message events, color-coded by type (user=blue, assistant=green, tool=yellow).",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			if err := live.RunTimeline(dataDir, args[0]); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		},
	}
	c.Flags().StringVar(&dataDir, "data-dir", "", "override Devin data directory")
	return c
}

// ---- live-ext command (demo, once, light modes) ----

// cmdLiveExt implements: devinmonitor live-ext [--demo] [--once] [--light] [--theme <name>]
// Since we cannot edit the existing `live` command in main.go, this provides
// the extended live mode with the new flags.
func cmdLiveExt() *cobra.Command {
	var (
		dataDir string
		demo    bool
		once    bool
		light   bool
		theme   string
		interval int
	)
	c := &cobra.Command{
		Use:   "live-ext",
		Short: "Extended live TUI with demo, once, and light modes",
		Long:  "Extended live dashboard with interactive overlays (command palette, settings, help, popups, split pane, time window cycling, log tailing).\nFlags:\n  --demo   Run with synthetic demo data (no database needed)\n  --once   Render one frame and exit (non-interactive, for scripts/CI)\n  --light  Minimal rendering for slow terminals\n  --theme  Override theme (auto, dark, dracula, nord, ...)",
		Run: func(cmd *cobra.Command, args []string) {
			opts := live.RunOptions{
				Demo:    demo,
				Once:    once,
				Light:   light,
				Theme:   theme,
			}
			if err := live.RunLiveExt(dataDir, interval, opts); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		},
	}
	c.Flags().StringVar(&dataDir, "data-dir", "", "override Devin data directory")
	c.Flags().IntVar(&interval, "interval", 3, "refresh interval in seconds")
	c.Flags().BoolVar(&demo, "demo", false, "run with synthetic demo data")
	c.Flags().BoolVar(&once, "once", false, "render one frame and exit")
	c.Flags().BoolVar(&light, "light", false, "minimal rendering for slow terminals")
	c.Flags().StringVar(&theme, "theme", "", "override theme (auto, dark, dracula, nord, ...)")
	return c
}
