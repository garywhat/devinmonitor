package analytics

import (
	"github.com/garywhat/devinmonitor/internal/model"
)

// DetectCompaction scans a session's messages for context compaction events.
// A compaction is detected when num_tokens_preceding drops significantly
// between consecutive assistant messages (indicating the context window was
// compacted/summarized).
//
// The threshold is a drop of more than 30% from the previous value.
func DetectCompaction(s *model.Session) []model.CompactionEvent {
	var events []model.CompactionEvent
	var prevTokens int

	for _, m := range s.Messages {
		if m.Role != "assistant" || m.NumTokensPreceding <= 0 {
			continue
		}
		cur := m.NumTokensPreceding
		if prevTokens > 0 {
			drop := prevTokens - cur
			dropPct := float64(drop) / float64(prevTokens)
			// Significant drop (>30%) indicates compaction.
			if drop > 0 && dropPct > 0.30 {
				events = append(events, model.CompactionEvent{
					SessionID:     s.ID,
					Timestamp:     m.CreatedAt,
					BeforeTokens:  prevTokens,
					AfterTokens:   cur,
				})
			}
		}
		prevTokens = cur
	}
	return events
}

// DetectCompactionAll scans all sessions for compaction events.
func DetectCompactionAll(ss []model.Session) []model.CompactionEvent {
	var all []model.CompactionEvent
	for i := range ss {
		events := DetectCompaction(&ss[i])
		all = append(all, events...)
	}
	return all
}

// CompactionSummary holds aggregate compaction stats.
type CompactionSummary struct {
	TotalEvents   int
	TotalTokensSaved int
	AvgDropPct    float64
	Events        []model.CompactionEvent
}

// CompactionStats computes aggregate compaction statistics across sessions.
func CompactionStats(ss []model.Session) CompactionSummary {
	events := DetectCompactionAll(ss)
	var sum CompactionSummary
	sum.Events = events
	sum.TotalEvents = len(events)
	var totalDropPct float64
	for _, e := range events {
		saved := e.BeforeTokens - e.AfterTokens
		sum.TotalTokensSaved += saved
		if e.BeforeTokens > 0 {
			totalDropPct += float64(saved) / float64(e.BeforeTokens)
		}
	}
	if sum.TotalEvents > 0 {
		sum.AvgDropPct = totalDropPct / float64(sum.TotalEvents) * 100
	}
	return sum
}
