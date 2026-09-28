package reader

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/garywhat/devinmonitor/internal/model"
)

// v1Reader adapts the current Devin CLI schema (refinery v1 era).
type v1Reader struct {
	db   *sql.DB
	path string
	ver  int
	// Counts from the last Sessions() call, so a caller can say how much of the
	// history came from the second source and whether anything was unreadable.
	recoveredFromTranscripts int
	skippedTranscripts       int
	transcriptErr            error
}

func newV1Reader(path string, ver int) (*v1Reader, error) {
	// Read-only, WAL-safe, query_only to avoid blocking Devin's writes.
	// modernc.org/sqlite supports URI-style options via "file:" prefix.
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_journal_mode=WAL&_query_only=1&_busy_timeout=5000", abs)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// Single connection avoids WAL contention surprises.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	r := &v1Reader{db: db, path: path, ver: ver}
	if ver == 0 {
		r.ver = r.detectVersion()
	}
	return r, nil
}

func (r *v1Reader) detectVersion() int {
	var v int
	// Table may not exist on very old builds; ignore error.
	_ = r.db.QueryRow("SELECT COALESCE(MAX(version),0) FROM refinery_schema_history").Scan(&v)
	return v
}

func (r *v1Reader) SchemaVersion() int { return r.ver }
func (r *v1Reader) DBPath() string     { return r.path }
func (r *v1Reader) Close() error       { return r.db.Close() }

// ---- JSON helper structs (mirror Devin's chat_message shape) ----

