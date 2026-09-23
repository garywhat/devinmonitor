// Package reader adapts Devin CLI's internal SQLite schema to model types.
//
// It isolates schema-specific SQL/JSON parsing so that reports and UI never
// touch the raw DB. When Devin CLI changes its schema, only this package
// needs a new adapter (e.g. v2.go) selected by DetectSchemaVersion.
package reader

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/garywhat/devinmonitor/internal/model"
)

// MaxSupportedSchema is the highest refinery schema version this build has been
// validated against.
//
// It used to be 999 with a comment claiming the schema had no version row,
// which made ErrSchemaUnsupported unreachable: a future schema could drop or
// rename a column and we would silently parse it as if it were supported,
// reporting wrong numbers. For a monitoring tool a loud failure beats a
// plausible wrong figure, so the ceiling is the version actually exercised.
//
// That guard then earned its keep immediately: Devin shipped version 17 while
// this was being developed, and instead of quietly misreading it the tool said
// so. v17 turned out to be additive — a new `subagent_heads` table, with every
// column we read intact — so the ceiling moved to 17 rather than the guard being
// relaxed.
//
// A bump that is in fact compatible can still be forced through with
// DEVINMONITOR_ALLOW_UNKNOWN_SCHEMA=1, which is far easier to explain to a user
// than silently wrong data.
const MaxSupportedSchema = 17

// allowUnknownSchema reports whether the escape hatch is set.
func allowUnknownSchema() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DEVINMONITOR_ALLOW_UNKNOWN_SCHEMA"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ErrNoDB is returned when sessions.db cannot be located.
type ErrNoDB struct{ Path string }

func (e *ErrNoDB) Error() string { return fmt.Sprintf("sessions.db not found at: %s", e.Path) }

// ErrSchemaUnsupported is returned when the schema version exceeds our adapters.
type ErrSchemaUnsupported struct{ Ver, Max int }

func (e *ErrSchemaUnsupported) Error() string {
	return fmt.Sprintf("unsupported schema version %d: this build understands up to %d.\n"+
		"Upgrade devinmonitor, or set DEVINMONITOR_ALLOW_UNKNOWN_SCHEMA=1 to try anyway "+
		"(readings may be wrong)", e.Ver, e.Max)
}

// Reader reads normalized data from Devin CLI's session store.
type Reader interface {
	// Sessions returns all sessions (newest first), with messages aggregated.
	Sessions() ([]model.Session, error)
	// Session returns a single session by ID, with messages.
	Session(id string) (*model.Session, error)
	// SchemaVersion returns the detected refinery schema version (0 if none).
	SchemaVersion() int
	// DBPath returns the resolved database path.
	DBPath() string
	// Close releases any resources.
	Close() error
}

// Open auto-detects the DB path and returns a Reader for the current schema.
// dataDir overrides the auto-detected directory when non-empty.
func Open(dataDir string) (Reader, error) {
	path, err := ResolveDBPath(dataDir)
	if err != nil {
		return nil, err
	}
	ver, err := DetectSchemaVersion(path)
	if err != nil {
		return nil, fmt.Errorf("detect schema: %w", err)
	}
	if ver > MaxSupportedSchema && !allowUnknownSchema() {
		return nil, &ErrSchemaUnsupported{Ver: ver, Max: MaxSupportedSchema}
	}
	// Currently only v1 exists.
	return newV1Reader(path, ver)
}

// ResolveDBPath finds sessions.db across platforms.
//
// Order: dataDir arg > DEVIN_DATA_DIR env > platform defaults.
func ResolveDBPath(dataDir string) (string, error) {
	// An explicitly provided dataDir is authoritative: if dataDir/sessions.db
	// does not exist, it is an error rather than silently falling through to
	// the environment or platform defaults. This prevents the CLI from
	// unknowingly analyzing the wrong (default) database when the user points
	// at a bad path.
	if dataDir != "" {
		c := filepath.Join(dataDir, "sessions.db")
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
		return "", &ErrNoDB{Path: c}
	}

	candidates := []string{}
	if env := os.Getenv("DEVIN_DATA_DIR"); env != "" {
		candidates = append(candidates, filepath.Join(env, "sessions.db"))
	}
	for _, p := range platformDefaults() {
		candidates = append(candidates, filepath.Join(p, "sessions.db"))
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	if len(candidates) > 0 {
		return "", &ErrNoDB{Path: candidates[0]}
	}
	return "", &ErrNoDB{Path: "(unknown)"}
}

// platformDefaults returns candidate Devin CLI data directories per OS.
func platformDefaults() []string {
	home, _ := os.UserHomeDir()
	var out []string
	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			out = append(out, filepath.Join(appdata, "devin", "cli"))
		}
		if localappdata := os.Getenv("LOCALAPPDATA"); localappdata != "" {
			out = append(out, filepath.Join(localappdata, "devin", "cli"))
		}
	default: // linux, darwin, freebsd...
		if home != "" {
			// XDG default.
			out = append(out, filepath.Join(home, ".local", "share", "devin", "cli"))
			// macOS sometimes uses Library/Application Support.
			if runtime.GOOS == "darwin" {
				out = append(out, filepath.Join(home, "Library", "Application Support", "devin", "cli"))
			}
		}
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			out = append(out, filepath.Join(xdg, "devin", "cli"))
		}
	}
	return out
}

// DetectSchemaVersion reads refinery_schema_history max(version).
// Returns 0 if the table is empty or missing (treated as v1 baseline).
func DetectSchemaVersion(dbPath string) (int, error) {
	r, err := newV1Reader(dbPath, 0)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	return r.SchemaVersion(), nil
}
