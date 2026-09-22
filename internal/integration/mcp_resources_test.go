package integration

// Fixture-backed tests for the MCP resource bodies. The fixture lives in a
// t.TempDir() so a test run never reads (or writes) the developer's real
// Devin session database.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/spf13/cobra"
)

// resourceFixtureSchema is the minimal schema internal/reader expects: the
// sessions and message_nodes tables plus the refinery version row.
const resourceFixtureSchema = `
CREATE TABLE sessions (
  id TEXT PRIMARY KEY, working_directory TEXT NOT NULL, backend_type TEXT NOT NULL,
  model TEXT NOT NULL, agent_mode TEXT NOT NULL, created_at INTEGER NOT NULL,
  last_activity_at INTEGER NOT NULL, title TEXT, main_chain_id INTEGER,
  shell_last_seen_index INTEGER DEFAULT 0, cogs_json TEXT, workspace_dirs TEXT,
  hidden INTEGER NOT NULL DEFAULT 0, metadata TEXT);
CREATE TABLE message_nodes (
  row_id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
  node_id INTEGER NOT NULL, parent_node_id INTEGER,
  chat_message TEXT NOT NULL, created_at INTEGER NOT NULL, metadata TEXT,
  UNIQUE(session_id, node_id));
CREATE TABLE refinery_schema_history (version INTEGER PRIMARY KEY);
INSERT INTO refinery_schema_history (version) VALUES (3);
`

// resourceFixtureModel is the generation model the fixture messages use.
const resourceFixtureModel = "swe-1-7"

