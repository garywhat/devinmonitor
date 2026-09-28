package reader

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// Transcript ingestion tests.
//
// The fixtures deliberately reproduce the two things that made this source
// tricky in the real data, both of which bit during development:
//
//   - `step_id` is a NUMBER in ATIF. Declaring it as a string made every
//     transcript fail to unmarshal, and because the loader treated a bad file as
//     "skip it", the entire second source vanished while the tool still looked
//     healthy. That is the regression the first test pins.
//   - `prompt_tokens` INCLUDES `cached_tokens`, unlike sessions.db where
//     uncached input and cache reads are separate columns.

const transcriptWithNumberStepID = `{
  "schema_version": "ATIF-v1.7",
  "session_id": "tx-1",
  "agent": {
    "name": "devin",
    "version": "3000.4.25",
    "model_name": "SWE-1.7 Max",
    "tool_definitions": [{"type": "function", "function": {"name": "read"}}],
    "extra": {"backend": "Windsurf", "permission_mode": "Bypass"}
  },
  "steps": [
    {"step_id": 1, "timestamp": "2026-08-01T10:00:00.000000+00:00", "source": "system",
     "message": {"role": "system"}},
    {"step_id": 2, "timestamp": "2026-08-01T10:00:05.000000+00:00", "source": "user",
     "message": {"role": "user"}},
    {"step_id": 3, "timestamp": "2026-08-01T10:00:10.000000+00:00", "source": "agent",
     "model_name": "SWE-1.7 Max",
     "metrics": {"prompt_tokens": 1000, "completion_tokens": 100, "cached_tokens": 900},
     "tool_calls": [{"tool_call_id": "abc", "function_name": "read", "arguments": {"path": "/a.go"}}]},
    {"step_id": 4, "timestamp": "2026-08-02T11:30:00.000000+00:00", "source": "agent",
     "model_name": "SWE-1.7 Max",
     "metrics": {"prompt_tokens": 2000, "completion_tokens": 50, "cached_tokens": 1500},
     "tool_calls": [{"tool_call_id": "def", "function_name": "write", "arguments": {}},
                    {"tool_call_id": "ghi", "function_name": "read", "arguments": {}}]}
  ],
  "final_metrics": {
    "total_prompt_tokens": 3000,
    "total_completion_tokens": 150,
    "total_cached_tokens": 2400,
    "total_steps": 4
  }
}`

