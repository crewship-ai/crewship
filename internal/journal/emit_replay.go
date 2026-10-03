package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrReplayConflict = errors.New("journal replay identity conflicts with stored content")

// EmitReplaySync durably records a replay event once. Callers supply stable
// identity, timestamp and trace/span, and scrub payloads before calling it.
// A repeated identity is accepted only when every immutable field agrees.
// The bool reports a new insertion; false does not re-run commit observers.
// Deduplication requires the original journal row to remain retained. A
// replay consumer must pin its entries or keep durable receipts until its
// source cursor is acknowledged; normal journal retention is not a receipt.
// This is not an outbox for external effects: a crash after commit can still
// interrupt best-effort observer delivery. It also does not authorize a scope.
func (w *Writer) EmitReplaySync(ctx context.Context, e Entry) (string, bool, error) {
	if e.ID == "" || e.TS.IsZero() || e.TraceID == "" || e.SpanID == "" {
		return "", false, errors.New("journal replay requires stable id, timestamp, trace and span")
	}
	e, err := prepareEntry(ctx, e)
	if err != nil {
		return "", false, err
	}
	exists, err := w.matchReplayEntry(ctx, e)
	if err != nil {
		return "", false, err
	}
	if exists {
		return e.ID, false, nil
	}
	if err = w.persistOne(ctx, e); err != nil {
		// Another caller can win between lookup and insertion. The primary key
		// makes the collision atomic; only matching committed content counts.
		if exists, matchErr := w.matchReplayEntry(ctx, e); matchErr == nil && exists {
			return e.ID, false, nil
		} else if matchErr != nil {
			return "", false, errors.Join(err, matchErr)
		}
		return "", false, err
	}
	w.afterCommit([]Entry{e})
	return e.ID, true, nil
}

func (w *Writer) matchReplayEntry(ctx context.Context, e Entry) (bool, error) {
	payload, err := e.payloadJSON()
	if err != nil {
		return false, err
	}
	refs, err := e.refsJSON()
	if err != nil {
		return false, err
	}
	expires := ""
	if e.ExpiresAt != nil {
		expires = e.ExpiresAt.UTC().Format(time.RFC3339Nano) // tsformat:allow: exact comparison with journal persistBatch's existing expires_at serialization
	}
	// Compare the stored JSON directly, without decoding numbers through
	// float64. Large integer counters must survive retry without precision loss.
	var matches bool
	err = w.db.QueryRowContext(ctx, `SELECT
		workspace_id=? AND COALESCE(crew_id,'')=? AND COALESCE(agent_id,'')=?
		AND COALESCE(mission_id,'')=? AND ts=? AND entry_type=? AND severity=?
		AND COALESCE(priority_at_emit,'')=? AND actor_type=? AND COALESCE(actor_id,'')=?
		AND summary=? AND payload=? AND refs=? AND COALESCE(trace_id,'')=?
		AND COALESCE(span_id,'')=? AND COALESCE(expires_at,'')=?
		FROM journal_entries WHERE id=?`,
		e.WorkspaceID, e.CrewID, e.AgentID, e.MissionID, e.TS.UTC().Format("2006-01-02T15:04:05.000Z"),
		string(e.Type), string(e.Severity), priorityOrNormal(e.Priority), string(e.ActorType), e.ActorID,
		e.Summary, payload, refs, e.TraceID, e.SpanID, expires, e.ID).Scan(&matches)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("journal replay lookup: %w", err)
	}
	if !matches {
		return false, ErrReplayConflict
	}
	return true, nil
}
