package analytics

import (
	"sort"

	"github.com/garywhat/devinmonitor/internal/model"
)

// AnalyzeContext breaks down what fills a session's context window by
// tool type and message category. It uses NumTokensPreceding to track
// context growth and attributes token deltas to the preceding content.
func AnalyzeContext(s *model.Session) model.ContextAnalysis {
	ca := model.ContextAnalysis{
		SessionID:  s.ID,
		ByTool:     map[string]int64{},
		ByCategory: map[string]int64{},
	}

	var prevTokens int64
	var totalTokens int64

	for _, m := range s.Messages {
		cur := int64(m.NumTokensPreceding)
		if cur <= 0 {
			continue
		}
		delta := cur - prevTokens
		if delta < 0 {
			delta = 0 // compaction event; skip negative deltas
		}
		if delta > 0 {
			ca.ByCategory[m.Role] += delta
			totalTokens += delta
			// Attribute tool-result tokens to the tool that produced them.
			if m.Role == "tool" {
				ca.ByTool["tool_result"] += delta
			}
			// Attribute assistant tool-call tokens to the tools called.
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					ca.ByTool[tc.Name] += delta / int64(len(m.ToolCalls))
				}
			}
		}
		prevTokens = cur
	}

	ca.TotalTokens = totalTokens
	return ca
}

// ContextBreakdown returns sorted category and tool breakdowns for display.
type ContextBreakdown struct {
	Categories []ContextEntry
	Tools      []ContextEntry
	Total      int64
}

// ContextEntry is a single entry in the context breakdown.
type ContextEntry struct {
	Label string
	Tokens int64
	Pct    float64
}

// ContextBreakdownForSession returns a sorted, display-ready context breakdown.
func ContextBreakdownForSession(s *model.Session) ContextBreakdown {
	ca := AnalyzeContext(s)
	var cb ContextBreakdown
	cb.Total = ca.TotalTokens

	for label, tokens := range ca.ByCategory {
		entry := ContextEntry{Label: label, Tokens: tokens}
		if cb.Total > 0 {
			entry.Pct = float64(tokens) / float64(cb.Total) * 100
		}
		cb.Categories = append(cb.Categories, entry)
	}
	sort.Slice(cb.Categories, func(i, j int) bool {
		return cb.Categories[i].Tokens > cb.Categories[j].Tokens
	})

	for label, tokens := range ca.ByTool {
		entry := ContextEntry{Label: label, Tokens: tokens}
		if cb.Total > 0 {
			entry.Pct = float64(tokens) / float64(cb.Total) * 100
		}
		cb.Tools = append(cb.Tools, entry)
	}
	sort.Slice(cb.Tools, func(i, j int) bool {
		return cb.Tools[i].Tokens > cb.Tools[j].Tokens
	})

	return cb
}
