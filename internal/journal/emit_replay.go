package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrReplayConflict = errors.New("journal replay identity conflicts with stored content")

// EmitReplaySync durably records a replay event once. Callers supply stable
// identity, timestamp and trace/span, and scrub payloads before calling it.
// A repeated identity is accepted only when every immutable field agrees.
// The bool reports a new insertion; false does not re-run commit observers.
// A receipt committed with the journal row preserves deduplication even if
// retention removes that row. Receipts must outlive the replay source.
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

	digest, err := replayDigest(e)
	if err != nil {
		return "", false, err
	}
	inserted := false
	w.writeMu.Lock()
	err = withTx(ctx, w.db, func(tx *sql.Tx) error {
		var prior, workspace, run string
		err := tx.QueryRowContext(ctx, `SELECT content_hash, workspace_id, run_id FROM journal_replay_receipts WHERE entry_id=?`, e.ID).Scan(&prior, &workspace, &run)
		if err == nil {
			if prior != digest || workspace != e.WorkspaceID || run != e.TraceID {
				return ErrReplayConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("journal replay receipt: %w", err)
		}
		// An existing journal row from the pre-receipt implementation must match
		// exactly before it can acquire a receipt. Never overwrite its identity.
		exists, err := matchReplayEntry(ctx, tx, e)
		if err != nil {
			return err
		}
		if !exists {
			if err = w.persistEntriesTx(ctx, tx, []Entry{e}); err != nil {
				return err
			}
			inserted = true
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO journal_replay_receipts(entry_id,workspace_id,run_id,content_hash) VALUES(?,?,?,?)`, e.ID, e.WorkspaceID, e.TraceID, digest)
		return err
	})
	w.writeMu.Unlock()
	if err != nil {
		return "", false, err
	}
	if inserted {
		w.afterCommit([]Entry{e})
	}
	return e.ID, inserted, nil
}

// The digest binds all immutable fields using their persisted precision. It
// stores no raw output or credentials and survives journal compaction.
func replayDigest(e Entry) (string, error) {
	e.TS = e.TS.UTC().Truncate(time.Millisecond)
	if len(e.Payload) == 0 {
		e.Payload = map[string]any{}
	}
	if len(e.Refs) == 0 {
		e.Refs = map[string]any{}
	}
	if e.ExpiresAt != nil {
		expiry := e.ExpiresAt.UTC()
		e.ExpiresAt = &expiry
	}
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type replayQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func matchReplayEntry(ctx context.Context, query replayQuery, e Entry) (bool, error) {
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
	err = query.QueryRowContext(ctx, `SELECT
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
