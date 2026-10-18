package documents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const LatestSchema = 3

func supportedSchema(schema int) bool { return schema >= 1 && schema <= LatestSchema }

// UpgradeRequest changes content interpretation only after a human's explicit
// original-revision confirmation. Binary/projection bytes and character IDs do
// not change. The empty Yjs v1 update preserves the contiguous durable log.
// Authorization precedes even exact retries; rotated leases cannot reopen access.
func (s *Store) UpgradeRequest(ctx context.Context, k Key, lease Lease, id, fingerprint string, fromSchema, targetSchema int, ifRev int64, generation *int64, auth Authorize) (r Receipt, err error) {
	err = s.tx(ctx, k, auth, func(tx *sql.Tx, actor string) error {
		if id == "" || len(id) > 256 || len(fingerprint) > 256 || generation == nil {
			return errors.New("upgrade identity and original generation required")
		}
		raw, e := json.Marshal(struct {
			Fingerprint     string
			From, Target    int
			Rev, Generation int64
		}{fingerprint, fromSchema, targetSchema, ifRev, *generation})
		if e != nil {
			return e
		}
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		old, stored, e := receipt(ctx, tx, k, id)
		if e == nil {
			if stored != digest || old.Actor != actor || old.Kind != "upgrade" {
				return ErrDuplicate
			}
			r = old
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e = fence(ctx, tx, k, lease); e != nil {
			return e
		}
		v, e := state(ctx, tx, k)
		if e != nil {
			return e
		}
		if v.HeadRev != ifRev || v.Generation != *generation {
			return ErrConflict
		}
		if targetSchema != LatestSchema || (fromSchema != 1 && fromSchema != 2) || v.Schema != fromSchema {
			return ErrSchema
		}
		var logBytes int
		if e = s.admitNewReceipt(ctx, tx, k); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, "SELECT coalesce(sum(length(payload)),0) FROM document_updates WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&logBytes); e != nil {
			return e
		}
		if logBytes+2 > s.opts.MaxLogBytes {
			return ErrQuota
		}
		r = Receipt{id, v.HeadRev + 1, v.Epoch + 1, actor, "upgrade"}
		if _, e = tx.ExecContext(ctx, "INSERT INTO document_updates VALUES(?,?,?,?)", k.WorkspaceID, k.PageID, r.Rev, []byte{0, 0}); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE documents SET schema_version=?,head_rev=?,epoch=?,generation=generation+1,writer='',owner='',lease_until=0 WHERE workspace_id=? AND page_id=?", targetSchema, r.Rev, r.Epoch, k.WorkspaceID, k.PageID); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)", k.WorkspaceID, k.PageID, id, digest, fingerprint, r.Rev, r.Epoch, actor, r.Kind)
		return e
	})
	return
}
