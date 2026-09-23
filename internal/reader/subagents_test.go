package reader

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// subagentHeadsDDL mirrors the v17 migration, as measured against Devin's real
// database (`SELECT sql FROM sqlite_master WHERE name='subagent_heads'`).
//
// The PRIMARY KEY (session_id, agent_id) is load-bearing for this file: it
// means one session holds at most ONE head row per agent, so the fixture must
// not try to give one agent two nodes — the first draft of these tests did, and
// the database rejected it.
const subagentHeadsDDL = `
CREATE TABLE subagent_heads (
    session_id    TEXT    NOT NULL,
    agent_id      TEXT    NOT NULL,
    chain_node_id INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (session_id, agent_id),
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);`

// headRow is one fixture row for subagent_heads.
type headRow struct {
	sessionID   string
	agentID     string
	chainNodeID int
	updatedAt   int64
}

// writeHeadsFixture builds on the package's existing writeFixtureTo convention
// (a sessions/message_nodes/refinery version fixture with session 's1') and
// then applies the v17 table plus extra sessions and rows on top.
//
// withTable=false reproduces a database whose recorded version is 17 but where
// the table was never created, which is the second guard SubagentHeads carries.
func writeHeadsFixture(t *testing.T, dir string, version int, withTable bool, extraSessions []string, heads []headRow) {
	t.Helper()
	writeFixtureTo(t, dir, version)

	if !withTable {
		if len(heads) != 0 {
			t.Fatal("fixture asks for head rows without creating the table")
		}
		return
	}
	path := filepath.Join(dir, "sessions.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(subagentHeadsDDL); err != nil {
		t.Fatalf("subagent_heads schema: %v", err)
	}
	for _, id := range extraSessions {
		if _, err := db.Exec(
			`INSERT INTO sessions (id, working_directory, backend_type, model, agent_mode,
			 created_at, last_activity_at, title, hidden, metadata)
			 VALUES (?,'/tmp/p','devin','swe-1-7','bypass',1700000000,1700000000,'t',0,'{}')`, id); err != nil {
			t.Fatalf("session row %s: %v", id, err)
		}
	}
	for _, h := range heads {
		if _, err := db.Exec(
			`INSERT INTO subagent_heads (session_id, agent_id, chain_node_id, updated_at)
			 VALUES (?,?,?,?)`, h.sessionID, h.agentID, h.chainNodeID, h.updatedAt); err != nil {
			t.Fatalf("head row %+v: %v", h, err)
		}
	}
}

// openHeadsReader opens the fixture through the public Open path (so the real
// version gate and ceiling check run) and hands back the concrete reader.
func openHeadsReader(t *testing.T, dir string) *v1Reader {
	t.Helper()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open fixture: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	vr, ok := r.(*v1Reader)
	if !ok {
		t.Fatalf("Open returned %T, want *v1Reader", r)
	}
	return vr
}

