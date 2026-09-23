// Package report computes aggregated views over sessions for CLI output.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// FormatDur formats a duration as compact h/m/s.
func FormatDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// FormatTok formats token counts with k/M suffixes.
func FormatTok(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// FormatCost formats USD cost; shows "free" for zero on free models.
func FormatCost(cost float64, isFree bool) string {
	if isFree || cost == 0 {
		return "free"
	}
	return fmt.Sprintf("$%.2f", cost)
}

// SessionCost returns the authoritative cost (credit) if non-zero,
// otherwise an estimate from token pricing.
func SessionCost(s *model.Session) (cost float64, estimated bool) {
	if s.CreditCost > 0 || s.ACUCost > 0 {
		return s.CreditCost + s.ACUCost, false
	}
	p := model.LookupPricing(s.Model)
	return model.EstimateCost(p, s.InputTokens, s.OutputTokens, s.CacheRead, s.CacheWrite), p.Free || p.InputPerM == 0 && p.OutputPerM == 0
}

// ---- Sessions report ----

type SessionRow struct {
	ID            string
	Title         string
	Model         string
	Mode          string
	Project       string
	Requests      int
	InputTok      int64
	OutputTok     int64
	CacheRead     int64
	Duration      time.Duration
	Cost          float64
	CostEstimated bool
	IsFree        bool
	ToolCalls     map[string]int
	SubAgents     int // workflow sub-agent count (0 if standalone)
	// LastActivity is the session's last-activity timestamp, used to sort
	// the list newest-first (matches the reader's ORDER BY and docs).
	LastActivity time.Time
}

// BuildSessionRows converts sessions to display rows, sorted newest first.
func BuildSessionRows(ss []model.Session) []SessionRow {
	rows := make([]SessionRow, 0, len(ss))
	for _, s := range ss {
		dur := s.LastActivityAt.Sub(s.CreatedAt)
		cost, est := SessionCost(&s)
		p := model.LookupPricing(s.Model)
		rows = append(rows, SessionRow{
			ID:            s.ID,
			Title:         s.Title,
			Model:         s.Model,
			Mode:          s.AgentMode,
			Project:       baseProject(s.WorkingDir),
			Requests:      s.AssistantCount,
			InputTok:      s.InputTokens,
			OutputTok:     s.OutputTokens,
			CacheRead:     s.CacheRead,
			Duration:      dur,
			Cost:          cost,
			CostEstimated: est,
			IsFree:        p.Free,
			ToolCalls:     s.ToolCalls,
			SubAgents:     len(s.SubAgentCalls),
			LastActivity:  s.LastActivityAt,
		})
	}
	// Newest first by last activity, matching the reader's ORDER BY and the
	// documented "newest first" behavior. (Previously this sorted by request
	// count, which contradicted the docs.)
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].LastActivity.After(rows[j].LastActivity)
	})
	return rows
}

func baseProject(dir string) string {
	if dir == "" {
		return "-"
	}
	// Use the last path component. BOTH separators count: the data can come
	// from Windows, and internal/project already split on "\\". Splitting on
	// "/" alone made the `projects` listing name a directory
	// "C:\\work\\proj" while the `project` and `git` commands called the same
	// directory "proj".
	dir = strings.TrimRight(dir, "/\\")
	if i := strings.LastIndexAny(dir, "/\\"); i >= 0 {
		return dir[i+1:]
	}
	return dir
}

// ---- Projects report ----

// ProjectRow contains per-project aggregated stats.
type ProjectRow struct {
	Name       string
	Sessions   int
	Requests   int
	InputTok   int64
	OutputTok  int64
	CacheRead  int64
	CacheWrite int64
	Cost       float64
	IsFree     bool
	Models     []string
}

