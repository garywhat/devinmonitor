package live

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/config"
)

// Theme is a named color palette for the TUI.
// All fields are hex color strings (e.g. "#282a36").
type Theme struct {
	Name    string
	Bg      string
	Fg      string
	Accent  string
	Warning string
	Success string
	Error   string
	Muted   string
}

// Themes is the registry of all built-in themes.
var Themes = map[string]Theme{
	"auto": {
		Name:    "auto",
		Bg:      "#1e1e2e",
		Fg:      "#cdd6f4",
		Accent:  "#89b4fa",
		Warning: "#f9e2af",
		Success: "#a6e3a1",
		Error:   "#f38ba8",
		Muted:   "#6c7086",
	},
	"dark": {
		Name:    "dark",
		Bg:      "#1e1e2e",
		Fg:      "#cdd6f4",
		Accent:  "#89b4fa",
		Warning: "#f9e2af",
		Success: "#a6e3a1",
		Error:   "#f38ba8",
		Muted:   "#6c7086",
	},
	"light": {
		Name:    "light",
		Bg:      "#eff1f5",
		Fg:      "#4c4f69",
		Accent:  "#1e66f5",
		Warning: "#df8e1d",
		Success: "#40a02b",
		Error:   "#d20f39",
		Muted:   "#9ca0b0",
	},
	"dracula": {
		Name:    "dracula",
		Bg:      "#282a36",
		Fg:      "#f8f8f2",
		Accent:  "#bd93f9",
		Warning: "#ffb86c",
		Success: "#50fa7b",
		Error:   "#ff5555",
		Muted:   "#6272a4",
	},
	"nord": {
		Name:    "nord",
		Bg:      "#2e3440",
		Fg:      "#d8dee9",
		Accent:  "#88c0d0",
		Warning: "#ebcb8b",
		Success: "#a3be8c",
		Error:   "#bf616a",
		Muted:   "#4c566a",
	},
	"solarized-dark": {
		Name:    "solarized-dark",
		Bg:      "#002b36",
		Fg:      "#839496",
		Accent:  "#268bd2",
		Warning: "#b58900",
		Success: "#859900",
		Error:   "#dc322f",
		Muted:   "#586e75",
	},
	"solarized-light": {
		Name:    "solarized-light",
		Bg:      "#fdf6e3",
		Fg:      "#657b83",
		Accent:  "#268bd2",
		Warning: "#b58900",
		Success: "#859900",
		Error:   "#dc322f",
		Muted:   "#93a1a1",
	},
	"gruvbox": {
		Name:    "gruvbox",
		Bg:      "#282828",
		Fg:      "#ebdbb2",
		Accent:  "#83a598",
		Warning: "#fabd2f",
		Success: "#b8bb26",
		Error:   "#fb4934",
		Muted:   "#928374",
	},
	"monokai": {
		Name:    "monokai",
		Bg:      "#272822",
		Fg:      "#f8f8f2",
		Accent:  "#66d9ef",
		Warning: "#e6db74",
		Success: "#a6e22e",
		Error:   "#f92672",
		Muted:   "#75715e",
	},
	"tokyo-night": {
		Name:    "tokyo-night",
		Bg:      "#1a1b26",
		Fg:      "#a9b1d6",
		Accent:  "#7aa2f7",
		Warning: "#e0af68",
		Success: "#9ece6a",
		Error:   "#f7768e",
		Muted:   "#565f89",
	},
	"catppuccin": {
		Name:    "catppuccin",
		Bg:      "#1e1e2e",
		Fg:      "#cdd6f4",
		Accent:  "#cba6f7",
		Warning: "#f9e2af",
		Success: "#a6e3a1",
		Error:   "#f38ba8",
		Muted:   "#6c7086",
	},
	"everforest": {
		Name:    "everforest",
		Bg:      "#2d353b",
		Fg:      "#d3c6aa",
		Accent:  "#7fbbb3",
		Warning: "#dbbc7f",
		Success: "#a7c080",
		Error:   "#e67e80",
		Muted:   "#859289",
	},
	"gruvbox-light": {
		Name:    "gruvbox-light",
		Bg:      "#fbf1c7",
		Fg:      "#3c3836",
		Accent:  "#076678",
		Warning: "#b57614",
		Success: "#79740e",
		Error:   "#cc241d",
		Muted:   "#928374",
	},
	"rose-pine": {
		Name:    "rose-pine",
		Bg:      "#191724",
		Fg:      "#e0def4",
		Accent:  "#31748f",
		Warning: "#ebbcba",
		Success: "#31748f",
		Error:   "#eb6f92",
		Muted:   "#6e6a86",
	},
	"github": {
		Name:    "github",
		Bg:      "#0d1117",
		Fg:      "#c9d1d9",
		Accent:  "#58a6ff",
		Warning: "#d29922",
		Success: "#3fb950",
		Error:   "#f85149",
		Muted:   "#484f58",
	},
}