// TestSubagentHeadsV17ReturnsRows is the happy path: rows come back in primary
// key order with timestamps parsed. The rows are inserted deliberately out of
// agent order so that the ordering is proven rather than inherited from insert
// order. Because (session_id, agent_id) is the primary key there is exactly one
// row per agent, so the fixture gives each agent one node.
func TestSubagentHeadsV17ReturnsRows(t *testing.T) {
	dir := t.TempDir()
	writeHeadsFixture(t, dir, 17, true, nil, []headRow{
		{sessionID: "s1", agentID: "ccc333", chainNodeID: 5, updatedAt: 1700000300},
		{sessionID: "s1", agentID: "aaa111", chainNodeID: 9, updatedAt: 1700000100},
		{sessionID: "s1", agentID: "bbb222", chainNodeID: 3, updatedAt: 1700000200},
	})

	got, err := openHeadsReader(t, dir).SubagentHeads("s1")
	if err != nil {
		t.Fatalf("SubagentHeads: %v", err)
	}
	want := []SubagentHead{
		{SessionID: "s1", AgentID: "aaa111", ChainNodeID: 9, UpdatedAt: time.Unix(1700000100, 0)},
		{SessionID: "s1", AgentID: "bbb222", ChainNodeID: 3, UpdatedAt: time.Unix(1700000200, 0)},
		{SessionID: "s1", AgentID: "ccc333", ChainNodeID: 5, UpdatedAt: time.Unix(1700000300, 0)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d heads, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("head[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestSubagentHeadsV17EmptyIsNotAnError pins the shape every real database has
// returned so far: the table exists and is empty. That must be an empty result
// with a nil error, so callers can distinguish "nothing recorded" from failure.
func TestSubagentHeadsV17EmptyIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeHeadsFixture(t, dir, 17, true, nil, nil)

	got, err := openHeadsReader(t, dir).SubagentHeads("s1")
	if err != nil {
		t.Fatalf("SubagentHeads on an empty table returned an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d heads from an empty table, want 0: %+v", len(got), got)
	}
}

// TestSubagentHeadsV16TableAbsentIsEmpty is the compatibility guarantee: the
// table does not exist below v17, and a v16 database must read as empty with a
// nil error rather than as a failure. The fixture here has no subagent_heads
// table at all, so a query would error if the version gate did not fire.
func TestSubagentHeadsV16TableAbsentIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeHeadsFixture(t, dir, 16, false, nil, nil)

	r := openHeadsReader(t, dir)
	if got := r.SchemaVersion(); got != 16 {
		t.Fatalf("fixture version = %d, want 16", got)
	}
	got, err := r.SubagentHeads("s1")
	if err != nil {
		t.Fatalf("SubagentHeads on a v16 database returned an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d heads on a v16 database, want 0: %+v", len(got), got)
	}
}

// TestSubagentHeadsV17TableMissingIsEmpty covers the narrower second guard: a
// database that reports v17 but lacks the table still reads as empty, so the
// optional table can never break the report it is surfaced in.
func TestSubagentHeadsV17TableMissingIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeHeadsFixture(t, dir, 17, false, nil, nil)

	got, err := openHeadsReader(t, dir).SubagentHeads("s1")
	if err != nil {
		t.Fatalf("SubagentHeads with v17 but no table returned an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d heads with no table present, want 0: %+v", len(got), got)
	}
}

// TestSubagentHeadsSeveralAgentsInOneSession checks that a session's heads are
// not collapsed to one row per session: the table's key is (session, agent).
func TestSubagentHeadsSeveralAgentsInOneSession(t *testing.T) {
	dir := t.TempDir()
	writeHeadsFixture(t, dir, 17, true, nil, []headRow{
		{sessionID: "s1", agentID: "a1", chainNodeID: 100, updatedAt: 1700000100},
		{sessionID: "s1", agentID: "a2", chainNodeID: 101, updatedAt: 1700000101},
		{sessionID: "s1", agentID: "a3", chainNodeID: 102, updatedAt: 1700000102},
		{sessionID: "s1", agentID: "a4", chainNodeID: 103, updatedAt: 1700000103},
	})

	got, err := openHeadsReader(t, dir).SubagentHeads("s1")
	if err != nil {
		t.Fatalf("SubagentHeads: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d heads, want 4: %+v", len(got), got)
	}
	for i, wantAgent := range []string{"a1", "a2", "a3", "a4"} {
		if got[i].AgentID != wantAgent {
			t.Errorf("head[%d].AgentID = %q, want %q", i, got[i].AgentID, wantAgent)
		}
	}
}

// TestSubagentHeadsScopedToOneSession proves the read is keyed by session: rows
// belonging to another session in the same database must not appear, and that
// other session must still return its own.
func TestSubagentHeadsScopedToOneSession(t *testing.T) {
	dir := t.TempDir()
	writeHeadsFixture(t, dir, 17, true, []string{"s2"}, []headRow{
		{sessionID: "s1", agentID: "a1", chainNodeID: 11, updatedAt: 1700000100},
		{sessionID: "s2", agentID: "b1", chainNodeID: 22, updatedAt: 1700000200},
		{sessionID: "s2", agentID: "b2", chainNodeID: 33, updatedAt: 1700000300},
	})

	r := openHeadsReader(t, dir)
	s1, err := r.SubagentHeads("s1")
	if err != nil {
		t.Fatalf("SubagentHeads(s1): %v", err)
	}
	if len(s1) != 1 || s1[0].AgentID != "a1" || s1[0].ChainNodeID != 11 {
		t.Errorf("SubagentHeads(s1) = %+v, want only a1/11", s1)
	}
	s2, err := r.SubagentHeads("s2")
	if err != nil {
		t.Fatalf("SubagentHeads(s2): %v", err)
	}
	if len(s2) != 2 || s2[0].AgentID != "b1" || s2[1].AgentID != "b2" {
		t.Errorf("SubagentHeads(s2) = %+v, want b1 then b2", s2)
	}

	// A session with no recorded heads is empty, not an error.
	none, err := r.SubagentHeads("no-such-session")
	if err != nil {
		t.Fatalf("SubagentHeads(unknown): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("SubagentHeads(unknown) = %+v, want empty", none)
	}
}

// TestSubagentHeadsDoesNotDependOnLiveDB is a guard against the fixture tests
// silently passing because DEVIN_DATA_DIR pointed them elsewhere: everything
// above resolves its path from the temp dir passed to Open.
func TestSubagentHeadsDoesNotDependOnLiveDB(t *testing.T) {
	t.Setenv("DEVIN_DATA_DIR", "")
	dir := t.TempDir()
	writeHeadsFixture(t, dir, 17, true, nil, []headRow{
		{sessionID: "s1", agentID: "a1", chainNodeID: 7, updatedAt: 1700000100},
	})
	got, err := openHeadsReader(t, dir).SubagentHeads("s1")
	if err != nil {
		t.Fatalf("SubagentHeads: %v", err)
	}
	if len(got) != 1 || got[0].ChainNodeID != 7 {
		t.Fatalf("SubagentHeads = %+v, want one head at node 7", got)
	}
}