// BuildProjectRows aggregates stats per project (by working directory).
func BuildProjectRows(ss []model.Session) []ProjectRow {
	byProj := map[string]*ProjectRow{}
	for _, s := range ss {
		proj := baseProject(s.WorkingDir)
		pr := byProj[proj]
		if pr == nil {
			pr = &ProjectRow{Name: proj, IsFree: true}
			byProj[proj] = pr
		}
		pr.Sessions++
		pr.Requests += s.AssistantCount
		pr.InputTok += s.InputTokens
		pr.OutputTok += s.OutputTokens
		pr.CacheRead += s.CacheRead
		pr.CacheWrite += s.CacheWrite
		cost, _ := SessionCost(&s)
		pr.Cost += cost
		// IsFree is true only if ALL sessions in the project carry no cost
		// (authoritative credit/ACU or estimate). Any non-zero cost makes it
		// a paid project. (Starts true; flipped off on first paid session.)
		if cost > 0 {
			pr.IsFree = false
		}
		// Collect unique models.
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.GenerationModel == "" {
				continue
			}
			if !contains(pr.Models, m.GenerationModel) {
				pr.Models = append(pr.Models, m.GenerationModel)
			}
		}
	}
	rows := make([]ProjectRow, 0, len(byProj))
	for _, pr := range byProj {
		rows = append(rows, *pr)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Requests > rows[j].Requests
	})
	return rows
}

// ---- Agents report ----

// AgentStats holds subagent usage statistics per profile.
type AgentStats struct {
	Profile      string
	Calls        int
	Sessions     int             // number of distinct sessions using this profile
	Background   int             // background calls
	Foreground   int             // foreground calls
	ReadCalls    int             // read_subagent calls (main agent waiting for subagent)
	Completed    int             // calls with a completion notification
	Durations    []time.Duration // durations for completed calls
	AvgDuration  time.Duration
	MaxDuration  time.Duration
	TaskLens     []int // task description lengths
	AvgTaskLen   int
	MaxTaskLen   int
	OutputLens   []int // completion notification content lengths
	AvgOutputLen int
	MaxOutputLen int
	SessionIDs   map[string]bool

	// ChainHeads and ChainHeadNodes surface the v17 `subagent_heads` table,
	// which records (session_id, agent_id, chain_node_id, updated_at) — one row
	// per subagent, with a node id and nothing we can interpret beyond the
	// column name.
	//
	// ChainHeads is the number of recorded head rows in the sessions attributed
	// to this profile; ChainHeadNodes are their chain_node_id values, in the
	// session/agent order they were read. Both are zero when the database has
	// no head rows, which is the case on every database observed so far, so on
	// current data nothing downstream changes.
	//
	// The table has no profile column, so heads are attributed at session
	// granularity using the same most-active-profile rule already used for
	// ReadCalls below. Counts are therefore exact per session and approximate
	// per profile; see BuildAgentStatsWithHeads.
	ChainHeads     int
	ChainHeadNodes []int
}

// AgentChainHead is one recorded subagent chain head, as read from the v17
// `subagent_heads` table by internal/reader (reader.SubagentHead).
//
// SessionID is supplied by the map key in BuildAgentStatsWithHeads, so it is
// not repeated here. Nothing in this type interprets ChainNodeID beyond its
// column name: it is the node the subagent's chain currently heads at.
type AgentChainHead struct {
	AgentID     string
	ChainNodeID int
}

// BuildAgentStats aggregates subagent usage across all sessions, grouped by profile.
//
// It carries no chain-head data, which is what every caller but one wants:
// internal/export renders the same rows without a place for the optional v17
// column. Passing no heads yields exactly the output this function produced
// before chain heads existed, field for field.
func BuildAgentStats(ss []model.Session) []AgentStats {
	return BuildAgentStatsWithHeads(ss, nil)
}

