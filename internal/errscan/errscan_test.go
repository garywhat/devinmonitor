package errscan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/garywhat/devinmonitor/internal/model"
)

var t0 = time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)

func msg(role, content string) model.Message {
	return model.Message{Role: role, Content: content, CreatedAt: t0}
}

// fixture holds 5 error occurrences spread over 10 assistant messages, so the
// error rate is exactly 50.0%. It also exercises every fallback of the model
// attribution chain and both blank-body and non-assistant scanning.
func fixture() []model.Session {
	return []model.Session{
		{
			ID:          "sess-a",
			Model:       "devin-v1",
			LatestModel: "devin-latest",
			Messages: []model.Message{
				{Role: "assistant", Content: "Let me run the tests.", CreatedAt: t0, GenerationModel: "gpt-role-a"},
				msg("tool", "bash: /etc/hosts: Permission denied"),
				msg("assistant", "Running the linter now."),
				msg("tool", "zsh: command not found: ripgrep"),
				msg("assistant", "Retrying without sudo."),
				msg("assistant", " "), // blank assistant body: counted, not scanned
				msg("assistant", "Done."),
				msg("tool", "   "), // blank tool body: not an error occurrence
			},
		},
		{
			ID:    "sess-b",
			Model: "devin-v0",
			Messages: []model.Message{
				msg("assistant", "Checking the build."),
				msg("user", "Command timed out after 5m"),
				msg("tool", "zsh: command not found: fd"),
				msg("assistant", "Trying a different path."),
				msg("assistant", "All good."),
				{Role: "assistant", Content: "Command timed out after 30s", CreatedAt: t0, GenerationModel: "claude-role-b"},
				msg("assistant", "Wrapping up."),
			},
		},
	}
}

