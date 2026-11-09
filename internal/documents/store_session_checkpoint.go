package documents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/storageheadroom"
)

// Session checkpoints make History show more than the initial revision.
// Individual edits arrive every few hundred milliseconds, so pinning each one
// would exhaust the History budget within minutes. Instead a commit pins its
// confirmed result when the newest checkpoint is older than
// sessionCheckpointInterval, or was written by someone else at least
// sessionAuthorChangeGap ago, unless the result is byte-identical to that
// checkpoint. One author therefore pins at most every ten minutes, and
// interleaved authors at most every five (12 an hour): a 1 MB document fills
// the default 8 MiB automatic share after about 40 minutes of joint editing.
//
// Session pins are best effort: they never refuse or roll back the edit. They
// write no receipt (so they do not use the operation admission window) and
// share at most half of MaxHistoryBytes, so explicit checkpoints and restores
// keep their headroom. They are also skipped while the workspace storage budget
// is above sessionWorkspaceHighWater, so automatic snapshots never use the
// space later edits need. Nothing here deletes a checkpoint (retention is a
// separate decision, R71 §0.11): a pin that does not fit, or that the storage
// budget or disk headroom refuses, is skipped and the edit commits unchanged.
// A skip is remembered for sessionCheckpointInterval, which bounds the work a
// full document costs per commit and lets History report it.
const (
	sessionCheckpointInterval = 10 * time.Minute
	sessionAuthorChangeGap    = 5 * time.Minute
	sessionCheckpointPrefix   = "session-"
	// Automatic snapshots stop while the workspace or instance storage budget
	// would be more than 80% used, leaving the rest for edits.
	sessionWorkspaceHighWater = 0.8
	sessionSkipMemory         = 4096
)

// sessionSkips remembers, per document, when an automatic snapshot was last
// skipped for lack of space. Process-local and bounded; a restart forgets it,
// and SessionHistoryFull then falls back to its size estimate. Several server
// processes sharing one database would each keep their own copy, which only
// affects how promptly the flag appears. A skip is recorded before the outer
// commit, so a rolled-back edit can still leave it for one interval.
var sessionSkips = struct {
	sync.Mutex
	at map[Key]time.Time
}{at: map[Key]time.Time{}}

func recordSessionSkip(k Key) {
	sessionSkips.Lock()
	defer sessionSkips.Unlock()
	if len(sessionSkips.at) >= sessionSkipMemory {
		sessionSkips.at = map[Key]time.Time{}
	}
	sessionSkips.at[k] = time.Now()
}

func recentSessionSkip(k Key) bool {
	sessionSkips.Lock()
	defer sessionSkips.Unlock()
	at, ok := sessionSkips.at[k]
	if ok && time.Since(at) >= sessionCheckpointInterval {
		delete(sessionSkips.at, k)
		return false
	}
	return ok
}

// pinSession runs inside the commit transaction after the head was advanced to r.Rev.
// Its own work runs in a savepoint; any failure there is undone and ignored.
func (s *Store) pinSession(ctx context.Context, t *sql.Tx, k Key, r Receipt, c Commit, actor string) error {
	if recentSessionSkip(k) || !s.sessionPinDue(ctx, t, k, actor, c.CandidateSnapshot) {
		return nil
	}
	need := len(c.CandidateSnapshot) + len(c.Projection)
	if need > s.opts.MaxHistoryBytes/2 {
		recordSessionSkip(k)
		return nil
	}
	if _, err := t.ExecContext(ctx, "SAVEPOINT document_session_pin"); err != nil {
		return nil
	}
	if err := s.insertSessionPin(ctx, t, k, r, c, actor, need); err != nil {
		// Only a lack of space is reported as the History limit; other failures are just skipped.
		if errors.Is(err, errSessionPinSkipped) || errors.Is(err, storageheadroom.ErrInsufficient) || strings.Contains(err.Error(), "artifact_storage_quota") {
			recordSessionSkip(k)
		}
		// If even the rollback fails (a cancelled context, or SQLite already
		// rolled the whole transaction back after SQLITE_FULL or an I/O error),
		// the outer transaction cannot commit either, so returning that error
		// loses nothing the edit had before this change.
		if _, rollbackErr := t.ExecContext(ctx, "ROLLBACK TO document_session_pin"); rollbackErr != nil {
			return rollbackErr
		}
	}
	_, err := t.ExecContext(ctx, "RELEASE document_session_pin")
	return err
}

