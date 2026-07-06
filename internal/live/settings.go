package live

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
)

// extReader is an alias for reader.Reader, used by the ExtModel.
type extReader = reader.Reader

// openExtReader opens a reader for the extended live mode.
func openExtReader(dataDir string) (extReader, error) {
	return reader.Open(dataDir)
}

// TimeWindow constants for time window cycling.
const (
	twToday = iota
	twWeek
	twMonth
	twAll
)

var timeWindowNames = []string{"today", "week", "month", "all"}

// ExtModel is the extended bubbletea model that wraps the base live model_
// and adds overlay features: command palette, settings panel, help overlay,
// session detail popup, model breakdown popup, split pane, view toggle,
// time window cycling, and live log tailing.
type ExtModel struct {
	inner    model_ // the base live dashboard model
	light    bool   // light rendering mode
	reader   extReader
	interval time.Duration

	// Overlay state.
	showHelp            bool
	showSettings        bool
	showCommandPalette  bool
	showSessionDetail   bool
	showModelBreakdown  bool
	splitPane           bool
	paneView            bool // false=list(table), true=card grid
	timeWindow          int
	logTailing          bool

	// Command palette state.
	cpQuery    string
	cpIndex    int
	cpCommands []cpCommand

	// Settings form state.
	settingsCursor int
	settingsFields []settingsField

	// Dimensions cached for overlay rendering.
	width  int
	height int
}

// cpCommand is a single entry in the command palette.
type cpCommand struct {
	Name     string
	Shortcut string
	Action   func()
}

// settingsField is a single editable field in the settings panel.
type settingsField struct {
	Label   string
	Key     string
	Value   string
	Options []string // for enum fields
}

// newExtModel creates an ExtModel backed by a real reader.
func newExtModel(r extReader, intervalSec int, light bool) ExtModel {
	interval := time.Duration(intervalSec) * time.Second
	if interval < 1*time.Second {
		interval = 3 * time.Second
	}
	m := ExtModel{
		reader:   r,
		interval: interval,
		light:    light,
		inner: model_{
			reader:   r,
			interval: interval,
			loading:  true,
		},
	}
	m.initCommandPalette()
	m.initSettingsFields()
	return m
}

// newExtModelWithSessions creates an ExtModel with pre-loaded sessions
// (used by demo mode and --once mode).
func newExtModelWithSessions(ss []model.Session, light bool) ExtModel {
	m := ExtModel{
		light:  light,
		inner: model_{
			sessions: ss,
			loading:  false,
		},
	}
	m.initCommandPalette()
	m.initSettingsFields()
	return m
}

func (m *ExtModel) initCommandPalette() {
	m.cpCommands = []cpCommand{
		{Name: "Toggle Help", Shortcut: "?"},
		{Name: "Toggle Settings", Shortcut: "s"},
		{Name: "Toggle Split Pane", Shortcut: "|"},
		{Name: "Toggle List/Pane View", Shortcut: "v"},
		{Name: "Cycle Time Window", Shortcut: "t"},
		{Name: "Toggle Log Tailing", Shortcut: "l"},
		{Name: "Session Detail Popup", Shortcut: "Enter"},
		{Name: "Model Breakdown Popup", Shortcut: "m"},
		{Name: "Next Session", Shortcut: "r"},
		{Name: "Cycle Locale", Shortcut: "L"},
		{Name: "Quit", Shortcut: "q"},
	}
}

func (m *ExtModel) initSettingsFields() {
	cfg := config.Global()
	m.settingsFields = []settingsField{
		{Label: "Theme", Key: "theme", Value: cfg.Theme, Options: ListThemeNames()},
		{Label: "Refresh Interval (s)", Key: "refreshInterval", Value: fmt.Sprintf("%d", cfg.RefreshInterval)},
		{Label: "Currency", Key: "currency", Value: cfg.Currency, Options: []string{"USD", "EUR", "CNY", "GBP", "JPY"}},
		{Label: "Budget Daily ($)", Key: "budgetDaily", Value: fmt.Sprintf("%.2f", cfg.BudgetDaily)},
		{Label: "Budget Weekly ($)", Key: "budgetWeekly", Value: fmt.Sprintf("%.2f", cfg.BudgetWeekly)},
		{Label: "Budget Monthly ($)", Key: "budgetMonthly", Value: fmt.Sprintf("%.2f", cfg.BudgetMonthly)},
		{Label: "Timezone", Key: "timezone", Value: cfg.Timezone, Options: []string{"auto", "UTC", "America/New_York", "Europe/London", "Asia/Shanghai"}},
		{Label: "Time Format", Key: "timeFormat", Value: cfg.TimeFormat, Options: []string{"auto", "12h", "24h"}},
	}
}