// BuildAgentStatsWithHeads is BuildAgentStats plus the v17 `subagent_heads`
// records, keyed by session ID. A nil or empty map means "no heads recorded"
// and reproduces BuildAgentStats exactly — no existing field changes, only the
// ChainHeads/ChainHeadNodes fields get values, and only when heads are passed.
//
// Attribution, stated plainly because the table does not make it for us:
// subagent_heads records (session_id, agent_id) with NO profile column, while
// these rows are grouped by profile. So each session's heads are credited to
// the profile that dominated that session's run_subagent calls — the same rule
// the read_subagent block below already applies, and the only rule that keeps
// every head counted exactly once. Consequences, both deliberate:
//
//   - A profile's count is exact when its sessions are exclusive to it and
//     approximate when a session used several profiles; the TOTAL row is exact.
//   - A session whose heads exist but from which no run_subagent call was
//     parsed has no dominant profile and contributes no heads anywhere. That
//     needs a session with heads and no subagent call, which cannot happen if
//     parsing sees the call.
func BuildAgentStatsWithHeads(ss []model.Session, headsBySession map[string][]AgentChainHead) []AgentStats {
	byProfile := map[string]*AgentStats{}
	for _, s := range ss {
		for _, sa := range s.SubAgentCalls {
			prof := sa.Profile
			if prof == "" {
				prof = "unknown"
			}
			st := byProfile[prof]
			if st == nil {
				st = &AgentStats{Profile: prof, SessionIDs: map[string]bool{}}
				byProfile[prof] = st
			}
			st.Calls++
			if sa.IsBackground {
				st.Background++
			} else {
				st.Foreground++
			}
			if sa.HasCompletion {
				st.Completed++
				// Cap the duration at the parent session's lifetime: a
				// sub-agent cannot outlive its session. This guards against
				// timestamp skew from Devin's duplicate (streaming+final)
				// message nodes, which can carry wildly different created_at
				// values and otherwise produce inflated (e.g. ~312h) or
				// negative pseudo-durations.
				d := sa.EndTime.Sub(sa.StartTime)
				sessDur := s.LastActivityAt.Sub(s.CreatedAt)
				if sessDur < 0 {
					sessDur = 0
				}
				if d > 0 {
					if d > sessDur {
						d = sessDur
					}
					st.Durations = append(st.Durations, d)
				}
				if sa.OutputLen > 0 {
					st.OutputLens = append(st.OutputLens, sa.OutputLen)
				}
			}
			if len(sa.Task) > 0 {
				st.TaskLens = append(st.TaskLens, len(sa.Task))
			}
			if !st.SessionIDs[s.ID] {
				st.SessionIDs[s.ID] = true
				st.Sessions++
			}
		}
		// Count read_subagent calls per profile is not possible (read_subagent
		// args only have agent_id, not profile). So we distribute read_subagent
		// calls as a session-level metric, not per-profile. We'll track it
		// separately.
	}
	// Aggregate read_subagent calls across all sessions.
	// Since read_subagent doesn't specify a profile, we attribute them to
	// the profile that was used in that session. If a session has multiple
	// profiles, we can't attribute precisely, so we count per-session.
	// For simplicity, we count total read_subagent calls and distribute
	// them proportionally. But actually, it's more useful to show per-profile.
	// Let's count read_subagent per session and attribute to the profile
	// that has the most calls in that session.
	for _, s := range ss {
		if s.ReadSubAgentCalls == 0 {
			continue
		}
		// Find the dominant profile in this session.
		profCalls := map[string]int{}
		for _, sa := range s.SubAgentCalls {
			prof := sa.Profile
			if prof == "" {
				prof = "unknown"
			}
			profCalls[prof]++
		}
		var domProf string
		var domCount int
		for p, c := range profCalls {
			if c > domCount {
				domCount = c
				domProf = p
			}
		}
		if domProf != "" && byProfile[domProf] != nil {
			byProfile[domProf].ReadCalls += s.ReadSubAgentCalls
		}
	}
	// Credit recorded chain heads to each session's dominant profile, per the
	// rule documented on BuildAgentStatsWithHeads.
	//
	// Ties are broken by profile name rather than by map iteration order. The
	// read_subagent block above leaves ties to the map (so a tie is
	// nondeterministic there), but it is deliberately left untouched: making it
	// deterministic would change an existing value in tie cases, which this
	// change is not allowed to do. Nothing here reuses that loop's result, so
	// the two rules cannot disagree on anything but the choice of tie winner.
	for _, s := range ss {
		heads := headsBySession[s.ID]
		if len(heads) == 0 {
			continue
		}
		profCalls := map[string]int{}
		for _, sa := range s.SubAgentCalls {
			prof := sa.Profile
			if prof == "" {
				prof = "unknown"
			}
			profCalls[prof]++
		}
		var domProf string
		var domCount int
		for p, c := range profCalls {
			if c > domCount || (c == domCount && p < domProf) {
				domCount = c
				domProf = p
			}
		}
		st := byProfile[domProf]
		if st == nil {
			continue
		}
		st.ChainHeads += len(heads)
		for _, h := range heads {
			st.ChainHeadNodes = append(st.ChainHeadNodes, h.ChainNodeID)
		}
	}
	// Compute averages.
	for _, st := range byProfile {
		if len(st.Durations) > 0 {
			var sum time.Duration
			for _, d := range st.Durations {
				sum += d
				if d > st.MaxDuration {
					st.MaxDuration = d
				}
			}
			st.AvgDuration = sum / time.Duration(len(st.Durations))
		}
		if len(st.TaskLens) > 0 {
			var sum int
			for _, l := range st.TaskLens {
				sum += l
				if l > st.MaxTaskLen {
					st.MaxTaskLen = l
				}
			}
			st.AvgTaskLen = sum / len(st.TaskLens)
		}
		if len(st.OutputLens) > 0 {
			var sum int
			for _, l := range st.OutputLens {
				sum += l
				if l > st.MaxOutputLen {
					st.MaxOutputLen = l
				}
			}
			st.AvgOutputLen = sum / len(st.OutputLens)
		}
	}
	rows := make([]AgentStats, 0, len(byProfile))
	for _, st := range byProfile {
		rows = append(rows, *st)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Calls > rows[j].Calls
	})
	return rows
}

