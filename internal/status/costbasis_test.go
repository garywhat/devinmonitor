package status

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCombineCostBasis covers the aggregation rule, including the two states
// that only exist at the aggregate level: "mixed" (both meters contributed) and
// "unavailable" (nobody established a basis).
func TestCombineCostBasis(t *testing.T) {
	cases := []struct {
		name  string
		bases []CostBasis
		want  CostBasis
	}{
		{"only ACU", []CostBasis{CostBasisACU, CostBasisACU}, CostBasisACU},
		{"only token estimates", []CostBasis{CostBasisTokenEstimate}, CostBasisTokenEstimate},
		{"ACU and token estimates mix", []CostBasis{CostBasisACU, CostBasisTokenEstimate}, CostBasisMixed},
		{"an already-mixed item contaminates the total",
			[]CostBasis{CostBasisMixed, CostBasisACU}, CostBasisMixed},
		{"nothing at all", nil, CostBasisUnavailable},
		{"only unavailable", []CostBasis{CostBasisUnavailable, CostBasisUnavailable}, CostBasisUnavailable},
		{"unavailable does not mask a real basis",
			[]CostBasis{CostBasisUnavailable, CostBasisACU}, CostBasisACU},
		{"an unknown value degrades to unavailable", []CostBasis{"wat"}, CostBasisUnavailable},
		{"an empty value degrades to unavailable", []CostBasis{""}, CostBasisUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CombineCostBasis(c.bases...); got != c.want {
				t.Errorf("CombineCostBasis(%v) = %q, want %q", c.bases, got, c.want)
			}
		})
	}
}

func TestNormalizeCostBasis(t *testing.T) {
	for _, known := range []CostBasis{CostBasisACU, CostBasisTokenEstimate, CostBasisMixed, CostBasisUnavailable} {
		if got := NormalizeCostBasis(known); got != known {
			t.Errorf("NormalizeCostBasis(%q) = %q, want itself", known, got)
		}
	}
	for _, unknown := range []CostBasis{"", "ACU", "tokens", "official"} {
		if got := NormalizeCostBasis(unknown); got != CostBasisUnavailable {
			t.Errorf("NormalizeCostBasis(%q) = %q, want %q", unknown, got, CostBasisUnavailable)
		}
	}
}

// TestCostBasisTagAndLegend is the reader-facing contract: every basis has a
// tag that can ride on a printed number, and every legend says — in every
// branch — that ACU and tokens are not convertible. That last clause is the one
// that actually stops a reader comparing two figures, so its absence is a
// failure and not a style choice.
func TestCostBasisTagAndLegend(t *testing.T) {
	tags := map[CostBasis]string{
		CostBasisACU:           "[acu]",
		CostBasisTokenEstimate: "[token est]",
		CostBasisMixed:         "[mixed units]",
		CostBasisUnavailable:   "[no basis]",
	}
	for basis, want := range tags {
		if got := basis.Tag(); got != want {
			t.Errorf("%q.Tag() = %q, want %q", basis, got, want)
		}
		if !strings.Contains(basis.Legend(), "Cost basis:") {
			t.Errorf("%q.Legend() does not announce the basis: %q", basis, basis.Legend())
		}
	}
	// "unavailable" is the one branch with nothing to convert from, so it is the
	// only legend allowed to omit the non-convertibility sentence.
	for _, basis := range []CostBasis{CostBasisACU, CostBasisTokenEstimate, CostBasisMixed} {
		legend := basis.Legend()
		if !strings.Contains(legend, "ACU") || !strings.Contains(legend, "token") {
			t.Errorf("%q.Legend() must name both units: %q", basis, legend)
		}
		if !strings.Contains(legend, "no conversion") && !strings.Contains(legend, "not comparable") {
			t.Errorf("%q.Legend() must say the units cannot be compared: %q", basis, legend)
		}
	}
	if !CostBasisMixed.IsMixed() {
		t.Error("CostBasisMixed.IsMixed() = false")
	}
	for _, basis := range []CostBasis{CostBasisACU, CostBasisTokenEstimate, CostBasisUnavailable, ""} {
		if basis.IsMixed() {
			t.Errorf("%q.IsMixed() = true", basis)
		}
	}
}

// TestSnapshotCarriesCostBasis pins the wire contract: the basis is required on
// the breakdown and on every model row, it is never empty, and it survives a
// JSON round trip.
func TestSnapshotCarriesCostBasis(t *testing.T) {
	snap := BuildSnapshot(Input{
		Now:            time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC),
		TodayCost:      1.25,
		TotalSessions:  2,
		ActiveSessions: 1,
		CostProvenance: ConfidenceOfficial,
		CostBasis:      CostBasisACU,
		ByModel: []ModelRow{
			{Model: "a", Cost: 1.25, CostBasis: CostBasisACU, Share: 100},
		},
	})
	if snap.Breakdown.CostBasis != CostBasisACU {
		t.Errorf("breakdown costBasis = %q, want %q", snap.Breakdown.CostBasis, CostBasisACU)
	}
	if snap.Breakdown.ByModel[0].CostBasis != CostBasisACU {
		t.Errorf("row costBasis = %q, want %q", snap.Breakdown.ByModel[0].CostBasis, CostBasisACU)
	}

	b, err := EncodeJSON(snap)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var breakdown map[string]json.RawMessage
	if err := json.Unmarshal(raw["breakdown"], &breakdown); err != nil {
		t.Fatal(err)
	}
	if _, ok := breakdown["costBasis"]; !ok {
		t.Errorf("breakdown JSON has no costBasis key: %s", b)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(breakdown["byModel"], &rows); err != nil {
		t.Fatal(err)
	}
	if _, ok := rows[0]["costBasis"]; !ok {
		t.Errorf("model row JSON has no costBasis key: %s", b)
	}

	// A caller that forgot to classify anything must still produce a legal
	// value: the schema's enum has no empty string.
	forgotten := BuildSnapshot(Input{Now: time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)})
	if forgotten.Breakdown.CostBasis != CostBasisUnavailable {
		t.Errorf("unset basis = %q, want %q", forgotten.Breakdown.CostBasis, CostBasisUnavailable)
	}
	if !strings.Contains(forgotten.Breakdown.CostBasis.Legend(), "no basis") {
		t.Errorf("unset basis legend = %q", forgotten.Breakdown.CostBasis.Legend())
	}
}

// TestRenderCompactCarriesTheBasis: the smallest surface in the tool must not
// print a bare dollar figure. The tag is attached to the number so that a reader
// who copies one figure out of a statusline still has its unit.
func TestRenderCompactCarriesTheBasis(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	cases := map[CostBasis]string{
		CostBasisACU:           "[acu]",
		CostBasisTokenEstimate: "[token est]",
		CostBasisMixed:         "[mixed units]",
		CostBasisUnavailable:   "[no basis]",
	}
	for basis, tag := range cases {
		snap := BuildSnapshot(Input{
			Now: now, TodayCost: 1.23, TotalSessions: 19, TotalRequests: 29249,
			CostBasis: basis,
		})
		got := RenderCompact(snap)
		if !strings.Contains(got, "$1.23 "+tag) {
			t.Errorf("basis %q: RenderCompact = %q, want it to contain %q", basis, got, "$1.23 "+tag)
		}
		assertSingleLine(t, got)
	}
}
