// Package model defines the normalized data structures used across devinmonitor.
//
// These types are independent of Devin CLI's internal SQLite schema so that
// the reader layer can adapt to schema changes without touching reports/UI.
package model

import (
	"strings"
	"time"
)

// Session is a normalized Devin CLI session.
// VendorDimension is one figure Devin reports for a session.
type VendorDimension struct {
	UID   string // e.g. "input_tokens", "agent_messages", "model"
	Label string // human label as Devin writes it, e.g. "Input tokens"
	Value float64
	Text  string // set instead of Value for string-valued dimensions
}

// VendorDimensionValue returns a numeric dimension by uid, and whether it was
// present. A missing dimension is normal: the field is populated asynchronously
// by Devin, so a session still running may not have it yet.
func (s *Session) VendorDimensionValue(uid string) (float64, bool) {
	for _, d := range s.VendorDimensions {
		if d.UID == uid {
			return d.Value, true
		}
	}
	return 0, false
}

// CompactionUsage is the token volume attributable to context compaction.
//
// Compaction is a system operation -- Devin re-summarises the conversation to
// stay inside the context window -- and it is billed in tokens like anything
// else, but Devin's own per-session accounting (metadata.response_dimensions)
// EXCLUDES it. Measured on the real database: counting compaction put us 1.082x
// and 1.208x above Devin's figures on two sessions, and removing it matched
// both exactly.
//
// So both numbers are real and they answer different questions: the raw totals
// say what the session consumed, and (totals - compaction) says what Devin
// attributes to the agent. They are kept apart rather than collapsed, because
// silently picking one would make our numbers disagree with the bill for no
// visible reason.
type CompactionUsage struct {
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
}

// IsCompactorModel reports whether a generation model is Devin's compactor
// rather than an agent model.
func IsCompactorModel(name string) bool {
	return strings.Contains(strings.ToLower(name), "compact")
}

// TokenTotal returns this session's total tokens, cache included.
func (s *Session) TokenTotal() int64 {
	return s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
}

// BillableTokenTotal returns the total tokens excluding compaction, i.e. the
// quantity Devin's own response_dimensions reports.
func (s *Session) BillableTokenTotal() int64 {
	return s.TokenTotal() - s.Compaction.Total()
}

// Total returns the compaction token volume.
func (c CompactionUsage) Total() int64 {
	return c.InputTokens + c.OutputTokens + c.CacheRead + c.CacheWrite
}

// SessionSource identifies which local record a session was read from.
type SessionSource string

const (
	// SourceSessionsDB is Devin's sessions.db: per-message metrics, latency,
	// finish reasons, tool calls and the ACU/credit accounting.
	SourceSessionsDB SessionSource = "sessions_db"
	// SourceTranscript is Devin's transcripts/<id>.json (ATIF): the only record
	// that survives once sessions.db has been pruned. It has per-STEP metrics
	// and timestamps but no cost accounting.
	SourceTranscript SessionSource = "transcript"
)

// String makes the source printable.
func (s SessionSource) String() string { return string(s) }

type Session struct {
	ID             string
	WorkingDir     string
	BackendType    string
	Model          string
	AgentMode      string // normal / plan / bypass
	CreatedAt      time.Time
	LastActivityAt time.Time
	Title          string
	MainChainID    int
	Hidden         bool
	WorkspaceDirs  []string
	// Source names where this session was read from.
	//
	// Devin prunes sessions.db but keeps transcripts/*.json, so a session can
	// survive only in the transcript. Those rows carry different and coarser
	// numbers (per-step rather than per-request, and no ACU cost at all), so
	// every consumer must be able to tell them apart rather than blending them
	// into one column.
	Source SessionSource
	// Cost from Devin's own accounting (authoritative when non-zero).
	CreditCost float64
	ACUCost    float64
	// Aggregated from assistant messages.
	Messages     []Message
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
	// Compaction is the share of the totals above produced by Devin's
	// context-compaction passes rather than by the agent's own work.
	// Subtracting it yields the figure Devin's own accounting reports.
	Compaction CompactionUsage
	// VendorDimensions is Devin's OWN accounting for this session, from
	// metadata.response_dimensions -- the numbers its CLI's /session-stats
	// prints. It is the only offline cross-check for our totals, so it is
	// carried rather than discarded: a report can show both, and a test can
	// assert they agree instead of trusting ours on its own.
	VendorDimensions []VendorDimension
	ToolCalls        map[string]int // tool name -> count
	AssistantCount   int            // number of assistant turns (= requests)
	// LatestModel is the generation_model from the most recent assistant
	// message. More accurate than the session-level Model field (which is
	// set at creation time and doesn't update when the user switches models).
	LatestModel string
	// SubAgentCalls contains all run_subagent invocations in this session.
	SubAgentCalls []SubAgentCall
	// ReadSubAgentCalls counts how many times the main agent called read_subagent
	// (explicitly waiting for a background subagent to finish).
	ReadSubAgentCalls int
}

