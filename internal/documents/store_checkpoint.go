package documents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

func (s *Store) Checkpoint(ctx context.Context, k Key, l Lease, id string, snapshot []byte, ifRev int64, a Authorize) (Checkpoint, error) {
	return s.CheckpointRequest(ctx, k, l, id, "internal-checkpoint", snapshot, ifRev, a)
}

// CheckpointRequest retains the original semantic fingerprint for service retries.
func (s *Store) CheckpointRequest(ctx context.Context, k Key, l Lease, id, fingerprint string, snapshot []byte, ifRev int64, a Authorize) (Checkpoint, error) {
	return s.CheckpointRequestAtGeneration(ctx, k, l, id, fingerprint, snapshot, ifRev, nil, a)
}
func (s *Store) CheckpointRequestAtGeneration(ctx context.Context, k Key, l Lease, id, fingerprint string, snapshot []byte, ifRev int64, generation *int64, a Authorize) (Checkpoint, error) {
	return s.CheckpointRequestGuarded(ctx, k, l, id, fingerprint, snapshot, ifRev, generation, a, nil)
}
func (s *Store) CheckpointRequestGuarded(ctx context.Context, k Key, l Lease, id, fingerprint string, snapshot []byte, ifRev int64, generation *int64, a Authorize, guard MutationGuard) (c Checkpoint, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, actor string) error {
		if id == "" || len(id) > 256 || len(fingerprint) > 256 {
			return errors.New("checkpoint id required")
		}

		raw, _ := json.Marshal(struct {
			ID, Fingerprint string
			Snapshot        []byte
			IfRev           int64
			IfGeneration    *int64 `json:",omitempty"`
		}{id, fingerprint, snapshot, ifRev, generation})
		sum := sha256.Sum256(raw)
		checkpointDigest := hex.EncodeToString(sum[:])
		old, stored, lookupErr := receipt(ctx, t, k, id)
		if lookupErr == nil && (stored != checkpointDigest || old.Actor != actor || old.Kind != "checkpoint") {
			return ErrDuplicate
		}
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		var existing Checkpoint
		existing.ID = id
		err := t.QueryRowContext(ctx, "SELECT rev,schema_version,snapshot,projection,actor FROM document_checkpoints WHERE workspace_id=? AND page_id=? AND id=?", k.WorkspaceID, k.PageID, id).Scan(&existing.Rev, &existing.Schema, &existing.Snapshot, &existing.Projection, &existing.Actor)
		if err == nil {
			if existing.Rev != ifRev || existing.Actor != actor || !bytes.Equal(existing.Snapshot, snapshot) {
				return ErrDuplicate
			}
			current, err := state(ctx, t, k)
			if err != nil {
				return err
			}
			if !supportedSchema(current.Schema) {
				return ErrSchema
			}
			c = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		if err := fence(ctx, t, k, l); err != nil {
			return err
		}
		v, err := state(ctx, t, k)
		if err != nil {
			return err
		}
		if v.HeadRev != ifRev || (generation != nil && *generation != v.Generation) {
			return ErrConflict
		}
		if guard != nil {
			if err = guard(ctx, t, actor, v); err != nil {
				return err
			}
		}
		if err = s.valid(v.Schema, snapshot, v.Projection); err != nil {
			return err
		}
		if err = s.admitNewReceipt(ctx, t, k); err != nil {
			return err
		}
		var size int
		if err = t.QueryRowContext(ctx, "SELECT coalesce(sum(length(snapshot)+length(projection)),0) FROM document_checkpoints WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&size); err != nil {
			return err
		}
		if size+len(snapshot)+len(v.Projection) > s.opts.MaxHistoryBytes {
			return ErrQuota
		}
		c = Checkpoint{id, v.HeadRev, v.Schema, snapshot, v.Projection, actor}
		_, err = t.ExecContext(ctx, "INSERT INTO document_checkpoints(workspace_id,page_id,id,rev,schema_version,snapshot,projection,actor) VALUES(?,?,?,?,?,?,?,?)", k.WorkspaceID, k.PageID, id, c.Rev, c.Schema, snapshot, []byte(c.Projection), actor)
		if err != nil {
			return err
		}
		_, err = t.ExecContext(ctx, "INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)", k.WorkspaceID, k.PageID, id, checkpointDigest, fingerprint, v.HeadRev, v.Epoch, actor, "checkpoint")
		return err
	})
	return
}
func (s *Store) Restore(ctx context.Context, k Key, l Lease, id string, ifRev int64, a Authorize) (Receipt, error) {
	requestID, err := token()
	if err != nil {
		return Receipt{}, err
	}
	return s.RestoreRequest(ctx, k, l, id, requestID, ifRev, a)
}

