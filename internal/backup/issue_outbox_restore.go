package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"time"
)

// Replay MAC receipts are installation-local runtime state, not backup data.
// A lost-ACK journal projection can nevertheless already be in the bundle.
// Acknowledge only exact immutable tuples, never approximate text/ID matches.
func reconcileRestoredIssueOutbox(ctx context.Context, tx *sql.Tx) error {
	exists, err := tableExistsTx(ctx, tx, "issue_mutation_outbox")
	if err != nil || !exists {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,workspace_id,mission_id,payload FROM issue_mutation_outbox WHERE kind='journal' AND state<>'done'`)
	if err != nil {
		return err
	}
	type candidate struct{ id, workspace, mission, raw string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.workspace, &c.mission, &c.raw); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		var p struct {
			Journal *struct {
				ID, WorkspaceID, CrewID, AgentID, MissionID                            string
				TS                                                                     time.Time
				Type, Severity, Priority, ActorType, ActorID, Summary, TraceID, SpanID string
				Payload, Refs                                                          json.RawMessage
				ExpiresAt                                                              *time.Time
			} `json:"journal"`
		}
		if json.Unmarshal([]byte(c.raw), &p) != nil || p.Journal == nil {
			continue
		}
		e := p.Journal
		if e.ID == "" || e.WorkspaceID != c.workspace || e.MissionID != c.mission {
			continue
		}
		var crew, agent, ts, kind, severity, priority, actor, actorID, summary, payload, refs, trace, span, expires string
		// Since v167 journal targets are historical TEXT, not live foreign keys.
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(crew_id,''),COALESCE(agent_id,''),ts,entry_type,severity,priority,actor_type,COALESCE(actor_id,''),summary,payload,refs,COALESCE(trace_id,''),COALESCE(span_id,''),COALESCE(expires_at,'') FROM journal_entries WHERE id=? AND workspace_id=? AND mission_id=?`, e.ID, c.workspace, c.mission).Scan(&crew, &agent, &ts, &kind, &severity, &priority, &actor, &actorID, &summary, &payload, &refs, &trace, &span, &expires)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		storedTS, parseErr := time.Parse(time.RFC3339Nano, ts) // tsformat:allow: compare signed journal instants without truncating nanoseconds or rewriting stored audit timestamps
		expectedPriority := e.Priority
		if expectedPriority == "" {
			expectedPriority = "normal"
		}
		if parseErr != nil || !storedTS.Equal(e.TS) || crew != e.CrewID || agent != e.AgentID || kind != e.Type || severity != e.Severity || priority != expectedPriority || actor != e.ActorType || actorID != e.ActorID || summary != e.Summary || trace != e.TraceID || span != e.SpanID || !sameOutboxJSON([]byte(payload), e.Payload) || !sameOutboxJSON([]byte(refs), e.Refs) {
			continue
		}
		if e.ExpiresAt == nil {
			if expires != "" {
				continue
			}
		} else {
			actual, err := time.Parse(time.RFC3339Nano, expires) // tsformat:allow: exact signed journal expiry comparison must preserve source precision
			if err != nil || !actual.Equal(*e.ExpiresAt) {
				continue
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE issue_mutation_outbox SET state='done',payload='{}',failure_code='',failed_at=NULL,completed_at=COALESCE(completed_at,created_at),lease_token=NULL,lease_expires_at=NULL,last_error='' WHERE id=?`, c.id); err != nil {
			return err
		}
	}
	return nil
}
func sameOutboxJSON(a, b []byte) bool {
	normalize := func(raw []byte) ([]byte, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		return json.Marshal(value)
	}
	x, err := normalize(a)
	if err != nil {
		return false
	}
	y, err := normalize(b)
	return err == nil && bytes.Equal(x, y)
}