// HasChainHeads reports whether any row carries a recorded chain head.
//
// This is the gate for the optional "Chain heads" column: on a database where
// Devin is not writing subagent_heads — every database observed so far — it is
// false and the column is omitted entirely, so a user never sees a column that
// can never be populated and the report does not imply that Devin tracks
// subagent chains when it does not.
func HasChainHeads(stats []AgentStats) bool {
	for _, st := range stats {
		if st.ChainHeads > 0 {
			return true
		}
	}
	return false
}

// ---- Models report ----

type ModelRow struct {
	Name         string
	Requests     int
	Sessions     int
	InputTok     int64
	OutputTok    int64
	CacheRead    int64
	CacheWrite   int64
	CreditCost   float64
	ACUCost      float64
	EstCost      float64
	IsFree       bool
	TTFTP50      float64
	TTFTP95      float64
	TotalP50     float64
	TotalP95     float64
	TokPerSecP50 float64
	TruncPct     float64 // % of finish_reason == "length"
	CostPct      float64 // % of total cost across all models
}

// BuildModelRows aggregates per-model stats across all sessions.
func BuildModelRows(ss []model.Session) []ModelRow {
	byModel := map[string]*model.ModelStats{}
	modelSessions := map[string]map[string]bool{} // model -> set of session IDs
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.GenerationModel == "" {
				continue
			}
			ms := byModel[m.GenerationModel]
			if ms == nil {
				ms = &model.ModelStats{Name: m.GenerationModel, FinishReasons: map[string]int{}}
				byModel[m.GenerationModel] = ms
				modelSessions[m.GenerationModel] = map[string]bool{}
			}
			ms.Requests++
			if m.Metrics != nil {
				ms.InputTokens += m.Metrics.InputTokens
				ms.OutputTokens += m.Metrics.OutputTokens
				ms.CacheRead += m.Metrics.CacheReadTokens
				ms.CacheWrite += m.Metrics.CacheWriteTokens
				if m.Metrics.TTFTMs > 0 {
					ms.TTFTs = append(ms.TTFTs, m.Metrics.TTFTMs)
				}
				if m.Metrics.TotalTimeMs > 0 {
					ms.TotalTimes = append(ms.TotalTimes, m.Metrics.TotalTimeMs)
				}
				if m.Metrics.TokensPerSec > 0 {
					ms.TokensPerSec = append(ms.TokensPerSec, m.Metrics.TokensPerSec)
				}
			}
			if m.FinishReason != "" {
				ms.FinishReasons[m.FinishReason]++
			}
			if !modelSessions[m.GenerationModel][s.ID] {
				modelSessions[m.GenerationModel][s.ID] = true
			}
		}
		// Credit cost attribution: attribute session credit to the model that
		// actually served this session's requests (the dominant generation
		// model, by request count). Tokens are already counted under
		// GenerationModel, so attributing cost under the session-level s.Model
		// would split cost and tokens across rows whenever the user switches
		// models mid-session.
		if s.CreditCost > 0 || s.ACUCost > 0 {
			target := dominantModel(&s)
			if target != "" {
				if ms, ok := byModel[target]; ok {
					ms.CreditCost += s.CreditCost
					ms.ACUCost += s.ACUCost
				}
			}
		}
	}

	// Compute total cost across all models for cost% calculation.
	totalCostAll := 0.0
	for _, ms := range byModel {
		p := model.LookupPricing(ms.Name)
		cost := ms.CreditCost + ms.ACUCost
		if cost == 0 {
			cost = model.EstimateCost(p, ms.InputTokens, ms.OutputTokens, ms.CacheRead, ms.CacheWrite)
		}
		totalCostAll += cost
	}

	rows := make([]ModelRow, 0, len(byModel))
	for _, ms := range byModel {
		p := model.LookupPricing(ms.Name)
		est := model.EstimateCost(p, ms.InputTokens, ms.OutputTokens, ms.CacheRead, ms.CacheWrite)
		trunc := 0
		total := 0
		for r, c := range ms.FinishReasons {
			total += c
			if r == "length" {
				trunc += c
			}
		}
		truncPct := 0.0
		if total > 0 {
			truncPct = float64(trunc) / float64(total) * 100
		}
		// Cost% based on actual cost (credit or estimate).
		actualCost := ms.CreditCost + ms.ACUCost
		if actualCost == 0 {
			actualCost = est
		}
		costPct := 0.0
		if totalCostAll > 0 {
			costPct = actualCost / totalCostAll * 100
		}
		rows = append(rows, ModelRow{
			Name:         ms.Name,
			Requests:     ms.Requests,
			Sessions:     len(modelSessions[ms.Name]),
			InputTok:     ms.InputTokens,
			OutputTok:    ms.OutputTokens,
			CacheRead:    ms.CacheRead,
			CacheWrite:   ms.CacheWrite,
			CreditCost:   ms.CreditCost,
			ACUCost:      ms.ACUCost,
			EstCost:      est,
			IsFree:       p.Free,
			TTFTP50:      model.Percentile(ms.TTFTs, 50),
			TTFTP95:      model.Percentile(ms.TTFTs, 95),
			TotalP50:     model.Percentile(ms.TotalTimes, 50),
			TotalP95:     model.Percentile(ms.TotalTimes, 95),
			TokPerSecP50: model.Percentile(ms.TokensPerSec, 50),
			TruncPct:     truncPct,
			CostPct:      costPct,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].InputTok > rows[j].InputTok
	})
	return rows
}