// RestoreRequest supports exactly-once retries using a caller-generated request ID.
func (s *Store) RestoreRequest(ctx context.Context, k Key, l Lease, id, requestID string, ifRev int64, a Authorize) (Receipt, error) {
	return s.RestoreRequestWithFingerprint(ctx, k, l, id, requestID, "internal-restore", ifRev, a)
}
func (s *Store) RestoreRequestWithFingerprint(ctx context.Context, k Key, l Lease, id, requestID, fingerprint string, ifRev int64, a Authorize) (Receipt, error) {
	return s.RestoreRequestWithFingerprintAtGeneration(ctx, k, l, id, requestID, fingerprint, ifRev, nil, a)
}
func (s *Store) RestoreRequestWithFingerprintAtGeneration(ctx context.Context, k Key, l Lease, id, requestID, fingerprint string, ifRev int64, generation *int64, a Authorize) (r Receipt, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, actor string) error {
		if requestID == "" || len(requestID) > 256 || len(id) > 256 || len(fingerprint) > 256 {
			return errors.New("restore request id required")
		}
		raw, _ := json.Marshal(struct {
			Checkpoint, Fingerprint string
			IfRev                   int64
			IfGeneration            *int64 `json:",omitempty"`
		}{id, fingerprint, ifRev, generation})
		sum := sha256.Sum256(raw)
		restoreDigest := hex.EncodeToString(sum[:])
		previous, stored, lookupErr := receipt(ctx, t, k, requestID)
		if lookupErr == nil {
			if stored != restoreDigest || previous.Actor != actor {
				return ErrDuplicate
			}
			r = previous
			return nil
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		if err := fence(ctx, t, k, l); err != nil {
			return err
		}
		v, err := state(ctx, t, k)
		if err != nil {
			return err
		}
		if v.HeadRev != ifRev || (generation != nil && *generation != v.Generation) {
			return ErrConflict
		}
		if !supportedSchema(v.Schema) {
			return ErrSchema
		}
		var c Checkpoint
		if err = t.QueryRowContext(ctx, "SELECT rev,schema_version,snapshot,projection FROM document_checkpoints WHERE workspace_id=? AND page_id=? AND id=?", k.WorkspaceID, k.PageID, id).Scan(&c.Rev, &c.Schema, &c.Snapshot, &c.Projection); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if err = s.valid(c.Schema, c.Snapshot, c.Projection); err != nil {
			return err
		}
		if err = s.admitNewReceipt(ctx, t, k); err != nil {
			return err
		}
		r = Receipt{requestID, v.HeadRev + 1, v.Epoch + 1, actor, "restore"}
		if _, err = t.ExecContext(ctx, "UPDATE documents SET head_rev=?,epoch=?,generation=generation+1,schema_version=?,snapshot=?,snapshot_rev=?,projection=?,writer='',owner='',lease_until=0 WHERE workspace_id=? AND page_id=?", r.Rev, r.Epoch, c.Schema, c.Snapshot, r.Rev, []byte(c.Projection), k.WorkspaceID, k.PageID); err != nil {
			return err
		}
		if _, err = t.ExecContext(ctx, "DELETE FROM document_updates WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID); err != nil {
			return err
		}
		_, err = t.ExecContext(ctx, "INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)", k.WorkspaceID, k.PageID, r.ID, restoreDigest, fingerprint, r.Rev, r.Epoch, actor, r.Kind)
		return err
	})
	return
}

func (s *Store) CheckpointRead(ctx context.Context, k Key, id string, a Authorize) (c Checkpoint, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, _ string) error {
		c.ID = id
		err := t.QueryRowContext(ctx, "SELECT rev,schema_version,snapshot,projection,actor FROM document_checkpoints WHERE workspace_id=? AND page_id=? AND id=?", k.WorkspaceID, k.PageID, id).Scan(&c.Rev, &c.Schema, &c.Snapshot, &c.Projection, &c.Actor)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return
}

// Stats reports logical retained payload bytes (not SQLite allocation or WAL
// size), so operators can distinguish replay compaction from pinned history.
type Stats struct {
	SnapshotBytes, ProjectionBytes, ReplayBytes, CheckpointBytes int64
	ReplayUpdates, Checkpoints, Receipts                         int64
}

func (s *Store) Stats(ctx context.Context, k Key, a Authorize) (v Stats, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, _ string) error {
		if err := t.QueryRowContext(ctx, "SELECT length(snapshot),length(projection) FROM documents WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&v.SnapshotBytes, &v.ProjectionBytes); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if err := t.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(payload)),0) FROM document_updates WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&v.ReplayUpdates, &v.ReplayBytes); err != nil {
			return err
		}
		if err := t.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(snapshot)+length(projection)),0) FROM document_checkpoints WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&v.Checkpoints, &v.CheckpointBytes); err != nil {
			return err
		}
		return t.QueryRowContext(ctx, "SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&v.Receipts)
	})
	return
}
