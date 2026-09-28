package reader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/garywhat/devinmonitor/internal/model"
)

// Transcript ingestion: the second local source.
//
// Devin prunes sessions.db but keeps ~/.local/share/devin/cli/transcripts/*.json
// written in the ATIF format. On the machine this was developed against that
// mattered a lot: 17 transcripts against 7 sessions in the database, with 11
// sessions surviving ONLY as transcripts. Reading only sessions.db therefore
// made about 60% of the session history invisible.
//
// Three properties of the format decided the design, and all three were
// measured rather than assumed:
//
//  1. **`prompt_tokens` INCLUDES `cached_tokens`.** A step reading
//     {"prompt_tokens":98110,"cached_tokens":97354} sent 756 uncached tokens,
//     not 98110. sessions.db splits the two into separate fields, so the
//     conversion has to happen here or the same session would be counted two
//     different ways depending on which file it came from.
//
//  2. **The granularity differs from sessions.db.** A transcript records one
//     "step" per agent turn (252 for a session that made 736 requests in the
//     database), because a step wraps a whole tool loop. Summing transcript
//     totals into database totals would therefore be wrong, which is why
//     transcripts are used to FILL GAPS and never to add to a session the
//     database already has.
//
//  3. **Per-step sums equal `final_metrics` exactly**, on all 17 transcripts
//     checked. So the totals are trustworthy, and both are available.
//
// A transcript carries no ACU or credit cost, so cost stays zero rather than
// being invented; the row is labelled with its source so a reader can tell.

// transcriptFile is one ATIF document.
type transcriptFile struct {
	SchemaVersion string `json:"schema_version"`
	SessionID     string `json:"session_id"`
	Agent         struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		ModelName string `json:"model_name"`
		ToolDefs  []any  `json:"tool_definitions"`
		Extra     struct {
			Backend        string `json:"backend"`
			PermissionMode string `json:"permission_mode"`
		} `json:"extra"`
	} `json:"agent"`
	Steps []transcriptStep `json:"steps"`
	Final struct {
		TotalPromptTokens     int64 `json:"total_prompt_tokens"`
		TotalCompletionTokens int64 `json:"total_completion_tokens"`
		TotalCachedTokens     int64 `json:"total_cached_tokens"`
		TotalSteps            int   `json:"total_steps"`
	} `json:"final_metrics"`
}

// transcriptStep is one turn. Only the fields we actually use are decoded.
type transcriptStep struct {
	// StepID is a NUMBER in ATIF, not a string -- declaring it as one made every
	// transcript fail to unmarshal and the whole second source silently vanish.
	// It is not used, so it is decoded permissively rather than guessed at.
	StepID    any    `json:"step_id"`
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"` // system / user / agent
	ModelName string `json:"model_name"`
	Metrics   *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		CachedTokens     int64 `json:"cached_tokens"`
	} `json:"metrics"`
	ToolCalls []struct {
		ToolCallID   string          `json:"tool_call_id"`
		FunctionName string          `json:"function_name"`
		Arguments    json.RawMessage `json:"arguments"`
	} `json:"tool_calls"`
}

// transcriptDirFor returns the transcripts directory that sits beside a
// sessions.db.
func transcriptDirFor(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "transcripts")
}

// loadTranscriptSessions reads every transcript in dir and returns one session
// per readable file.
//
// A malformed or unreadable transcript is skipped rather than failing the whole
// call: the directory is written by another process and a partially flushed
// file should cost one session, not the entire report. The number skipped is
// returned so a caller can surface it.
func loadTranscriptSessions(dir string) (sessions []model.Session, skipped int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil // no transcripts is the normal case on a fresh install
		}
		return nil, 0, fmt.Errorf("read transcripts: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, ok := readTranscript(filepath.Join(dir, e.Name()))
		if !ok {
			skipped++
			continue
		}
		sessions = append(sessions, s)
	}
	// Newest first, matching the ORDER BY the database query uses, so a caller
	// that shows "the most recent sessions" sees one coherent ordering.
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].LastActivityAt.After(sessions[j].LastActivityAt)
	})
	return sessions, skipped, nil
}

