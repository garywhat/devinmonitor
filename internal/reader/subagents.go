// Package reader — sub-agent chain heads from the v17 `subagent_heads` table.
//
// This file adds one narrow, version-gated read. It deliberately stops at the
// column names: nothing here interprets what a "chain" or a "head" means,
// because there is no data to check such a reading against. Devin writes this
// table only once it starts tracking chain heads, and on every database
// observed so far it is empty, so an empty result is the normal case rather
// than evidence that no sub-agent ran.
package reader

import (
	"fmt"
	"strings"
	"time"
)

// subagentHeadsSchema is the refinery schema version that introduced the
// `subagent_heads` table. reader.go records the same fact on MaxSupportedSchema
// ("v17 turned out to be additive — a new `subagent_heads` table"), and the
// table is absent in every earlier version.
const subagentHeadsSchema = 17

// SubagentHead records which message node is the current head of a subagent's
// chain, as tracked by the v17 `subagent_heads` table.
//
// One row per (SessionID, AgentID) — that pair is the table's primary key.
// ChainNodeID is only ever the column's own name: the node_id the chain for
// that agent currently heads at. Whether it is a message_nodes.node_id, and
// what "head" implies about earlier nodes, is NOT established by anything we
// can read, so no caller should assume more than the name says.
type SubagentHead struct {
	SessionID   string
	AgentID     string
	ChainNodeID int
	UpdatedAt   time.Time
}

// SubagentHeads returns the recorded chain heads for one session, ordered by
// agent_id — the primary key's second column, so the order is total (the key
// guarantees at most one row per agent) and stable across calls and tests.
// chain_node_id is kept as a secondary sort key only so the query states its
// intent; it cannot break a tie the primary key leaves open.
//
// Version-gated on r.ver, the version already resolved when the reader opened:
//
//   - Below v17 the table does not exist, and a v16 database must read as "no
//     heads recorded yet" rather than as an error. Checking r.ver costs no
//     query (subagent_heads is optional metadata, not a hot path) and the
//     version→table mapping is written down in this package already, so we do
//     not need to probe sqlite_master to learn it.
//   - As a second, narrower guard, SQLite's "no such table" error is tolerated
//     and also reads as empty. The table is additive: a database that reports
//     v17 but lacks it (a hand-built fixture, a partially applied migration)
//     should lose the chain-head column, not fail the whole `agents` report.
//     Every other error is returned — a genuinely broken database must not be
//     disguised as a session with no heads.
//
// The result is empty (never an error) when the table exists but holds no rows
// for this session, which is what every database observed so far returns.
func (r *v1Reader) SubagentHeads(sessionID string) ([]SubagentHead, error) {
	if r.ver < subagentHeadsSchema {
		return nil, nil
	}
	rows, err := r.db.Query(`
		SELECT session_id, agent_id, chain_node_id, updated_at
		FROM subagent_heads WHERE session_id = ?
		ORDER BY agent_id ASC, chain_node_id ASC`, sessionID)
	if err != nil {
		if isMissingTable(err, "subagent_heads") {
			return nil, nil
		}
		return nil, fmt.Errorf("query subagent heads: %w", err)
	}
	defer rows.Close()

	var out []SubagentHead
	for rows.Next() {
		var h SubagentHead
		var updatedAt int64
		if err := rows.Scan(&h.SessionID, &h.AgentID, &h.ChainNodeID, &updatedAt); err != nil {
			// A malformed value is reported, never guessed at, and never
			// panics: scanning a NULL or an out-of-range integer into these
			// fields returns an error from database/sql.
			return nil, fmt.Errorf("scan subagent head: %w", err)
		}
		// tsToTime (extensions.go) is the package's existing timestamp
		// convention and auto-detects seconds/ms/ns. We have no populated row
		// to confirm the unit of updated_at from, so we reuse it rather than
		// assuming seconds here: on the seconds case it is exact, and on
		// anything else it degrades to a plausible time instead of a ~1970 one.
		h.UpdatedAt = tsToTime(updatedAt)
		out = append(out, h)
	}
	return out, rows.Err()
}

// isMissingTable reports whether err is SQLite's "no such table: <name>".
//
// modernc.org/sqlite surfaces it as e.g.
// "SQL logic error: no such table: subagent_heads (1)", so we match on both
// halves instead of trusting the error type of one driver build.
func isMissingTable(err error, name string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such table") && strings.Contains(msg, strings.ToLower(name))
}
