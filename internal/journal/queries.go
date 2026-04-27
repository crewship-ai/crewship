package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Query filters List and Stream reads. Zero fields are omitted from the
// resulting WHERE clause so "give me everything in workspace W" is just
// Query{WorkspaceID: "W"}. Limit defaults to 100 when zero; the handler
// validates against an upper bound (1000) before calling in.
type Query struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	Types       []EntryType
	Severities  []Severity
	Priorities  []Priority // empty → any priority; use to surface only 'permanent'+'high'+'pin' etc.
	Since       time.Time
	Until       time.Time
	Cursor      string // opaque — set from a prior page's last entry ID+TS
	Limit       int
}

// List returns a page of entries matching q, newest first. Pagination is
// keyset: the caller passes the last page's final (ts, id) back as Cursor
// and gets rows strictly older. Offset-based paging is avoided so deep
// pagination stays O(log n).
func List(ctx context.Context, db *sql.DB, q Query) ([]Entry, string, error) {
	if q.WorkspaceID == "" {
		return nil, "", fmt.Errorf("journal: List requires workspace_id")
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}

	var (
		conds []string
		args  []any
	)
	conds = append(conds, "workspace_id = ?")
	args = append(args, q.WorkspaceID)

	if q.CrewID != "" {
		conds = append(conds, "crew_id = ?")
		args = append(args, q.CrewID)
	}
	if q.AgentID != "" {
		conds = append(conds, "agent_id = ?")
		args = append(args, q.AgentID)
	}
	if q.MissionID != "" {
		conds = append(conds, "mission_id = ?")
		args = append(args, q.MissionID)
	}
	if len(q.Types) > 0 {
		placeholders := strings.Repeat("?,", len(q.Types))
		placeholders = placeholders[:len(placeholders)-1]
		conds = append(conds, "entry_type IN ("+placeholders+")")
		for _, t := range q.Types {
			args = append(args, string(t))
		}
	}
	if len(q.Severities) > 0 {
		placeholders := strings.Repeat("?,", len(q.Severities))
		placeholders = placeholders[:len(placeholders)-1]
		conds = append(conds, "severity IN ("+placeholders+")")
		for _, s := range q.Severities {
			args = append(args, string(s))
		}
	}
	if len(q.Priorities) > 0 {
		placeholders := strings.Repeat("?,", len(q.Priorities))
		placeholders = placeholders[:len(placeholders)-1]
		conds = append(conds, "priority IN ("+placeholders+")")
		for _, p := range q.Priorities {
			args = append(args, string(p))
		}
	}
	if !q.Since.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, q.Since.UTC().Format(time.RFC3339Nano))
	}
	if !q.Until.IsZero() {
		conds = append(conds, "ts <= ?")
		args = append(args, q.Until.UTC().Format(time.RFC3339Nano))
	}
	if q.Cursor != "" {
		cTS, cID, err := decodeCursor(q.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("journal: bad cursor: %w", err)
		}
		// Strict < on (ts, id) keeps pagination monotonic when many entries
		// share a millisecond timestamp.
		conds = append(conds, "(ts < ? OR (ts = ? AND id < ?))")
		args = append(args, cTS, cTS, cID)
	}

	query := `SELECT id, workspace_id, crew_id, agent_id, mission_id, ts, entry_type,
		severity, priority, actor_type, actor_id, summary, payload, refs, trace_id, span_id, expires_at
		FROM journal_entries
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY ts DESC, id DESC
		LIMIT ?`
	args = append(args, q.Limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("journal: list: %w", err)
	}
	defer rows.Close()

	out := make([]Entry, 0, q.Limit)
	for rows.Next() {
		var (
			e                                                  Entry
			crewID, agentID, missionID, actorID                sql.NullString
			traceID, spanID, expires                           sql.NullString
			payloadStr, refsStr, tsStr, sev, prio, actor, kind string
		)
		if err := rows.Scan(
			&e.ID,
			&e.WorkspaceID,
			&crewID,
			&agentID,
			&missionID,
			&tsStr,
			&kind,
			&sev,
			&prio,
			&actor,
			&actorID,
			&e.Summary,
			&payloadStr,
			&refsStr,
			&traceID,
			&spanID,
			&expires,
		); err != nil {
			return nil, "", fmt.Errorf("journal: scan: %w", err)
		}
		e.CrewID = crewID.String
		e.AgentID = agentID.String
		e.MissionID = missionID.String
		e.ActorID = actorID.String
		e.TraceID = traceID.String
		e.SpanID = spanID.String
		e.Type = EntryType(kind)
		e.Severity = Severity(sev)
		e.Priority = Priority(prio)
		e.ActorType = ActorType(actor)
		if ts, err := parseJournalTS(tsStr); err == nil {
			e.TS = ts
		}
		if expires.Valid {
			if t, err := parseJournalTS(expires.String); err == nil {
				e.ExpiresAt = &t
			}
		}
		if payloadStr != "" && payloadStr != "{}" {
			_ = json.Unmarshal([]byte(payloadStr), &e.Payload)
		}
		if refsStr != "" && refsStr != "{}" {
			_ = json.Unmarshal([]byte(refsStr), &e.Refs)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(out) == q.Limit {
		last := out[len(out)-1]
		nextCursor = encodeCursor(last.TS, last.ID)
	}
	return out, nextCursor, nil
}

// Get fetches a single entry by ID scoped to a workspace. The workspace
// scope is enforced here rather than at the handler so no cross-tenant
// leaks are possible from an API bug upstream.
func Get(ctx context.Context, db *sql.DB, workspaceID, id string) (*Entry, error) {
	// Direct indexed lookup by (workspace_id, id). A prior version ran
	// a List(Limit:1) first as a "fast path" but that list has no ID
	// filter, so it returns the most recent row in the workspace, not
	// the requested one — which means the fallback below always ran,
	// the List call was pure waste, and CodeRabbit flagged it.
	row := db.QueryRowContext(ctx, `SELECT id, workspace_id, crew_id, agent_id, mission_id, ts,
		entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id, span_id, expires_at
		FROM journal_entries WHERE workspace_id = ? AND id = ?`, workspaceID, id)
	var (
		e                                                  Entry
		crewID, agentID, missionID, actorID                sql.NullString
		traceID, spanID, expires                           sql.NullString
		payloadStr, refsStr, tsStr, sev, prio, actor, kind string
	)
	if err := row.Scan(
		&e.ID, &e.WorkspaceID, &crewID, &agentID, &missionID, &tsStr,
		&kind, &sev, &prio, &actor, &actorID, &e.Summary, &payloadStr, &refsStr,
		&traceID, &spanID, &expires,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.CrewID = crewID.String
	e.AgentID = agentID.String
	e.MissionID = missionID.String
	e.ActorID = actorID.String
	e.TraceID = traceID.String
	e.SpanID = spanID.String
	e.Type = EntryType(kind)
	e.Severity = Severity(sev)
	e.Priority = Priority(prio)
	e.ActorType = ActorType(actor)
	if ts, err := parseJournalTS(tsStr); err == nil {
		e.TS = ts
	}
	if expires.Valid {
		if t, err := parseJournalTS(expires.String); err == nil {
			e.ExpiresAt = &t
		}
	}
	if payloadStr != "" && payloadStr != "{}" {
		_ = json.Unmarshal([]byte(payloadStr), &e.Payload)
	}
	if refsStr != "" && refsStr != "{}" {
		_ = json.Unmarshal([]byte(refsStr), &e.Refs)
	}
	return &e, nil
}

// Count returns the total number of entries matching q. The handler uses
// it to render "N events" badges without paging the full result set.
func Count(ctx context.Context, db *sql.DB, q Query) (int64, error) {
	if q.WorkspaceID == "" {
		return 0, fmt.Errorf("journal: Count requires workspace_id")
	}
	var (
		conds = []string{"workspace_id = ?"}
		args  = []any{q.WorkspaceID}
	)
	if q.CrewID != "" {
		conds = append(conds, "crew_id = ?")
		args = append(args, q.CrewID)
	}
	if q.AgentID != "" {
		conds = append(conds, "agent_id = ?")
		args = append(args, q.AgentID)
	}
	if q.MissionID != "" {
		conds = append(conds, "mission_id = ?")
		args = append(args, q.MissionID)
	}
	// Mirror List's Type / Severity / Until filters so Count matches
	// the result set you'd actually paginate through. Previously Count
	// ignored these, so filtered UI views would show a total that
	// didn't line up with the rows the user could see.
	if len(q.Types) > 0 {
		placeholders := strings.Repeat("?,", len(q.Types))
		placeholders = placeholders[:len(placeholders)-1]
		conds = append(conds, "entry_type IN ("+placeholders+")")
		for _, t := range q.Types {
			args = append(args, string(t))
		}
	}
	if len(q.Severities) > 0 {
		placeholders := strings.Repeat("?,", len(q.Severities))
		placeholders = placeholders[:len(placeholders)-1]
		conds = append(conds, "severity IN ("+placeholders+")")
		for _, s := range q.Severities {
			args = append(args, string(s))
		}
	}
	if len(q.Priorities) > 0 {
		placeholders := strings.Repeat("?,", len(q.Priorities))
		placeholders = placeholders[:len(placeholders)-1]
		conds = append(conds, "priority IN ("+placeholders+")")
		for _, p := range q.Priorities {
			args = append(args, string(p))
		}
	}
	if !q.Since.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, q.Since.UTC().Format(time.RFC3339Nano))
	}
	if !q.Until.IsZero() {
		conds = append(conds, "ts <= ?")
		args = append(args, q.Until.UTC().Format(time.RFC3339Nano))
	}
	query := `SELECT COUNT(*) FROM journal_entries WHERE ` + strings.Join(conds, " AND ")
	var n int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// encodeCursor packs (ts, id) into an opaque string the caller echoes
// back. Format intentionally opaque so we can migrate to a different
// pagination scheme without breaking API consumers.
func encodeCursor(ts time.Time, id string) string {
	return ts.UTC().Format("2006-01-02T15:04:05.000Z") + "|" + id
}

func decodeCursor(s string) (string, string, error) {
	i := strings.IndexByte(s, '|')
	if i < 0 {
		return "", "", fmt.Errorf("missing separator")
	}
	return s[:i], s[i+1:], nil
}

// parseJournalTS accepts both the milli-precision format we write and the
// second-precision format SQLite's built-in datetime('now') produces, so
// rows inserted by migration backfill code still parse cleanly.
func parseJournalTS(s string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("journal: unparseable timestamp %q", s)
}
