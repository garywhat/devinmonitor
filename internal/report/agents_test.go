package report

import (
	"reflect"
	"testing"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// agentFixtureSession builds a session whose sub-agent calls give it a
// predictable dominant profile: `exploreCalls` calls of profile "explore" and
// `generalCalls` of profile "general".
func agentFixtureSession(id string, exploreCalls, generalCalls int) model.Session {
	s := model.Session{
		ID:             id,
		CreatedAt:      time.Unix(1700000000, 0),
		LastActivityAt: time.Unix(1700000600, 0),
	}
	for i := 0; i < exploreCalls; i++ {
		s.SubAgentCalls = append(s.SubAgentCalls, model.SubAgentCall{Profile: "explore", Task: "t"})
	}
	for i := 0; i < generalCalls; i++ {
		s.SubAgentCalls = append(s.SubAgentCalls, model.SubAgentCall{Profile: "general", Task: "t"})
	}
	return s
}

// withoutHeads zeroes the chain-head fields so a row built with heads can be
// compared against one built without them, proving the enrichment adds nothing
// to the pre-existing columns.
func withoutHeads(st AgentStats) AgentStats {
	st.ChainHeads = 0
	st.ChainHeadNodes = nil
	return st
}

func rowFor(t *testing.T, stats []AgentStats, profile string) AgentStats {
	t.Helper()
	for _, st := range stats {
		if st.Profile == profile {
			return st
		}
	}
	t.Fatalf("no row for profile %q in %+v", profile, stats)
	return AgentStats{}
}

// TestBuildAgentStatsWithHeadsCarriesHeadsOnly is the core guarantee of the
// enrichment: heads reach the row of the session's dominant profile, and every
// pre-existing field is byte-for-byte what it was without heads.
func TestBuildAgentStatsWithHeadsCarriesHeadsOnly(t *testing.T) {
	ss := []model.Session{agentFixtureSession("s1", 2, 1)}
	heads := map[string][]AgentChainHead{
		"s1": {{AgentID: "a2", ChainNodeID: 102}, {AgentID: "a1", ChainNodeID: 101}},
	}

	baseline := BuildAgentStats(ss)
	enriched := BuildAgentStatsWithHeads(ss, heads)

	if len(baseline) != len(enriched) {
		t.Fatalf("row count changed: %d without heads, %d with", len(baseline), len(enriched))
	}
	if !reflect.DeepEqual(baseline, []AgentStats{withoutHeads(rowFor(t, enriched, "explore")), withoutHeads(rowFor(t, enriched, "general"))}) {
		t.Errorf("existing values changed when heads were passed:\n got %+v\nwant %+v", enriched, baseline)
	}

	explore := rowFor(t, enriched, "explore")
	if explore.ChainHeads != 2 {
		t.Errorf("explore.ChainHeads = %d, want 2", explore.ChainHeads)
	}
	if !reflect.DeepEqual(explore.ChainHeadNodes, []int{102, 101}) {
		t.Errorf("explore.ChainHeadNodes = %v, want [102 101] (read order preserved)", explore.ChainHeadNodes)
	}
	if g := rowFor(t, enriched, "general"); g.ChainHeads != 0 || g.ChainHeadNodes != nil {
		t.Errorf("general row carries heads from the dominant profile: %+v", g)
	}

	if !HasChainHeads(enriched) {
		t.Error("HasChainHeads(enriched) = false, want true (the optional column must appear)")
	}
	if HasChainHeads(baseline) {
		t.Error("HasChainHeads(baseline) = true, want false (the column must be absent on current data)")
	}
}

// TestBuildAgentStatsWithHeadsNoHeadsIsInert pins today's behaviour: no head
// records means BuildAgentStatsWithHeads is exactly BuildAgentStats, so the
// `agents` output is unchanged on every database seen so far.
func TestBuildAgentStatsWithHeadsNoHeadsIsInert(t *testing.T) {
	ss := []model.Session{
		agentFixtureSession("s1", 2, 1),
		agentFixtureSession("s2", 0, 3),
	}
	baseline := BuildAgentStats(ss)
	for _, heads := range []map[string][]AgentChainHead{nil, {}} {
		got := BuildAgentStatsWithHeads(ss, heads)
		if !reflect.DeepEqual(got, baseline) {
			t.Errorf("BuildAgentStatsWithHeads(ss, %v) = %+v, want %+v", heads, got, baseline)
		}
		if HasChainHeads(got) {
			t.Error("HasChainHeads = true with no heads recorded; the column would appear empty")
		}
	}
}

// TestBuildAgentStatsWithHeadsIsSessionScoped checks that heads belonging to a
// session outside the report are not credited to any row.
func TestBuildAgentStatsWithHeadsIsSessionScoped(t *testing.T) {
	ss := []model.Session{agentFixtureSession("s1", 2, 1)}
	heads := map[string][]AgentChainHead{
		"s-elsewhere": {{AgentID: "z", ChainNodeID: 7}},
	}
	got := BuildAgentStatsWithHeads(ss, heads)
	if HasChainHeads(got) {
		t.Errorf("heads for another session were credited: %+v", got)
	}
}

// TestBuildAgentStatsHeadsWithoutSubagentCalls documents the one case heads can
// be lost: a session with recorded heads but no parsed run_subagent call has no
// dominant profile, so it produces no row at all and its heads surface nowhere.
// It is asserted rather than silently assumed so the limitation stays visible.
func TestBuildAgentStatsHeadsWithoutSubagentCalls(t *testing.T) {
	ss := []model.Session{agentFixtureSession("s1", 0, 0)}
	heads := map[string][]AgentChainHead{"s1": {{AgentID: "a1", ChainNodeID: 5}}}

	got := BuildAgentStatsWithHeads(ss, heads)
	if len(got) != 0 {
		t.Errorf("got %d rows for a session with no sub-agent calls, want 0: %+v", len(got), got)
	}
	if HasChainHeads(got) {
		t.Error("HasChainHeads = true for heads that could not be attributed")
	}
}

// TestBuildAgentStatsHeadTieBreakIsDeterministic covers profiles with equal call
// counts: the heads must land on one stable profile, not on whichever the map
// iteration happened to visit. Repeated to catch map-order dependence.
func TestBuildAgentStatsHeadTieBreakIsDeterministic(t *testing.T) {
	for i := 0; i < 50; i++ {
		ss := []model.Session{agentFixtureSession("s1", 1, 1)}
		heads := map[string][]AgentChainHead{"s1": {{AgentID: "a1", ChainNodeID: 9}}}
		got := BuildAgentStatsWithHeads(ss, heads)
		if explore := rowFor(t, got, "explore"); explore.ChainHeads != 1 {
			t.Fatalf("iteration %d: explore.ChainHeads = %d, want 1 (name-ordered tie-break)", i, explore.ChainHeads)
		}
		if general := rowFor(t, got, "general"); general.ChainHeads != 0 {
			t.Fatalf("iteration %d: general.ChainHeads = %d, want 0", i, general.ChainHeads)
		}
	}
}