// Message is a single chat message node.
type Message struct {
	NodeID     int
	Role       string // system / user / assistant / tool
	Content    string
	CreatedAt  time.Time
	ToolCallID string // for role=tool messages: the tool_call_id this result belongs to
	// Assistant-only fields (zero for other roles).
	Metrics         *Metrics
	FinishReason    string
	GenerationModel string
	// MessageID and RequestID identify the request this message belongs to.
	// Devin writes 2-3 message nodes per request -- each carrying an identical
	// copy of the metrics -- so both are needed to count a request once. See
	// the dedup in the reader's aggregation.
	MessageID          string
	RequestID          string
	ToolCalls          []ToolCall
	NumTokensPreceding int // context size at this point (if available)
}

// Metrics holds per-request performance/usage data from assistant messages.
type Metrics struct {
	TTFTMs           float64 // time to first token
	TotalTimeMs      float64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TokensPerSec     float64
}

// ToolCall is a single tool invocation extracted from an assistant message.
type ToolCall struct {
	ID   string
	Name string
	// Arguments kept as raw JSON string; readers don't parse it.
	Arguments string
}

// SubAgentCall is a parsed run_subagent invocation.
type SubAgentCall struct {
	Title         string    // task title
	Profile       string    // subagent_explore / subagent_general / etc.
	IsBackground  bool      // whether the subagent runs in the background
	Task          string    // full task description
	AgentID       string    // agent_id from tool result (for background subagents)
	StartTime     time.Time // when the run_subagent tool call was made
	EndTime       time.Time // when completion notification arrived (zero if not found)
	HasCompletion bool      // whether a completion notification was found
	OutputLen     int       // character count of the completion notification content
}

// ModelStats aggregates usage for a single model across sessions.
type ModelStats struct {
	Name         string
	Requests     int
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
	CreditCost   float64
	ACUCost      float64
	// Latency distribution (ms).
	TTFTs        []float64
	TotalTimes   []float64
	TokensPerSec []float64
	// Finish reason counts.
	FinishReasons map[string]int
}

// Percentile returns the p-th percentile (0-100) of xs. Returns 0 if empty.
//
// Uses linear-interpolation-between-closest-ranks (the "rank" method familiar
// from numpy's default), which is well-behaved on small samples: for 2 samples
// p95 lands near the larger value instead of collapsing to the minimum that a
// nearest-rank `int((n-1)*p/100)` index would produce.
func Percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	if len(xs) == 1 {
		return xs[0]
	}
	// Sort a copy; the input is left untouched.
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sortFloats(sorted)
	// Rank r in [0, n-1].
	r := (p / 100.0) * float64(len(sorted)-1)
	lo := int(r)
	hi := lo + 1
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := r - float64(lo)
	return sorted[lo] + frac*(sorted[hi]-sorted[lo])
}

func sortFloats(a []float64) {
	// Insertion sort — slices are small (per-session request counts).
	for i := 1; i < len(a); i++ {
		key := a[i]
		j := i - 1
		for j >= 0 && a[j] > key {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = key
	}
}

// TimeBucket is a daily/weekly/monthly aggregation.
type TimeBucket struct {
	Label        string // date / week / month label
	Sessions     int    // distinct sessions that contributed
	Requests     int
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CreditCost   float64
	ACUCost      float64
	ByModel      map[string]*ModelStats
}

// DayStart returns t truncated to midnight local.
func DayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