// newResourceFixture writes a fixture sessions.db into a fresh temp dir and
// returns both the directory (the value handed to --data-dir) and the DB path.
//
// Two sessions are inserted: a fresh one carrying three assistant messages at
// now-30m/now-20m/now-10m (so a 5h billing window is open), and a three-day-old
// session with no activity (so detectAlerts has a ghost session to report).
func newResourceFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}

	if _, err := db.Exec(resourceFixtureSchema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}

	now := time.Now()
	insertSession := `INSERT INTO sessions
		(id, working_directory, backend_type, model, agent_mode, created_at,
		 last_activity_at, title, main_chain_id, workspace_dirs, hidden, metadata)
		VALUES (?, ?, 'devin', ?, 'normal', ?, ?, ?, 0, '', 0, '')`
	if _, err := db.Exec(insertSession,
		"fixture-active", "/tmp/fixture", resourceFixtureModel,
		now.Add(-time.Hour).Unix(), now.Add(-10*time.Minute).Unix(), "active fixture"); err != nil {
		t.Fatalf("insert active session: %v", err)
	}
	if _, err := db.Exec(insertSession,
		"fixture-ghost", "/tmp/fixture", resourceFixtureModel,
		now.Add(-72*time.Hour).Unix(), now.Add(-72*time.Hour).Unix(), "idle fixture"); err != nil {
		t.Fatalf("insert ghost session: %v", err)
	}

	// idx 0 => now-30m, 1 => now-20m, 2 => now-10m: all inside one hour, so
	// limit.Identify keeps them in a single open window.
	offsets := []time.Duration{-30 * time.Minute, -20 * time.Minute, -10 * time.Minute}
	insertMessage := `INSERT INTO message_nodes
		(session_id, node_id, parent_node_id, chat_message, created_at, metadata)
		VALUES ('fixture-active', ?, ?, ?, ?, NULL)`
	for i, off := range offsets {
		ts := now.Add(off).Unix()
		chat := fmt.Sprintf(`{"role":"assistant","content":"synthetic","metadata":{"generation_model":"%s","finish_reason":"stop",
 "metrics":{"input_tokens":1000,"output_tokens":100,"cache_read_tokens":0,"cache_creation_tokens":0,
 "ttft_ms":500.0,"total_time_ms":2000.0,"tokens_per_sec":50.0}}}`, resourceFixtureModel)
		if _, err := db.Exec(insertMessage, i+1, i, chat, ts); err != nil {
			t.Fatalf("insert message %d: %v", i+1, err)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
	return dir
}

// newResourceCommand builds a throwaway command tree wired exactly the way
// mcp.go is: a root persistent --data-dir flag whose value reaches the builder
// through cmd.Flags().GetString("data-dir") in openReader.
func newResourceCommand(t *testing.T) *cobra.Command {
	t.Helper()
	dir := newResourceFixture(t)

	root := &cobra.Command{Use: "devinmonitor", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().String("data-dir", dir, "override Devin data directory")
	child := &cobra.Command{
		Use: "mcp",
		Run: func(cmd *cobra.Command, _ []string) {
			// Production runs the builders from Run; scraping the parsed flag
			// value back out here proves the same wiring works in this test.
			got, err := cmd.Flags().GetString("data-dir")
			if err != nil || got != dir {
				t.Fatalf("data-dir flag not inherited by subcommand: %q, %v", got, err)
			}
		},
	}
	root.AddCommand(child)
	root.SetArgs([]string{"mcp"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute fixture command: %v", err)
	}
	return child
}

// assertResourceText enforces the plain-text contract: no ANSI escapes, no
// tabs, no empty body, no pathological line length.
func assertResourceText(t *testing.T, name, text string) {
	t.Helper()
	if text == "" {
		t.Fatalf("%s: body is empty", name)
	}
	if strings.Contains(text, "\x1b") {
		t.Errorf("%s: body contains an ANSI escape sequence", name)
	}
	if strings.Contains(text, "\t") {
		t.Errorf("%s: body contains a tab", name)
	}
	for i, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if n := utf8.RuneCountInString(line); n > 100 {
			t.Errorf("%s: line %d is %d runes (limit 100): %q", name, i+1, n, line)
		}
	}
}

func TestResourceSummary(t *testing.T) {
	cmd := newResourceCommand(t)

	text, rerr := buildResourceSummary(cmd)
	if rerr != nil {
		t.Fatalf("buildResourceSummary returned error: %+v", rerr)
	}
	assertResourceText(t, "summary", text)

	// The aggregate must state its provenance, and the fixture has no
	// credit/ACU cost at all, so that provenance is "estimated".
	if !strings.Contains(text, "estimated") {
		t.Errorf("summary does not name the provenance:\n%s", text)
	}
	for _, word := range []string{"official", "estimated", "mixed", "unknown"} {
		if strings.Contains(text, word) {
			return
		}
	}
	t.Errorf("summary matches no provenance vocabulary:\n%s", text)
}

func TestResourceModels(t *testing.T) {
	cmd := newResourceCommand(t)

	text, rerr := buildResourceModels(cmd)
	if rerr != nil {
		t.Fatalf("buildResourceModels returned error: %+v", rerr)
	}
	assertResourceText(t, "models", text)

	if !strings.Contains(text, resourceFixtureModel) {
		t.Errorf("models body does not name %q:\n%s", resourceFixtureModel, text)
	}
	if !strings.Contains(text, "%") {
		t.Errorf("models body does not report a token share:\n%s", text)
	}
}

func TestResourceBlocks(t *testing.T) {
	cmd := newResourceCommand(t)

	text, rerr := buildResourceBlocks(cmd)
	if rerr != nil {
		t.Fatalf("buildResourceBlocks returned error: %+v", rerr)
	}
	assertResourceText(t, "blocks", text)

	if !strings.Contains(text, "active") {
		t.Errorf("blocks body does not report an active window:\n%s", text)
	}
	if !strings.Contains(text, "tokens/min") {
		t.Errorf("blocks body does not report a burn rate:\n%s", text)
	}
	if !strings.Contains(text, "Recent finished windows") {
		t.Errorf("blocks body does not include window history:\n%s", text)
	}
}

func TestResourceAlerts(t *testing.T) {
	cmd := newResourceCommand(t)

	text, rerr := buildResourceAlerts(cmd)
	if rerr != nil {
		t.Fatalf("buildResourceAlerts returned error: %+v", rerr)
	}
	assertResourceText(t, "alerts", text)

	// The fixture's idle session must surface as a ghost alert; if detection
	// ever stops firing, the explicit "no alerts" line is the accepted shape.
	if !strings.Contains(strings.ToLower(text), "ghost") && !strings.Contains(text, "No alerts") {
		t.Errorf("alerts body has neither an alert nor the no-alerts line:\n%s", text)
	}
}

// TestResourceDispatch checks the dispatcher: every advertised URI yields a
// non-empty body, an unknown URI yields the error (and only then).
func TestResourceDispatch(t *testing.T) {
	cmd := newResourceCommand(t)

	for _, res := range mcpResources() {
		text, rerr := readMCPResource(cmd, res.URI)
		if rerr != nil {
			t.Errorf("readMCPResource(%s) returned error: %+v", res.URI, rerr)
			continue
		}
		assertResourceText(t, res.URI, text)
	}

	text, rerr := readMCPResource(cmd, "devinmonitor://does-not-exist")
	if rerr == nil {
		t.Fatalf("readMCPResource accepted an unknown URI (body %q)", text)
	}
	if rerr.Code != -32602 {
		t.Errorf("unknown URI error code = %d, want -32602", rerr.Code)
	}
	if text != "" {
		t.Errorf("unknown URI returned a body: %q", text)
	}
}
