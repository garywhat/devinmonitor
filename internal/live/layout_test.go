package live

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/i18n"
	"github.com/garywhat/devinmonitor/internal/model"
)

// TestMain freezes the two pieces of global state the renderers read, which
// they would otherwise take from the developer's machine and silently change
// every measured width:
//
//   - config.Path() memoises behind sync.Once, so without this the real
//     ~/.devinmonitor would be captured as the config dir;
//   - the i18n catalogues must be loaded (main.go calls i18n.Init()) and the
//     locale pinned, or i18n.T returns raw keys whose length differs from the
//     translated string.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "devinmonitor-live-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "live_test: MkdirTemp:", err)
		os.Exit(1)
	}
	os.Setenv("DEVINMONITOR_CONFIG_DIR", dir)
	if err := i18n.Init(); err != nil {
		fmt.Fprintln(os.Stderr, "live_test: i18n.Init:", err)
		os.Exit(1)
	}
	i18n.SetLocale("en")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// layoutFixture is deliberately rich: several requests, distinct finish
// reasons, tools and a CJK title, so width accounting is exercised with
// double-width runes. Timestamps are fixed instants; the assertions below never
// compare header text, which is the one wall-clock element.
func layoutFixture() []model.Session {
	base := time.Date(2026, 9, 22, 10, 0, 0, 0, time.Local)
	s := model.Session{
		ID: "layout-fixture", Title: "重构认证中间件 refactor auth middleware",
		Model: "claude-sonnet-4-5", AgentMode: "normal",
		CreatedAt: base, LastActivityAt: base.Add(30 * time.Minute),
		CreditCost: 1.25, InputTokens: 51000, OutputTokens: 2900,
		CacheRead: 41400, CacheWrite: 6900, AssistantCount: 12,
		ToolCalls: map[string]int{"read": 6, "edit": 3, "exec": 2},
	}
	for i := 0; i < 12; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		msg := model.Message{
			NodeID: i + 1, Role: "assistant", CreatedAt: at,
			NumTokensPreceding: 3000 + i*100, FinishReason: "stop",
			GenerationModel: "claude-sonnet-4-5",
			Metrics: &model.Metrics{
				InputTokens: 4000, OutputTokens: 240, CacheReadTokens: 3400,
				TTFTMs: 900 + float64(i)*40, TotalTimeMs: 5000, TokensPerSec: 48 + float64(i),
			},
		}
		switch i % 4 {
		case 1:
			msg.FinishReason = "length"
		case 2:
			msg.FinishReason = "tool_calls"
			msg.ToolCalls = []model.ToolCall{{ID: "t1", Name: "read", Arguments: `{"path":"/repo/a.go"}`}}
		}
		s.Messages = append(s.Messages, msg)
	}
	return []model.Session{s}
}

// renderFor builds the model the way RenderSnapshotSession does, so these tests
// exercise the same path the public snapshot helpers use.
func renderFor(ss []model.Session, w, h, current, tab int) model_ {
	return model_{sessions: ss, width: w, height: h, current: current, tab: tab}
}

// TestEveryTierFitsTheTerminal is the regression guard for a measured defect:
// the compact tier rendered 20 lines in 93 columns into an 80x12 pane, and the
// tiny tier rendered 54 columns into 40. Height overflow merely loses lines
// (bubbletea drops them from the top of an over-tall frame), but WIDTH overflow
// wraps and tears the box drawing apart.
//
// The matrix covers every tier and both sides of both breakpoints.
func TestEveryTierFitsTheTerminal(t *testing.T) {
	ss := layoutFixture()
	sizes := [][2]int{
		{200, 40}, {120, 28}, {119, 28}, {120, 27}, {119, 20},
		{80, 12}, {79, 12}, {80, 11}, {79, 40}, {60, 20},
		{40, 5}, {40, 6}, {30, 4}, {100, 30},
	}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			for tab := 0; tab < 4; tab++ {
				out := stripANSI(renderFor(ss, w, h, 0, tab).View())
				lines := strings.Split(out, "\n")
				if len(lines) > h {
					t.Errorf("tab %d: rendered %d lines into a %d-row terminal", tab, len(lines), h)
				}
				for i, l := range lines {
					if n := len([]rune(l)); n > w {
						t.Errorf("tab %d: line %d is %d columns wide, terminal is %d\n  %q",
							tab, i+1, n, w, l)
					}
				}
			}
		})
	}
}

