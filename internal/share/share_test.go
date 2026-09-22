package share

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// Fixture values. The IDs and full paths must never survive into a report; the
// last path segment ("secret-project") is the only project data that may leave.
const (
	fixtureID1    = "deadbeef-1111-2222-3333-cafebabe0001"
	fixtureID2    = "deadbeef-1111-2222-3333-cafebabe0002"
	fixtureUnix   = "/Users/placeholder/secret-project"
	fixtureWin    = `C:\Users\placeholder\secret-project`
	fixtureParent = "/Users/placeholder"
	fixtureWinDir = `C:\Users\placeholder`
	fixtureErr    = "SECRET_RAW_ERROR_TEXT_9f3a: connection refused talking to 10.0.0.7"
)

// fixtureSessions builds an in-memory session set. No SQLite fixture, no disk,
// and certainly not the developer's real database.
func fixtureSessions() []model.Session {
	return []model.Session{
		{
			ID:             fixtureID1,
			WorkingDir:     fixtureUnix,
			Model:          "claude-sonnet-4-5",
			AgentMode:      "normal",
			CreatedAt:      time.Date(2024, 5, 6, 7, 0, 0, 0, time.UTC),
			LastActivityAt: time.Date(2024, 5, 6, 8, 0, 0, 0, time.UTC),
			Title:          "fix the flaky test in " + fixtureUnix,
			CreditCost:     1.25,
			InputTokens:    1200,
			OutputTokens:   600,
			CacheRead:      300,
			AssistantCount: 3,
			Messages: []model.Message{
				{NodeID: 1, Role: "user", Content: "why did " + fixtureUnix + " break?"},
				{NodeID: 2, Role: "tool", Content: fixtureErr, ToolCallID: "call-1"},
				{NodeID: 3, Role: "assistant", Content: fixtureErr},
			},
		},
		{
			ID:             fixtureID2,
			WorkingDir:     fixtureWin,
			Model:          "claude-sonnet-4-5",
			AgentMode:      "plan",
			CreatedAt:      time.Date(2024, 5, 6, 9, 0, 0, 0, time.UTC),
			LastActivityAt: time.Date(2024, 5, 6, 9, 30, 0, 0, time.UTC),
			InputTokens:    2000,
			OutputTokens:   1000,
			AssistantCount: 2,
			Messages: []model.Message{
				{NodeID: 1, Role: "tool", Content: fixtureErr},
			},
		},
	}
}

// jsonContains reports whether needle appears in s, either literally or in the
// escaped form JSON uses for backslashes (Windows paths).
func jsonContains(s, needle string) bool {
	if strings.Contains(s, needle) {
		return true
	}
	escaped := strconv.Quote(needle)
	return strings.Contains(s, escaped[1:len(escaped)-1])
}

func mustMarshal(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	return string(data)
}

// ---- SanitizeProject ----