// readTranscript converts one transcript into a session. The bool is false when
// the file cannot be used.
func readTranscript(path string) (model.Session, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Session{}, false
	}
	var tf transcriptFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return model.Session{}, false
	}
	if tf.SessionID == "" {
		// Fall back to the filename, which is the session id by convention.
		tf.SessionID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	if tf.SessionID == "" {
		return model.Session{}, false
	}

	s := model.Session{
		ID:          tf.SessionID,
		Source:      model.SourceTranscript,
		BackendType: tf.Agent.Extra.Backend,
		Model:       tf.Agent.ModelName,
		AgentMode:   strings.ToLower(tf.Agent.Extra.PermissionMode),
		ToolCalls:   map[string]int{},
	}

	// Timestamps and tool usage come from the steps, which carry a real
	// per-step clock (every step has one) and the tool calls themselves.
	for _, st := range tf.Steps {
		if ts, ok := parseRFC3339(st.Timestamp); ok {
			if s.CreatedAt.IsZero() || ts.Before(s.CreatedAt) {
				s.CreatedAt = ts
			}
			if ts.After(s.LastActivityAt) {
				s.LastActivityAt = ts
			}
		}
		if st.Source == "agent" {
			s.AssistantCount++
			if st.ModelName != "" {
				s.LatestModel = st.ModelName
			}
			for _, tc := range st.ToolCalls {
				if tc.FunctionName != "" {
					s.ToolCalls[tc.FunctionName]++
				}
			}
		}
	}

	// Tokens from final_metrics: the authoritative totals, verified to equal
	// the per-step sums on every transcript measured.
	//
	// The conversion is the important part. ATIF's prompt_tokens is the whole
	// prompt INCLUDING what was served from cache, while sessions.db keeps
	// uncached input and cache reads in separate columns. Subtracting cached
	// here is what makes a transcript session comparable with a database
	// session at all; without it every transcript session would look like it
	// sent its entire cached context as fresh input.
	prompt := tf.Final.TotalPromptTokens
	cached := tf.Final.TotalCachedTokens
	if cached > prompt {
		// Defensive: a malformed file must not produce a negative count.
		cached = prompt
	}
	s.CacheRead = cached
	s.InputTokens = prompt - cached
	s.OutputTokens = tf.Final.TotalCompletionTokens

	// No cost accounting exists in a transcript. Leave it at zero rather than
	// inferring one: the caller labels the row with its source, and CostBasis
	// reports the absence. Inventing a figure here is exactly the kind of
	// plausible-but-wrong number this codebase has been removing.
	if s.AgentMode == "" {
		s.AgentMode = "normal"
	}
	if s.ToolCalls == nil {
		s.ToolCalls = map[string]int{}
	}
	return s, true
}

// mergeTranscriptSessions appends the transcript sessions the database does not
// already have.
//
// Databases win on a conflict, deliberately: sessions.db carries per-message
// latency, finish reasons and Devin's own ACU accounting, and its granularity
// (per request) is finer than a transcript's (per step). Blending the two would
// compare different units, so a session present in both keeps its database row
// and the transcript is used only where the database has nothing.
func mergeTranscriptSessions(dbSessions, transcriptSessions []model.Session) (merged []model.Session, recovered int) {
	have := make(map[string]bool, len(dbSessions))
	for i := range dbSessions {
		if dbSessions[i].Source == "" {
			dbSessions[i].Source = model.SourceSessionsDB
		}
		have[dbSessions[i].ID] = true
	}
	merged = dbSessions
	for i := range transcriptSessions {
		if have[transcriptSessions[i].ID] {
			continue
		}
		merged = append(merged, transcriptSessions[i])
		recovered++
	}
	return merged, recovered
}

// transcriptsAvailable reports whether a transcripts directory exists, so a
// caller can decide whether to mention the second source at all.
func transcriptsAvailable(dbPath string) bool {
	info, err := os.Stat(transcriptDirFor(dbPath))
	return err == nil && info.IsDir()
}

// TranscriptRecovery implements TranscriptSource. The counts describe the last
// Sessions() call.
func (r *v1Reader) TranscriptRecovery() (int, int, error) {
	return r.recoveredFromTranscripts, r.skippedTranscripts, r.transcriptErr
}
