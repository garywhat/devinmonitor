// Characterisation tests for the project package.
//
// These cover the pure helpers (path → project name, command/session
// categorisation, JSON argument parsing, table sorting) and the grouping and
// aggregation that the `projects` command performs. Every fixture is in memory:
// no database is opened and no `git` subprocess is started. The functions that
// can only be exercised by shelling out to git (readGitLog, printSessionGitCommits,
// printGitOverview) are deliberately not tested here — see the package report.
package project

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/report"
)

// ---- fixtures & helpers ----

// unpricedModel is absent from the built-in pricing table, so a session using
// it with no CreditCost estimates to exactly 0 cost.
const unpricedModel = "zz-unpriced-model"

func pt(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("project_test: bad fixture time " + s + ": " + err.Error())
	}
	return v
}

// projectFixture groups as follows (grouping key = last path component):
//
//	app   : 3 sessions (one plain, one with a trailing "/", one with "//")
//	other : 1 session
//
// Requests are 9 and 7 so the aggregation order is deterministic.
func projectFixture() []model.Session {
	return []model.Session{
		{ID: "app-1", WorkingDir: "/repos/app", AssistantCount: 5,
			InputTokens: 1000, OutputTokens: 100, CacheRead: 50, CacheWrite: 5, CreditCost: 2.5,
			Messages: []model.Message{{Role: "assistant", GenerationModel: "m1"}}},
		{ID: "app-2", WorkingDir: "/repos/app/", AssistantCount: 3,
			InputTokens: 200, OutputTokens: 20, CacheRead: 10, CacheWrite: 1, CreditCost: 1.5,
			Messages: []model.Message{{Role: "assistant", GenerationModel: "m2"}}},
		{ID: "app-3", WorkingDir: "/repos/app//", AssistantCount: 1, CreditCost: 1.0},
		{ID: "other-1", WorkingDir: "/elsewhere/other", AssistantCount: 7,
			InputTokens: 7, OutputTokens: 7, CreditCost: 0.25},
	}
}

func rowByName(rows []report.ProjectRow, name string) (report.ProjectRow, bool) {
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return report.ProjectRow{}, false
}

func rowNames(rows []report.ProjectRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-done
}

// ---- baseProject ----

