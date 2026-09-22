package reader

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// fixtureSchema is the minimum the reader touches: a sessions table, a
// message_nodes table and the refinery version row it detects.
const fixtureSchema = `
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
`

// TestSchemaVersionAtCeilingReads guards the happy path: a database at exactly
// MaxSupportedSchema must open, since that is the version we validated against.
func TestSchemaVersionAtCeilingReads(t *testing.T) {
	dir := t.TempDir()
	writeFixtureTo(t, dir, MaxSupportedSchema)

	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open at the supported ceiling failed: %v", err)
	}
	defer r.Close()
	if got := r.SchemaVersion(); got != MaxSupportedSchema {
		t.Errorf("SchemaVersion() = %d, want %d", got, MaxSupportedSchema)
	}
}

// TestSchemaVersionAboveCeilingFailsLoudly is the regression guard for the bug
// this replaced: MaxSupportedSchema was 999, so the check was unreachable and a
// future schema would have been parsed silently as if it were supported.
func TestSchemaVersionAboveCeilingFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	writeFixtureTo(t, dir, MaxSupportedSchema+1)

	_, err := Open(dir)
	if err == nil {
		t.Fatal("Open above the supported ceiling succeeded; want ErrSchemaUnsupported")
	}
	var unsup *ErrSchemaUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("error is %T (%v), want *ErrSchemaUnsupported", err, err)
	}
	if unsup.Ver != MaxSupportedSchema+1 || unsup.Max != MaxSupportedSchema {
		t.Errorf("ErrSchemaUnsupported = {%d, %d}, want {%d, %d}",
			unsup.Ver, unsup.Max, MaxSupportedSchema+1, MaxSupportedSchema)
	}
	// The message has to tell the user what to do about it.
	msg := err.Error()
	for _, want := range []string{"DEVINMONITOR_ALLOW_UNKNOWN_SCHEMA", "Upgrade devinmonitor"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not mention %q", msg, want)
		}
	}
}

// TestUnknownSchemaEscapeHatch documents the deliberate way out of the ceiling.
func TestUnknownSchemaEscapeHatch(t *testing.T) {
	dir := t.TempDir()
	writeFixtureTo(t, dir, MaxSupportedSchema+1)
	t.Setenv("DEVINMONITOR_ALLOW_UNKNOWN_SCHEMA", "1")

	r, err := Open(dir)
	if err != nil {
		t.Fatalf("escape hatch did not take effect: %v", err)
	}
	defer r.Close()
	if got := r.SchemaVersion(); got != MaxSupportedSchema+1 {
		t.Errorf("SchemaVersion() = %d, want %d", got, MaxSupportedSchema+1)
	}
}

// TestAllowUnknownSchemaParsing pins the accepted truthy spellings.
func TestAllowUnknownSchemaParsing(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " yes ", "on"} {
		t.Setenv("DEVINMONITOR_ALLOW_UNKNOWN_SCHEMA", v)
		if !allowUnknownSchema() {
			t.Errorf("allowUnknownSchema() = false for %q, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Setenv("DEVINMONITOR_ALLOW_UNKNOWN_SCHEMA", v)
		if allowUnknownSchema() {
			t.Errorf("allowUnknownSchema() = true for %q, want false", v)
		}
	}
}

// writeFixtureTo writes the fixture into an existing directory, so a test can
// keep a handle on it for assertions.
func writeFixtureTo(t *testing.T, dir string, version int) {
	t.Helper()
	path := filepath.Join(dir, "sessions.db")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clean fixture: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec("INSERT INTO refinery_schema_history (version) VALUES (?)", version); err != nil {
		t.Fatalf("version row: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (id, working_directory, backend_type, model, agent_mode,
		 created_at, last_activity_at, title, hidden, metadata)
		 VALUES ('s1','/tmp/p','devin','swe-1-7','bypass',1700000000,1700000000,'t',0,'{}')`); err != nil {
		t.Fatalf("session row: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
