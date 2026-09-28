package export

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/status"
)

// vendorCounts models a session the way the free-tier case the field exists for
// looks in the database: Devin's OWN per-session token accounting
// (metadata.response_dimensions) is present, and there is no ACU cost at all.
func vendorCounts(input int64) []model.VendorDimension {
	return []model.VendorDimension{{UID: "input_tokens", Label: "Input tokens", Value: float64(input)}}
}

func billable(id string, tokens int64) model.Session {
	return model.Session{ID: id, Model: "devin-v2", InputTokens: tokens, AssistantCount: 1}
}

// TestSessionCostBasis covers the four states at session granularity, plus the
// case the whole dimension exists for: a session that carries Devin's own token
// counts and NO ACU cost is a token-basis figure, not an ACU one and not
// "unavailable". Collapsing it into either would recreate the confusion.
func TestSessionCostBasis(t *testing.T) {
	cases := []struct {
		name string
		sess model.Session
		want status.CostBasis
	}{
		{"credit cost is ACU-billed",
			model.Session{ID: "s", CreditCost: 4.5, InputTokens: 100}, status.CostBasisACU},
		{"ACU cost is ACU-billed",
			model.Session{ID: "s", ACUCost: 2.5, InputTokens: 100}, status.CostBasisACU},
		{"credit and ACU together are still ACU-billed",
			model.Session{ID: "s", CreditCost: 1, ACUCost: 1, InputTokens: 100}, status.CostBasisACU},
		{"official token counts with no ACU cost are a token estimate",
			model.Session{ID: "s", Model: "devin-v2", InputTokens: 1000, OutputTokens: 200,
				VendorDimensions: vendorCounts(1200)}, status.CostBasisTokenEstimate},
		{"tokens with no vendor counts are still a token estimate",
			billable("s", 500), status.CostBasisTokenEstimate},
		{"a free model with tokens is a zero-valued token estimate",
			model.Session{ID: "s", Model: "glm-5-2", InputTokens: 500}, status.CostBasisTokenEstimate},
		{"only cache tokens still count as a token basis",
			model.Session{ID: "s", Model: "devin-v2", CacheRead: 900}, status.CostBasisTokenEstimate},
		{"a session with nothing in it has no basis", model.Session{ID: "s"}, status.CostBasisUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SessionCostBasis(&c.sess); got != c.want {
				t.Errorf("SessionCostBasis(%+v) = %q, want %q", c.sess, got, c.want)
			}
		})
	}

	if got := SessionCostBasis(nil); got != status.CostBasisUnavailable {
		t.Errorf("SessionCostBasis(nil) = %q, want %q", got, status.CostBasisUnavailable)
	}
}

