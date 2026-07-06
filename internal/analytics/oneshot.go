package analytics

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/garywhat/devinmonitor/internal/model"
)

// editTools is the set of tool names that modify files.
var editTools = map[string]bool{
	"edit": true,
	"write": true,
	"notebook_edit": true,
}

// execTools is the set of tool names that execute commands (indicating a
// possible build/test/run between edits, which signals a retry cycle).
var execTools = map[string]bool{
	"exec":          true,
	"shell_command": true,
}

// extractFilePath parses a tool call's JSON arguments and returns the
// file_path field if present.
func extractFilePath(arguments string) string {
	if arguments == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(arguments), &m); err != nil {
		return ""
	}
	v, ok := m["file_path"]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// OneShotRateForSession computes one-shot/retry metrics for a single session.
// A retry is detected when the same file_path is edited again after an exec
// tool call occurred in between (indicating the first edit likely failed or
// was incomplete).
func OneShotRateForSession(s *model.Session) model.OneShotRate {
	return detectRetries(s.Messages)
}

// OneShotRateAggregate computes aggregate one-shot/retry metrics across all
// sessions, tracking per-file retry counts.
func OneShotRateAggregate(ss []model.Session) model.OneShotRate {
	var total model.OneShotRate
	total.FileRetries = map[string]int{}
	for i := range ss {
		per := OneShotRateForSession(&ss[i])
		total.TotalEdits += per.TotalEdits
		total.Retries += per.Retries
		for file, count := range per.FileRetries {
			total.FileRetries[file] += count
		}
	}
	if total.TotalEdits > 0 {
		oneShot := total.TotalEdits - total.Retries
		total.OneShotPct = float64(oneShot) / float64(total.TotalEdits) * 100
	}
	return total
}

// detectRetries scans messages in order, tracking edit operations per file.
// A retry is counted when a file is edited, then an exec occurs, then the
// same file is edited again.
func detectRetries(messages []model.Message) model.OneShotRate {
	var osr model.OneShotRate
	osr.FileRetries = map[string]int{}

	// Track the last edit per file and whether an exec happened since.
	lastEdit := map[string]int{} // file_path -> index of last edit message
	execSinceEdit := map[string]bool{}

	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if editTools[tc.Name] {
				fp := extractFilePath(tc.Arguments)
				if fp == "" {
					continue
				}
				osr.TotalEdits++
				if execSinceEdit[fp] && lastEdit[fp] > 0 {
					osr.Retries++
					osr.FileRetries[fp]++
				}
				lastEdit[fp] = m.NodeID
				execSinceEdit[fp] = false
			} else if execTools[tc.Name] {
				// Mark exec-since-edit for all files that have been edited.
				for fp := range lastEdit {
					execSinceEdit[fp] = true
				}
			}
		}
	}
	if osr.TotalEdits > 0 {
		oneShot := osr.TotalEdits - osr.Retries
		osr.OneShotPct = float64(oneShot) / float64(osr.TotalEdits) * 100
	}
	return osr
}

// RetryRate returns the average retries per edit operation and a sorted list
// of files with the most retries.
func RetryRate(osr model.OneShotRate) (avgRetries float64, topFiles []FileRetry) {
	if osr.TotalEdits > 0 {
		avgRetries = float64(osr.Retries) / float64(osr.TotalEdits)
	}
	for file, count := range osr.FileRetries {
		if count > 0 {
			topFiles = append(topFiles, FileRetry{File: file, Retries: count})
		}
	}
	sort.Slice(topFiles, func(i, j int) bool {
		return topFiles[i].Retries > topFiles[j].Retries
	})
	return
}

// FileRetry pairs a file path with its retry count.
type FileRetry struct {
	File    string
	Retries int
}

// shortFile returns the last two path components for compact display.
func shortFile(path string) string {
	if path == "" {
		return ""
	}
	path = strings.ReplaceAll(path, "\\", "/")
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		return path
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}
