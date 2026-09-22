package reader

import (
	"sort"
	"sync"

	"github.com/garywhat/devinmonitor/internal/model"
)

// LiveReader wraps a Reader and serves the session list for the real-time live
// dashboard without re-loading every session's full message history on every
// poll.
//
// A full Sessions() on a large session database can take seconds because it
// loads and JSON-decodes every message node. The live dashboard polls
// frequently (e.g. every 500ms), which makes a full reload each tick feel
// non-realtime. LiveReader solves this with a cheap per-session fingerprint
// (message count + max node_id): on each poll it re-fingerprints all sessions
// (~ms) and only re-loads messages for sessions whose fingerprint changed. When
// nothing changes, a poll is effectively free.
type LiveReader struct {
	inner Reader

	mu           sync.Mutex
	fingerprints map[string]sessionFingerprint // sessionID -> fingerprint
	sessions     []model.Session               // cached, newest-first
	loaded       bool
}

type sessionFingerprint struct {
	count   int
	maxNode int
}

// NewLiveReader wraps r with incremental session caching.
func NewLiveReader(r Reader) *LiveReader {
	return &LiveReader{inner: r, fingerprints: map[string]sessionFingerprint{}}
}

// innerType is the concrete session reader the fingerprint query needs.
// We assert to *v1Reader at runtime and fall back to a full reload otherwise.
func (lr *LiveReader) fingerprintAll() (map[string]sessionFingerprint, error) {
	v, ok := lr.inner.(*v1Reader)
	if !ok {
		// Schema unknown to us; signal "reload everything" by returning empty.
		return nil, nil
	}
	rows, err := v.db.Query(
		`SELECT session_id, COUNT(*), MAX(node_id) FROM message_nodes GROUP BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sessionFingerprint{}
	for rows.Next() {
		var sid string
		var fp sessionFingerprint
		if err := rows.Scan(&sid, &fp.count, &fp.maxNode); err != nil {
			return nil, err
		}
		out[sid] = fp
	}
	return out, rows.Err()
}

// Sessions returns the session list, re-loading only sessions whose message
// set changed since the last call. Sessions that changed (or are new) are fully
// re-loaded and re-aggregated; unchanged ones are served from cache.
func (lr *LiveReader) Sessions() ([]model.Session, error) {
	// Cheap fingerprints for every session.
	fps, err := lr.fingerprintAll()
	if err != nil {
		return nil, err
	}
	if fps == nil {
		// Not a schema we can fingerprint cheaply: fall back to full reload.
		return lr.inner.Sessions()
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()

	changed := map[string]bool{}
	if !lr.loaded {
		// First poll: we must load everything.
		for id := range fps {
			changed[id] = true
		}
	} else {
		for id, fp := range fps {
			if old, ok := lr.fingerprints[id]; !ok || old != fp {
				changed[id] = true
			}
		}
		// Sessions that disappeared from the DB (deleted) become stale; drop them.
	}

	if !lr.loaded {
		// Full load path.
		all, err := lr.inner.Sessions()
		if err != nil {
			return nil, err
		}
		lr.sessions = all
		lr.loaded = true
		for i := range all {
			id := all[i].ID
			if fp, ok := fps[id]; ok {
				lr.fingerprints[id] = fp
			}
		}
		return lr.sessions, nil
	}

	if len(changed) == 0 {
		// Nothing changed; serve the cache. Fast path (~ms).
		return lr.sessions, nil
	}

	// Reload only the sessions whose fingerprints changed. Build a working
	// set of session ids to keep, dropping any that are no longer present.
	keep := map[string]bool{}
	for id := range fps {
		keep[id] = true
	}
	var kept []model.Session
	for _, s := range lr.sessions {
		if keep[s.ID] {
			kept = append(kept, s)
		}
	}
	lr.sessions = kept

	byID := map[string]model.Session{}
	for _, s := range lr.sessions {
		byID[s.ID] = s
	}
	for id := range changed {
		if !keep[id] {
			continue
		}
		s, err := lr.inner.Session(id)
		if err != nil {
			// A session we couldn't reload (e.g. transient); keep the old one.
			continue
		}
		byID[id] = *s
		lr.fingerprints[id] = fps[id]
	}
	// Rebuild the slice (order is not guaranteed by the map iteration).
	out := make([]model.Session, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastActivityAt.After(out[j].LastActivityAt)
	})
	lr.sessions = out
	return out, nil
}

// Session delegates directly to the inner reader.
func (lr *LiveReader) Session(id string) (*model.Session, error) { return lr.inner.Session(id) }

// SchemaVersion delegates to the inner reader.
func (lr *LiveReader) SchemaVersion() int { return lr.inner.SchemaVersion() }

// DBPath delegates to the inner reader.
func (lr *LiveReader) DBPath() string { return lr.inner.DBPath() }

// Close delegates to the inner reader.
func (lr *LiveReader) Close() error { return lr.inner.Close() }

// compile-time check that LiveReader satisfies Reader.
var _ Reader = (*LiveReader)(nil)