// ---- Bubbletea implementation ----

func (m ExtModel) Init() tea.Cmd {
	if m.reader != nil {
		return tea.Batch(poll(m.reader), tick(m.interval))
	}
	return nil
}

func (m ExtModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.inner.width, m.inner.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		if m.reader != nil {
			return m, tea.Batch(poll(m.reader), tick(m.interval))
		}
		return m, nil

	case pollMsg:
		m.inner.loading = false
		if msg.err != nil {
			m.inner.err = msg.err
			return m, nil
		}
		m.inner.sessions = filterByTimeWindow(msg.sessions, m.timeWindow)
		m.inner.lastPoll = time.Now()
		m.inner.err = nil
		if m.inner.current >= len(m.inner.sessions) {
			m.inner.current = 0
		}
		return m, nil

	case tea.KeyMsg:
		// If an overlay is active, handle its keys first.
		if m.showCommandPalette {
			return m.updateCommandPalette(msg)
		}
		if m.showSettings {
			return m.updateSettings(msg)
		}
		if m.showHelp {
			switch msg.String() {
			case "?", "esc", "q":
				m.showHelp = false
			}
			return m, nil
		}
		if m.showSessionDetail {
			switch msg.String() {
			case "esc", "q", "enter":
				m.showSessionDetail = false
			}
			return m, nil
		}
		if m.showModelBreakdown {
			switch msg.String() {
			case "esc", "q", "m":
				m.showModelBreakdown = false
			}
			return m, nil
		}

		// Handle overlay toggle keys.
		switch msg.String() {
		case "?":
			m.showHelp = true
			return m, nil
		case "s":
			m.showSettings = true
			m.settingsCursor = 0
			return m, nil
		case "ctrl+p":
			m.showCommandPalette = true
			m.cpQuery = ""
			m.cpIndex = 0
			return m, nil
		case "enter":
			if len(m.inner.sessions) > 0 {
				m.showSessionDetail = true
			}
			return m, nil
		case "m":
			m.showModelBreakdown = true
			return m, nil
		case "|":
			m.splitPane = !m.splitPane
			return m, nil
		case "v":
			m.paneView = !m.paneView
			// Save preference to config.
			cfg := config.Global()
			if cfg.SavedFlags == nil {
				cfg.SavedFlags = map[string]string{}
			}
			if m.paneView {
				cfg.SavedFlags["view"] = "pane"
			} else {
				cfg.SavedFlags["view"] = "list"
			}
			_ = config.SaveGlobal()
			return m, nil
		case "t":
			m.timeWindow = (m.timeWindow + 1) % 4
			// Re-filter current sessions.
			if m.reader != nil {
				// Will be re-filtered on next poll; for immediate effect,
				// re-filter from inner.sessions (which are already loaded).
				// We need the unfiltered set — but inner.sessions is already
				// filtered. This is a best-effort immediate update.
			}
			return m, nil
		case "l":
			m.logTailing = !m.logTailing
			return m, nil
		}

		// Delegate remaining keys to the inner model.
		newInner, cmd := m.inner.Update(msg)
		m.inner = newInner.(model_)
		return m, cmd
	}
	return m, nil
}

