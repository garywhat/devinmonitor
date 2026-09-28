package reader

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// This file pins three fixes that all come from the same discovery: Devin
// writes 2-3 message_nodes per request, each carrying an IDENTICAL copy of the
// metrics, and the created_at COLUMN is the batch write time rather than the
// message's own time.
//
// Measured on the real database before the fixes: summing every node inflated
// token totals by 2.00x-3.42x against Devin's own accounting, and bucketing by
// the column put 60% of all tokens on the wrong day.

// writeDupFixture builds a database that reproduces the duplication exactly:
// three requests, and each request present as three nodes with identical
// metrics. Request 2 is a compaction pass, which Devin's own accounting
// excludes.
func writeDupFixture(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "sessions.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec("INSERT INTO refinery_schema_history (version) VALUES (?)", MaxSupportedSchema); err != nil {
		t.Fatalf("version row: %v", err)
	}

	// Devin's own accounting for this session: 300 input, 30 output, and two
	// agent messages. If our dedup is right, input - compaction == 300.
	sessionMeta := `{"total_credit_cost":0,"total_acu_cost":0,"response_dimensions":[` +
		`{"uid":"agent_messages","kind":{"CumulativeMetric":{"label":"Agent messages","value":2}}},` +
		`{"uid":"input_tokens","kind":{"CumulativeMetric":{"label":"Input tokens","value":300}}},` +
		`{"uid":"output_tokens","kind":{"CumulativeMetric":{"label":"Output tokens","value":30}}},` +
		`{"uid":"model","kind":{"Metric":{"label":"Model","value":"SWE-1.7"}}}]}`
	if _, err := db.Exec(
		`INSERT INTO sessions (id, working_directory, backend_type, model, agent_mode,
		 created_at, last_activity_at, title, hidden, metadata)
		 VALUES ('dup','/tmp/p','devin','swe-1-7','bypass',1700000000,1700000000,'t',0,?)`,
		sessionMeta); err != nil {
		t.Fatalf("session row: %v", err)
	}

	// A batch-write timestamp for the COLUMN, deliberately far from the real
	// message times so a test can tell which one was used.
	const batchColumn = 1790000000 // 2026-09-16-ish

	type req struct {
		id, msgID, model, createdAt string
		in, out                     int
	}
	reqs := []req{
		{"r1", "m1", "swe-1-7", "2026-08-05T10:00:00.000000Z", 100, 10},
		{"r2", "m2", "compactor", "2026-08-05T11:00:00.000000Z", 50, 5},
		{"r3", "m3", "swe-1-7", "2026-08-07T09:30:00.000000Z", 200, 20},
	}

	node := 0
	for _, r := range reqs {
		payload, err := json.Marshal(map[string]any{
			"message_id": r.msgID,
			"role":       "assistant",
			"content":    "x",
			"metadata": map[string]any{
				"request_id":       r.id,
				"created_at":       r.createdAt,
				"generation_model": r.model,
				"finish_reason":    "stop",
				"metrics": map[string]any{
					"input_tokens":          r.in,
					"output_tokens":         r.out,
					"cache_read_tokens":     0,
					"cache_creation_tokens": 0,
					"ttft_ms":               100,
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// Three identical nodes per request -- the real duplication.
		for copy := 0; copy < 3; copy++ {
			node++
			if _, err := db.Exec(
				`INSERT INTO message_nodes (session_id, node_id, chat_message, created_at) VALUES ('dup',?,?,?)`,
				node, string(payload), batchColumn); err != nil {
				t.Fatalf("message node: %v", err)
			}
		}
	}
}

// TestMetricsAreCountedOncePerRequest is the regression guard for the 2.0-3.4x
// inflation. Without the dedup this fixture reports 1050 input tokens
// (3 requests x 3 copies x their value) instead of 350.
func TestMetricsAreCountedOncePerRequest(t *testing.T) {
	dir := t.TempDir()
	writeDupFixture(t, dir)
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	ss, err := r.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(ss) != 1 {
		t.Fatalf("got %d sessions, want 1", len(ss))
	}
	s := ss[0]

	// 100 + 50 + 200, counted once each despite 9 nodes.
	if s.InputTokens != 350 {
		t.Errorf("InputTokens = %d, want 350 (each request counted once, not 3x)", s.InputTokens)
	}
	if s.OutputTokens != 35 {
		t.Errorf("OutputTokens = %d, want 35", s.OutputTokens)
	}
	// Two agent requests plus the compaction pass.
	if s.AssistantCount != 3 {
		t.Errorf("AssistantCount = %d, want 3 (each request once)", s.AssistantCount)
	}
}

// TestCompactionIsTrackedSeparatelyAndReconcilesWithTheVendor checks the whole
// point of the split: Devin's own figure excludes compaction, so our totals
// minus compaction must equal it.
func TestCompactionIsTrackedSeparatelyAndReconcilesWithTheVendor(t *testing.T) {
	dir := t.TempDir()
	writeDupFixture(t, dir)
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	ss, err := r.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	s := ss[0]

	if s.Compaction.InputTokens != 50 {
		t.Errorf("Compaction.InputTokens = %d, want 50 (the compactor request only)", s.Compaction.InputTokens)
	}
	if got, want := s.BillableTokenTotal(), int64(300+30); got != want {
		t.Errorf("BillableTokenTotal() = %d, want %d", got, want)
	}

	// The vendor figure must have been parsed, and must agree.
	v, ok := s.VendorDimensionValue("input_tokens")
	if !ok {
		t.Fatal("response_dimensions were not parsed: input_tokens missing")
	}
	if v != 300 {
		t.Errorf("vendor input_tokens = %v, want 300", v)
	}
	// This is the assertion that would have caught the original bug.
	if s.InputTokens-s.Compaction.InputTokens != int64(v) {
		t.Errorf("our non-compaction input = %d but Devin reports %v",
			s.InputTokens-s.Compaction.InputTokens, v)
	}
	if mv, ok := s.VendorDimensionValue("agent_messages"); !ok || mv != 2 {
		t.Errorf("vendor agent_messages = %v (present=%v), want 2", mv, ok)
	}
}

// TestMessageTimeComesFromTheJSONNotTheColumn pins the misattribution fix. The
// fixture writes every row with the SAME batch created_at column, so if the
// reader used the column all messages would land on one day and the
// cross-midnight/跨月 split below would vanish.
func TestMessageTimeComesFromTheJSONNotTheColumn(t *testing.T) {
	dir := t.TempDir()
	writeDupFixture(t, dir)
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	ss, err := r.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	s := ss[0]

	seen := map[string]bool{}
	for _, m := range s.Messages {
		if m.Role != "assistant" {
			continue
		}
		if m.CreatedAt.Year() == 2026 && m.CreatedAt.Month() == time.September {
			t.Errorf("message %d used the batch column date (%s); it must use metadata.created_at",
				m.NodeID, m.CreatedAt.Format(time.RFC3339))
		}
		seen[m.CreatedAt.Format("2006-01-02")] = true
	}
	// Request 1 and 2 are on 2026-08-05, request 3 on 2026-08-07.
	want := map[string]bool{"2026-08-05": true, "2026-08-07": true}
	if len(seen) != len(want) {
		t.Fatalf("distinct message days = %v, want %v", seen, want)
	}
	for d := range want {
		if !seen[d] {
			t.Errorf("missing message day %s (got %v)", d, seen)
		}
	}
}

// TestDedupFallsBackWhenIdentifiersAreMissing makes sure a row without a
// message_id or request_id still counts exactly once rather than being dropped
// or duplicated.
func TestDedupFallsBackWhenIdentifiersAreMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(fixtureSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec("INSERT INTO refinery_schema_history (version) VALUES (?)", MaxSupportedSchema); err != nil {
		t.Fatalf("version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, working_directory, backend_type, model, agent_mode,
		created_at, last_activity_at, title, hidden, metadata)
		VALUES ('bare','/tmp/p','devin','swe-1-7','bypass',1700000000,1700000000,'t',0,'{}')`); err != nil {
		t.Fatalf("session: %v", err)
	}
	// No message_id, no request_id -- only the node distinguishes them.
	for i := 1; i <= 2; i++ {
		payload := fmt.Sprintf(`{"role":"assistant","content":"x","metadata":{"created_at":"2026-08-05T10:0%d:00.000000Z","metrics":{"input_tokens":10,"output_tokens":1}}}`, i-1)
		if _, err := db.Exec(`INSERT INTO message_nodes (session_id, node_id, chat_message, created_at) VALUES ('bare',?,?,1700000000)`,
			i, payload); err != nil {
			t.Fatalf("node: %v", err)
		}
	}
	db.Close()

	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	ss, err := r.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if ss[0].InputTokens != 20 {
		t.Errorf("InputTokens = %d, want 20 (two identifier-less messages must both count)", ss[0].InputTokens)
	}
	if ss[0].AssistantCount != 2 {
		t.Errorf("AssistantCount = %d, want 2", ss[0].AssistantCount)
	}
}