// ---- Model detail ----

// ModelDetail contains detailed stats for a single model.
type ModelDetail struct {
	Name         string
	FirstUsed    time.Time
	LastUsed     time.Time
	Sessions     int
	DaysUsed     int
	Requests     int
	InputTok     int64
	OutputTok    int64
	CacheRead    int64
	CacheWrite   int64
	CreditCost   float64
	ACUCost      float64
	EstCost      float64
	IsFree       bool
	TTFTP50      float64
	TTFTP95      float64
	TotalP50     float64
	TotalP95     float64
	TokPerSecP50 float64
	TruncPct     float64
	Tools        []ToolUsageRow
}

// ToolUsageRow contains per-tool usage stats.
type ToolUsageRow struct {
	Name        string
	Calls       int
	Success     int
	Failed      int
	SuccessRate float64
}

// BuildModelDetail aggregates detailed stats for a single model (fuzzy match).
func BuildModelDetail(ss []model.Session, query string) (*ModelDetail, error) {
	// Find matching model name (case-insensitive substring).
	query = strings.ToLower(query)
	var matchedName string
	byModel := map[string]*model.ModelStats{}
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.GenerationModel == "" {
				continue
			}
			ms := byModel[m.GenerationModel]
			if ms == nil {
				ms = &model.ModelStats{Name: m.GenerationModel, FinishReasons: map[string]int{}}
				byModel[m.GenerationModel] = ms
				if strings.Contains(strings.ToLower(m.GenerationModel), query) {
					if matchedName == "" || len(m.GenerationModel) < len(matchedName) {
						matchedName = m.GenerationModel
					}
				}
			}
			// Aggregate stats.
			ms.Requests++
			if m.Metrics != nil {
				ms.InputTokens += m.Metrics.InputTokens
				ms.OutputTokens += m.Metrics.OutputTokens
				ms.CacheRead += m.Metrics.CacheReadTokens
				ms.CacheWrite += m.Metrics.CacheWriteTokens
				if m.Metrics.TTFTMs > 0 {
					ms.TTFTs = append(ms.TTFTs, m.Metrics.TTFTMs)
				}
				if m.Metrics.TotalTimeMs > 0 {
					ms.TotalTimes = append(ms.TotalTimes, m.Metrics.TotalTimeMs)
				}
				if m.Metrics.TokensPerSec > 0 {
					ms.TokensPerSec = append(ms.TokensPerSec, m.Metrics.TokensPerSec)
				}
			}
			if m.FinishReason != "" {
				ms.FinishReasons[m.FinishReason]++
			}
		}
		// Credit cost attribution: use the dominant generation model so the
		// cost lands on the same model row as the tokens it paid for.
		if s.CreditCost > 0 || s.ACUCost > 0 {
			if target := dominantModel(&s); target != "" {
				if ms, ok := byModel[target]; ok {
					ms.CreditCost += s.CreditCost
					ms.ACUCost += s.ACUCost
				}
			}
		}
	}
	if matchedName == "" {
		return nil, fmt.Errorf("no model matching %q found", query)
	}

	ms := byModel[matchedName]
	p := model.LookupPricing(matchedName)
	est := model.EstimateCost(p, ms.InputTokens, ms.OutputTokens, ms.CacheRead, ms.CacheWrite)

	// Collect sessions and time range for this model.
	sessionIDs := map[string]bool{}
	var firstUsed, lastUsed time.Time
	daysUsed := map[string]bool{}
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.GenerationModel != matchedName {
				continue
			}
			sessionIDs[s.ID] = true
			if firstUsed.IsZero() || m.CreatedAt.Before(firstUsed) {
				firstUsed = m.CreatedAt
			}
			if m.CreatedAt.After(lastUsed) {
				lastUsed = m.CreatedAt
			}
			daysUsed[m.CreatedAt.Format("2006-01-02")] = true
		}
	}

	// Truncation percentage.
	trunc, total := 0, 0
	for r, c := range ms.FinishReasons {
		total += c
		if r == "length" {
			trunc += c
		}
	}
	truncPct := 0.0
	if total > 0 {
		truncPct = float64(trunc) / float64(total) * 100
	}

	// Tool usage for this model.
	// Note: Devin's ToolCall doesn't track success/failure, so we only
	// report call counts. The SuccessRate column is shown as "-" when
	// success tracking is unavailable.
	toolStats := map[string]*ToolUsageRow{}
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.GenerationModel != matchedName {
				continue
			}
			for _, tc := range m.ToolCalls {
				name := tc.Name
				if name == "" {
					continue
				}
				tr := toolStats[name]
				if tr == nil {
					tr = &ToolUsageRow{Name: name}
					toolStats[name] = tr
				}
				tr.Calls++
			}
		}
	}
	tools := make([]ToolUsageRow, 0, len(toolStats))
	for _, tr := range toolStats {
		if tr.Calls > 0 {
			tr.SuccessRate = float64(tr.Success) / float64(tr.Calls) * 100
		}
		tools = append(tools, *tr)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Calls > tools[j].Calls
	})

	return &ModelDetail{
		Name:         matchedName,
		FirstUsed:    firstUsed,
		LastUsed:     lastUsed,
		Sessions:     len(sessionIDs),
		DaysUsed:     len(daysUsed),
		Requests:     ms.Requests,
		InputTok:     ms.InputTokens,
		OutputTok:    ms.OutputTokens,
		CacheRead:    ms.CacheRead,
		CacheWrite:   ms.CacheWrite,
		CreditCost:   ms.CreditCost,
		ACUCost:      ms.ACUCost,
		EstCost:      est,
		IsFree:       p.Free,
		TTFTP50:      model.Percentile(ms.TTFTs, 50),
		TTFTP95:      model.Percentile(ms.TTFTs, 95),
		TotalP50:     model.Percentile(ms.TotalTimes, 50),
		TotalP95:     model.Percentile(ms.TotalTimes, 95),
		TokPerSecP50: model.Percentile(ms.TokensPerSec, 50),
		TruncPct:     truncPct,
		Tools:        tools,
	}, nil
}