func TestCategorizeEveryCategory(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"user interruption proceed", "user doesn't want to proceed with this tool use", "User Interruption"},
		{"user interruption action", "user doesn't want to take this action", "User Interruption"},
		{"user interruption request", "… [Request interrupted by user] …", "User Interruption"},
		{"command timeout", "Command timed out after 120000ms", "Command Timeout"},
		{"file not read", "File has not been read yet. Read it first.", "File Not Read"},
		{"file modified", "File has been modified since read, either by the user or by a linter.", "File Modified"},
		{"file too large", "File content (312345 tokens) exceeds maximum allowed tokens (25000).", "File Too Large"},
		{"permission denied", "bash: /etc/hosts: Permission denied", "Permission Error"},
		{"permission blocked", "This command was blocked because it tried to cd to /Users/role_a/private", "Permission Error"},
		{"content replace", "String to replace not found in file.", "Content Not Found"},
		{"content string", "String not found in file: needle", "Content Not Found"},
		{"content module", "ModuleNotFoundError: No module named 'requests'", "Content Not Found"},
		{"content path", "cat: /tmp/nope: No such file or directory", "Content Not Found"},
		{"content file", "File does not exist: src/missing.go", "Content Not Found"},
		{"tool not found", "zsh: command not found: ripgrep", "Tool Not Found"},
		{"no changes", "No changes to make: the file already matches.", "No Changes"},
		{"unmatched", "I refactored the parser and everything looks fine.", other},
		{"empty", "", other},
	}
	for _, tc := range cases {
		if got := Categorize(tc.in); got != tc.want {
			t.Errorf("%s: Categorize(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// The table is ordered: a body matching both an earlier and a later category
// must resolve to the earlier one.
func TestCategorizeOrdering(t *testing.T) {
	cases := []struct{ in, want string }{
		// User Interruption (1) beats Content Not Found (7).
		{"[Request interrupted] No such file or directory", "User Interruption"},
		// Command Timeout (2) beats Content Not Found (7).
		{"Command timed out: No such file or directory", "Command Timeout"},
		// File Too Large (5) beats Content Not Found (7).
		{"exceeds maximum allowed; File does not exist", "File Too Large"},
		// Permission Error (6) beats Content Not Found (7).
		{"Permission denied: No such file or directory", "Permission Error"},
		// Content Not Found (7) beats Tool Not Found (8).
		{"No such file or directory: command not found", "Content Not Found"},
		// Tool Not Found (8) beats No Changes (9).
		{"command not found; No changes to make", "Tool Not Found"},
	}
	for _, tc := range cases {
		if got := Categorize(tc.in); got != tc.want {
			t.Errorf("Categorize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The regression the lookahead exists to prevent: a lone "cd to" must never be
// a permission error.
func TestPermissionErrorTwoPartPattern(t *testing.T) {
	both := "This command was blocked: it cannot cd to /Users/role_b/private"
	if got := Categorize(both); got != "Permission Error" {
		t.Errorf("both conditions present: got %q, want %q", got, "Permission Error")
	}
	onlyCdTo := "Run cd to /tmp and then check the logs"
	if got := Categorize(onlyCdTo); got == "Permission Error" {
		t.Errorf("only %q present: got Permission Error, want a non-permission category", "cd to")
	}
	onlyBlocked := "The request was blocked by the upstream proxy"
	if got := Categorize(onlyBlocked); got == "Permission Error" {
		t.Errorf("only %q present: got Permission Error", "was blocked")
	}
	if got := Categorize(onlyCdTo); got != other {
		t.Errorf("Categorize(%q) = %q, want %q", onlyCdTo, got, other)
	}
}

// matcherFor must evaluate the lookahead pattern as a real conjunction, which
// means it cannot be going through the literal-substring fallback.
func TestLookaheadMatcherIsConjunction(t *testing.T) {
	m := matcherFor(`(?=.*cd to)(?=.*was blocked)`)
	if !m("the command was blocked before it could cd to /tmp/x") {
		t.Error("both conditions in reverse order: want match")
	}
	if !m("cd to /Users/x was blocked") {
		t.Error("both conditions: want match")
	}
	if m("cd to /tmp is allowed") {
		t.Error(`only "cd to": want no match`)
	}
	if m("the request was blocked by the proxy") {
		t.Error(`only "was blocked": want no match`)
	}
}

func TestCategorizeCaseInsensitive(t *testing.T) {
	cases := []struct{ in, want string }{
		{"COMMAND TIMED OUT", "Command Timeout"},
		{"PERMISSION DENIED", "Permission Error"},
		{"no such file or directory", "Content Not Found"},
		{"file has been MODIFIED since read", "File Modified"},
		{"COMMAND NOT FOUND: FD", "Tool Not Found"},
		{"user DOESN'T want to proceed", "User Interruption"},
		{"COMMAND WAS BLOCKED, CANNOT CD TO /tmp", "Permission Error"},
	}
	for _, tc := range cases {
		if got := Categorize(tc.in); got != tc.want {
			t.Errorf("Categorize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCategoriesShape(t *testing.T) {
	want := []string{
		"User Interruption",
		"Command Timeout",
		"File Not Read",
		"File Modified",
		"File Too Large",
		"Permission Error",
		"Content Not Found",
		"Tool Not Found",
		"No Changes",
	}
	if len(Categories) != len(want) {
		t.Fatalf("Categories has %d entries, want %d", len(Categories), len(want))
	}
	for i, name := range want {
		if Categories[i].Name != name {
			t.Errorf("Categories[%d].Name = %q, want %q", i, Categories[i].Name, name)
		}
		if len(Categories[i].Patterns) == 0 {
			t.Errorf("Categories[%d] (%s) has no patterns", i, name)
		}
		for _, p := range Categories[i].Patterns {
			if matcherFor(p) == nil {
				t.Errorf("Categories[%d] (%s): pattern %q has no matcher", i, name, p)
			}
		}
	}
	for _, c := range Categories {
		if c.Name == other {
			t.Errorf("Categories must not contain the %q bucket", other)
		}
	}
}

func TestScanFixture(t *testing.T) {
	rep := Scan(fixture(), "")

	if rep.Total != 5 {
		t.Errorf("Total = %d, want 5", rep.Total)
	}
	if rep.AssistantMessages != 10 {
		t.Errorf("AssistantMessages = %d, want 10", rep.AssistantMessages)
	}
	if rep.ErrorRate != 50.0 {
		t.Errorf("ErrorRate = %v, want 50.0", rep.ErrorRate)
	}
	if len(rep.Findings) != 5 {
		t.Fatalf("len(Findings) = %d, want 5", len(rep.Findings))
	}

	// Count desc, then name asc.
	wantCats := []CategoryCount{
		{Category: "Command Timeout", Count: 2, Percent: 40.0},
		{Category: "Tool Not Found", Count: 2, Percent: 40.0},
		{Category: "Permission Error", Count: 1, Percent: 20.0},
	}
	if len(rep.ByCategory) != len(wantCats) {
		t.Fatalf("ByCategory = %+v, want %+v", rep.ByCategory, wantCats)
	}
	for i, want := range wantCats {
		if rep.ByCategory[i] != want {
			t.Errorf("ByCategory[%d] = %+v, want %+v", i, rep.ByCategory[i], want)
		}
	}

	// Findings keep scan order and carry the role-appropriate model name.
	wantFindings := []struct {
		category, session, model string
	}{
		{"Permission Error", "sess-a", "devin-latest"}, // tool msg -> session LatestModel
		{"Tool Not Found", "sess-a", "devin-latest"},   // "user" would use the same fallback
		{"Command Timeout", "sess-b", "devin-v0"},      // user msg -> session Model (no LatestModel)
		{"Tool Not Found", "sess-b", "devin-v0"},       // tool msg -> session Model
		{"Command Timeout", "sess-b", "claude-role-b"}, // GenerationModel wins
	}
	for i, want := range wantFindings {
		got := rep.Findings[i]
		if got.Category != want.category || got.SessionID != want.session || got.Model != want.model {
			t.Errorf("Findings[%d] = %+v, want category=%q session=%q model=%q",
				i, got, want.category, want.session, want.model)
		}
		if !got.Timestamp.Equal(t0) {
			t.Errorf("Findings[%d].Timestamp = %v, want %v", i, got.Timestamp, t0)
		}
		if got.Content == "" {
			t.Errorf("Findings[%d].Content is empty", i)
		}
	}
}

func TestScanEmptyInput(t *testing.T) {
	rep := Scan(nil, "")
	if rep.Total != 0 || rep.AssistantMessages != 0 || rep.ErrorRate != 0 {
		t.Errorf("Scan(nil) = %+v, want all zero", rep)
	}
	if len(rep.ByCategory) != 0 || len(rep.Findings) != 0 {
		t.Errorf("Scan(nil) slices = %+v / %+v, want empty", rep.ByCategory, rep.Findings)
	}
	if rep.ByCategory == nil || rep.Findings == nil {
		t.Error("Scan(nil) slices must be empty, not nil, so JSON encodes []")
	}
}

func TestScanSessionFilterMatchingNothing(t *testing.T) {
	rep := Scan(fixture(), "sess-missing")
	if rep.Total != 0 || rep.AssistantMessages != 0 || rep.ErrorRate != 0 {
		t.Errorf("filtered report = %+v, want all zero", rep)
	}
	if len(rep.ByCategory) != 0 || len(rep.Findings) != 0 {
		t.Errorf("filtered report slices = %+v / %+v, want empty", rep.ByCategory, rep.Findings)
	}
}

func TestScanSessionFilterMatchingOne(t *testing.T) {
	rep := Scan(fixture(), "sess-a")
	if rep.Total != 2 {
		t.Errorf("Total = %d, want 2", rep.Total)
	}
	if rep.AssistantMessages != 5 {
		t.Errorf("AssistantMessages = %d, want 5", rep.AssistantMessages)
	}
	if rep.ErrorRate != 40.0 {
		t.Errorf("ErrorRate = %v, want 40.0", rep.ErrorRate)
	}
	wantCats := []CategoryCount{
		{Category: "Permission Error", Count: 1, Percent: 50.0},
		{Category: "Tool Not Found", Count: 1, Percent: 50.0},
	}
	if len(rep.ByCategory) != len(wantCats) {
		t.Fatalf("ByCategory = %+v, want %+v", rep.ByCategory, wantCats)
	}
	for i, want := range wantCats {
		if rep.ByCategory[i] != want {
			t.Errorf("ByCategory[%d] = %+v, want %+v", i, rep.ByCategory[i], want)
		}
	}
	for _, f := range rep.Findings {
		if f.SessionID != "sess-a" {
			t.Errorf("finding from session %q leaked into a filtered scan", f.SessionID)
		}
	}
}

func TestScanZeroAssistantMessages(t *testing.T) {
	ss := []model.Session{{
		ID: "sess-only-tools",
		Messages: []model.Message{
			msg("tool", "zsh: command not found: ffmpeg"),
		},
	}}
	rep := Scan(ss, "")
	if rep.Total != 1 {
		t.Errorf("Total = %d, want 1", rep.Total)
	}
	if rep.AssistantMessages != 0 {
		t.Errorf("AssistantMessages = %d, want 0", rep.AssistantMessages)
	}
	if rep.ErrorRate != 0 {
		t.Errorf("ErrorRate = %v, want 0 (zero division)", rep.ErrorRate)
	}
	if len(rep.ByCategory) != 1 || rep.ByCategory[0].Percent != 100.0 {
		t.Errorf("ByCategory = %+v, want one category at 100.0", rep.ByCategory)
	}
}

func TestScanPercentRounding(t *testing.T) {
	ss := []model.Session{{
		ID: "sess-thirds",
		Messages: []model.Message{
			msg("assistant", "One."),
			msg("tool", "zsh: command not found: a"),
			msg("tool", "zsh: command not found: b"),
			msg("assistant", "Two."),
			msg("assistant", "Three."),
			msg("user", "File has not been read yet."),
		},
	}}
	rep := Scan(ss, "")
	if rep.Total != 3 || rep.AssistantMessages != 3 {
		t.Fatalf("Total = %d, AssistantMessages = %d, want 3 / 3", rep.Total, rep.AssistantMessages)
	}
	if rep.ErrorRate != 100.0 {
		t.Errorf("ErrorRate = %v, want 100.0", rep.ErrorRate)
	}
	want := []CategoryCount{
		{Category: "Tool Not Found", Count: 2, Percent: 66.7}, // 2/3 rounded to 1 decimal
		{Category: "File Not Read", Count: 1, Percent: 33.3},  // 1/3 rounded to 1 decimal
	}
	if len(rep.ByCategory) != len(want) {
		t.Fatalf("ByCategory = %+v, want %+v", rep.ByCategory, want)
	}
	for i := range want {
		if rep.ByCategory[i] != want[i] {
			t.Errorf("ByCategory[%d] = %+v, want %+v", i, rep.ByCategory[i], want[i])
		}
	}
}

// A non-empty body matching no category is not an error occurrence; only
// pattern matches become findings.
func TestScanIgnoresUnmatchedBodies(t *testing.T) {
	ss := []model.Session{{
		ID: "sess-quiet",
		Messages: []model.Message{
			msg("assistant", "Everything is fine."),
			msg("tool", "package main\n\nfunc main() {}\n"),
			msg("user", "thanks!"),
		},
	}}
	rep := Scan(ss, "")
	if rep.Total != 0 || len(rep.Findings) != 0 || len(rep.ByCategory) != 0 {
		t.Errorf("report = %+v, want no findings", rep)
	}
	if rep.AssistantMessages != 1 {
		t.Errorf("AssistantMessages = %d, want 1", rep.AssistantMessages)
	}
	if rep.ErrorRate != 0 {
		t.Errorf("ErrorRate = %v, want 0", rep.ErrorRate)
	}
}

func TestScanTruncatesContentTo200Runes(t *testing.T) {
	long := strings.Repeat("é", 260) + "tail"
	body := "Command timed out: " + long
	ss := []model.Session{{
		ID:       "sess-long",
		Messages: []model.Message{msg("tool", body)},
	}}
	rep := Scan(ss, "")
	if rep.Total != 1 {
		t.Fatalf("Total = %d, want 1", rep.Total)
	}
	got := rep.Findings[0].Content
	if n := len([]rune(got)); n != maxContentRunes {
		t.Errorf("truncated content has %d runes, want %d", n, maxContentRunes)
	}
	if !utf8.ValidString(got) {
		t.Error("truncated content is not valid UTF-8 (cut mid-rune)")
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated content does not end with an ellipsis: %q", got)
	}
	// The ellipsis is part of the 200-rune budget, and the multi-byte
	// characters must survive intact.
	want := string([]rune(body)[:maxContentRunes-1]) + "…"
	if got != want {
		t.Errorf("truncation mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestScanContentExactlyAtLimitIsUntouched(t *testing.T) {
	body := "Command timed out: " + strings.Repeat("x", maxContentRunes-len([]rune("Command timed out: ")))
	if n := len([]rune(body)); n != maxContentRunes {
		t.Fatalf("fixture body has %d runes, want %d", n, maxContentRunes)
	}
	ss := []model.Session{{
		ID:       "sess-boundary",
		Messages: []model.Message{msg("tool", body)},
	}}
	rep := Scan(ss, "")
	if rep.Total != 1 {
		t.Fatalf("Total = %d, want 1", rep.Total)
	}
	if got := rep.Findings[0].Content; got != body {
		t.Errorf("content at the limit was modified: %q", got)
	}
}

func TestJSONReportExamplesSemantics(t *testing.T) {
	rep := Scan(fixture(), "")
	if len(rep.Findings) != 5 {
		t.Fatalf("fixture produced %d findings, want 5", len(rep.Findings))
	}

	// examples == 0: findings dropped, but present as an empty array.
	v0 := jsonReport(rep, 0)
	if v0.Findings == nil {
		t.Error("jsonReport(..., 0) must not produce a nil findings slice")
	}
	if len(v0.Findings) != 0 {
		t.Errorf("jsonReport(..., 0) kept %d findings, want 0", len(v0.Findings))
	}
	raw, err := json.Marshal(v0)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"findings":[]`) {
		t.Errorf("examples=0 JSON must encode findings as [], got %s", raw)
	}
	if v0.Total != rep.Total || v0.ErrorRate != rep.ErrorRate || len(v0.ByCategory) != len(rep.ByCategory) {
		t.Error("jsonReport(..., 0) altered the aggregate fields")
	}

	// examples == 1: at most one finding per category, in scan order.
	v1 := jsonReport(rep, 1)
	wantCats := []string{"Permission Error", "Tool Not Found", "Command Timeout"}
	if len(v1.Findings) != len(wantCats) {
		t.Fatalf("jsonReport(..., 1) has %d findings, want %d: %+v", len(v1.Findings), len(wantCats), v1.Findings)
	}
	for i, want := range wantCats {
		if v1.Findings[i].Category != want {
			t.Errorf("jsonReport(..., 1) Findings[%d].Category = %q, want %q", i, v1.Findings[i].Category, want)
		}
	}

	// examples == 2: two per category where available (2 + 2 + 1).
	v2 := jsonReport(rep, 2)
	if len(v2.Findings) != 5 {
		t.Errorf("jsonReport(..., 2) has %d findings, want 5", len(v2.Findings))
	}
	per := map[string]int{}
	for _, f := range v2.Findings {
		per[f.Category]++
	}
	if per["Command Timeout"] != 2 || per["Tool Not Found"] != 2 || per["Permission Error"] != 1 {
		t.Errorf("jsonReport(..., 2) per-category counts = %v, want Command Timeout 2, Tool Not Found 2, Permission Error 1", per)
	}

	// The source report is never mutated.
	if len(rep.Findings) != 5 {
		t.Errorf("jsonReport mutated the source report: %d findings remain", len(rep.Findings))
	}
}

func TestWriteJSONShape(t *testing.T) {
	rep := Scan(fixture(), "")
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep, 0); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Error("JSON output must end with a trailing newline")
	}
	if !strings.Contains(out, "\"findings\": []") {
		t.Errorf("indented JSON must contain an empty findings array:\n%s", out)
	}

	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if back.Total != rep.Total || back.AssistantMessages != rep.AssistantMessages || back.ErrorRate != rep.ErrorRate {
		t.Errorf("round-trip mismatch: %+v vs %+v", back, rep)
	}
	if len(back.ByCategory) != 3 {
		t.Errorf("round-trip ByCategory = %+v, want 3 entries", back.ByCategory)
	}
}