func (m ExtModel) View() string {
	if m.inner.quitting {
		return ""
	}

	// Overlays take over the full screen.
	if m.showHelp {
		return RenderHelpOverlay(m.width, m.height)
	}
	if m.showSettings {
		return RenderSettingsPanel(m.width, m.height, m.settingsFields, m.settingsCursor)
	}
	if m.showCommandPalette {
		return RenderCommandPalette(m.cpCommands, m.cpQuery, m.cpIndex, m.width, m.height)
	}
	if m.showSessionDetail {
		s := m.inner.currentSession()
		return RenderSessionDetailPopup(s, m.width, m.height)
	}
	if m.showModelBreakdown {
		return RenderModelBreakdownPopup(m.inner.sessions, m.width, m.height)
	}

	// Base view from inner model.
	base := m.inner.View()

	// If light mode, simplify rendering.
	if m.light {
		return m.renderLight()
	}

	// If log tailing is on, append log tail below.
	if m.logTailing && m.height > 10 {
		tailH := m.height / 4
		baseH := m.height - tailH - 1
		baseView := m.inner.View()
		// Truncate base view to baseH lines.
		baseLines := strings.Split(baseView, "\n")
		if len(baseLines) > baseH {
			baseLines = baseLines[:baseH]
		}
		base = strings.Join(baseLines, "\n") + "\n" + RenderLogTail(m.inner.sessions, m.width, tailH)
	}

	// If split pane, render split view.
	if m.splitPane && m.width >= 60 {
		return m.renderSplitPane()
	}

	// If pane view (card grid), render cards.
	if m.paneView {
		return m.renderPaneView()
	}

	// Status bar with time window indicator.
	statusBar := m.renderStatusBar()
	return base + "\n" + statusBar
}

// renderLight renders a minimal view for slow terminals.
func (m ExtModel) renderLight() string {
	s := m.inner.currentSession()
	modelName := s.LatestModel
	if modelName == "" {
		modelName = s.Model
	}
	cost, _ := report.SessionCost(s)
	p := model.LookupPricing(modelName)
	if cost == 0 && !p.Free && p.InputPerM > 0 {
		cost = model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
	}
	dur := s.LastActivityAt.Sub(s.CreatedAt)
	tw := timeWindowNames[m.timeWindow]
	return fmt.Sprintf("%s · %s · %s · %s · reqs %d · %s · [%s]",
		s.ID, modelName, s.AgentMode,
		report.FormatCost(cost, p.Free),
		s.AssistantCount, report.FormatDur(dur), tw)
}

// renderStatusBar renders the bottom status bar showing current time window
// and active toggles.
func (m ExtModel) renderStatusBar() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("window: %s", timeWindowNames[m.timeWindow]))
	if m.splitPane {
		parts = append(parts, "split")
	}
	if m.paneView {
		parts = append(parts, "pane")
	}
	if m.logTailing {
		parts = append(parts, "log")
	}
	parts = append(parts, "? help  s settings  Ctrl+P palette  | split  v view  t time  l log  m models  Enter detail  q quit")
	return dimStyle.Render(strings.Join(parts, "  ·  "))
}

// renderSplitPane renders sessions list on left, details on right.
func (m ExtModel) renderSplitPane() string {
	w := m.width
	if w == 0 {
		w = 80
	}
	leftW := w * 2 / 5
	rightW := w - leftW - 1

	// Left: session list.
	s := m.inner.currentSession()
	var leftLines []string
	leftLines = append(leftLines, titleStyle.Render("Sessions"))
	for i, sess := range m.inner.sessions {
		marker := "  "
		if i == m.inner.current {
			marker = "▶ "
		}
		title := sess.Title
		if title == "" {
			title = sess.ID
		}
		if len(title) > leftW-6 {
			title = title[:leftW-7] + "…"
		}
		leftLines = append(leftLines, fmt.Sprintf("%s%s", marker, title))
	}

	// Right: session detail (compact).
	modelName := s.LatestModel
	if modelName == "" {
		modelName = s.Model
	}
	cost, _ := report.SessionCost(s)
	p := model.LookupPricing(modelName)
	if cost == 0 && !p.Free && p.InputPerM > 0 {
		cost = model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
	}
	dur := s.LastActivityAt.Sub(s.CreatedAt)
	rightLines := []string{
		titleStyle.Render("Details"),
		fmt.Sprintf("ID:       %s", s.ID),
		fmt.Sprintf("Title:    %s", s.Title),
		fmt.Sprintf("Model:    %s", modelName),
		fmt.Sprintf("Mode:     %s", s.AgentMode),
		fmt.Sprintf("Project:  %s", s.WorkingDir),
		fmt.Sprintf("Requests: %d", s.AssistantCount),
		fmt.Sprintf("Duration: %s", report.FormatDur(dur)),
		fmt.Sprintf("Input:    %s", report.FormatTok(s.InputTokens)),
		fmt.Sprintf("Output:   %s", report.FormatTok(s.OutputTokens)),
		fmt.Sprintf("Cache R:  %s", report.FormatTok(s.CacheRead)),
		fmt.Sprintf("Cost:     %s", report.FormatCost(cost, p.Free)),
	}

	left := strings.Join(leftLines, "\n")
	right := strings.Join(rightLines, "\n")

	// Pad to same height.
	leftH := strings.Count(left, "\n") + 1
	rightH := strings.Count(right, "\n") + 1
	maxH := leftH
	if rightH > maxH {
		maxH = rightH
	}
	for len(strings.Split(left, "\n")) < maxH {
		left += "\n"
	}
	for len(strings.Split(right, "\n")) < maxH {
		right += "\n"
	}

	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(left),
		"│",
		lipgloss.NewStyle().Width(rightW).Render(right),
	) + "\n" + m.renderStatusBar()
}

