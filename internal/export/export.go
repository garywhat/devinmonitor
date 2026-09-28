// Package export produces normalized, schema-stable JSON exports of session data.
//
// The export format is independent of Devin CLI's internal SQLite schema, so
// machine consumers — `share`, the scripts under scripts/, a dashboard — can
// read it without knowing about schema migrations. It is deliberately a LOCAL
// contract: the tool sends nothing anywhere (the web panel is loopback-only),
// and nothing in this package uploads or opens a socket.
package export

import (
	"encoding/json"
	"io"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/status"
)

// SchemaVersion is the version of this normalized export format.
// Bump when the structure changes in a backward-incompatible way.
const SchemaVersion = 1

// Document is the top-level export container.
type Document struct {
	ExportSchema int          `json:"export_schema"`
	GeneratedAt  time.Time    `json:"generated_at"`
	Sessions     []ExpSession `json:"sessions"`
	// CostBasis is the meter behind the sessions' cost figures, combined: the
	// one value a consumer needs before it may compare any two rows' costs, or
	// this document's total against another document's. "mixed" means the
	// document holds both acu-billed and token-estimated sessions.
	CostBasis status.CostBasis `json:"cost_basis"`
}

// ExpCompaction is the compaction share of a session's token counters.
//
// It is emitted on EVERY session, zeros included, so a consumer can tell "this
// session never compacted" from "this export predates the field" — the two need
// different handling, and an omitted object collapses them.
//
// The keys are snake_case like every other key in this document; mixing styles
// inside one object would make a consumer carry two conventions to read one
// session.
type ExpCompaction struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CacheRead    int64 `json:"cache_read"`
	CacheWrite   int64 `json:"cache_write"`
}

// ExpSession is a normalized session for export.
type ExpSession struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Project          string    `json:"project"`
	WorkingDir       string    `json:"working_dir"`
	Model            string    `json:"model"`
	AgentMode        string    `json:"agent_mode"`
	BackendType      string    `json:"backend_type"`
	CreatedAt        time.Time `json:"created_at"`
	LastActivityAt   time.Time `json:"last_activity_at"`
	DurationSec      float64   `json:"duration_sec"`
	Requests         int       `json:"requests"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	// TotalTokens is the four counters above added together, cache included.
	// It is exported as total_tokens to keep this document on one naming
	// convention (snake_case); the JSON surfaces outside this package spell the
	// same figure "totalTokens".
	TotalTokens int64 `json:"total_tokens"`
	// Compaction is the part of TotalTokens produced by Devin's
	// context-compaction passes rather than by the agent's own work.
	Compaction ExpCompaction `json:"compaction"`
	// BillableTokens is TotalTokens minus Compaction — the figure Devin's own
	// per-session accounting (response_dimensions) reports, and the one
	// scripts/verify-against-vendor.sh compares against.
	//
	// It deliberately does NOT reuse the name of the raw total: "total tokens"
	// means the cache-inclusive, compaction-INCLUDED figure everywhere, so
	// publishing a compaction-excluded number under that name would reproduce
	// the exact same-name-different-meaning bug this export is meant to stamp
	// out. Exported as billable_tokens, snake_case like its neighbours.
	BillableTokens int64   `json:"billable_tokens"`
	CreditCost     float64 `json:"credit_cost"`
	ACUCost        float64 `json:"acu_cost"`
	EstimatedCost  float64 `json:"estimated_cost"`
	IsFree         bool    `json:"is_free"`
	// CostBasis names the meter behind this session's cost. The three component
	// fields above say what the components ARE, but not which one is "the" cost
	// for this session: that takes the precedence rule (credit/ACU wins when
	// non-zero, otherwise the token estimate). A consumer should not have to
	// re-implement that rule, and the known consumer of this document,
	// scripts/verify-against-vendor.sh, would otherwise hold the fourth copy of
	// it. A single session is never "mixed": its figure comes from one meter.
	CostBasis string         `json:"cost_basis"`
	ToolCalls map[string]int `json:"tool_calls"`
	Requests2 []ExpRequest   `json:"requests_detail,omitempty"`
}

// ExpRequest is a per-request record (assistant turn).
type ExpRequest struct {
	RequestID        string    `json:"request_id"`
	Model            string    `json:"model"`
	FinishReason     string    `json:"finish_reason"`
	CreatedAt        time.Time `json:"created_at"`
	TTFTMs           float64   `json:"ttft_ms"`
	TotalTimeMs      float64   `json:"total_time_ms"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	TokensPerSec     float64   `json:"tokens_per_sec"`
	ContextSize      int       `json:"context_size"`
	ToolCalls        []string  `json:"tool_calls"`
}

// BuildDocument creates a normalized export from sessions.
// includeRequests controls whether per-request detail is included.
func BuildDocument(ss []model.Session, includeRequests bool) Document {
	doc := Document{
		ExportSchema: SchemaVersion,
		GeneratedAt:  time.Now(),
		Sessions:     make([]ExpSession, 0, len(ss)),
		CostBasis:    CostBasisOfSessions(ss),
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
			TotalTokens:      s.TokenTotal(),
			Compaction: ExpCompaction{
				InputTokens:  s.Compaction.InputTokens,
				OutputTokens: s.Compaction.OutputTokens,
				CacheRead:    s.Compaction.CacheRead,
				CacheWrite:   s.Compaction.CacheWrite,
			},
			// BillableTokenTotal is total-minus-compaction; if a caller has
			// already zeroed the counters (a synthetic fixture), it still
			// telescopes: Total - Compaction.Total().
			BillableTokens: s.BillableTokenTotal(),
			CreditCost:     s.CreditCost,
			ACUCost:        s.ACUCost,
			EstimatedCost:  est,
			IsFree:         p.Free,
			CostBasis:      string(SessionCostBasis(&s)),
			ToolCalls:      s.ToolCalls,
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