func TestBaseProject(t *testing.T) {
	cases := []struct {
		dir  string
		want string
	}{
		{"", "-"},
		{"/home/me/proj", "proj"},
		{"/home/me/proj/", "proj"},  // one trailing separator
		{"/home/me/proj//", "proj"}, // several trailing separators
		{"proj", "proj"},            // no separator at all
		{"/", ""},                   // root trims away to an empty name
		{"C:\\work\\proj", "proj"},  // windows separators are handled here
		{"C:/work/proj", "proj"},
		{"/home/me/my proj", "my proj"},
	}
	for _, c := range cases {
		if got := baseProject(c.dir); got != c.want {
			t.Errorf("baseProject(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}

// ---- BuildProjectRows: grouping ----

func TestBuildProjectRows_GroupsByLastPathComponent(t *testing.T) {
	rows := report.BuildProjectRows(projectFixture())
	if len(rows) != 2 {
		t.Fatalf("got rows %v, want 2 projects", rowNames(rows))
	}
	app, ok := rowByName(rows, "app")
	if !ok {
		t.Fatalf("got rows %v, want an %q project", rowNames(rows), "app")
	}
	if app.Sessions != 3 {
		t.Errorf("app sessions = %d, want 3", app.Sessions)
	}
	if app.Requests != 9 {
		t.Errorf("app requests = %d, want 9", app.Requests)
	}
	if app.InputTok != 1200 || app.OutputTok != 120 || app.CacheRead != 60 || app.CacheWrite != 6 {
		t.Errorf("app tokens = %d/%d/%d/%d, want 1200/120/60/6",
			app.InputTok, app.OutputTok, app.CacheRead, app.CacheWrite)
	}
	if len(app.Models) != 2 || app.Models[0] != "m1" || app.Models[1] != "m2" {
		t.Errorf("app models = %v, want [m1 m2] (union, first-seen order)", app.Models)
	}
}

func TestBuildProjectRows_TrailingSeparatorDoesNotSplitAProject(t *testing.T) {
	ss := []model.Session{
		{ID: "a", WorkingDir: "/repos/app"},
		{ID: "b", WorkingDir: "/repos/app/"},
		{ID: "c", WorkingDir: "/repos/app//"},
		{ID: "d", WorkingDir: "/repos/app///"},
	}
	rows := report.BuildProjectRows(ss)
	if len(rows) != 1 {
		t.Fatalf("got rows %v, want one project", rowNames(rows))
	}
	if rows[0].Name != "app" || rows[0].Sessions != 4 {
		t.Errorf("got %+v, want project %q with 4 sessions", rows[0], "app")
	}
}

func TestBuildProjectRows_DifferentParentsWithSameBasenameAreMerged(t *testing.T) {
	// The grouping key is the *last path component*, not the full working
	// directory, so /a/shared and /b/shared collapse into one project.
	ss := []model.Session{
		{ID: "x", WorkingDir: "/team-a/shared"},
		{ID: "y", WorkingDir: "/team-b/shared"},
	}
	rows := report.BuildProjectRows(ss)
	if len(rows) != 1 || rows[0].Name != "shared" || rows[0].Sessions != 2 {
		t.Fatalf("got %v, want a single %q project with 2 sessions", rowNames(rows), "shared")
	}
}

func TestBuildProjectRows_EmptyWorkingDirLandsInDash(t *testing.T) {
	ss := []model.Session{
		{ID: "empty", WorkingDir: "", AssistantCount: 2},
		{ID: "also-empty", WorkingDir: "", AssistantCount: 1},
		{ID: "real", WorkingDir: "/repos/app", AssistantCount: 4},
	}
	rows := report.BuildProjectRows(ss) // must not panic
	if len(rows) != 2 {
		t.Fatalf("got rows %v, want 2 projects", rowNames(rows))
	}
	dash, ok := rowByName(rows, "-")
	if !ok {
		t.Fatalf("got rows %v, want a %q project for empty working dirs", rowNames(rows), "-")
	}
	if dash.Sessions != 2 || dash.Requests != 3 {
		t.Errorf("dash project = %d sessions / %d requests, want 2/3", dash.Sessions, dash.Requests)
	}

	if got := report.BuildProjectRows(nil); len(got) != 0 {
		t.Errorf("BuildProjectRows(nil) returned %v, want no rows", rowNames(got))
	}
}

func TestBuildProjectRows_WindowsPathIsSplitOnBackslash(t *testing.T) {
	// This used to BE a bug: report's basename helper split only on "/" while
	// project.baseProject also handled "\\", so the `projects` listing called
	// this directory "C:\\work\\proj" while `project` and `git` called it
	// "proj". Both now split on either separator, so the two surfaces agree.
	rows := report.BuildProjectRows([]model.Session{{ID: "win", WorkingDir: `C:\work\proj`}})
	if len(rows) != 1 {
		t.Fatalf("got rows %v, want 1", rowNames(rows))
	}
	if rows[0].Name != "proj" {
		t.Errorf("project name = %q, want %q", rows[0].Name, "proj")
	}
	// The two helpers must agree, which is the point of the fix.
	if got := baseProject(`C:\work\proj`); got != rows[0].Name {
		t.Errorf("project.baseProject = %q but report.BuildProjectRows = %q; they must agree",
			got, rows[0].Name)
	}
}

// ---- BuildProjectRows: aggregation ----

func TestBuildProjectRows_TotalsEqualSumOfParts(t *testing.T) {
	ss := projectFixture()
	rows := report.BuildProjectRows(ss)

	var wantSessions, wantRequests int
	var wantInput, wantOutput, wantCacheRead, wantCacheWrite int64
	var wantCost float64
	for i := range ss {
		wantSessions++
		wantRequests += ss[i].AssistantCount
		wantInput += ss[i].InputTokens
		wantOutput += ss[i].OutputTokens
		wantCacheRead += ss[i].CacheRead
		wantCacheWrite += ss[i].CacheWrite
		cost, _ := report.SessionCost(&ss[i])
		wantCost += cost
	}

	var gotSessions, gotRequests int
	var gotInput, gotOutput, gotCacheRead, gotCacheWrite int64
	var gotCost float64
	for _, r := range rows {
		gotSessions += r.Sessions
		gotRequests += r.Requests
		gotInput += r.InputTok
		gotOutput += r.OutputTok
		gotCacheRead += r.CacheRead
		gotCacheWrite += r.CacheWrite
		gotCost += r.Cost
	}

	if gotSessions != wantSessions || gotRequests != wantRequests {
		t.Errorf("totals = %d sessions / %d requests, want %d/%d",
			gotSessions, gotRequests, wantSessions, wantRequests)
	}
	if gotInput != wantInput || gotOutput != wantOutput || gotCacheRead != wantCacheRead || gotCacheWrite != wantCacheWrite {
		t.Errorf("token totals = %d/%d/%d/%d, want %d/%d/%d/%d",
			gotInput, gotOutput, gotCacheRead, gotCacheWrite,
			wantInput, wantOutput, wantCacheRead, wantCacheWrite)
	}
	if gotCost != wantCost {
		t.Errorf("cost total = %v, want %v", gotCost, wantCost)
	}
	if gotCost != 5.25 {
		t.Errorf("cost total = %v, want 5.25 (2.5 + 1.5 + 1.0 + 0.25)", gotCost)
	}
}

func TestBuildProjectRows_SortsByRequestsDescending(t *testing.T) {
	ss := []model.Session{
		{ID: "one", WorkingDir: "/w/one", AssistantCount: 1},
		{ID: "five", WorkingDir: "/w/five", AssistantCount: 5},
		{ID: "three", WorkingDir: "/w/three", AssistantCount: 3},
	}
	rows := report.BuildProjectRows(ss)
	want := []string{"five", "three", "one"}
	if got := rowNames(rows); len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
}

func TestBuildProjectRows_IsFreeOnlyWhenEverySessionIsFree(t *testing.T) {
	free := report.BuildProjectRows([]model.Session{
		{ID: "f1", WorkingDir: "/w/free", Model: unpricedModel},
		{ID: "f2", WorkingDir: "/w/free", Model: unpricedModel, InputTokens: 1000},
	})
	if len(free) != 1 || !free[0].IsFree {
		t.Fatalf("got %+v, want a single free project", free)
	}
	if free[0].Cost != 0 {
		t.Errorf("free project cost = %v, want 0", free[0].Cost)
	}

	mixed := report.BuildProjectRows([]model.Session{
		{ID: "f1", WorkingDir: "/w/mixed", Model: unpricedModel},
		{ID: "p1", WorkingDir: "/w/mixed", CreditCost: 0.01},
	})
	if len(mixed) != 1 || mixed[0].IsFree {
		t.Fatalf("got %+v, want a single paid project (one session has cost)", mixed)
	}
}

// ---- drill-down / detail smoke tests (no git, no database) ----

func TestPrintProjectDrilldownDoesNotPanic(t *testing.T) {
	ss := []model.Session{
		{ID: "s1", Title: "t1", WorkingDir: "/repos/app", AssistantCount: 2,
			CreatedAt: pt("2026-01-05T10:00:00Z"), LastActivityAt: pt("2026-01-05T11:00:00Z"),
			CreditCost: 0.5,
			Messages: []model.Message{
				{Role: "assistant", CreatedAt: pt("2026-01-05T10:30:00Z"), GenerationModel: "m1",
					Metrics: &model.Metrics{InputTokens: 10, OutputTokens: 5}},
				{Role: "assistant", CreatedAt: pt("2026-01-05T10:40:00Z"), GenerationModel: "m2"},
			},
			ToolCalls: map[string]int{"exec": 2}},
	}
	out := captureStdout(t, func() { printProjectDrilldown("app", ss, 30) })
	if !strings.Contains(out, "Project: app (last 30 days)") {
		t.Errorf("drilldown output missing header: %q", out)
	}
	if !strings.Contains(out, "Daily Breakdown:") || !strings.Contains(out, "Model Breakdown:") {
		t.Errorf("drilldown output missing sections: %q", out)
	}
	if !strings.Contains(out, "Tool Breakdown:") {
		t.Errorf("drilldown output missing tool section: %q", out)
	}

	// Empty session list must still render the headers without panicking.
	empty := captureStdout(t, func() { printProjectDrilldown("none", nil, 30) })
	if !strings.Contains(empty, "Project: none (last 30 days)") {
		t.Errorf("empty drilldown output missing header: %q", empty)
	}
}

func TestPrintProjectDetailDoesNotPanic(t *testing.T) {
	ss := []model.Session{
		{ID: "s1", Title: "t1", Model: "m1", AssistantCount: 2, InputTokens: 10, OutputTokens: 5,
			CreatedAt: pt("2026-01-05T10:00:00Z"), LastActivityAt: pt("2026-01-05T11:00:00Z"),
			CreditCost: 0.5, ToolCalls: map[string]int{"exec": 2}},
	}
	out := captureStdout(t, func() { printProjectDetail("app", ss, 30) })
	if !strings.Contains(out, "Project Detail: app") {
		t.Errorf("detail output missing panel title: %q", out)
	}
	empty := captureStdout(t, func() { printProjectDetail("none", nil, 30) })
	if !strings.Contains(empty, "Sessions:       0") {
		t.Errorf("empty detail output missing zero session count: %q", empty)
	}
}

// ---- pure helpers ----

func TestCategorizeCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"git status", "git"},
		{"GIT status", "git"},
		{"git", "git"},
		{"npm test", "node/js"},
		{"pnpm install", "node/js"},
		{"bun run dev", "node/js"},
		{"go build ./...", "go"},
		{"python3 -m pytest", "python"},
		{"uv run pytest", "python"},
		{"docker compose up", "docker"},
		{"make all", "make"},
		{"cargo test", "rust"},
		{"kubectl get pods", "k8s"},
		{"helm list", "k8s"},
		{"terraform plan", "terraform"},
		{"ssh host uptime", "ssh"},
		{"curl https://example.test", "http"},
		{"rg TODO", "search"},
		{"rm -rf build", "filesystem"},
		{"echo hello", "shell-builtin"},
		{"FOO=bar npm test", "node/js"}, // leading env assignment is stripped
		{`FOO="a b" go test`, "other"},  // quoted values are not understood; the
		// whitespace split leaves `b"` as the first word
		{"  ", "other"},
		{"unknowncmd --flag", "other"},
		{"/usr/bin/git status", "other"}, // absolute paths are not resolved
	}
	for _, c := range cases {
		if got := categorizeCommand(c.cmd); got != c.want {
			t.Errorf("categorizeCommand(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestCategorizeSession(t *testing.T) {
	cases := []struct {
		name string
		s    model.Session
		want string
	}{
		{"title says tests", model.Session{Title: "Write unit tests"}, "Testing"},
		{"title says fix", model.Session{Title: "Fix the login bug"}, "Debugging"},
		{"title says refactor", model.Session{Title: "Refactor the parser"}, "Refactoring"},
		{"title says readme", model.Session{Title: "Update the README"}, "Documentation"},
		{"title says deploy", model.Session{Title: "Deploy to prod"}, "DevOps"},
		{"title says commit", model.Session{Title: "Commit the changes"}, "Git/VCS"},
		{"nothing matches", model.Session{Title: "Implement the parser"}, "Coding"},
		{"empty session", model.Session{}, "Coding"},
		{"tool name matches git", model.Session{Title: "misc", ToolCalls: map[string]int{"git": 1}}, "Git/VCS"},
		{"tool name matches deploy", model.Session{Title: "zzz", ToolCalls: map[string]int{"deploy_tool": 1}}, "DevOps"},
		{"testing wins over debugging", model.Session{Title: "Debug the test failure"}, "Testing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := categorizeSession(c.s); got != c.want {
				t.Errorf("categorizeSession(%+v) = %q, want %q", c.s, got, c.want)
			}
		})
	}
}

func TestExtractMCPServerName(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		{`{"server_name":"srv-a"}`, "srv-a"},
		{`{"serverName":"srv-b"}`, "srv-b"},
		{`{"server":"srv-c"}`, "srv-c"},
		{`{"name":"srv-d"}`, "srv-d"},
		{`{"other":"x"}`, ""},
		{`{"server_name":123}`, ""},
		{`{"server_name":null}`, ""},
		{`not json`, ""},
		{`{`, ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := extractMCPServerName(c.args); got != c.want {
			t.Errorf("extractMCPServerName(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestExtractShellCommand(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		{`{"command":"ls -la"}`, "ls -la"},
		{`{"cmd":"pwd"}`, "pwd"},
		{`{"shell_command":"whoami"}`, "whoami"},
		{`{"script":"echo hi"}`, "echo hi"},
		{`{"other":"x"}`, ""},
		{`{"command":42}`, ""},
		{`{bad`, ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := extractShellCommand(c.args); got != c.want {
			t.Errorf("extractShellCommand(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestFirstWordAndTruncateStr(t *testing.T) {
	words := map[string]string{
		"git status": "git",
		"  spaced  ": "spaced",
		"a\tb":       "a",
		"a\nb":       "a",
		"single":     "single",
		"":           "",
		"   ":        "",
	}
	for in, want := range words {
		if got := firstWord(in); got != want {
			t.Errorf("firstWord(%q) = %q, want %q", in, got, want)
		}
	}

	truncs := []struct {
		in   string
		max  int
		want string
	}{
		{"abcdef", 3, "ab…"},
		{"abcdef", 6, "abcdef"},
		{"abc", 10, "abc"},
		{"", 5, ""},
		{"abcdef", 1, "…"},
	}
	for _, c := range truncs {
		if got := truncateStr(c.in, c.max); got != c.want {
			t.Errorf("truncateStr(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestSortedModelKeysOrdersByRequestsDescending(t *testing.T) {
	stats := map[string]*model.ModelStats{
		"a": {Name: "a", Requests: 1},
		"b": {Name: "b", Requests: 5},
		"c": {Name: "c", Requests: 3},
	}
	got := sortedModelKeys(stats)
	want := []string{"b", "c", "a"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if empty := sortedModelKeys(nil); len(empty) != 0 {
		t.Errorf("sortedModelKeys(nil) = %v, want empty", empty)
	}
	if got := sortedModelKeys(map[string]*model.ModelStats{"z": {Requests: 2}, "y": {Requests: 2}}); len(got) != 2 {
		t.Errorf("sortedModelKeys with ties returned %v, want both keys", got)
	}
}

func TestCompactModels(t *testing.T) {
	if got := compactModels(nil); got != "-" {
		t.Errorf("compactModels(nil) = %q, want %q", got, "-")
	}
	if got := compactModels([]string{"m1", "m2"}); got != "m1, m2" {
		t.Errorf("compactModels = %q, want %q", got, "m1, m2")
	}
}

// TestCategorizeCommandAssignmentOnlyTerminates pins the loop guard in
// categorizeCommand: an env-assignment-only command must not spin forever.
// The timeout matters — without the guard the call never returns.
func TestCategorizeCommandAssignmentOnlyTerminates(t *testing.T) {
	done := make(chan string, 1)
	go func() { done <- categorizeCommand("FOO=bar") }()
	select {
	case got := <-done:
		if got != "other" {
			t.Errorf("categorizeCommand(%q) = %q, want %q", "FOO=bar", got, "other")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("categorizeCommand did not terminate for an assignment-only command")
	}
}