// renderPaneView renders sessions as cards in a grid layout.
func (m ExtModel) renderPaneView() string {
	w := m.width
	if w == 0 {
		w = 80
	}
	cardW := 36
	cardsPerRow := w / (cardW + 1)
	if cardsPerRow < 1 {
		cardsPerRow = 1
	}

	var rows []string
	for i := 0; i < len(m.inner.sessions); i += cardsPerRow {
		var cards []string
		for j := i; j < i+cardsPerRow && j < len(m.inner.sessions); j++ {
			s := m.inner.sessions[j]
			modelName := s.LatestModel
			if modelName == "" {
				modelName = s.Model
			}
			cost, _ := report.SessionCost(&s)
			p := model.LookupPricing(modelName)
			if cost == 0 && !p.Free && p.InputPerM > 0 {
				cost = model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
			}
			dur := s.LastActivityAt.Sub(s.CreatedAt)
			title := s.Title
			if title == "" {
				title = s.ID
			}
			borderColor := lipgloss.Color("238")
			if j == m.inner.current {
				borderColor = lipgloss.Color("39")
			}
			card := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(borderColor).
				Width(cardW - 2).
				Padding(0, 1).
				Render(fmt.Sprintf("%s\n%s · %s\nreqs %d · %s · %s",
					titleStyle.Render(truncateStr(title, cardW-4)),
					modelName, s.AgentMode,
					s.AssistantCount,
					report.FormatDur(dur),
					report.FormatCost(cost, p.Free)))
			cards = append(cards, card)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}

	return strings.Join(rows, "\n") + "\n" + m.renderStatusBar()
}

// ---- Command Palette ----

func (m ExtModel) updateCommandPalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.showCommandPalette = false
	case "enter":
		// Execute selected command.
		filtered := m.filteredCommands()
		if m.cpIndex < len(filtered) {
			cmd := filtered[m.cpIndex]
			m.showCommandPalette = false
			// Trigger the command's action by simulating its key.
			return m.triggerCommand(cmd.Shortcut)
		}
	case "up", "k":
		if m.cpIndex > 0 {
			m.cpIndex--
		}
	case "down", "j":
		filtered := m.filteredCommands()
		if m.cpIndex < len(filtered)-1 {
			m.cpIndex++
		}
	case "backspace":
		if len(m.cpQuery) > 0 {
			m.cpQuery = m.cpQuery[:len(m.cpQuery)-1]
			m.cpIndex = 0
		}
	default:
		// Accumulate typed characters.
		s := msg.String()
		if len(s) == 1 && s[0] >= 32 && s[0] < 127 {
			m.cpQuery += s
			m.cpIndex = 0
		}
	}
	return m, nil
}