// ---- Time series (daily / weekly) ----

type TimeRow struct {
	Label         string
	Requests      int
	Sessions      int
	SubAgents     int
	InputTok      int64
	OutputTok     int64
	CacheRead     int64
	CacheWrite    int64
	Cost          float64
	CostEstimated bool
	Models        []string
	ByModel       map[string]*model.ModelStats

	// Internal bookkeeping for cost provenance.
	anyCredit   bool // at least one session carried authoritative credit/ACU cost
	anyEstimate bool // at least one session contributed estimated cost
}

// BuildDaily aggregates by calendar day (local time).
func BuildDaily(ss []model.Session) []TimeRow {
	return buildTimeBuckets(ss, func(t time.Time) string {
		return t.Format("2006-01-02")
	})
}

// BuildWeekly aggregates by week, with configurable start day.
// startDay: 0=Sunday, 1=Monday, ..., 6=Saturday.
func BuildWeekly(ss []model.Session, startDay time.Weekday) []TimeRow {
	return buildTimeBuckets(ss, func(t time.Time) string {
		// Snap to week start.
		days := (int(t.Weekday()) - int(startDay) + 7) % 7
		weekStart := t.AddDate(0, 0, -days)
		return weekStart.Format("2006-01-02")
	})
}

// BuildMonthly aggregates by calendar month.
func BuildMonthly(ss []model.Session) []TimeRow {
	return buildTimeBuckets(ss, func(t time.Time) string {
		return t.Format("2006-01")
	})
}