type chatMessage struct {
	MessageID  string          `json:"message_id"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []rawToolCall   `json:"tool_calls"`
	Thinking   json.RawMessage `json:"thinking"`
	Metadata   *msgMetadata    `json:"metadata"`
}

type rawToolCall struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
	Index     int                    `json:"index"`
	Kind      string                 `json:"kind"`
	// OpenAI-style nested form
	Function *struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	} `json:"function"`
}

type msgMetadata struct {
	NumTokens          *int     `json:"num_tokens"`
	RequestID          string   `json:"request_id"`
	Metrics            *metrics `json:"metrics"`
	FinishReason       string   `json:"finish_reason"`
	GenerationModel    string   `json:"generation_model"`
	CreatedAt          string   `json:"created_at"`
	NumTokensPreceding *int     `json:"num_tokens_preceding"`
}

type metrics struct {
	TTFTMs           *float64 `json:"ttft_ms"`
	TotalTimeMs      *float64 `json:"total_time_ms"`
	InputTokens      *int64   `json:"input_tokens"`
	OutputTokens     *int64   `json:"output_tokens"`
	CacheReadTokens  *int64   `json:"cache_read_tokens"`
	CacheWriteTokens *int64   `json:"cache_creation_tokens"`
	TokensPerSec     *float64 `json:"tokens_per_sec"`
}

type sessionMetadata struct {
	TotalCreditCost float64 `json:"total_credit_cost"`
	TotalACUCost    float64 `json:"total_acu_cost"`
	// ResponseDimensions is Devin's own accounting for the session -- the same
	// figures its CLI's /session-stats prints. It was in a column we already
	// read and we ignored it, which is how our token totals stayed 2-3x too
	// high without anyone noticing. It is parsed now so a report can be
	// cross-checked against the vendor instead of trusted on its own.
	ResponseDimensions []struct {
		UID  string `json:"uid"`
		Kind struct {
			Metric           *dimMetric `json:"Metric"`
			CumulativeMetric *dimMetric `json:"CumulativeMetric"`
		} `json:"kind"`
	} `json:"response_dimensions"`
}

// dimMetric is one value inside a response dimension. Devin wraps numeric
// metrics and string labels in the same shape, so both fields are read.
type dimMetric struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Text  string  `json:"text"`
}

// ResponseDimension is one of Devin's own reported figures for a session.
type ResponseDimension struct {
	UID   string
	Label string
	Value float64
	Text  string
}

// ResponseDimensions flattens Devin's per-session accounting.
func (m *sessionMetadata) Dimensions() []ResponseDimension {
	if m == nil {
		return nil
	}
	out := make([]ResponseDimension, 0, len(m.ResponseDimensions))
	for _, d := range m.ResponseDimensions {
		k := d.Kind.CumulativeMetric
		if k == nil {
			k = d.Kind.Metric
		}
		if k == nil {
			continue
		}
		out = append(out, ResponseDimension{UID: d.UID, Label: k.Label, Value: k.Value, Text: k.Text})
	}
	return out
}

// ---- Sessions ----

func (r *v1Reader) Sessions() ([]model.Session, error) {
	rows, err := r.db.Query(`
		SELECT id, working_directory, backend_type, model, agent_mode,
		       created_at, last_activity_at, COALESCE(title,''), COALESCE(main_chain_id,0),
		       COALESCE(workspace_dirs,''), hidden, COALESCE(metadata,'')
		FROM sessions ORDER BY last_activity_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var out []model.Session
	for rows.Next() {
		var s model.Session
		var createdAt, lastAct int64
		var wsDirs, meta string
		if err := rows.Scan(&s.ID, &s.WorkingDir, &s.BackendType, &s.Model, &s.AgentMode,
			&createdAt, &lastAct, &s.Title, &s.MainChainID, &wsDirs, &s.Hidden, &meta); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		s.CreatedAt = time.Unix(createdAt, 0)
		s.LastActivityAt = time.Unix(lastAct, 0)
		s.WorkspaceDirs = parseStringArray(wsDirs)
		var sm sessionMetadata
		if meta != "" {
			_ = json.Unmarshal([]byte(meta), &sm)
		}
		s.CreditCost = sm.TotalCreditCost
		s.ACUCost = sm.TotalACUCost
		s.VendorDimensions = toVendorDimensions(sm.Dimensions())
		// Skip hidden/deleted sessions so every consumer of Sessions() sees
		// consistent totals and lists. This matches FilteredSessions and
		// SessionCount, which already exclude hidden rows. Direct lookup via
		// Session(id) still returns a hidden session when requested by exact ID.
		if s.Hidden {
			continue
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load messages for each session.
	for i := range out {
		msgs, err := r.loadMessages(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Messages = msgs
		aggregate(&out[i])
	}

	// Second source: transcripts.
	//
	// Devin prunes sessions.db and keeps transcripts/*.json, so reading only
	// the database hid 11 of 18 sessions on the machine this was measured
	// against. Database rows win on a conflict -- they are finer-grained (per
	// request rather than per step) and carry the ACU accounting -- and a
	// transcript is used only for a session the database no longer has.
	ts, skipped, terr := loadTranscriptSessions(transcriptDirFor(r.path))
	if terr != nil {
		// A broken transcripts directory must not fail the report -- the
		// database is the primary source -- but it must not be swallowed
		// either: going quiet here is how "we read half the history" becomes
		// invisible. The error is kept so a caller can surface it.
		r.transcriptErr = terr
	} else {
		merged, recovered := mergeTranscriptSessions(out, ts)
		out = merged
		r.recoveredFromTranscripts = recovered
		r.skippedTranscripts = skipped
	}
	return out, nil
}

func (r *v1Reader) Session(id string) (*model.Session, error) {
	var s model.Session
	var createdAt, lastAct int64
	var wsDirs, meta string
	err := r.db.QueryRow(`
		SELECT id, working_directory, backend_type, model, agent_mode,
		       created_at, last_activity_at, COALESCE(title,''), COALESCE(main_chain_id,0),
		       COALESCE(workspace_dirs,''), hidden, COALESCE(metadata,'')
		FROM sessions WHERE id = ?`, id).
		Scan(&s.ID, &s.WorkingDir, &s.BackendType, &s.Model, &s.AgentMode,
			&createdAt, &lastAct, &s.Title, &s.MainChainID, &wsDirs, &s.Hidden, &meta)
	if err != nil {
		return nil, fmt.Errorf("query session %s: %w", id, err)
	}
	s.CreatedAt = time.Unix(createdAt, 0)
	s.LastActivityAt = time.Unix(lastAct, 0)
	s.WorkspaceDirs = parseStringArray(wsDirs)
	var sm sessionMetadata
	if meta != "" {
		_ = json.Unmarshal([]byte(meta), &sm)
	}
	s.CreditCost = sm.TotalCreditCost
	s.ACUCost = sm.TotalACUCost
	s.VendorDimensions = toVendorDimensions(sm.Dimensions())

	msgs, err := r.loadMessages(id)
	if err != nil {
		return nil, err
	}
	s.Messages = msgs
	aggregate(&s)
	return &s, nil
}

func (r *v1Reader) loadMessages(sessionID string) ([]model.Message, error) {
	rows, err := r.db.Query(`
		SELECT node_id, chat_message, created_at
		FROM message_nodes WHERE session_id = ? ORDER BY node_id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var out []model.Message
	for rows.Next() {
		var m model.Message
		var raw string
		var createdAt int64
		if err := rows.Scan(&m.NodeID, &raw, &createdAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		// Provisional: the message_nodes.created_at COLUMN. It is overwritten
		// below by the message's own timestamp when the JSON carries one.
		m.CreatedAt = time.Unix(createdAt, 0)

		var cm chatMessage
		if err := json.Unmarshal([]byte(raw), &cm); err != nil {
			continue
		}
		m.Role = cm.Role
		m.Content = cm.Content
		m.ToolCallID = cm.ToolCallID
		if cm.Metadata != nil {
			// The message's own timestamp, and the authoritative one.
			//
			// The created_at COLUMN is a batch write time: this database has
			// 48,254 rows sharing only 57 distinct values, one covering 16,371
			// rows. Bucketing by it put 60% of all tokens on the wrong day and
			// erased whole days from the daily/weekly/monthly reports. The
			// JSON value is per-message (tens of thousands of distinct values)
			// and covers every row, so it wins; the column stays only as a
			// fallback for a row that omitted it.
			if t, ok := parseRFC3339(cm.Metadata.CreatedAt); ok {
				m.CreatedAt = t
			}
			m.RequestID = cm.Metadata.RequestID
			m.MessageID = cm.MessageID
			m.FinishReason = cm.Metadata.FinishReason
			m.GenerationModel = cm.Metadata.GenerationModel
			if cm.Metadata.NumTokensPreceding != nil {
				m.NumTokensPreceding = *cm.Metadata.NumTokensPreceding
			}
			if cm.Metadata.Metrics != nil {
				m.Metrics = decodeMetrics(cm.Metadata.Metrics)
			}
		}
		for _, tc := range cm.ToolCalls {
			name := tc.Name
			args := tc.Arguments
			if name == "" && tc.Function != nil {
				name = tc.Function.Name
				args = tc.Function.Arguments
			}
			if name == "" {
				continue
			}
			argBytes, _ := json.Marshal(args)
			m.ToolCalls = append(m.ToolCalls, model.ToolCall{ID: tc.ID, Name: name, Arguments: string(argBytes)})
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// parseRFC3339 parses the timestamp Devin writes inside a message's metadata,
// e.g. "2026-08-31T03:29:15.685135Z".
func parseRFC3339(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// toVendorDimensions converts the reader's parse shape into the model's.
func toVendorDimensions(in []ResponseDimension) []model.VendorDimension {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.VendorDimension, 0, len(in))
	for _, d := range in {
		out = append(out, model.VendorDimension{UID: d.UID, Label: d.Label, Value: d.Value, Text: d.Text})
	}
	return out
}

func decodeMetrics(m *metrics) *model.Metrics {
	out := &model.Metrics{}
	if m.TTFTMs != nil {
		out.TTFTMs = *m.TTFTMs
	}
	if m.TotalTimeMs != nil {
		out.TotalTimeMs = *m.TotalTimeMs
	}
	if m.InputTokens != nil {
		out.InputTokens = *m.InputTokens
	}
	if m.OutputTokens != nil {
		out.OutputTokens = *m.OutputTokens
	}
	if m.CacheReadTokens != nil {
		out.CacheReadTokens = *m.CacheReadTokens
	}
	if m.CacheWriteTokens != nil {
		out.CacheWriteTokens = *m.CacheWriteTokens
	}
	if m.TokensPerSec != nil {
		out.TokensPerSec = *m.TokensPerSec
	}
	return out
}

// aggregate fills session-level totals from messages.
func aggregate(s *model.Session) {
	if s.ToolCalls == nil {
		s.ToolCalls = map[string]int{}
	}

	// First pass: collect agent_id from tool result messages and
	// completion notifications from system messages.
	type completionInfo struct {
		endTime   time.Time
		outputLen int
	}
	completions := map[string]completionInfo{} // agent_id → completion
	toolCallIDToAgentID := map[string]string{}

	for _, m := range s.Messages {
		// Tool result messages contain "Background subagent started with agent_id=XXX".
		if m.Role == "tool" {
			if aid := extractAgentID(m.Content); aid != "" && m.ToolCallID != "" {
				toolCallIDToAgentID[m.ToolCallID] = aid
			}
		}
		// System messages may contain <subagent_completion_notification>.
		if m.Role == "system" && strings.Contains(m.Content, "<subagent_completion_notification>") {
			if aid := extractAgentID(m.Content); aid != "" {
				completions[aid] = completionInfo{
					endTime:   m.CreatedAt,
					outputLen: len(m.Content),
				}
			}
		}
	}

	// Second pass: aggregate assistant messages.
	//
	// Deduplicate run_subagent and read_subagent calls by tool_call_id
	// (Devin stores each assistant message twice: streaming + final).
	seenSubAgent := map[string]bool{}
	seenReadSubAgent := map[string]bool{}
	// Deduplicate the request itself, for exactly the same reason.
	//
	// The tool-call dedup above has always been here, but the METRICS were
	// summed from every node -- and Devin writes 2-3 nodes per request, each
	// carrying an identical copy of the metrics object. Measured against
	// Devin's own response_dimensions, that inflated every token total by
	// 2.00x-3.42x (e.g. one session read 169.6M where Devin reports 49.6M).
	// Counting each request once brings us back to 1.00x on the sessions where
	// compaction is excluded.
	//
	// The key is the message id, falling back to the request id and finally to
	// the node, so a row that lacks both still counts exactly once.
	seenRequest := map[string]bool{}
	for _, m := range s.Messages {
		if m.Role != "assistant" {
			continue
		}
		key := m.MessageID
		if key == "" {
			key = m.RequestID
		}
		if key == "" {
			key = "node:" + strconv.Itoa(m.NodeID)
		}
		if !seenRequest[key] {
			seenRequest[key] = true
			s.AssistantCount++
			if m.Metrics != nil {
				s.InputTokens += m.Metrics.InputTokens
				s.OutputTokens += m.Metrics.OutputTokens
				s.CacheRead += m.Metrics.CacheReadTokens
				s.CacheWrite += m.Metrics.CacheWriteTokens
				// Compaction is billed but excluded from Devin's own
				// per-session accounting, so track it separately. Both
				// numbers are real; see model.CompactionUsage.
				if model.IsCompactorModel(m.GenerationModel) {
					s.Compaction.InputTokens += m.Metrics.InputTokens
					s.Compaction.OutputTokens += m.Metrics.OutputTokens
					s.Compaction.CacheRead += m.Metrics.CacheReadTokens
					s.Compaction.CacheWrite += m.Metrics.CacheWriteTokens
				}
			}
		}
		for _, tc := range m.ToolCalls {
			s.ToolCalls[tc.Name]++
			if tc.Name == "run_subagent" && tc.Arguments != "" {
				if tc.ID != "" && seenSubAgent[tc.ID] {
					continue
				}
				if tc.ID != "" {
					seenSubAgent[tc.ID] = true
				}
				sa := parseSubAgentCall(tc.Arguments, m.CreatedAt)
				if sa != nil {
					if aid, ok := toolCallIDToAgentID[tc.ID]; ok {
						sa.AgentID = aid
						if comp, ok := completions[aid]; ok && !comp.endTime.IsZero() {
							// Only treat the subagent as completed when the
							// completion notification carries a real timestamp.
							// A zero/missing created_at yields a ~1970 end time
							// and a huge negative pseudo-duration, which is why
							// some subagents previously showed nonsense "-" or
							// -496k-hour artifacts.
							sa.HasCompletion = true
							sa.EndTime = comp.endTime
							sa.OutputLen = comp.outputLen
						}
					}
					s.SubAgentCalls = append(s.SubAgentCalls, *sa)
				}
			}
			if tc.Name == "read_subagent" {
				if tc.ID != "" && seenReadSubAgent[tc.ID] {
					continue
				}
				if tc.ID != "" {
					seenReadSubAgent[tc.ID] = true
				}
				s.ReadSubAgentCalls++
			}
		}
		if m.GenerationModel != "" {
			s.LatestModel = m.GenerationModel
		}
	}
}

// extractAgentID finds an agent_id (hex string) from message content.
var agentIDRe = regexp.MustCompile(`agent_id=([a-f0-9]+)`)

func extractAgentID(content string) string {
	m := agentIDRe.FindStringSubmatch(content)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// parseSubAgentCall extracts subagent metadata from run_subagent arguments JSON.
func parseSubAgentCall(argsJSON string, createdAt time.Time) *model.SubAgentCall {
	var args struct {
		Title        string `json:"title"`
		Profile      string `json:"profile"`
		IsBackground bool   `json:"is_background"`
		Task         string `json:"task"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil
	}
	return &model.SubAgentCall{
		Title:        args.Title,
		Profile:      args.Profile,
		IsBackground: args.IsBackground,
		Task:         args.Task,
		StartTime:    createdAt,
	}
}

// parseStringArray handles workspace_dirs which may be JSON array or empty.
func parseStringArray(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err == nil {
		return arr
	}
	// Some builds store comma-separated.
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			arr = append(arr, p)
		}
	}
	return arr
}

// SortedToolNames returns tool names sorted by count desc, for stable display.
func SortedToolNames(tc map[string]int) []string {
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range tc {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.k
	}
	return out
}

// itoa helper to avoid strconv import noise in callers.
func itoa(i int) string { return strconv.Itoa(i) }

var _ = itoa