// TestTierSelection pins which tier each size selects, including the exact
// boundary pairs. The thresholds are `width >= 120 && height >= 28` for Full
// and `width >= 80 && height >= 12` for Compact, so one unit below either
// threshold must fall to the next tier down.
func TestTierSelection(t *testing.T) {
	ss := layoutFixture()
	cases := []struct {
		w, h     int
		wantFull bool
		wantCmp  bool
		wantTiny bool
	}{
		{200, 40, true, false, false},
		{120, 28, true, false, false}, // exactly at both thresholds
		{119, 28, false, true, false}, // one column short
		{120, 27, false, true, false}, // one row short
		{80, 12, false, true, false},  // exactly at the compact thresholds
		{79, 12, false, false, false}, // one column short -> mini
		{80, 11, false, false, false}, // one row short -> mini
		{40, 5, false, false, true},   // below 6 rows -> tiny
		{40, 6, false, false, false},  // exactly 6 rows -> mini
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dx%d", c.w, c.h), func(t *testing.T) {
			m := renderFor(ss, c.w, c.h, 0, 0)
			got := m.renderTier()
			var want string
			switch {
			case c.wantFull:
				want = m.viewFull()
			case c.wantCmp:
				want = m.viewCompact()
			case c.wantTiny:
				want = m.viewTiny()
			default:
				want = m.viewMini()
			}
			if got != want {
				t.Errorf("renderTier picked the wrong layout at %dx%d", c.w, c.h)
			}
		})
	}
}

// TestFitToTerminal covers the clamp directly, including cases a rendering test
// cannot reach cheaply.
func TestFitToTerminal(t *testing.T) {
	t.Run("clips height", func(t *testing.T) {
		if got := fitToTerminal("a\nb\nc\nd", 10, 2); got != "a\nb" {
			t.Errorf("got %q, want %q", got, "a\nb")
		}
	})
	t.Run("reserves one column for autowrap", func(t *testing.T) {
		// Content exactly as wide as the terminal is what triggers the
		// autowrap bug, so the clamp must leave one column free.
		got := fitToTerminal(strings.Repeat("x", 20), 10, 5)
		if n := len([]rune(got)); n >= 10 {
			t.Errorf("clamped line is %d columns, want < 10", n)
		}
	})
	t.Run("does not cut a double-width rune in half", func(t *testing.T) {
		got := fitToTerminal("中文模型名称", 5, 5)
		if strings.ContainsRune(got, '\ufffd') {
			t.Fatalf("truncation produced a replacement character: %q", got)
		}
		if n := len([]rune(got)); n > 3 {
			t.Errorf("got %q (%d runes), expected at most 3 runes within 5 columns", got, n)
		}
	})
	t.Run("leaves a fitting frame alone", func(t *testing.T) {
		in := "short\nlines"
		if got := fitToTerminal(in, 40, 20); got != in {
			t.Errorf("got %q, want the input unchanged", got)
		}
	})
	t.Run("empty and degenerate input", func(t *testing.T) {
		if got := fitToTerminal("", 10, 10); got != "" {
			t.Errorf("empty frame = %q, want empty", got)
		}
		if got := fitToTerminal("abc", 0, 10); got != "abc" {
			t.Errorf("zero width should pass through, got %q", got)
		}
	})
}

// TestSnapshotEntryPointsAgree checks the three public render helpers produce
// the same frame at tab 0, since they chain into one another.
func TestSnapshotEntryPointsAgree(t *testing.T) {
	ss := layoutFixture()
	for _, sz := range [][2]int{{200, 40}, {100, 30}, {60, 20}} {
		w, h := sz[0], sz[1]
		a := RenderSnapshot(ss, w, h)
		b := RenderSnapshotTab(ss, w, h, 0)
		c := RenderSnapshotSession(ss, w, h, 0, 0)
		if a != b || b != c {
			t.Errorf("%dx%d: the three snapshot entry points disagree at tab 0", w, h)
		}
	}
}

// TestViewStatesDoNotPanic walks the early-return states, which sit ahead of the
// tier logic and are easy to break while refactoring it.
func TestViewStatesDoNotPanic(t *testing.T) {
	ss := layoutFixture()
	t.Run("loading", func(t *testing.T) {
		m := model_{loading: true, width: 100, height: 30}
		if m.View() == "" {
			t.Error("loading view is empty")
		}
	})
	t.Run("error", func(t *testing.T) {
		m := model_{err: fmt.Errorf("boom"), width: 100, height: 30}
		if !strings.Contains(m.View(), "boom") {
			t.Error("error view does not show the error")
		}
	})
	t.Run("no sessions", func(t *testing.T) {
		m := model_{width: 100, height: 30}
		if m.View() == "" {
			t.Error("empty view is empty")
		}
	})
	t.Run("zero width", func(t *testing.T) {
		m := model_{sessions: ss, width: 0, height: 30}
		if m.View() == "" {
			t.Error("zero-width view is empty")
		}
	})
}
