// Package export produces normalized, schema-stable JSON exports of session data.
//
// The export format is independent of Devin CLI's internal SQLite schema, so
// it can be uploaded to a future web service without that service needing to
// know about schema migrations. This is the stable contract for sharing.
package export

import (
	"encoding/json"
	"io"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// SchemaVersion is the version of this normalized export format.
// Bump when the structure changes in a backward-incompatible way.
const SchemaVersion = 1

// Document is the top-level export container.
type Document struct {
	ExportSchema int       `json:"export_schema"`
	GeneratedAt  time.Time `json:"generated_at"`
	Sessions     []ExpSession `json:"sessions"`
}

// ExpSession is a normalized session for export.
type ExpSession struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Project        string    `json:"project"`
	WorkingDir     string    `json:"working_dir"`
	Model          string    `json:"model"`
	AgentMode      string    `json:"agent_mode"`
	BackendType    string    `json:"backend_type"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	DurationSec    float64   `json:"duration_sec"`
	Requests       int       `json:"requests"`
	InputTokens    int64     `json:"input_tokens"`
	OutputTokens   int64     `json:"output_tokens"`
	CacheReadTokens int64    `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CreditCost     float64   `json:"credit_cost"`
	ACUCost        float64   `json:"acu_cost"`
	EstimatedCost  float64   `json:"estimated_cost"`
	IsFree         bool      `json:"is_free"`
	ToolCalls      map[string]int `json:"tool_calls"`
	Requests2      []ExpRequest `json:"requests_detail,omitempty"`
}

// ExpRequest is a per-request record (assistant turn).
type ExpRequest struct {
	RequestID       string  `json:"request_id"`
	Model           string  `json:"model"`
	FinishReason    string  `json:"finish_reason"`
	CreatedAt       time.Time `json:"created_at"`
	TTFTMs          float64 `json:"ttft_ms"`
	TotalTimeMs     float64 `json:"total_time_ms"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CacheReadTokens int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	TokensPerSec    float64 `json:"tokens_per_sec"`
	ContextSize     int     `json:"context_size"`
	ToolCalls       []string `json:"tool_calls"`
}

// BuildDocument creates a normalized export from sessions.
// includeRequests controls whether per-request detail is included.
func BuildDocument(ss []model.Session, includeRequests bool) Document {
	doc := Document{
		ExportSchema: SchemaVersion,
		GeneratedAt:  time.Now(),
		Sessions:     make([]ExpSession, 0, len(ss)),
	}
	for _, s := range ss {
		p := model.LookupPricing(s.Model)
		est := model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite)
		es := ExpSession{
			ID:               s.ID,
			Title:            s.Title,
			Project:          baseProject(s.WorkingDir),
			WorkingDir:       s.WorkingDir,
			Model:            s.Model,
			AgentMode:        s.AgentMode,
			BackendType:      s.BackendType,
			CreatedAt:        s.CreatedAt,
			LastActivityAt:   s.LastActivityAt,
			DurationSec:      s.LastActivityAt.Sub(s.CreatedAt).Seconds(),
			Requests:         s.AssistantCount,
			InputTokens:      s.InputTokens,
			OutputTokens:     s.OutputTokens,
			CacheReadTokens:  s.CacheRead,
			CacheWriteTokens: s.CacheWrite,
			CreditCost:       s.CreditCost,
			ACUCost:          s.ACUCost,
			EstimatedCost:    est,
			IsFree:           p.Free,
			ToolCalls:        s.ToolCalls,
		}
		if includeRequests {
			for _, m := range s.Messages {
				if m.Role != "assistant" {
					continue
				}
				er := ExpRequest{
					RequestID:    m.RequestID,
					Model:        m.GenerationModel,
					FinishReason: m.FinishReason,
					CreatedAt:    m.CreatedAt,
					ContextSize:  m.NumTokensPreceding,
				}
				if m.Metrics != nil {
					er.TTFTMs = m.Metrics.TTFTMs
					er.TotalTimeMs = m.Metrics.TotalTimeMs
					er.InputTokens = m.Metrics.InputTokens
					er.OutputTokens = m.Metrics.OutputTokens
					er.CacheReadTokens = m.Metrics.CacheReadTokens
					er.CacheWriteTokens = m.Metrics.CacheWriteTokens
					er.TokensPerSec = m.Metrics.TokensPerSec
				}
				for _, tc := range m.ToolCalls {
					er.ToolCalls = append(er.ToolCalls, tc.Name)
				}
				es.Requests2 = append(es.Requests2, er)
			}
		}
		doc.Sessions = append(doc.Sessions, es)
	}
	return doc
}

// WriteJSON writes the document as pretty-printed JSON to w.
func WriteJSON(w io.Writer, doc Document) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func baseProject(dir string) string {
	if dir == "" {
		return ""
	}
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '/' {
			return dir[i+1:]
		}
	}
	return dir
}