// writeTranscript drops a transcript file into <dir>/transcripts.
func writeTranscript(t *testing.T, dir, name, body string) {
	t.Helper()
	td := filepath.Join(dir, "transcripts")
	if err := os.MkdirAll(td, 0o755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(td, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

// TestTranscriptStepIDIsANumber is the regression guard for the bug that made
// the whole second source silently disappear.
func TestTranscriptStepIDIsANumber(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "tx-1.json", transcriptWithNumberStepID)

	sessions, skipped, err := loadTranscriptSessions(filepath.Join(dir, "transcripts"))
	if err != nil {
		t.Fatalf("loadTranscriptSessions: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped %d transcript(s); step_id must be decoded permissively", skipped)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1 -- a numeric step_id must not fail the file", len(sessions))
	}
}

// TestTranscriptPromptTokensExcludeCache is the unit conversion that makes a
// transcript session comparable with a database one at all.
func TestTranscriptPromptTokensExcludeCache(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "tx-1.json", transcriptWithNumberStepID)

	sessions, _, err := loadTranscriptSessions(filepath.Join(dir, "transcripts"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := sessions[0]

	// ATIF: prompt 3000 of which 2400 was cached -> 600 freshly sent.
	if s.InputTokens != 600 {
		t.Errorf("InputTokens = %d, want 600 (prompt 3000 minus cached 2400)", s.InputTokens)
	}
	if s.CacheRead != 2400 {
		t.Errorf("CacheRead = %d, want 2400", s.CacheRead)
	}
	if s.OutputTokens != 150 {
		t.Errorf("OutputTokens = %d, want 150", s.OutputTokens)
	}
	// The trap this guards: without the subtraction every transcript session
	// would look like it sent its whole cached context as fresh input.
	if s.InputTokens+s.CacheRead != 3000 {
		t.Errorf("input+cache = %d, want the reported prompt total 3000", s.InputTokens+s.CacheRead)
	}
}

// TestTranscriptMetadataAndTimestamps covers the rest of the mapping.
func TestTranscriptMetadataAndTimestamps(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "tx-1.json", transcriptWithNumberStepID)

	sessions, _, err := loadTranscriptSessions(filepath.Join(dir, "transcripts"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := sessions[0]

	if s.Source != model.SourceTranscript {
		t.Errorf("Source = %q, want %q so a consumer can tell the sources apart", s.Source, model.SourceTranscript)
	}
	if s.Model != "SWE-1.7 Max" {
		t.Errorf("Model = %q", s.Model)
	}
	if s.BackendType != "Windsurf" {
		t.Errorf("BackendType = %q, want Windsurf", s.BackendType)
	}
	if s.AgentMode != "bypass" {
		t.Errorf("AgentMode = %q, want bypass (lower-cased)", s.AgentMode)
	}
	// Only the two agent steps count as assistant turns.
	if s.AssistantCount != 2 {
		t.Errorf("AssistantCount = %d, want 2 agent steps", s.AssistantCount)
	}
	// read twice, write once.
	if s.ToolCalls["read"] != 2 || s.ToolCalls["write"] != 1 {
		t.Errorf("ToolCalls = %v, want read:2 write:1", s.ToolCalls)
	}
	// The clock comes from the steps, which all carry a real timestamp.
	wantStart := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 8, 2, 11, 30, 0, 0, time.UTC)
	if !s.CreatedAt.Equal(wantStart) {
		t.Errorf("CreatedAt = %s, want the first step %s", s.CreatedAt, wantStart)
	}
	if !s.LastActivityAt.Equal(wantEnd) {
		t.Errorf("LastActivityAt = %s, want the last step %s", s.LastActivityAt, wantEnd)
	}
	// A transcript carries no cost accounting; inventing one is the bug class
	// this codebase keeps removing.
	if s.CreditCost != 0 || s.ACUCost != 0 {
		t.Errorf("cost = credit %v / acu %v, want zero: a transcript has no accounting", s.CreditCost, s.ACUCost)
	}
}

// TestTranscriptMergePrefersTheDatabase pins the rule that decides whether a
// session is counted at all: the database wins, transcripts only fill gaps.
func TestTranscriptMergePrefersTheDatabase(t *testing.T) {
	dbSessions := []model.Session{{ID: "shared"}, {ID: "db-only"}}
	transcripts := []model.Session{
		{ID: "shared", Source: model.SourceTranscript},
		{ID: "tx-only", Source: model.SourceTranscript},
	}
	merged, recovered := mergeTranscriptSessions(dbSessions, transcripts)

	if recovered != 1 {
		t.Errorf("recovered = %d, want 1 (only tx-only is new)", recovered)
	}
	if len(merged) != 3 {
		t.Fatalf("merged has %d sessions, want 3", len(merged))
	}
	seen := map[string]model.SessionSource{}
	for _, s := range merged {
		seen[s.ID] = s.Source
	}
	if seen["shared"] != model.SourceSessionsDB {
		t.Errorf("shared session came from %q; the database must win on a conflict", seen["shared"])
	}
	if seen["db-only"] != model.SourceSessionsDB {
		t.Errorf("db-only session lost its source label: %q", seen["db-only"])
	}
	if seen["tx-only"] != model.SourceTranscript {
		t.Errorf("tx-only source = %q", seen["tx-only"])
	}
}

// TestTranscriptMalformedFileIsSkippedNotFatal keeps one bad file from costing
// the entire report.
func TestTranscriptMalformedFileIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "good.json", transcriptWithNumberStepID)
	writeTranscript(t, dir, "truncated.json", `{"schema_version":"ATIF-v1.7","steps":[`)
	writeTranscript(t, dir, "notjson.json", "hello")
	if err := os.WriteFile(filepath.Join(dir, "transcripts", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions, skipped, err := loadTranscriptSessions(filepath.Join(dir, "transcripts"))
	if err != nil {
		t.Fatalf("a bad transcript must not fail the load: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("got %d usable sessions, want 1", len(sessions))
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (the truncated and the non-JSON file)", skipped)
	}
}

// TestTranscriptMissingDirectoryIsNormal covers a fresh install.
func TestTranscriptMissingDirectoryIsNormal(t *testing.T) {
	sessions, skipped, err := loadTranscriptSessions(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("a missing transcripts directory must not be an error: %v", err)
	}
	if len(sessions) != 0 || skipped != 0 {
		t.Errorf("got %d sessions / %d skipped, want zero of each", len(sessions), skipped)
	}
}

// TestTranscriptCachedExceedingPromptIsClamped keeps a malformed file from
// producing a negative input count.
func TestTranscriptCachedExceedingPromptIsClamped(t *testing.T) {
	dir := t.TempDir()
	body := `{"schema_version":"ATIF-v1.7","session_id":"bad",
	  "steps":[{"step_id":1,"timestamp":"2026-08-01T10:00:00Z","source":"agent","metrics":{"prompt_tokens":10,"completion_tokens":1,"cached_tokens":999}}],
	  "final_metrics":{"total_prompt_tokens":10,"total_completion_tokens":1,"total_cached_tokens":999,"total_steps":1}}`
	writeTranscript(t, dir, "bad.json", body)

	sessions, _, err := loadTranscriptSessions(filepath.Join(dir, "transcripts"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if s := sessions[0]; s.InputTokens < 0 {
		t.Errorf("InputTokens = %d; cached > prompt must not yield a negative count", s.InputTokens)
	}
}

// TestTranscriptSessionsAreOrderedNewestFirst matches the database query's
// ordering so a caller showing recent sessions sees one coherent list.
func TestTranscriptSessionsAreOrderedNewestFirst(t *testing.T) {
	dir := t.TempDir()
	mk := func(id, ts string) string {
		return `{"schema_version":"ATIF-v1.7","session_id":"` + id + `",
		  "steps":[{"step_id":1,"timestamp":"` + ts + `","source":"agent","metrics":{"prompt_tokens":10,"completion_tokens":1,"cached_tokens":0}}],
		  "final_metrics":{"total_prompt_tokens":10,"total_completion_tokens":1,"total_cached_tokens":0,"total_steps":1}}`
	}
	writeTranscript(t, dir, "old.json", mk("old", "2026-01-01T00:00:00Z"))
	writeTranscript(t, dir, "new.json", mk("new", "2026-09-01T00:00:00Z"))
	writeTranscript(t, dir, "mid.json", mk("mid", "2026-05-01T00:00:00Z"))

	sessions, _, err := loadTranscriptSessions(filepath.Join(dir, "transcripts"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions", len(sessions))
	}
	want := []string{"new", "mid", "old"}
	for i, w := range want {
		if sessions[i].ID != w {
			t.Errorf("position %d = %q, want %q (newest first)", i, sessions[i].ID, w)
		}
	}
}

// TestTranscriptIdFallsBackToFilename covers a file whose body omits the id.
func TestTranscriptIdFallsBackToFilename(t *testing.T) {
	dir := t.TempDir()
	body := `{"schema_version":"ATIF-v1.7",
	  "steps":[{"step_id":1,"timestamp":"2026-08-01T10:00:00Z","source":"agent","metrics":{"prompt_tokens":5,"completion_tokens":1,"cached_tokens":0}}],
	  "final_metrics":{"total_prompt_tokens":5,"total_completion_tokens":1,"total_cached_tokens":0,"total_steps":1}}`
	writeTranscript(t, dir, "from-filename.json", body)

	sessions, _, err := loadTranscriptSessions(filepath.Join(dir, "transcripts"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "from-filename" {
		t.Fatalf("got %+v, want one session ided from-filename", sessions)
	}
}