// ListThemeNames returns all theme names sorted alphabetically.
func ListThemeNames() []string {
	names := make([]string, 0, len(Themes))
	for k := range Themes {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// GetTheme resolves a theme name to its Theme definition.
// The "auto" theme resolves based on the terminal background color
// (falls back to dark).
func GetTheme(name string) Theme {
	if name == "" || name == "auto" {
		// Detect light/dark from terminal. lipgloss has no direct API for
		// background detection in v1.1.0, so we default to dark.
		if isLightTerminal() {
			return Themes["light"]
		}
		return Themes["dark"]
	}
	if t, ok := Themes[name]; ok {
		return t
	}
	// Unknown theme: fall back to dark.
	return Themes["dark"]
}

// isLightTerminal attempts to detect a light terminal background.
// In practice, we can't reliably detect this without terminfo queries;
// we default to false (dark terminal) which is the common case.
func isLightTerminal() bool {
	return false
}

// ApplyTheme updates the package-level lipgloss styles to use the given theme.
func ApplyTheme(t Theme) {
	fg := lipgloss.Color(t.Fg)
	accent := lipgloss.Color(t.Accent)
	warning := lipgloss.Color(t.Warning)
	success := lipgloss.Color(t.Success)
	errColor := lipgloss.Color(t.Error)
	muted := lipgloss.Color(t.Muted)
	value := lipgloss.Color(t.Fg)

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	labelStyle = lipgloss.NewStyle().Foreground(muted)
	valueStyle = lipgloss.NewStyle().Foreground(value)
	tokenStyle = lipgloss.NewStyle().Foreground(success)
	costStyle = lipgloss.NewStyle().Foreground(warning)
	warnStyle = lipgloss.NewStyle().Foreground(warning)
	dimStyle = lipgloss.NewStyle().Foreground(muted)
	greenStyle = lipgloss.NewStyle().Foreground(success)
	yellowStyle = lipgloss.NewStyle().Foreground(warning)
	redStyle = lipgloss.NewStyle().Foreground(errColor)
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).Foreground(fg)
}

// ApplyGlobalTheme loads the theme from the global config and applies it.
func ApplyGlobalTheme() {
	cfg := config.Global()
	t := GetTheme(cfg.Theme)
	ApplyTheme(t)
}

// SetGlobalTheme sets the theme in the global config and saves it.
func SetGlobalTheme(name string) error {
	if _, ok := Themes[name]; !ok && name != "auto" {
		return fmt.Errorf("unknown theme: %s (available: %s)", name, strings.Join(ListThemeNames(), ", "))
	}
	cfg := config.Global()
	cfg.Theme = name
	ApplyTheme(GetTheme(name))
	return config.SaveGlobal()
}

// CurrentThemeName returns the active theme name from config.
func CurrentThemeName() string {
	return config.Global().Theme
}

// FormatThemeList renders the theme list as a string for CLI output.
func FormatThemeList() string {
	current := CurrentThemeName()
	var b strings.Builder
	b.WriteString("Available themes:\n")
	for _, name := range ListThemeNames() {
		t := Themes[name]
		marker := "  "
		if name == current {
			marker = "▶ "
		}
		swatch := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Render("●")
		b.WriteString(fmt.Sprintf("%s%s %-18s %s\n", marker, swatch, name, t.Name))
	}
	b.WriteString(fmt.Sprintf("\nCurrent: %s\n", current))
	return b.String()
}

// FormatThemeShow renders the current theme's color palette.
func FormatThemeShow() string {
	t := GetTheme(CurrentThemeName())
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Theme: %s\n\n", t.Name))
	colors := []struct {
		label string
		hex   string
	}{
		{"Background", t.Bg},
		{"Foreground", t.Fg},
		{"Accent", t.Accent},
		{"Warning", t.Warning},
		{"Success", t.Success},
		{"Error", t.Error},
		{"Muted", t.Muted},
	}
	for _, c := range colors {
		swatch := lipgloss.NewStyle().
			Foreground(lipgloss.Color(c.hex)).
			Render("████")
		b.WriteString(fmt.Sprintf("  %-12s %s  %s\n", c.label, swatch, c.hex))
	}
	return b.String()
}
