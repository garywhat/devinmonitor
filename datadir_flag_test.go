package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/garywhat/devinmonitor/internal/reader"

	_ "modernc.org/sqlite"
)

// The helper-process environment variables. main() calls os.Exit on failure, so
// a command's exit code cannot be observed from inside the same process. The
// documented Go pattern is to re-exec this test binary and let TestMain hand
// control to the real main() with a reconstructed argv.
//
// Arguments travel through an environment variable, which cannot contain NUL on
// POSIX, so the ASCII unit separator is used instead.
const (
	envRunCLI = "DEVINMONITOR_TEST_RUN_CLI"
	envArgs   = "DEVINMONITOR_TEST_ARGS"

	// argSep must be a byte that can appear in an environment variable and is
	// not expected in CLI arguments.
	argSep = "\x1f"
)

// TestMain doubles as the CLI helper process. When envRunCLI is set, os.Args is
// replaced with the arguments in envArgs and the real CLI runs.
func TestMain(m *testing.M) {
	if os.Getenv(envRunCLI) == "1" {
		args := []string{"devinmonitor"}
		if raw := os.Getenv(envArgs); raw != "" {
			args = append(args, strings.Split(raw, argSep)...)
		}
		os.Args = args
		main() // returns only on success; os.Exit(1) paths never come back
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runCLI executes the real CLI in a subprocess and returns stdout, stderr and
// the exit code.
func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		envRunCLI+"=1",
		envArgs+"="+strings.Join(args, argSep),
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %v as a subprocess: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// budgetCommands are the six commands that called openReader("") and therefore
// ignored --data-dir. Regression: all six printed the DEFAULT database's
// numbers and exited 0 when pointed at an empty directory.
var budgetCommands = [][]string{
	{"cost"},
	{"budget"},
	{"burn-rate"},
	{"projection"},
	{"top-cost"},
	{"plan", "show"},
}

// TestDataDirIsNotSilentlyIgnored is the regression guard for the worst kind of
// bug: a user pointing --data-dir at another machine's database silently got
// this machine's numbers with exit status 0, so nothing signalled the mistake.
func TestDataDirIsNotSilentlyIgnored(t *testing.T) {
	empty := t.TempDir()

	for _, sub := range budgetCommands {
		args := append([]string{"--data-dir", empty}, sub...)
		_, stderr, code := runCLI(t, args...)

		if code == 0 {
			t.Errorf("%v exited 0; want non-zero — it silently read the default database", args)
			continue
		}
		if !strings.Contains(stderr, "sessions.db not found") {
			t.Errorf("%v stderr = %q; want it to name the missing sessions.db under --data-dir",
				args, strings.TrimSpace(stderr))
		}
		if !strings.Contains(stderr, empty) {
			t.Errorf("%v stderr = %q; want it to mention the requested directory %s",
				args, strings.TrimSpace(stderr), empty)
		}
	}
}

// TestDataDirControlCommandsAlreadyErrored pins the behaviour the six commands
// were brought in line with. If a future refactor makes every command ignore
// --data-dir, this test keeps failing loudly instead of letting the suite
// "pass" because the whole flag became a no-op.
func TestDataDirControlCommandsAlreadyErrored(t *testing.T) {
	empty := t.TempDir()

	for _, name := range []string{"sessions", "projects"} {
		_, stderr, code := runCLI(t, "--data-dir", empty, name)
		if code == 0 {
			t.Errorf("%s --data-dir <empty> exited 0; want non-zero", name)
			continue
		}
		if !strings.Contains(stderr, "sessions.db not found") {
			t.Errorf("%s stderr = %q; want it to name the missing sessions.db", name, strings.TrimSpace(stderr))
		}
	}
}

// TestDataDirAcceptsAValidDirectory is the positive control: the six commands
// must still work when --data-dir actually points at a database. Without this,
// making every command fail unconditionally would satisfy the guards above.
func TestDataDirAcceptsAValidDirectory(t *testing.T) {
	dir := validFixtureDir(t)

	for _, sub := range budgetCommands {
		args := append([]string{"--data-dir", dir}, sub...)
		_, stderr, code := runCLI(t, args...)
		if code != 0 {
			t.Errorf("%v exited %d with stderr %q; want 0 for a valid --data-dir",
				args, code, strings.TrimSpace(stderr))
		}
	}
}

// fixtureSchema is the minimum the reader touches, duplicated from
// internal/reader's test helper because it is not exported.
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

// validFixtureDir writes a schema-valid sessions.db into a fresh temp dir.
func validFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec("INSERT INTO refinery_schema_history (version) VALUES (?)",
		reader.MaxSupportedSchema); err != nil {
		t.Fatalf("version row: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (id, working_directory, backend_type, model, agent_mode,
		 created_at, last_activity_at, title, hidden, metadata)
		 VALUES ('s1','/tmp/p','devin','swe-1-7','bypass',1700000000,1700000000,'t',0,'{}')`); err != nil {
		t.Fatalf("session row: %v", err)
	}
	return dir
}
