package live

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/garywhat/devinmonitor/internal/model"
)

// RunOptions controls the live-ext run mode.
type RunOptions struct {
	Demo    bool // use synthetic demo data
	Once    bool // render one frame and exit (non-interactive)
	Light   bool // minimal rendering for slow terminals
	Theme   string // override theme
	Width   int    // override width (for --once)
	Height  int    // override height (for --once)
}

// RunLiveExt starts the extended live TUI with the given options.
// This is the entry point for `live-ext --demo`, `--once`, `--light`.
func RunLiveExt(dataDir string, intervalSec int, opts RunOptions) error {
	// Apply theme.
	if opts.Theme != "" {
		ApplyTheme(GetTheme(opts.Theme))
	} else {
		ApplyGlobalTheme()
	}

	if opts.Demo {
		return runDemo(opts)
	}

	// Normal mode: use the extended model with real data.
	if opts.Once {
		return runOnce(dataDir, opts)
	}
	if opts.Light {
		return runLight(dataDir, intervalSec)
	}

	// Full extended live mode.
	return runExtLive(dataDir, intervalSec)
}

// runDemo runs the TUI with synthetic demo data.
func runDemo(opts RunOptions) error {
	ss := GenerateDemoSessions()
	m := newExtModelWithSessions(ss, opts.Light)
	if opts.Once {
		// Render one frame and print to stdout.
		w := opts.Width
		if w == 0 {
			w = 100
		}
		h := opts.Height
		if h == 0 {
			h = 30
		}
		m.width, m.height = w, h
		fmt.Print(m.View())
		return nil
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// runOnce renders a single frame from real data and exits.
func runOnce(dataDir string, opts RunOptions) error {
	r, err := openReaderForExt(dataDir)
	if err != nil {
		return err
	}
	defer r.Close()
	ss, err := r.Sessions()
	if err != nil {
		return err
	}
	m := newExtModelWithSessions(ss, opts.Light)
	w := opts.Width
	if w == 0 {
		w = 100
	}
	h := opts.Height
	if h == 0 {
		h = 30
	}
	m.width, m.height = w, h
	fmt.Print(m.View())
	return nil
}

// runLight runs the extended live TUI in light mode (minimal rendering).
func runLight(dataDir string, intervalSec int) error {
	r, err := openReaderForExt(dataDir)
	if err != nil {
		return err
	}
	defer r.Close()
	m := newExtModel(r, intervalSec, true)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// runExtLive runs the full extended live TUI.
func runExtLive(dataDir string, intervalSec int) error {
	r, err := openReaderForExt(dataDir)
	if err != nil {
		return err
	}
	defer r.Close()
	m := newExtModel(r, intervalSec, false)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// openReaderForExt opens a reader for the extended live mode.
// This is a thin wrapper that will be defined in settings.go where the
// ExtModel lives. We define it here to keep demo.go self-contained.
func openReaderForExt(dataDir string) (extReader, error) {
	return openExtReader(dataDir)
}

// GenerateDemoSessions creates 8 synthetic sessions with realistic data
// for demo mode, screenshots, and testing.
func GenerateDemoSessions() []model.Session {
	now := time.Now()
	models := []string{"claude-sonnet-4-5", "claude-opus-4-1", "gpt-4o", "gemini-2.5-pro", "glm-5-2-high"}
	titles := []string{
		"Refactor auth middleware",
		"Fix race condition in worker pool",
		"Add pagination to API endpoints",
		"Implement dark mode toggle",
		"Optimize database queries",
		"Write integration tests for billing",
		"Migrate from REST to GraphQL",
		"Set up CI/CD pipeline",
	}
	projects := []string{
		"/home/user/projects/webapp",
		"/home/user/projects/api-server",
		"/home/user/projects/mobile-app",
		"/home/user/projects/cli-tool",
	}
	modes := []string{"normal", "plan", "bypass", "normal", "normal"}
	tools := []string{"edit", "exec", "read", "grep", "write", "bash"}

	sessions := make([]model.Session, 0, 8)
	for i := 0; i < 8; i++ {
		startTime := now.Add(-time.Duration(i*3+2) * time.Hour)
		endTime := startTime.Add(time.Duration(30+i*12) * time.Minute)
		modelName := models[i%len(models)]
		msgs := generateDemoMessages(startTime, endTime, i+3)

		s := model.Session{
			ID:              fmt.Sprintf("demo-%04d", i+1),
			WorkingDir:      projects[i%len(projects)],
			BackendType:     "anthropic",
			Model:           modelName,
			LatestModel:     modelName,
			AgentMode:       modes[i%len(modes)],
			CreatedAt:       startTime,
			LastActivityAt:  endTime,
			Title:           titles[i],
			Messages:        msgs,
			InputTokens:     0,
			OutputTokens:    0,
			CacheRead:       0,
			CacheWrite:      0,
			ToolCalls:       map[string]int{},
			AssistantCount:  0,
		}

		// Aggregate from messages.
		for _, msg := range msgs {
			if msg.Role == "assistant" {
				s.AssistantCount++
				if msg.Metrics != nil {
					s.InputTokens += msg.Metrics.InputTokens
					s.OutputTokens += msg.Metrics.OutputTokens
					s.CacheRead += msg.Metrics.CacheReadTokens
					s.CacheWrite += msg.Metrics.CacheWriteTokens
				}
			}
			if msg.Role == "tool" {
				// Count tool calls from preceding assistant message.
			}
			for _, tc := range msg.ToolCalls {
				s.ToolCalls[tc.Name]++
			}
		}

		// Add some tool calls.
		for j, t := range tools {
			s.ToolCalls[t] = (i + 1) * (len(tools) - j)
		}

		// Set cost based on model pricing.
		p := model.LookupPricing(modelName)
		if !p.Free && p.InputPerM > 0 {
			s.CreditCost = model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
		}

		// Add a sub-agent call to some sessions.
		if i%3 == 0 {
			s.SubAgentCalls = []model.SubAgentCall{{
				Title:   "Explore codebase structure",
				Profile: "subagent_explore",
				IsBackground: true,
				Task:    "Find all API endpoint definitions",
				StartTime: startTime.Add(5 * time.Minute),
				EndTime:   startTime.Add(8 * time.Minute),
				HasCompletion: true,
				OutputLen: 1200,
			}}
		}

		sessions = append(sessions, s)
	}
	return sessions
}

// generateDemoMessages creates a sequence of user/assistant/tool messages.
func generateDemoMessages(start, end time.Time, count int) []model.Message {
	var msgs []model.Message
	step := end.Sub(start) / time.Duration(count*2)
	for i := 0; i < count; i++ {
		// User message.
		msgs = append(msgs, model.Message{
			NodeID:    i*2 + 1,
			Role:      "user",
			Content:   fmt.Sprintf("Please help me with task %d: implement the required functionality.", i+1),
			CreatedAt: start.Add(time.Duration(i*2) * step),
		})
		// Assistant message with metrics.
		ttft := 0.8 + float64(i)*0.15
		total := 3.5 + float64(i)*0.5
		tps := 45.0 + float64(i)*3
		msgs = append(msgs, model.Message{
			NodeID:          i*2 + 2,
			Role:            "assistant",
			Content:         fmt.Sprintf("I'll help you implement task %d. Let me analyze the codebase and make the necessary changes.", i+1),
			CreatedAt:       start.Add(time.Duration(i*2+1) * step),
			GenerationModel: "claude-sonnet-4-5",
			FinishReason:    "tool_calls",
			RequestID:       fmt.Sprintf("req-%d", i+1),
			Metrics: &model.Metrics{
				TTFTMs:           ttft * 1000,
				TotalTimeMs:      total * 1000,
				InputTokens:      15000 + int64(i)*2000,
				OutputTokens:     800 + int64(i)*150,
				CacheReadTokens:  12000 + int64(i)*1800,
				CacheWriteTokens: 2000 + int64(i)*300,
				TokensPerSec:     tps,
			},
			ToolCalls: []model.ToolCall{
				{ID: fmt.Sprintf("tc-%d-1", i), Name: "read", Arguments: `{"file":"src/main.go"}`},
				{ID: fmt.Sprintf("tc-%d-2", i), Name: "edit", Arguments: `{"file":"src/main.go","old":"foo","new":"bar"}`},
			},
			NumTokensPreceding: 15000 + i*2000,
		})
		// Tool result.
		msgs = append(msgs, model.Message{
			NodeID:    i*2 + 3,
			Role:      "tool",
			Content:   "File updated successfully.",
			CreatedAt: start.Add(time.Duration(i*2+1)*step + 500*time.Millisecond),
			ToolCallID: fmt.Sprintf("tc-%d-2", i),
		})
	}
	return msgs
}
