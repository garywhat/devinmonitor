package live

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/reader"
	"github.com/garywhat/devinmonitor/internal/report"
)

// RunReplay starts the session replay TUI for the given session ID.
// It plays back messages in chronological order with timestamps.
// Controls: Space to pause/resume, Left/Right to step, 'q' to quit.
func RunReplay(dataDir, sessionID string) error {
	r, err := reader.Open(dataDir)
	if err != nil {
		return err
	}
	defer r.Close()
	s, err := r.Session(sessionID)
	if err != nil {
		return err
	}
	m := newReplayModel(s)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// replayModel is the bubbletea model for session replay mode.
type replayModel struct {
	session   *model.Session
	messages  []model.Message
	pos       int       // current message index
	paused    bool
	width     int
	height    int
	quitting  bool
	playSpeed time.Duration // delay between auto-advance
	lastTick  time.Time
}

func newReplayModel(s *model.Session) replayModel {
	msgs := make([]model.Message, len(s.Messages))
	copy(msgs, s.Messages)
	// Sort by creation time (should already be sorted, but ensure).
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0 && msgs[j].CreatedAt.Before(msgs[j-1].CreatedAt); j-- {
			msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
		}
	}
	return replayModel{
		session:   s,
		messages:  msgs,
		paused:    false,
		playSpeed: 800 * time.Millisecond,
	}
}

type replayTickMsg time.Time

func replayTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return replayTickMsg(t) })
}

func (m replayModel) Init() tea.Cmd {
	if len(m.messages) > 1 {
		return replayTick(m.playSpeed)
	}
	return nil
}

func (m replayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case replayTickMsg:
		if m.quitting || m.paused {
			return m, replayTick(m.playSpeed)
		}
		if m.pos < len(m.messages)-1 {
			m.pos++
		}
		return m, replayTick(m.playSpeed)
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case " ":
			m.paused = !m.paused
		case "right", "l":
			if m.pos < len(m.messages)-1 {
				m.pos++
			}
		case "left", "h":
			if m.pos > 0 {
				m.pos--
			}
		case "0":
			m.pos = 0
		case "g":
			m.pos = len(m.messages) - 1
		}
	}
	return m, nil
}

func (m replayModel) View() string {
	if m.quitting {
		return ""
	}
	if len(m.messages) == 0 {
		return "No messages in this session.\nPress q to quit."
	}

	w := m.width
	if w == 0 {
		w = 80
	}
	h := m.height
	if h == 0 {
		h = 24
	}

	var b strings.Builder

	// Header.
	title := m.session.Title
	if title == "" {
		title = m.session.ID
	}
	b.WriteString(titleStyle.Render("Replay: "+title) + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("Session %s  ·  %d messages  ·  position %d/%d",
		m.session.ID, len(m.messages), m.pos+1, len(m.messages))) + "\n")

	// Progress bar.
	pct := 0.0
	if len(m.messages) > 1 {
		pct = float64(m.pos) / float64(len(m.messages)-1) * 100
	}
	b.WriteString(progressBar(pct, float64(w-10)) + "\n\n")

	// Current message.
	msg := m.messages[m.pos]
	b.WriteString(renderReplayMessage(msg, w) + "\n")

	// Controls.
	status := "playing"
	if m.paused {
		status = "paused"
	}
	controls := fmt.Sprintf("[Space] %s  [←/→] step  [g] end  [0] start  [q] quit  ·  %s",
		status, dimStyle.Render(fmt.Sprintf("speed: %v", m.playSpeed)))
	b.WriteString("\n" + dimStyle.Render(controls))

	// Pad to fill height.
	result := b.String()
	lines := strings.Count(result, "\n") + 1
	if lines < h {
		result += strings.Repeat("\n", h-lines)
	}
	return result
}

// renderReplayMessage renders a single message in the replay view.
func renderReplayMessage(msg model.Message, w int) string {
	var b strings.Builder
	ts := msg.CreatedAt.Format("15:04:05")

	var roleStyle lipgloss.Style
	var roleLabel string
	switch msg.Role {
	case "user":
		roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
		roleLabel = "USER"
	case "assistant":
		roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("41")).Bold(true)
		roleLabel = "ASSISTANT"
	case "tool":
		roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true)
		roleLabel = "TOOL"
	case "system":
		roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true)
		roleLabel = "SYSTEM"
	default:
		roleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		roleLabel = strings.ToUpper(msg.Role)
	}

	b.WriteString(fmt.Sprintf("%s %s  ", dimStyle.Render(ts), roleStyle.Render(roleLabel)))

	// Model info for assistant messages.
	if msg.Role == "assistant" && msg.GenerationModel != "" {
		b.WriteString(dimStyle.Render("["+msg.GenerationModel+"]  "))
	}

	b.WriteString("\n")

	// Content (truncated to fit).
	content := msg.Content
	if content == "" && len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			content += fmt.Sprintf("[tool_call: %s(%s)]\n", tc.Name, truncateStr(tc.Arguments, 60))
		}
	}
	maxLines := 15
	lines := strings.Split(content, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, dimStyle.Render(fmt.Sprintf("... (%d more lines)", len(strings.Split(content, "\n"))-maxLines)))
	}
	for _, line := range lines {
		b.WriteString("  " + line + "\n")
	}

	// Metrics for assistant messages.
	if msg.Role == "assistant" && msg.Metrics != nil {
		m := msg.Metrics
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf(
			"TTFT: %.1fs  Total: %.1fs  Rate: %.0f tok/s  In: %s  Out: %s",
			m.TTFTMs/1000, m.TotalTimeMs/1000, m.TokensPerSec,
			report.FormatTok(m.InputTokens), report.FormatTok(m.OutputTokens))) + "\n")
	}

	// Finish reason.
	if msg.FinishReason != "" {
		b.WriteString("  " + labelStyle.Render("finish: ") + msg.FinishReason + "\n")
	}

	return b.String()
}
