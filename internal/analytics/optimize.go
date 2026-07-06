package analytics

import (
	"fmt"
	"sort"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// WasteScan analyzes sessions for waste patterns and returns optimization
// findings with copy-pasteable fix suggestions.
func WasteScan(ss []model.Session) []model.WasteFinding {
	var findings []model.WasteFinding

	// 1. Cache hit < 80% (unstable context).
	cacheStats := CacheStatsAggregate(ss)
	if cacheStats.CacheRead+cacheStats.InputTokens > 0 {
		hitPct := cacheStats.HitRatio * 100
		if hitPct < 80 {
			findings = append(findings, model.WasteFinding{
				Category:    "cache_miss",
				Description:  fmt.Sprintf("Cache hit ratio is %.1f%% (below 80%% threshold)", hitPct),
				Impact:       fmt.Sprintf("Estimated extra cost from cache misses: $%.2f", CacheSavingsDetailed(ss)*-1*0.1),
				Suggestion:   "Stabilize context by avoiding unnecessary tool calls between requests. Group related operations and minimize context churn. Consider using --plan mode for complex tasks to reduce trial-and-error.",
			})
		}
	}

	// 2. High retry rate on edits.
	osr := OneShotRateAggregate(ss)
	if osr.TotalEdits > 0 {
		retryPct := float64(osr.Retries) / float64(osr.TotalEdits) * 100
		if retryPct > 20 {
			findings = append(findings, model.WasteFinding{
				Category:    "retry_loop",
				Description:  fmt.Sprintf("Edit retry rate is %.1f%% (%d retries out of %d edits)", retryPct, osr.Retries, osr.TotalEdits),
				Impact:       fmt.Sprintf("%d edit operations were retried, wasting tokens on re-edits", osr.Retries),
				Suggestion:   "Review files with high retry counts. Ensure edits are correct before running builds. Use read to verify file state before editing.",
			})
		}
	}

	// 3. Excessive sub-agent fan-out.
	var totalSubAgents int
	var sessionsWithSubAgents int
	for i := range ss {
		n := len(ss[i].SubAgentCalls)
		totalSubAgents += n
		if n > 0 {
			sessionsWithSubAgents++
		}
	}
	if sessionsWithSubAgents > 0 {
		avgFanout := float64(totalSubAgents) / float64(sessionsWithSubAgents)
		if avgFanout > 10 {
			findings = append(findings, model.WasteFinding{
				Category:    "subagent_fanout",
				Description:  fmt.Sprintf("Average sub-agent fan-out is %.1f per session (%d total across %d sessions)", avgFanout, totalSubAgents, sessionsWithSubAgents),
				Impact:       "Excessive parallel sub-agents consume ACU budget without proportional output",
				Suggestion:   "Reduce sub-agent fan-out. Batch related tasks into fewer sub-agents. Use foreground sub-agents for sequential work to avoid redundant exploration.",
			})
		}
	}

	// 4. Long sessions with low output.
	for i := range ss {
		s := &ss[i]
		dur := s.LastActivityAt.Sub(s.CreatedAt)
		if dur > 30*time.Minute && s.OutputTokens < 1000 && s.AssistantCount > 20 {
			findings = append(findings, model.WasteFinding{
				Category:    "low_output_session",
				Description:  fmt.Sprintf("Session %s ran for %s with %d requests but only %s output tokens", s.ID, report.FormatDur(dur), s.AssistantCount, report.FormatTok(s.OutputTokens)),
				Impact:       "Long session with minimal productive output — likely stuck in a loop",
				Suggestion:   "Consider starting a fresh session. Long unproductive sessions accumulate context cost without progress. Use 'devinmonitor session <id>' to inspect what happened.",
			})
			break // report only one example
		}
	}

	// 5. Repeated read calls to same files.
	repeatedReads := detectRepeatedReads(ss)
	if len(repeatedReads) > 0 {
		top := repeatedReads[0]
		findings = append(findings, model.WasteFinding{
			Category:    "repeated_reads",
			Description:  fmt.Sprintf("File %s was read %d times across sessions", shortFile(top.File), top.Count),
			Impact:       fmt.Sprintf("%d redundant read calls, each re-sending file content into context", top.Count-1),
			Suggestion:   "Cache file contents mentally or in notes. Avoid re-reading files that haven't changed. Use grep for targeted searches instead of full file reads.",
		})
	}

	// 6. ACU budget exhaustion alert (partial rate-limit/quota detection).
	var totalACU float64
	for i := range ss {
		totalACU += ss[i].ACUCost
	}
	if totalACU > 0 {
		// Alert when ACU spend is high (no configured limit, use heuristic).
		findings = append(findings, model.WasteFinding{
			Category:    "acu_budget",
			Description:  fmt.Sprintf("Total ACU spend across sessions: %.2f", totalACU),
			Impact:       "Monitor ACU budget to avoid exhaustion. Devin uses ACU instead of time-based limits.",
			Suggestion:   "Set up budget alerts with 'devinmonitor budget --daily <limit>'. Track ACU spend trends with 'devinmonitor daily'.",
		})
	}

	return findings
}

// fileReadCount tracks how many times a file was read.
type fileReadCount struct {
	File  string
	Count int
}

// detectRepeatedReads finds files that were read many times across sessions.
func detectRepeatedReads(ss []model.Session) []fileReadCount {
	readCount := map[string]int{}
	for i := range ss {
		for _, m := range ss[i].Messages {
			if m.Role != "assistant" {
				continue
			}
			for _, tc := range m.ToolCalls {
				if tc.Name == "read" {
					fp := extractFilePath(tc.Arguments)
					if fp != "" {
						readCount[fp]++
					}
				}
			}
		}
	}
	var out []fileReadCount
	for fp, count := range readCount {
		if count > 3 {
			out = append(out, fileReadCount{File: fp, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count
	})
	return out
}
