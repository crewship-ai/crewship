package documents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Session checkpoints make History show more than the initial revision.
// Individual edits arrive every few hundred milliseconds, so pinning each one
// would exhaust the History budget within minutes. Instead a commit pins its
// confirmed result when the newest checkpoint is older than
// sessionCheckpointInterval, or was written by someone else at least
// sessionCheckpointMinGap ago. Concurrent authors therefore pin at most once a
// minute, and one long session about every ten minutes.
//
// Session pins are best effort: they never refuse or roll back the edit. They
// write no receipt (so they do not use the operation admission window) and
// share at most half of MaxHistoryBytes, so explicit checkpoints and restores
// keep their headroom. Nothing here deletes a checkpoint: until a retention
// policy exists (R71 §0.11), a pin that does not fit in that half, or that the
// workspace storage budget or disk headroom refuses, is skipped and the edit
// commits unchanged. SessionHistoryFull reports the first case so History can
// tell people why no new automatic snapshots appear.
const (
	sessionCheckpointInterval = 10 * time.Minute
	sessionCheckpointMinGap   = time.Minute
	sessionCheckpointPrefix   = "session-"
)

// pinSession runs inside the commit transaction after the head was advanced to r.Rev.
// Its own work runs in a savepoint; any failure there is undone and ignored.
func (s *Store) pinSession(ctx context.Context, t *sql.Tx, k Key, r Receipt, c Commit, actor string) error {
	if !s.sessionPinDue(ctx, t, k, actor) {
		return nil
	}
	budget := s.opts.MaxHistoryBytes / 2
	need := len(c.CandidateSnapshot) + len(c.Projection)
	if need > budget {
		return nil
	}
	if _, err := t.ExecContext(ctx, "SAVEPOINT document_session_pin"); err != nil {
		return nil
	}
	if err := s.insertSessionPin(ctx, t, k, r, c, actor, need); err != nil {
		// If even the rollback fails (for example a cancelled context), the outer
		// transaction cannot commit either, so returning that error loses nothing.
		if _, rollbackErr := t.ExecContext(ctx, "ROLLBACK TO document_session_pin"); rollbackErr != nil {
			return rollbackErr
		}
	}
	_, err := t.ExecContext(ctx, "RELEASE document_session_pin")
	return err
}

// sessionPinDue reports whether the newest checkpoint is old enough. An
// unreadable or future timestamp counts as due, so History cannot stall on it.
func (s *Store) sessionPinDue(ctx context.Context, t *sql.Tx, k Key, actor string) bool {
	var lastActor string
	var ageSeconds sql.NullFloat64
	err := t.QueryRowContext(ctx, `SELECT actor,(julianday('now')-julianday(created_at))*86400.0 FROM document_checkpoints WHERE workspace_id=? AND page_id=? ORDER BY rev DESC,created_at DESC LIMIT 1`, k.WorkspaceID, k.PageID).Scan(&lastActor, &ageSeconds)
	if err != nil {
		// No checkpoint (documents created before initial checkpoints) starts History;
		// a read failure skips the pin rather than the edit.
		return errors.Is(err, sql.ErrNoRows)
	}
	if !ageSeconds.Valid || ageSeconds.Float64 < 0 {
		return true
	}
	age := time.Duration(ageSeconds.Float64 * float64(time.Second))
	return age >= sessionCheckpointInterval || (lastActor != actor && age >= sessionCheckpointMinGap)
}

// sessionAllowance is how many bytes session pins may use in total, and how many they use now.
// Explicit checkpoints keep the other half of the budget, and the total never exceeds it.
func (s *Store) sessionAllowance(ctx context.Context, t *sql.Tx, k Key) (allowed, used int, err error) {
	var total int
	if err = t.QueryRowContext(ctx, `SELECT coalesce(sum(length(c.snapshot)+length(c.projection)),0),coalesce(sum(CASE WHEN `+sessionPinSQL+` THEN length(c.snapshot)+length(c.projection) ELSE 0 END),0) FROM document_checkpoints c WHERE c.workspace_id=? AND c.page_id=?`, sessionCheckpointPrefix, k.WorkspaceID, k.PageID).Scan(&total, &used); err != nil {
		return 0, 0, err
	}
	return min(s.opts.MaxHistoryBytes/2, s.opts.MaxHistoryBytes-(total-used)), used, nil
}

func (s *Store) insertSessionPin(ctx context.Context, t *sql.Tx, k Key, r Receipt, c Commit, actor string, need int) error {
	allowed, used, err := s.sessionAllowance(ctx, t, k)
	if err != nil {
		return err
	}
	if used+need > allowed {
		return errSessionPinSkipped
	}
	if err := admitDisk(ctx, t, need); err != nil {
		return err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%d", k.WorkspaceID, k.PageID, r.Epoch, r.Rev))
	id := sessionCheckpointPrefix + hex.EncodeToString(sum[:16])
	_, err = t.ExecContext(ctx, "INSERT OR IGNORE INTO document_checkpoints(workspace_id,page_id,id,rev,schema_version,snapshot,projection,actor) VALUES(?,?,?,?,?,?,?,?)", k.WorkspaceID, k.PageID, id, r.Rev, c.Schema, c.CandidateSnapshot, []byte(c.Projection), actor)
	return err
}

// SessionHistoryFull reports whether the next automatic snapshot would not fit
// in its half of the History budget, estimated from the stored head size. It
// runs the caller's authorization and reveals nothing but that boolean.
func (s *Store) SessionHistoryFull(ctx context.Context, k Key, a Authorize) (full bool, err error) {
	err = s.tx(ctx, k, a, func(t *sql.Tx, _ string) error {
		var head int
		if e := t.QueryRowContext(ctx, `SELECT length(snapshot)+length(projection) FROM documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&head); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		allowed, used, e := s.sessionAllowance(ctx, t, k)
		full = e == nil && used+head > allowed
		return e
	})
	return
}

var errSessionPinSkipped = errors.New("session checkpoint skipped")

// sessionPinSQL matches pins this file wrote. Explicit checkpoint IDs are chosen
// by clients and may also start with "session-", but every explicit checkpoint
// has a checkpoint receipt and a session pin never does, so those are never
// counted against the automatic half.
const sessionPinSQL = `substr(c.id,1,8)=? AND NOT EXISTS(SELECT 1 FROM document_receipts r WHERE r.workspace_id=c.workspace_id AND r.page_id=c.page_id AND r.id=c.id AND r.kind='checkpoint')`
