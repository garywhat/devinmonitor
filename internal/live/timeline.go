package live

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
)

// RunTimeline starts the timeline TUI for the given session ID.
// Shows a visual timeline of message events, color-coded by type.
func RunTimeline(dataDir, sessionID string) error {
	r, err := reader.Open(dataDir)
	if err != nil {
		return err
	}
	defer r.Close()
	s, err := r.Session(sessionID)
	if err != nil {
		return err
	}
	m := newTimelineModel(s)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// timelineModel is the bubbletea model for the timeline view.
type timelineModel struct {
	session  *model.Session
	messages []model.Message
	width    int
	height   int
	scroll   int
	quitting bool
}

func newTimelineModel(s *model.Session) timelineModel {
	msgs := make([]model.Message, len(s.Messages))
	copy(msgs, s.Messages)
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0 && msgs[j].CreatedAt.Before(msgs[j-1].CreatedAt); j-- {
			msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
		}
	}
	return timelineModel{session: s, messages: msgs}
}

func (m timelineModel) Init() tea.Cmd { return nil }

func (m timelineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "down", "j":
			m.scroll++
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		}
	}
	return m, nil
}

func (m timelineModel) View() string {
	if m.quitting {
		return ""
	}
	w := m.width
	if w == 0 {
		w = 80
	}
	h := m.height
	if h == 0 {
		h = 24
	}
	return RenderTimeline(m.session, w, h)
}

// RenderTimeline renders a visual timeline of session message activity.
// Messages are shown on a horizontal timeline, color-coded by type:
// user=blue, assistant=green, tool=yellow, system=muted.
func RenderTimeline(s *model.Session, width, height int) string {
	if width < 40 {
		width = 40
	}
	if height < 10 {
		height = 10
	}

	msgs := s.Messages
	if len(msgs) == 0 {
		return dimStyle.Render("No messages in this session.")
	}

	// Find time range.
	start := msgs[0].CreatedAt
	end := msgs[len(msgs)-1].CreatedAt
	dur := end.Sub(start)
	if dur <= 0 {
		dur = time.Second
	}

	var b strings.Builder
	title := s.Title
	if title == "" {
		title = s.ID
	}
	b.WriteString(titleStyle.Render("Timeline: "+title) + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("Session %s  ·  %d messages  ·  %s → %s  ·  %s",
		s.ID, len(msgs),
		start.Format("15:04:05"), end.Format("15:04:05"),
		formatDurLocal(dur))) + "\n")
	b.WriteString(strings.Repeat("─", width-2) + "\n\n")

	// Timeline bar.
	barW := width - 30
	if barW < 20 {
		barW = 20
	}
	b.WriteString(renderTimelineBar(msgs, start, dur, barW))
	b.WriteString("\n\n")

	// Message list (color-coded, with timeline position markers).
	b.WriteString(titleStyle.Render("Messages") + "\n")
	maxMsgs := height - 10
	if maxMsgs < 5 {
		maxMsgs = 5
	}
	if len(msgs) > maxMsgs {
		msgs = msgs[len(msgs)-maxMsgs:]
	}

	for _, msg := range msgs {
		// Position on timeline (0-1).
		pos := float64(msg.CreatedAt.Sub(start)) / float64(dur)
		markerCol := int(pos * float64(barW))
		if markerCol >= barW {
			markerCol = barW - 1
		}

		var roleStyle lipgloss.Style
		var marker string
		switch msg.Role {
		case "user":
			roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
			marker = "●"
		case "assistant":
			roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("41"))
			marker = "▲"
		case "tool":
			roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
			marker = "◆"
		default:
			roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
			marker = "·"
		}

		ts := msg.CreatedAt.Format("15:04:05")
		content := strings.ReplaceAll(msg.Content, "\n", " ")
		if len(content) > 40 {
			content = content[:40] + "…"
		}
		b.WriteString(fmt.Sprintf("%s %s %s  %s\n",
			dimStyle.Render(ts),
			roleStyle.Render(marker),
			roleStyle.Render(fmt.Sprintf("%-10s", msg.Role)),
			content))
	}

	b.WriteString("\n" + dimStyle.Render("Legend: ● user  ▲ assistant  ◆ tool  · system  · [q] quit"))

	return b.String()
}

// renderTimelineBar renders the horizontal timeline with event markers.
func renderTimelineBar(msgs []model.Message, start time.Time, dur time.Duration, width int) string {
	bar := make([]rune, width)
	for i := range bar {
		bar[i] = '─'
	}

	for _, msg := range msgs {
		pos := float64(msg.CreatedAt.Sub(start)) / float64(dur)
		col := int(pos * float64(width))
		if col < 0 {
			col = 0
		}
		if col >= width {
			col = width - 1
		}
		var marker rune
		switch msg.Role {
		case "user":
			marker = '●'
		case "assistant":
			marker = '▲'
		case "tool":
			marker = '◆'
		default:
			marker = '·'
		}
		bar[col] = marker
	}

	// Color the bar segments.
	var b strings.Builder
	b.WriteString(dimStyle.Render(fmt.Sprintf("%s ", start.Format("15:04"))))
	for i, r := range bar {
		switch r {
		case '●':
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("●"))
		case '▲':
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("41")).Render("▲"))
		case '◆':
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render("◆"))
		case '·':
			b.WriteString(dimStyle.Render("·"))
		default:
			_ = i
			b.WriteString(dimStyle.Render("─"))
		}
	}
	b.WriteString(dimStyle.Render(fmt.Sprintf(" %s", start.Add(dur).Format("15:04"))))
	return b.String()
}

// RenderLogTail renders a live log tailing view showing recent messages
// from all sessions in real-time.
func RenderLogTail(ss []model.Session, width, height int) string {
	if width < 40 {
		width = 40
	}
	if height < 5 {
		height = 5
	}

	// Collect recent messages across all sessions.
	type entry struct {
		sessionID string
		msg       model.Message
	}
	var entries []entry
	for _, s := range ss {
		for _, msg := range s.Messages {
			entries = append(entries, entry{s.ID, msg})
		}
	}

	// Sort by timestamp descending (newest first).
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].msg.CreatedAt.After(entries[j-1].msg.CreatedAt); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}

	// Take the most recent N that fit.
	maxLines := height - 3
	if maxLines < 1 {
		maxLines = 1
	}
	if len(entries) > maxLines {
		entries = entries[:maxLines]
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Live Log Tail") + "\n")
	b.WriteString(strings.Repeat("─", width-2) + "\n")

	for _, e := range entries {
		var roleStyle lipgloss.Style
		switch e.msg.Role {
		case "user":
			roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
		case "assistant":
			roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("41"))
		case "tool":
			roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
		default:
			roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		}
		ts := e.msg.Role
		content := strings.ReplaceAll(e.msg.Content, "\n", " ")
		maxContentW := width - 25
		if maxContentW < 10 {
			maxContentW = 10
		}
		if len(content) > maxContentW {
			content = content[:maxContentW] + "…"
		}
		b.WriteString(fmt.Sprintf("%s %s %s  %s\n",
			dimStyle.Render(e.msg.CreatedAt.Format("15:04:05")),
			roleStyle.Render(fmt.Sprintf("%-9s", ts)),
			dimStyle.Render(truncateStr(e.sessionID, 8)),
			content))
	}

	return b.String()
}

// formatDurLocal formats a duration compactly.
func formatDurLocal(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