func (m ExtModel) filteredCommands() []cpCommand {
	if m.cpQuery == "" {
		return m.cpCommands
	}
	q := strings.ToLower(m.cpQuery)
	var out []cpCommand
	for _, c := range m.cpCommands {
		if strings.Contains(strings.ToLower(c.Name), q) {
			out = append(out, c)
		}
	}
	return out
}

// triggerCommand simulates pressing the key for a command.
func (m ExtModel) triggerCommand(shortcut string) (tea.Model, tea.Cmd) {
	// Handle the shortcut directly rather than re-dispatching a KeyMsg,
	// since constructing a valid tea.KeyMsg requires knowing the exact
	// key type/encoding which varies by terminal.
	switch shortcut {
	case "?":
		m.showHelp = true
	case "s":
		m.showSettings = true
		m.settingsCursor = 0
	case "|":
		m.splitPane = !m.splitPane
	case "v":
		m.paneView = !m.paneView
	case "t":
		m.timeWindow = (m.timeWindow + 1) % 4
	case "l":
		m.logTailing = !m.logTailing
	case "Enter":
		if len(m.inner.sessions) > 0 {
			m.showSessionDetail = true
		}
	case "m":
		m.showModelBreakdown = true
	case "r":
		if len(m.inner.sessions) > 0 {
			m.inner.current = (m.inner.current + 1) % len(m.inner.sessions)
		}
	case "L":
		// Cycle locale via i18n — handled in inner model normally.
	case "q":
		m.inner.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// RenderCommandPalette renders the command palette overlay.
func RenderCommandPalette(commands []cpCommand, query string, index, width, height int) string {
	if width < 40 {
		width = 40
	}
	if height < 8 {
		height = 8
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Command Palette") + "\n")
	b.WriteString(strings.Repeat("─", width-4) + "\n")

	// Search input.
	input := "> " + query
	if query == "" {
		input += dimStyle.Render("type to search…")
	}
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render(input) + "\n")
	b.WriteString(strings.Repeat("·", width-4) + "\n")

	// Filtered list.
	q := strings.ToLower(query)
	for i, c := range commands {
		if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
			continue
		}
		marker := "  "
		if i == index {
			marker = "▶ "
		}
		shortcut := dimStyle.Render(fmt.Sprintf("[%s]", c.Shortcut))
		b.WriteString(fmt.Sprintf("%s%-30s %s\n", marker, c.Name, shortcut))
	}

	b.WriteString("\n" + dimStyle.Render("↑/↓ navigate  Enter select  Esc close"))

	content := b.String()
	return centerPopup(content, width, height)
}

// ---- Settings Panel ----

func (m ExtModel) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.showSettings = false
		// Save settings.
		m.saveSettings()
	case "up", "k":
		if m.settingsCursor > 0 {
			m.settingsCursor--
		}
	case "down", "j":
		if m.settingsCursor < len(m.settingsFields)-1 {
			m.settingsCursor++
		}
	case "left", "h":
		m.cycleSetting(-1)
	case "right", "l":
		m.cycleSetting(1)
	case "enter":
		m.cycleSetting(1)
	}
	return m, nil
}

func (m ExtModel) cycleSetting(dir int) {
	if m.settingsCursor < 0 || m.settingsCursor >= len(m.settingsFields) {
		return
	}
	f := &m.settingsFields[m.settingsCursor]
	if len(f.Options) > 0 {
		// Cycle through options.
		idx := -1
		for i, opt := range f.Options {
			if opt == f.Value {
				idx = i
				break
			}
		}
		idx += dir
		if idx < 0 {
			idx = len(f.Options) - 1
		}
		if idx >= len(f.Options) {
			idx = 0
		}
		f.Value = f.Options[idx]
	}
}

func (m ExtModel) saveSettings() {
	cfg := config.Global()
	for _, f := range m.settingsFields {
		switch f.Key {
		case "theme":
			cfg.Theme = f.Value
		case "currency":
			cfg.Currency = f.Value
		case "timezone":
			cfg.Timezone = f.Value
		case "timeFormat":
			cfg.TimeFormat = f.Value
		case "refreshInterval":
			var n int
			fmt.Sscanf(f.Value, "%d", &n)
			if n > 0 {
				cfg.RefreshInterval = n
			}
		case "budgetDaily":
			var v float64
			fmt.Sscanf(f.Value, "%f", &v)
			cfg.BudgetDaily = v
		case "budgetWeekly":
			var v float64
			fmt.Sscanf(f.Value, "%f", &v)
			cfg.BudgetWeekly = v
		case "budgetMonthly":
			var v float64
			fmt.Sscanf(f.Value, "%f", &v)
			cfg.BudgetMonthly = v
		}
	}
	_ = config.SaveGlobal()
	ApplyGlobalTheme()
}