// TestCostBasisOfSessions is the aggregate rule: the money alone cannot show
// that two zero-cost sessions were billed differently, which is why the basis is
// combined from the sessions and not from the sum.
func TestCostBasisOfSessions(t *testing.T) {
	acu := model.Session{ID: "acu", ACUCost: 3}
	estimate := billable("est", 100)

	cases := []struct {
		name string
		ss   []model.Session
		want status.CostBasis
	}{
		{"only ACU-billed sessions", []model.Session{acu, acu}, status.CostBasisACU},
		{"only token estimates", []model.Session{estimate}, status.CostBasisTokenEstimate},
		{"both meters in one figure", []model.Session{acu, estimate}, status.CostBasisMixed},
		{"both meters whose costs are both zero", []model.Session{
			{ID: "f", ACUCost: 0, InputTokens: 100}, // token-priced, free
			{ID: "a", ACUCost: 0.5, InputTokens: 0},
		}, status.CostBasisMixed},
		{"no sessions at all", nil, status.CostBasisUnavailable},
		{"sessions with no data", []model.Session{{ID: "empty"}}, status.CostBasisUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CostBasisOfSessions(c.ss); got != c.want {
				t.Errorf("CostBasisOfSessions = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCostBasisOfSessionsSince(t *testing.T) {
	now := time.Now()
	ss := []model.Session{
		{ID: "old-acu", ACUCost: 5, LastActivityAt: now.AddDate(0, 0, -40)},
		{ID: "new-est", Model: "devin-v2", InputTokens: 100, LastActivityAt: now},
	}
	if got := CostBasisOfSessionsSince(ss, now.AddDate(0, 0, -7)); got != status.CostBasisTokenEstimate {
		t.Errorf("recent basis = %q, want %q (the ACU session is outside the window)", got, status.CostBasisTokenEstimate)
	}
	if got := CostBasisOfSessionsSince(ss, now.AddDate(0, 0, -60)); got != status.CostBasisMixed {
		t.Errorf("wide basis = %q, want %q", got, status.CostBasisMixed)
	}
}

// TestBuildDocumentCarriesCostBasis: the session export publishes the cost
// components separately, but a consumer still has to apply the precedence rule
// (credit/ACU wins when non-zero) to know which component is "the" cost. The
// basis field removes that guesswork at both the row and the document level.
func TestBuildDocumentCarriesCostBasis(t *testing.T) {
	ss := []model.Session{
		{ID: "acu", Model: "devin-v2", CreditCost: 2.5, InputTokens: 100, AssistantCount: 1},
		{ID: "est", Model: "devin-v2", InputTokens: 900, OutputTokens: 100, AssistantCount: 1,
			VendorDimensions: vendorCounts(1000)},
	}
	doc := BuildDocument(ss, false)
	if doc.CostBasis != status.CostBasisMixed {
		t.Errorf("document cost_basis = %q, want %q", doc.CostBasis, status.CostBasisMixed)
	}
	rows := map[string]ExpSession{}
	for _, r := range doc.Sessions {
		rows[r.ID] = r
		if r.CostBasis == "" {
			t.Errorf("session %q carries no cost_basis", r.ID)
		}
	}
	if got := rows["acu"].CostBasis; got != string(status.CostBasisACU) {
		t.Errorf("acu session basis = %q, want %q", got, status.CostBasisACU)
	}
	if got := rows["est"].CostBasis; got != string(status.CostBasisTokenEstimate) {
		t.Errorf("free-tier session basis = %q, want %q", got, status.CostBasisTokenEstimate)
	}
	// A single session is never "mixed": one session's figure has one meter.
	for _, r := range doc.Sessions {
		if r.CostBasis == string(status.CostBasisMixed) {
			t.Errorf("session %q reports mixed; a row cannot be mixed", r.ID)
		}
	}

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["cost_basis"]; !ok {
		t.Errorf("document JSON has no cost_basis key: %s", raw)
	}
	var sess []map[string]json.RawMessage
	if err := json.Unmarshal(keys["sessions"], &sess); err != nil {
		t.Fatal(err)
	}
	for i, row := range sess {
		if _, ok := row["cost_basis"]; !ok {
			t.Errorf("session %d JSON has no cost_basis key: %s", i, raw)
		}
	}

	// An empty export still names its (absent) basis rather than omitting it.
	empty := BuildDocument(nil, false)
	if empty.CostBasis != status.CostBasisUnavailable {
		t.Errorf("empty document cost_basis = %q, want %q", empty.CostBasis, status.CostBasisUnavailable)
	}
}

// TestBuildStatusSnapshotLabelsEachPeriod: today and the month can differ, and
// the snapshot must say so per figure rather than pick one for the whole view.
func TestBuildStatusSnapshotLabelsEachPeriod(t *testing.T) {
	now := time.Now()
	ss := []model.Session{
		// Today: ACU-billed.
		{ID: "today-acu", ACUCost: 2, LastActivityAt: now},
		// Earlier this month: token-estimated.
		{ID: "month-est", Model: "devin-v2", InputTokens: 400, LastActivityAt: monthStartForTest(now).Add(time.Hour)},
		// Last month, and hidden: neither counted nor allowed to colour the basis.
		{ID: "old-acu", ACUCost: 9, LastActivityAt: monthStartForTest(now).AddDate(0, 0, -1)},
		{ID: "hidden-acu", ACUCost: 7, Hidden: true, LastActivityAt: now},
	}
	snap := BuildStatusSnapshot(ss)
	if snap.Sessions != 3 {
		t.Fatalf("Sessions = %d, want 3 (hidden sessions are not counted)", snap.Sessions)
	}
	if snap.TodayBasis != status.CostBasisACU {
		t.Errorf("TodayBasis = %q, want %q", snap.TodayBasis, status.CostBasisACU)
	}
	if snap.MonthBasis != status.CostBasisMixed {
		t.Errorf("MonthBasis = %q, want %q (ACU today + token estimate earlier in the month)", snap.MonthBasis, status.CostBasisMixed)
	}
	if snap.CostBasis != status.CostBasisMixed {
		t.Errorf("CostBasis = %q, want %q", snap.CostBasis, status.CostBasisMixed)
	}
}

func monthStartForTest(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
}

// TestStatusRenderersCarryBasisAndWarn is the reader-facing rule for the
// `status` command: every figure is tagged, and a snapshot whose figures span
// both meters says in words that they cannot be compared.
func TestStatusRenderersCarryBasisAndWarn(t *testing.T) {
	t.Run("single basis", func(t *testing.T) {
		snap := StatusSnapshot{
			GeneratedAt: time.Date(2026, 5, 4, 9, 8, 7, 0, time.UTC),
			TodayCost:   1.5, MonthCost: 12.25, Sessions: 3,
			TodayBasis: status.CostBasisACU, MonthBasis: status.CostBasisACU, CostBasis: status.CostBasisACU,
		}
		full := FullStatus(snap)
		if !strings.Contains(full, "Today:     $1.50 [acu]") || !strings.Contains(full, "Month:     $12.25 [acu]") {
			t.Errorf("FullStatus does not tag each figure:\n%s", full)
		}
		if !strings.Contains(full, "Cost basis: acu") {
			t.Errorf("FullStatus has no legend:\n%s", full)
		}
		compact := CompactStatus(snap)
		if strings.Contains(compact, "MIXED") {
			t.Errorf("a single-basis snapshot must not warn: %q", compact)
		}
		if !strings.Contains(compact, "$1.50 [acu]") || !strings.Contains(compact, "$12.25 [acu]") {
			t.Errorf("CompactStatus does not tag each figure: %q", compact)
		}
	})

	t.Run("mixed bases", func(t *testing.T) {
		snap := StatusSnapshot{
			GeneratedAt: time.Date(2026, 5, 4, 9, 8, 7, 0, time.UTC),
			TodayCost:   1.5, MonthCost: 12.25, Sessions: 3,
			TodayBasis: status.CostBasisACU, MonthBasis: status.CostBasisMixed, CostBasis: status.CostBasisMixed,
		}
		full := FullStatus(snap)
		if !strings.Contains(full, "MIXED UNITS") {
			t.Errorf("FullStatus must warn about mixed units:\n%s", full)
		}
		compact := CompactStatus(snap)
		if !strings.Contains(compact, "MIXED UNITS") {
			t.Errorf("CompactStatus must warn about mixed units: %q", compact)
		}
		if !strings.Contains(compact, "$1.50 [acu]") || !strings.Contains(compact, "$12.25 [mixed units]") {
			t.Errorf("CompactStatus lost a per-figure tag: %q", compact)
		}
		if strings.Contains(compact, "\n") {
			t.Errorf("CompactStatus must stay on one line: %q", compact)
		}
	})

	t.Run("today and month disagree without either being mixed", func(t *testing.T) {
		snap := StatusSnapshot{
			TodayBasis: status.CostBasisACU,
			MonthBasis: status.CostBasisTokenEstimate,
			CostBasis:  status.CostBasisMixed,
		}
		if !strings.Contains(CompactStatus(snap), "MIXED UNITS") {
			t.Errorf("two different bases in one line must warn: %q", CompactStatus(snap))
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		snap := StatusSnapshot{CostBasis: status.CostBasisUnavailable}
		full := FullStatus(snap)
		if !strings.Contains(full, "[no basis]") || !strings.Contains(full, "no basis — no cost or token data") {
			t.Errorf("FullStatus must say there is no basis:\n%s", full)
		}
	})
}
