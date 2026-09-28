package budget

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/garywhat/devinmonitor/internal/reader"

	_ "modernc.org/sqlite"
)

// budgetFixtureSchema is the minimum the reader touches, duplicated from
// internal/reader's fixture because that helper is not exported.
const budgetFixtureSchema = `
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

// writeBudgetFixture creates a valid sessions.db holding exactly one session
// with the given id.
func writeBudgetFixture(t *testing.T, dir, sessionID string) {
	t.Helper()
	path := filepath.Join(dir, "sessions.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(budgetFixtureSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec("INSERT INTO refinery_schema_history (version) VALUES (?)",
		reader.MaxSupportedSchema); err != nil {
		t.Fatalf("version row: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (id, working_directory, backend_type, model, agent_mode,
		 created_at, last_activity_at, title, hidden, metadata)
		 VALUES (?,'/tmp/p','devin','swe-1-7','bypass',1700000000,1700000000,'t',0,'{}')`,
		sessionID); err != nil {
		t.Fatalf("session row: %v", err)
	}
}

// TestOpenReaderHonorsDataDirFlag is the root-cause guard for the bug where
// every command in this package called openReader("") and therefore silently
// analysed the DEFAULT database, ignoring --data-dir entirely.
//
// Two fixtures are wired up at once so the test can tell them apart:
//   - the --data-dir flag points at a database whose only session is "flagged"
//   - DEVIN_DATA_DIR (the auto-detect fallback an empty argument would use)
//     points at a database whose only session is "fallback"
//
// The reader must report "flagged". Passing an empty string instead of the
// flag value would resolve to "fallback", failing the assertion with a message
// that names the actual bug rather than dying on os.Exit.
func TestOpenReaderHonorsDataDirFlag(t *testing.T) {
	flagDir := t.TempDir()
	writeBudgetFixture(t, flagDir, "flagged")

	fallbackDir := t.TempDir()
	writeBudgetFixture(t, fallbackDir, "fallback")
	t.Setenv("DEVIN_DATA_DIR", fallbackDir)

	root := &cobra.Command{Use: "root"}
	root.PersistentFlags().String("data-dir", "", "override Devin data directory")

	var got []string
	probe := &cobra.Command{
		Use: "probe",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				t.Fatalf("Sessions: %v", err)
			}
			for _, s := range ss {
				got = append(got, s.ID)
			}
		},
	}
	root.AddCommand(probe)
	root.SetArgs([]string{"--data-dir", flagDir, "probe"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(got) != 1 || got[0] != "flagged" {
		t.Fatalf("openReader read sessions %v; want [\"flagged\"] — it ignored --data-dir and fell back to DEVIN_DATA_DIR", got)
	}
}

// TestOpenReaderUsesFallbackWhenFlagAbsent pins the intended behaviour when the
// user does not pass --data-dir: auto-detection must still work.
func TestOpenReaderUsesFallbackWhenFlagAbsent(t *testing.T) {
	fallbackDir := t.TempDir()
	writeBudgetFixture(t, fallbackDir, "fallback")
	t.Setenv("DEVIN_DATA_DIR", fallbackDir)

	root := &cobra.Command{Use: "root"}
	root.PersistentFlags().String("data-dir", "", "override Devin data directory")

	var got []string
	probe := &cobra.Command{
		Use: "probe",
		Run: func(cmd *cobra.Command, args []string) {
			r := openReader(cmd)
			defer r.Close()
			ss, err := r.Sessions()
			if err != nil {
				t.Fatalf("Sessions: %v", err)
			}
			for _, s := range ss {
				got = append(got, s.ID)
			}
		},
	}
	root.AddCommand(probe)
	root.SetArgs([]string{"probe"})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(got) != 1 || got[0] != "fallback" {
		t.Fatalf("openReader read sessions %v; want [\"fallback\"] from DEVIN_DATA_DIR", got)
	}
}