// RenderSettingsPanel renders the interactive settings panel.
func RenderSettingsPanel(width, height int, fields []settingsField, cursor int) string {
	if width < 50 {
		width = 50
	}
	if height < 10 {
		height = 10
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Settings") + "\n")
	b.WriteString(strings.Repeat("─", width-4) + "\n")

	for i, f := range fields {
		marker := "  "
		if i == cursor {
			marker = "▶ "
		}
		value := valueStyle.Render(f.Value)
		if len(f.Options) > 0 {
			// Show as enum with arrows.
			value = fmt.Sprintf("← %s →", f.Value)
		}
		b.WriteString(fmt.Sprintf("%s%-24s %s\n", marker, labelStyle.Render(f.Label), value))
	}

	b.WriteString("\n" + dimStyle.Render("↑/↓ navigate  ←/→ change  Enter cycle  Esc save & close"))

	content := b.String()
	return centerPopup(content, width, height)
}

// ---- Help Overlay ----

// RenderHelpOverlay renders the help overlay with all keyboard shortcuts,
// categorized by function.
func RenderHelpOverlay(width, height int) string {
	if width < 50 {
		width = 50
	}
	if height < 16 {
		height = 16
	}

	categories := []struct {
		title string
		items []struct{ key, desc string }
	}{
		{
			title: "Navigation",
			items: []struct{ key, desc string }{
				{"r", "Cycle to next session"},
				{"1-4", "Switch tab (stream/context/latency/tools)"},
				{"Tab", "Cycle tabs"},
				{"↑/↓", "Scroll within pane"},
			},
		},
		{
			title: "Filtering",
			items: []struct{ key, desc string }{
				{"t", "Cycle time window (today/week/month/all)"},
				{"l", "Toggle live log tailing"},
			},
		},
		{
			title: "Views",
			items: []struct{ key, desc string }{
				{"|", "Toggle split pane"},
				{"v", "Toggle list/pane (card grid) view"},
				{"L", "Cycle locale"},
			},
		},
		{
			title: "Actions",
			items: []struct{ key, desc string }{
				{"?", "Toggle this help overlay"},
				{"s", "Open settings panel"},
				{"Ctrl+P", "Open command palette"},
				{"Enter", "Session detail popup"},
				{"m", "Model breakdown popup"},
				{"q", "Quit"},
			},
		},
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Help — Keyboard Shortcuts") + "\n")
	b.WriteString(strings.Repeat("─", width-4) + "\n\n")

	for _, cat := range categories {
		b.WriteString(titleStyle.Render(cat.title) + "\n")
		for _, item := range cat.items {
			b.WriteString(fmt.Sprintf("  %-12s %s\n",
				labelStyle.Render(item.key), item.desc))
		}
		b.WriteString("\n")
	}

	b.WriteString(dimStyle.Render("Press ? or Esc to close"))

	content := b.String()
	return centerPopup(content, width, height)
}

// ---- Helpers ----

// filterByTimeWindow filters sessions to only those within the selected
// time window.
func filterByTimeWindow(ss []model.Session, tw int) []model.Session {
	if tw == twAll {
		return ss
	}
	now := time.Now()
	var cutoff time.Time
	switch tw {
	case twToday:
		cutoff = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case twWeek:
		cutoff = now.AddDate(0, 0, -7)
	case twMonth:
		cutoff = now.AddDate(0, -1, 0)
	default:
		return ss
	}
	var out []model.Session
	for _, s := range ss {
		if s.CreatedAt.After(cutoff) || s.CreatedAt.Equal(cutoff) {
			out = append(out, s)
		}
	}
	return out
}