func TestSanitizeProject(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"unix path", "/Users/placeholder/secret-project", "secret-project"},
		{"windows path", `C:\Users\placeholder\secret-project`, "secret-project"},
		{"trailing unix separator", "/Users/placeholder/secret-project/", "secret-project"},
		{"trailing windows separator", `C:\Users\placeholder\secret-project\`, "secret-project"},
		{"repeated trailing separators", "/Users/placeholder/secret-project//", "secret-project"},
		{"bare name, no separator", "secret-project", "secret-project"},
		{"empty", "", UnknownLabel},
		{"root", "/", UnknownLabel},
		{"separators only", "///", UnknownLabel},
		{"dot", ".", UnknownLabel},
		{"path with spaces", "/Users/placeholder/my project/", "my project"},
		{"bare name with spaces", "my project", "my project"},
		{"trailing space", "/Users/placeholder/my project ", "my project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeProject(tc.in); got != tc.want {
				t.Errorf("SanitizeProject(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---- Build: nothing identifying survives ----

func TestBuildOmitsSessionIDsAndAbsolutePaths(t *testing.T) {
	rep := Build(fixtureSessions(), true, time.Now())
	doc := mustMarshal(t, rep)

	forbidden := []string{
		fixtureID1,
		fixtureID2,
		fixtureParent,
		fixtureWinDir,
		fixtureUnix,
		fixtureWin,
		"/Users/",
	}
	for _, f := range forbidden {
		if jsonContains(doc, f) {
			t.Errorf("report JSON leaks %q:\n%s", f, doc)
		}
	}

	// The aggregation must still be useful: both fixtures collapse to the same
	// last-segment label, and the totals reflect the input.
	if rep.Usage.TotalSessions != 2 {
		t.Errorf("TotalSessions = %d, want 2", rep.Usage.TotalSessions)
	}
	if rep.Usage.TotalRequests != 5 {
		t.Errorf("TotalRequests = %d, want 5", rep.Usage.TotalRequests)
	}
	if rep.Usage.TotalTokens != 4800 {
		t.Errorf("TotalTokens = %d, want 4800", rep.Usage.TotalTokens)
	}
	if len(rep.Usage.ByProject) != 1 || rep.Usage.ByProject[0].Project != "secret-project" {
		t.Fatalf("ByProject = %+v, want one %q entry", rep.Usage.ByProject, "secret-project")
	}
	if rep.Usage.ByProject[0].Sessions != 2 {
		t.Errorf("project sessions = %d, want 2", rep.Usage.ByProject[0].Sessions)
	}
	if len(rep.Usage.ByModel) != 1 || rep.Usage.ByModel[0].Model != "claude-sonnet-4-5" {
		t.Fatalf("ByModel = %+v, want one claude-sonnet-4-5 entry", rep.Usage.ByModel)
	}
	if rep.Usage.TotalCost <= 0 {
		t.Errorf("TotalCost = %v, want > 0", rep.Usage.TotalCost)
	}
	if !strings.Contains(doc, "secret-project") {
		t.Error("report should carry the sanitised project label")
	}
}

// ---- Build: the error section is aggregate-only ----

func TestBuildErrorSectionHasNoRawText(t *testing.T) {
	withErrors := mustMarshal(t, Build(fixtureSessions(), true, time.Now()))
	if !strings.Contains(withErrors, `"errors"`) {
		t.Fatalf("includeErrors=true did not produce an errors section:\n%s", withErrors)
	}
	for _, frag := range []string{fixtureErr, "SECRET_RAW_ERROR_TEXT_9f3a", "connection refused", "10.0.0.7"} {
		if jsonContains(withErrors, frag) {
			t.Errorf("errors section leaks raw error text %q:\n%s", frag, withErrors)
		}
	}

	without := mustMarshal(t, Build(fixtureSessions(), false, time.Now()))
	if strings.Contains(without, `"errors"`) {
		t.Errorf("includeErrors=false must omit the errors section:\n%s", without)
	}
}

// ---- Build: cost provenance ----

func TestCostProvenance(t *testing.T) {
	allowed := map[string]bool{
		ProvenanceOfficial:  true,
		ProvenanceEstimated: true,
		ProvenanceMixed:     true,
		ProvenanceUnknown:   true,
	}

	cases := []struct {
		name string
		ss   []model.Session
		want string
	}{
		{"empty input", nil, ProvenanceUnknown},
		{"fixtures mix official and estimated", fixtureSessions(), ProvenanceMixed},
		{"official only", []model.Session{{ID: fixtureID1, WorkingDir: fixtureUnix, Model: "claude-sonnet-4-5", CreditCost: 2, AssistantCount: 1}}, ProvenanceOfficial},
		{"estimated only", []model.Session{{ID: fixtureID1, WorkingDir: fixtureUnix, Model: "claude-sonnet-4-5", InputTokens: 1000, OutputTokens: 100, AssistantCount: 1}}, ProvenanceEstimated},
		{"free model carries no cost signal", []model.Session{{ID: fixtureID1, WorkingDir: fixtureUnix, Model: "glm-5-2", InputTokens: 1000, AssistantCount: 1}}, ProvenanceUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Build(tc.ss, false, time.Now()).Usage.CostProvenance
			if !allowed[got] {
				t.Fatalf("CostProvenance = %q, not one of official|estimated|mixed|unknown", got)
			}
			if got != tc.want {
				t.Errorf("CostProvenance = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---- Manifest ----

func TestManifestIsNonEmptyAndStable(t *testing.T) {
	m1, m2 := Manifest(), Manifest()
	if len(m1) == 0 {
		t.Fatal("Manifest() must describe at least one rule")
	}
	if !reflect.DeepEqual(m1, m2) {
		t.Errorf("Manifest() is not stable:\n%+v\n%+v", m1, m2)
	}

	actions := map[string]bool{"removed": true, "reduced": true, "aggregated": true}
	for _, r := range m1 {
		if r.Field == "" || r.Action == "" || r.Reason == "" {
			t.Errorf("incomplete redaction entry: %+v", r)
		}
		if !actions[r.Action] {
			t.Errorf("unknown action %q in %+v", r.Action, r)
		}
	}

	// The manifest must actually cover the four promises.
	var joined strings.Builder
	for _, r := range m1 {
		joined.WriteString(r.Field + " " + r.Reason + " ")
	}
	for _, want := range []string{"project", "session", "finding", "content"} {
		if !strings.Contains(strings.ToLower(joined.String()), want) {
			t.Errorf("manifest does not mention %q: %s", want, joined.String())
		}
	}

	// Callers must not be able to corrupt the shared rules by mutating the copy.
	m1[0].Field = "mutated"
	if Manifest()[0].Field == "mutated" {
		t.Error("Manifest() must return a fresh slice")
	}
}

// ---- GeneratedAt ----

func TestGeneratedAtIsUTCRFC3339(t *testing.T) {
	local := time.Date(2024, 5, 6, 7, 8, 9, 0, time.FixedZone("X", -7*60*60))
	rep := Build(nil, false, local)

	if !strings.HasSuffix(rep.GeneratedAt, "Z") {
		t.Errorf("GeneratedAt = %q, want a UTC (Z) timestamp", rep.GeneratedAt)
	}
	got, err := time.Parse(time.RFC3339, rep.GeneratedAt)
	if err != nil {
		t.Fatalf("GeneratedAt %q is not RFC3339: %v", rep.GeneratedAt, err)
	}
	if got.Location() != time.UTC {
		t.Errorf("GeneratedAt parsed in %v, want UTC", got.Location())
	}
	if !got.Equal(local) {
		t.Errorf("GeneratedAt = %v, want the same instant as %v", got, local)
	}
	if rep.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", rep.SchemaVersion, SchemaVersion)
	}
	if rep.Notice == "" {
		t.Error("Notice must never be empty")
	}
}

// ---- confirmation helper ----

func TestConfirmationNamesDestinationAndRedactionCount(t *testing.T) {
	msg := confirmation("report.json", 7)
	if !strings.Contains(msg, "report.json") {
		t.Errorf("confirmation must name the destination: %q", msg)
	}
	if !strings.Contains(msg, "7") {
		t.Errorf("confirmation must report the redaction count: %q", msg)
	}
	if !strings.Contains(confirmation("", 3), "stdout") {
		t.Errorf("confirmation must name stdout when no --output is given: %q", confirmation("", 3))
	}
	if n := len(Manifest()); !strings.Contains(confirmation("", n), strconv.Itoa(n)) {
		t.Errorf("confirmation must echo len(Manifest()) = %d", n)
	}
}

// ---- the "never phones home" guard ----

// TestNoNetworkImports parses every Go file in this package (tests included) and
// fails if it imports a networking or process-spawning package. It is cheap and
// durable: the brand promise is "local-first, nothing is uploaded", so the
// cheapest way to keep it is to make the import itself a test failure.
func TestNoNetworkImports(t *testing.T) {
	banned := map[string]bool{
		"net":           true,
		"net/http":      true,
		"net/url":       true,
		"os/exec":       true,
		"net/rpc":       true,
		"net/smtp":      true,
		"net/mail":      true,
		"net/textproto": true,
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	checked := 0
	sawLib := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		if name == "share.go" {
			sawLib = true
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", name, imp.Path.Value, err)
			}
			if banned[path] {
				t.Errorf("%s imports %q: the share command must never make a network call", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no Go source files found to check")
	}
	if !sawLib {
		t.Fatal("share.go was not among the parsed files; the guard is not testing the library")
	}
}
