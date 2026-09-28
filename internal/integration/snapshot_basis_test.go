package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/config"
	"github.com/garywhat/devinmonitor/internal/model"
	"github.com/garywhat/devinmonitor/internal/status"
)

// snapshotBasisOf assembles a snapshot for the given sessions and returns the
// breakdown, so the four states can be asserted without a database.
func snapshotBasisOf(t *testing.T, ss []model.Session) status.Breakdown {
	t.Helper()
	snap := buildProtocolSnapshot(ss, time.Now(), protocolOpts{}, &config.Config{})
	return snap.Breakdown
}

// TestBuildProtocolSnapshotCostBasis covers the four basis states on the
// machine-readable surface. `snapshot --json` is the surface other tools read,
// so "mixed" has to be visible there even when every dollar amount is 0.0 and
// the money alone could not show it.
func TestBuildProtocolSnapshotCostBasis(t *testing.T) {
	acu := model.Session{ID: "acu", Model: "devin-v2", ACUCost: 12.5, AssistantCount: 3}
	tokenOnly := model.Session{ID: "est", Model: "devin-v2", InputTokens: 1000, OutputTokens: 100, AssistantCount: 2}
	empty := model.Session{ID: "empty"}

	cases := []struct {
		name string
		ss   []model.Session
		want status.CostBasis
	}{
		{"only ACU billing", []model.Session{acu, acu}, status.CostBasisACU},
		{"only token estimates", []model.Session{tokenOnly}, status.CostBasisTokenEstimate},
		{"both meters at once", []model.Session{acu, tokenOnly}, status.CostBasisMixed},
		{"nothing to classify", nil, status.CostBasisUnavailable},
		{"sessions with no data at all", []model.Session{empty}, status.CostBasisUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := snapshotBasisOf(t, c.ss).CostBasis
			if got != c.want {
				t.Errorf("breakdown.costBasis = %q, want %q", got, c.want)
			}
		})
	}
}

// TestBuildProtocolSnapshotBasisIsIndependentOfProvenance is the point of the
// second dimension: two snapshots can share a provenance and still be different
// quantities.
func TestBuildProtocolSnapshotBasisIsIndependentOfProvenance(t *testing.T) {
	now := time.Now()
	tokenOnly := []model.Session{{ID: "est", Model: "devin-v2", InputTokens: 1000, AssistantCount: 1}}
	acu := []model.Session{{ID: "acu", Model: "devin-v2", ACUCost: 5, AssistantCount: 1}}

	estSnap := buildProtocolSnapshot(tokenOnly, now, protocolOpts{}, &config.Config{})
	acuSnap := buildProtocolSnapshot(acu, now, protocolOpts{}, &config.Config{})

	// Both are single-session snapshots whose cost is trustworthy enough to
	// report; only the basis tells a reader they are different kinds of number.
	if estSnap.Breakdown.CostBasis == acuSnap.Breakdown.CostBasis {
		t.Fatalf("basis is %q for both a token estimate and an ACU figure; the dimension is not doing its job",
			estSnap.Breakdown.CostBasis)
	}
	if estSnap.Breakdown.CostBasis != status.CostBasisTokenEstimate {
		t.Errorf("token-only basis = %q, want %q", estSnap.Breakdown.CostBasis, status.CostBasisTokenEstimate)
	}
	if acuSnap.Breakdown.CostBasis != status.CostBasisACU {
		t.Errorf("ACU basis = %q, want %q", acuSnap.Breakdown.CostBasis, status.CostBasisACU)
	}
}

// modelSession builds a session whose requests were served by genModel, which
// is what report.BuildModelRows keys a model row on (the message's
// generation_model, not the session-level Model field).
func modelSession(id, genModel string, acuCost float64, tokens int64) model.Session {
	return model.Session{
		ID: id, Model: genModel, ACUCost: acuCost,
		InputTokens: tokens, AssistantCount: 1,
		Messages: []model.Message{{
			NodeID: 1, Role: "assistant", GenerationModel: genModel,
			Metrics: &model.Metrics{InputTokens: tokens},
		}},
	}
}

// TestProtocolModelRowsCarryTheirOwnBasis: a per-model table is where a reader
// ranks models against each other, so each row must say which meter produced
// its cost. The free-tier case is asserted explicitly: Devin's own token counts
// with no ACU cost is a token estimate, not an ACU figure.
func TestProtocolModelRowsCarryTheirOwnBasis(t *testing.T) {
	rows := protocolModelRows([]model.Session{
		modelSession("a", "acu-model", 9, 0),
		func() model.Session {
			s := modelSession("b", "free-model", 0, 5000)
			// Devin's own accounting for this session: official token counts,
			// and no ACU cost anywhere.
			s.VendorDimensions = []model.VendorDimension{{UID: "input_tokens", Label: "Input tokens", Value: 5000}}
			return s
		}(),
		modelSession("c", "devin-v2", 0, 100),
	})
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	byModel := map[string]status.ModelRow{}
	for _, r := range rows {
		byModel[r.Model] = r
		if r.CostBasis == "" {
			t.Errorf("row %q carries no costBasis", r.Model)
		}
	}
	if got := byModel["acu-model"].CostBasis; got != status.CostBasisACU {
		t.Errorf("acu-model basis = %q, want %q", got, status.CostBasisACU)
	}
	if got := byModel["free-model"].CostBasis; got != status.CostBasisTokenEstimate {
		t.Errorf("free-model basis = %q, want %q: Devin's token counts are not an ACU figure",
			got, status.CostBasisTokenEstimate)
	}
	if got := byModel["devin-v2"].CostBasis; got != status.CostBasisTokenEstimate {
		t.Errorf("devin-v2 basis = %q, want %q", got, status.CostBasisTokenEstimate)
	}
}

// TestSnapshotJSONShowsBothBasesInOneDocument logs — and asserts — what a
// consumer actually receives when one output holds both meters. The log is
// deliberate: the before/after evidence for this change is this document.
func TestSnapshotJSONShowsBothBasesInOneDocument(t *testing.T) {
	ss := []model.Session{
		modelSession("acu", "acu-model", 9, 0),
		modelSession("free", "free-model", 0, 5000),
	}
	snap := buildProtocolSnapshot(ss, time.Now(), protocolOpts{}, &config.Config{})
	data, err := status.EncodeJSON(snap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("snapshot for one ACU-billed and one token-estimated session:\n%s", data)

	var doc struct {
		Breakdown struct {
			CostProvenance string `json:"costProvenance"`
			CostBasis      string `json:"costBasis"`
			ByModel        []struct {
				Model     string `json:"model"`
				CostBasis string `json:"costBasis"`
			} `json:"byModel"`
		} `json:"breakdown"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Breakdown.CostBasis != string(status.CostBasisMixed) {
		t.Errorf("breakdown.costBasis = %q, want %q", doc.Breakdown.CostBasis, status.CostBasisMixed)
	}
	bases := map[string]string{}
	for _, r := range doc.Breakdown.ByModel {
		bases[r.Model] = r.CostBasis
	}
	if bases["acu-model"] != string(status.CostBasisACU) || bases["free-model"] != string(status.CostBasisTokenEstimate) {
		t.Errorf("per-row bases = %v, want acu-model=acu and free-model=token_estimate", bases)
	}
}
