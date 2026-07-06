// Package model — extension types for the 98-feature implementation.
// These types are used by feature packages (budget, trends, analytics, etc.)
// and are independent of Devin CLI's schema.
package model

import "time"

// ---- Budget & Cost ----

// Budget holds user-configured spending limits.
type Budget struct {
	Daily   float64 // USD
	Weekly  float64
	Monthly float64
}

// BurnRate computes real-time spending velocity.
type BurnRate struct {
	PerHour   float64 // USD/hr based on recent activity
	PerDay    float64 // USD/day extrapolated
	PerWeek   float64
	PerMonth  float64
}

// CostProjection predicts future spending based on historical patterns.
type CostProjection struct {
	PredictedMonthEnd float64 // predicted total spend by end of current month
	RemainingBudget   float64 // remaining budget (if configured)
	DaysToExhaust     int     // days until budget exhausted (0 = already over)
	Confidence        float64 // 0-1, based on data volume

}

// CostBreakdown holds per-unit cost metrics.
type CostBreakdown struct {
	PerRequest float64
	PerSession float64
	PerToken   float64
	PerDay     float64
}

// ---- Trends & Charts ----

// TrendPoint is a single point in a time series chart.
type TrendPoint struct {
	Label string
	Cost  float64
	Tokens int64
}

// HeatmapCell is one cell in an activity heatmap (weekday × hour).
type HeatmapCell struct {
	Weekday int // 0=Sunday
	Hour    int // 0-23
	Count   int // activity count
	Cost    float64
}

// ContributionDay is one day in a GitHub-style contribution calendar.
type ContributionDay struct {
	Date    time.Time
	Count   int
	Cost    float64
	Level   int // 0-4 intensity level
}

// PeriodComparison compares two time periods side by side.
type PeriodComparison struct {
	Current  TimeBucket
	Previous TimeBucket
	DeltaPct map[string]float64 // metric name → % change
}

// ---- Efficiency & Analytics ----

// CacheStats holds cache efficiency metrics.
type CacheStats struct {
	CacheRead   int64
	CacheWrite  int64
	InputTokens int64
	HitRatio    float64 // cache_read / (cache_read + input_tokens)
	Leverage    float64 // cache_read / total_input
	SavingsUSD  float64 // estimated cost saved by caching
}

// EfficiencyScore is a composite token efficiency metric.
type EfficiencyScore struct {
	TokensPerDollar float64
	TokensPerRequest float64
	OutputVerbosity  float64 // output_tokens / request
	CacheSavingsPct  float64
}

// OneShotRate measures edit success without retries.
type OneShotRate struct {
	TotalEdits  int
	Retries     int
	OneShotPct  float64 // % of edits that succeeded first try
	FileRetries map[string]int // file_path → retry count
}

// TaskCategory classifies a session's work type.
type TaskCategory struct {
	Name string // Coding, Debugging, Testing, etc.
	Count int
	Cost  float64
}

// WasteFinding is a single optimization recommendation.
type WasteFinding struct {
	Category    string // e.g. "cache_miss", "retry_loop", "subagent_fanout"
	Description string
	Impact      string // estimated cost impact
	Suggestion  string
}

// CompactionEvent marks a context window compaction.
type CompactionEvent struct {
	SessionID string
	Timestamp time.Time
	BeforeTokens int
	AfterTokens  int
}

// ModelComparison compares two or more models side by side.
type ModelComparison struct {
	Models []ModelCompareRow
}

// ModelCompareRow is one row in a model comparison.
type ModelCompareRow struct {
	Name         string
	Requests     int
	InputTokens  int64
	OutputTokens int64
	Cost         float64
	AvgLatency   float64
	TokensPerSec float64
	CacheHitPct  float64
}

// ContextAnalysis breaks down what fills a session's context window.
type ContextAnalysis struct {
	SessionID    string
	TotalTokens  int64
	ByTool       map[string]int64 // tool name → estimated token contribution
	ByCategory   map[string]int64 // message type → token contribution
}

// ---- Filter & Search ----

// FilterOptions controls session filtering.
type FilterOptions struct {
	Model      string
	Project    string
	Mode       string // normal/plan/bypass
	FromDate   time.Time
	ToDate     time.Time
	SearchText string
	SortBy     string // cost, tokens, context, duration, recent
	SortDesc   bool
}

// SearchResult is a full-text search match.
type SearchResult struct {
	SessionID string
	NodeID    int
	Role      string
	Snippet   string
	Timestamp time.Time
}

// ---- Project Attribution ----

// ProjectDetail holds drill-down data for a single project.
type ProjectDetail struct {
	Name        string
	Path        string
	Sessions    int
	Cost        float64
	Tokens      int64
	DailyBreakdown []TrendPoint
	ModelBreakdown map[string]*ModelStats
	ToolBreakdown  map[string]int
}

// ToolAttribution distributes cost across tools.
type ToolAttribution struct {
	ToolName  string
	Calls     int
	CostShare float64 // proportional cost
	Tokens    int64
}

// ---- Notifications ----

// Notification is a desktop or webhook notification payload.
type Notification struct {
	Title   string
	Body    string
	Level   string // info, warning, critical
}

// ---- CLI Mode ----

// SessionListItem is a compact session row for `ls --json`.
type SessionListItem struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Model      string `json:"model"`
	Project    string `json:"project"`
	Cost       float64 `json:"cost"`
	Tokens     int64  `json:"tokens"`
	Duration   string `json:"duration"`
	Status     string `json:"status"`
}

// AlertItem is a single alert for `alerts --json`.
type AlertItem struct {
	Kind     string `json:"kind"` // low_context, idle, ghost, budget
	Severity string `json:"severity"` // info, warning, critical
	Message  string `json:"message"`
}

// ---- Reader Extension Types ----

// PromptHistoryEntry is a single prompt from prompt_history table.
type PromptHistoryEntry struct {
	ID        int
	Content   string
	Timestamp time.Time
	SessionID string
	IsShell   bool
}

// RenderedCommit is a rendered commit HTML from rendered_commits table.
type RenderedCommit struct {
	ID             int
	SessionID      string
	SequenceNumber int
	HTML           string
	CreatedAt      time.Time
}

// ToolCallStateEntry is a tool call state record.
type ToolCallStateEntry struct {
	SessionID           string
	ToolCallID          string
	ToolCallJSON        string
	ToolCallUpdateJSON  string
}
