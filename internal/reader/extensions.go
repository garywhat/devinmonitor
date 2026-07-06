// Package reader — extension methods for the 98-feature implementation.
// These add search, prompt history, rendered commits, and filtering
// capabilities on top of the existing Reader interface.
package reader

import (
	"fmt"
	"strings"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// SearchMessages performs full-text search across all session messages.
// Returns matching snippets sorted by recency.
func (r *v1Reader) SearchMessages(query string, limit int) ([]model.SearchResult, error) {
	if limit <= 0 {
		limit = 50
	}
	q := "%" + query + "%"
	rows, err := r.db.Query(`
		SELECT session_id, node_id,
		       json_extract(chat_message, '$.role'),
		       substr(json_extract(chat_message, '$.content'), 1, 200),
		       created_at
		FROM message_nodes
		WHERE chat_message LIKE ?
		ORDER BY created_at DESC
		LIMIT ?`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	var out []model.SearchResult
	for rows.Next() {
		var sr model.SearchResult
		var ts int64
		if err := rows.Scan(&sr.SessionID, &sr.NodeID, &sr.Role, &sr.Snippet, &ts); err != nil {
			return nil, err
		}
		sr.Timestamp = tsToTime(ts)
		out = append(out, sr)
	}
	return out, rows.Err()
}

// PromptHistory returns the prompt history for a session (or all if sessionID is empty).
func (r *v1Reader) PromptHistory(sessionID string) ([]model.PromptHistoryEntry, error) {
	q := `SELECT id, content, timestamp, session_id, is_shell FROM prompt_history`
	args := []interface{}{}
	if sessionID != "" {
		q += ` WHERE session_id = ?`
		args = append(args, sessionID)
	}
	q += ` ORDER BY timestamp ASC`
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("prompt history: %w", err)
	}
	defer rows.Close()

	var out []model.PromptHistoryEntry
	for rows.Next() {
		var e model.PromptHistoryEntry
		var ts int64
		if err := rows.Scan(&e.ID, &e.Content, &ts, &e.SessionID, &e.IsShell); err != nil {
			return nil, err
		}
		e.Timestamp = tsToTime(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// RenderedCommits returns rendered commit HTML for a session.
func (r *v1Reader) RenderedCommits(sessionID string) ([]model.RenderedCommit, error) {
	rows, err := r.db.Query(`
		SELECT id, session_id, sequence_number, rendered_html, created_at
		FROM rendered_commits WHERE session_id = ?
		ORDER BY sequence_number ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("rendered commits: %w", err)
	}
	defer rows.Close()

	var out []model.RenderedCommit
	for rows.Next() {
		var rc model.RenderedCommit
		var ts int64
		if err := rows.Scan(&rc.ID, &rc.SessionID, &rc.SequenceNumber, &rc.HTML, &ts); err != nil {
			return nil, err
		}
		rc.CreatedAt = tsToTime(ts)
		out = append(out, rc)
	}
	return out, rows.Err()
}

// ToolCallStates returns tool call state records for a session.
func (r *v1Reader) ToolCallStates(sessionID string) ([]model.ToolCallStateEntry, error) {
	rows, err := r.db.Query(`
		SELECT session_id, tool_call_id, tool_call_json, tool_call_update_json
		FROM tool_call_state WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("tool call state: %w", err)
	}
	defer rows.Close()

	var out []model.ToolCallStateEntry
	for rows.Next() {
		var e model.ToolCallStateEntry
		if err := rows.Scan(&e.SessionID, &e.ToolCallID, &e.ToolCallJSON, &e.ToolCallUpdateJSON); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppState returns all app_state key-value pairs.
func (r *v1Reader) AppState() (map[string]string, error) {
	rows, err := r.db.Query(`SELECT key, value FROM app_state`)
	if err != nil {
		return nil, fmt.Errorf("app state: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SessionCount returns the total number of non-hidden sessions.
func (r *v1Reader) SessionCount() (int, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE hidden = 0`).Scan(&n)
	return n, err
}

// MessageCount returns the total number of messages across all sessions.
func (r *v1Reader) MessageCount() (int, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM message_nodes`).Scan(&n)
	return n, err
}

// FilteredSessions returns sessions matching the given filter options.
func (r *v1Reader) FilteredSessions(opts model.FilterOptions) ([]model.Session, error) {
	ss, err := r.Sessions()
	if err != nil {
		return nil, err
	}
	var out []model.Session
	for _, s := range ss {
		if s.Hidden {
			continue
		}
		if opts.Model != "" && !strings.Contains(s.LatestModel, opts.Model) && !strings.Contains(s.Model, opts.Model) {
			continue
		}
		if opts.Project != "" && !strings.Contains(s.WorkingDir, opts.Project) {
			continue
		}
		if opts.Mode != "" && s.AgentMode != opts.Mode {
			continue
		}
		if !opts.FromDate.IsZero() && s.CreatedAt.Before(opts.FromDate) {
			continue
		}
		if !opts.ToDate.IsZero() && s.CreatedAt.After(opts.ToDate) {
			continue
		}
		if opts.SearchText != "" {
			found := false
			for _, m := range s.Messages {
				if strings.Contains(strings.ToLower(m.Content), strings.ToLower(opts.SearchText)) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// tsToTime converts a timestamp to time.Time, auto-detecting the unit.
// message_nodes.created_at is nanoseconds; sessions timestamps are seconds.
func tsToTime(ts int64) time.Time {
	switch {
	case ts > 1e18:
		return time.Unix(ts/1e9, ts%1e9) // nanoseconds
	case ts > 1e15:
		return time.Unix(ts/1e3, (ts%1e3)*1e6) // milliseconds
	default:
		return time.Unix(ts, 0) // seconds
	}
}