// WeekLabel converts a week-start date string (2006-01-02) to a "2006-W01" label.
func WeekLabel(weekStartStr string) string {
	t, err := time.Parse("2006-01-02", weekStartStr)
	if err != nil {
		return weekStartStr
	}
	_, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", t.Year(), week)
}

// WeekDateRange returns a human-readable date range for a week-start date string.
func WeekDateRange(weekStartStr string) string {
	t, err := time.Parse("2006-01-02", weekStartStr)
	if err != nil {
		return weekStartStr
	}
	end := t.AddDate(0, 0, 6)
	return fmt.Sprintf("%s ~ %s", t.Format("01-02"), end.Format("01-02"))
}

func buildTimeBuckets(ss []model.Session, key func(time.Time) string) []TimeRow {
	buckets := map[string]*TimeRow{}
	// Track which sessions have been counted per bucket to avoid double-counting.
	sessionSeen := map[string]map[string]bool{}
	for _, s := range ss {
		for _, m := range s.Messages {
			if m.Role != "assistant" || m.Metrics == nil {
				continue
			}
			k := key(m.CreatedAt)
			b := buckets[k]
			if b == nil {
				b = &TimeRow{Label: k, ByModel: map[string]*model.ModelStats{}}
				buckets[k] = b
				sessionSeen[k] = map[string]bool{}
			}
			b.Requests++
			b.InputTok += m.Metrics.InputTokens
			b.OutputTok += m.Metrics.OutputTokens
			b.CacheRead += m.Metrics.CacheReadTokens
			b.CacheWrite += m.Metrics.CacheWriteTokens
			mn := m.GenerationModel
			if mn == "" {
				mn = s.Model
			}
			if b.ByModel[mn] == nil {
				b.ByModel[mn] = &model.ModelStats{Name: mn, FinishReasons: map[string]int{}}
			}
			bm := b.ByModel[mn]
			bm.Requests++
			bm.InputTokens += m.Metrics.InputTokens
			bm.OutputTokens += m.Metrics.OutputTokens
			bm.CacheRead += m.Metrics.CacheReadTokens
			bm.CacheWrite += m.Metrics.CacheWriteTokens
			if !contains(b.Models, mn) {
				b.Models = append(b.Models, mn)
			}
			// Count unique sessions per bucket.
			if !sessionSeen[k][s.ID] {
				sessionSeen[k][s.ID] = true
				b.Sessions++
			}
		}
		// Credit cost and subagent attribution to the day of session activity.
		// Cost is the session's effective cost: authoritative credit/ACU when
		// present, otherwise an estimate from token pricing. Attribution goes
		// to the bucket of the session's last activity.
		if s.CreditCost > 0 || s.ACUCost > 0 || len(s.SubAgentCalls) > 0 {
			k := key(s.LastActivityAt)
			b := buckets[k]
			if b == nil {
				b = &TimeRow{Label: k, ByModel: map[string]*model.ModelStats{}}
				buckets[k] = b
				sessionSeen[k] = map[string]bool{}
			}
			if s.CreditCost > 0 || s.ACUCost > 0 {
				b.Cost += s.CreditCost + s.ACUCost
				b.anyCredit = true
			} else {
				// No official cost: use the estimate so free/unknown sessions
				// are not silently dropped when mixed with paid ones in a bucket.
				c, _ := SessionCost(&s)
				b.Cost += c
				b.anyEstimate = true
			}
			b.SubAgents += len(s.SubAgentCalls)
		}
	}

	// Fill estimated cost for buckets that carry no authoritative credit.
	// (A bucket that mixes paid and free sessions already has both parts
	// added above, so this only covers buckets purely from token data.)
	for _, b := range buckets {
		if b.Cost == 0 && !b.anyCredit {
			var est float64
			for mn, ms := range b.ByModel {
				p := model.LookupPricing(mn)
				est += model.EstimateCost(p, ms.InputTokens, ms.OutputTokens, ms.CacheRead, ms.CacheWrite)
			}
			b.Cost = est
			b.CostEstimated = true
		} else if b.Cost > 0 && b.anyEstimate && !b.anyCredit {
			b.CostEstimated = true
		}
	}

	out := make([]TimeRow, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// dominantModel returns the generation_model used by the most assistant
// messages in the session. This is used to attribute session-level credit/ACU
// cost to the model that actually served those requests. Returns "" when the
// session has no assistant messages with a GenerationModel.
func dominantModel(s *model.Session) string {
	counts := map[string]int{}
	for _, m := range s.Messages {
		if m.Role != "assistant" || m.GenerationModel == "" {
			continue
		}
		counts[m.GenerationModel]++
	}
	if len(counts) == 0 {
		return ""
	}
	best := ""
	max := 0
	for name, c := range counts {
		if c > max {
			max = c
			best = name
		}
	}
	return best
}

// ParseWeekday converts "monday".."sunday" to time.Weekday. Default Monday.
func ParseWeekday(s string) time.Weekday {
	days := map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
		"wednesday": time.Wednesday, "thursday": time.Thursday,
		"friday": time.Friday, "saturday": time.Saturday,
	}
	if d, ok := days[strings.ToLower(s)]; ok {
		return d
	}
	return time.Monday
}