// sessionPinDue reports whether the newest checkpoint is old enough and differs
// from this result. An unreadable or future timestamp counts as due, so History
// cannot stall on it.
func (s *Store) sessionPinDue(ctx context.Context, t *sql.Tx, k Key, actor string, snapshot []byte) bool {
	// Runs on every commit inside the writer transaction: read metadata only.
	var lastID, lastActor string
	var ageSeconds sql.NullFloat64
	var lastSize int
	err := t.QueryRowContext(ctx, `SELECT id,actor,(julianday('now')-julianday(created_at))*86400.0,length(snapshot) FROM document_checkpoints WHERE workspace_id=? AND page_id=? ORDER BY rev DESC,created_at DESC LIMIT 1`, k.WorkspaceID, k.PageID).Scan(&lastID, &lastActor, &ageSeconds, &lastSize)
	if err != nil {
		// No checkpoint (documents created before initial checkpoints) starts History;
		// a read failure skips the pin rather than the edit.
		return errors.Is(err, sql.ErrNoRows)
	}
	if ageSeconds.Valid && ageSeconds.Float64 >= 0 {
		age := time.Duration(ageSeconds.Float64 * float64(time.Second))
		if age < sessionCheckpointInterval && (lastActor == actor || age < sessionAuthorChangeGap) {
			return false
		}
	}
	// Due: only now, and only for an equal length, compare the newest checkpoint's bytes.
	if lastSize != len(snapshot) {
		return true
	}
	var lastSnapshot []byte
	if err = t.QueryRowContext(ctx, `SELECT snapshot FROM document_checkpoints WHERE workspace_id=? AND page_id=? AND id=?`, k.WorkspaceID, k.PageID, lastID).Scan(&lastSnapshot); err != nil {
		return false
	}
	return !bytes.Equal(lastSnapshot, snapshot)
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

// storageHighWater reports whether need more bytes would take the workspace or
// the instance storage budget above sessionWorkspaceHighWater. A missing budget
// row (no artifact storage charged yet) is not high.
func storageHighWater(ctx context.Context, t *sql.Tx, k Key, need int) (bool, error) {
	for _, q := range []struct {
		query string
		args  []any
	}{
		{`SELECT used_bytes,reserved_bytes,max_bytes FROM artifact_storage_workspaces WHERE workspace_id=?`, []any{k.WorkspaceID}},
		{`SELECT used_bytes,reserved_bytes,max_bytes FROM artifact_storage_global WHERE id=1`, nil},
	} {
		var used, reserved, max int64
		err := t.QueryRowContext(ctx, q.query, q.args...).Scan(&used, &reserved, &max)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if float64(used+reserved+int64(need)) > float64(max)*sessionWorkspaceHighWater {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) insertSessionPin(ctx context.Context, t *sql.Tx, k Key, r Receipt, c Commit, actor string, need int) error {
	allowed, used, err := s.sessionAllowance(ctx, t, k)
	if err != nil {
		return err
	}
	if used+need > allowed {
		return errSessionPinSkipped
	}
	if high, err := storageHighWater(ctx, t, k, need); err != nil || high {
		return errors.Join(errSessionPinSkipped, err)
	}
	if err := admitDisk(ctx, t, need); err != nil {
		return err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%d", k.WorkspaceID, k.PageID, r.Epoch, r.Rev))
	id := sessionCheckpointPrefix + hex.EncodeToString(sum[:16])
	_, err = t.ExecContext(ctx, "INSERT OR IGNORE INTO document_checkpoints(workspace_id,page_id,id,rev,schema_version,snapshot,projection,actor) VALUES(?,?,?,?,?,?,?,?)", k.WorkspaceID, k.PageID, id, r.Rev, c.Schema, c.CandidateSnapshot, []byte(c.Projection), actor)
	return err
}

// SessionHistoryFull reports whether History is not adding automatic snapshots
// of this document for lack of space: a skip this process saw within the last
// interval, or (after a restart) the next one estimated not to fit in its half
// of the History budget. It runs the caller's authorization and reveals
// nothing but that boolean.
func (s *Store) SessionHistoryFull(ctx context.Context, k Key, a Authorize) (full bool, err error) {
	err = s.tx(ctx, k, a, func(t *sql.Tx, _ string) error {
		var head int
		if e := t.QueryRowContext(ctx, `SELECT length(snapshot)+length(projection) FROM documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&head); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		if recentSessionSkip(k) {
			full = true
			return nil
		}
		allowed, used, e := s.sessionAllowance(ctx, t, k)
		full = e == nil && used+head > allowed
		return e
	})
	return
}

var errSessionPinSkipped = errors.New("session checkpoint skipped")

// sessionPinSQL matches pins this file wrote. Explicit checkpoint IDs are chosen
// by clients; new ones may not start with "session-" (CheckpointRequestGuarded),
// and every explicit checkpoint has a checkpoint receipt while a session pin
// never does, so older explicit ones are not counted against the automatic half.
const sessionPinSQL = `substr(c.id,1,8)=? AND NOT EXISTS(SELECT 1 FROM document_receipts r WHERE r.workspace_id=c.workspace_id AND r.page_id=c.page_id AND r.id=c.id AND r.kind='checkpoint')`
