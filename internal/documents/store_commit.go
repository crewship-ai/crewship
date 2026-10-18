package documents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

func digest(c Commit) (string, error) { // Include every semantic payload and precondition; never trust client digest alone.
	b, e := json.Marshal(struct {
		ID, Fingerprint  string
		IfRev            int64
		IfGeneration     *int64 `json:",omitempty"`
		Schema           int
		Update, Snapshot []byte
		Projection       json.RawMessage
		Hashes           map[string]string
		Kind             string
	}{c.ID, c.Fingerprint, c.IfRev, c.IfGeneration, c.Schema, c.Update, c.CandidateSnapshot, c.Projection, c.IfHashes, c.Kind})
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func hashes(p json.RawMessage, want map[string]string) error {
	if len(want) == 0 {
		return nil
	}
	var blocks []struct{ ID, Hash string }
	if e := json.Unmarshal(p, &blocks); e != nil {
		return fmt.Errorf("projection hashes: %w", e)
	}
	got := map[string]string{}
	for _, b := range blocks {
		got[b.ID] = b.Hash
	}
	for id, h := range want {
		v, ok := got[id]
		if !ok || v != h {
			return ErrConflict
		}
	}
	return nil
}
func (s *Store) Commit(ctx context.Context, k Key, c Commit, a Authorize) (r Receipt, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, actor string) error {
		if c.ID == "" || len(c.ID) > 256 || len(c.Fingerprint) > 256 || len(c.Kind) > 64 {
			return errors.New("commit id, fingerprint or kind invalid")
		}
		d, err := digest(c)
		if err != nil {
			return err
		}
		if err = fence(ctx, t, k, c.Lease); err != nil {
			return err
		}
		old, od, err := receipt(ctx, t, k, c.ID)
		if err == nil {
			if od != d || old.Actor != actor {
				return ErrDuplicate
			}
			r = old
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		v, err := state(ctx, t, k)
		if err != nil {
			return err
		}
		if c.IfRev != v.HeadRev || (c.IfGeneration != nil && *c.IfGeneration != v.Generation) {
			return ErrConflict
		}
		if c.Guard != nil {
			if err = c.Guard(ctx, t, actor, v); err != nil {
				return err
			}
		}
		if !supportedSchema(v.Schema) || (c.Schema != v.Schema && !(v.Schema == 1 && c.Schema == 2)) {
			return ErrSchema
		}
		if err = s.valid(c.Schema, c.CandidateSnapshot, c.Projection); err != nil {
			return err
		}
		if len(c.Update) == 0 {
			return errors.New("incremental update required")
		}
		if err = hashes(v.Projection, c.IfHashes); err != nil {
			return err
		}
		var logCount, logBytes int
		if err = s.admitNewReceipt(ctx, t, k); err != nil {
			return err
		}
		if err = t.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(payload)),0) FROM document_updates WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&logCount, &logBytes); err != nil {
			return err
		}
		if logBytes+len(c.Update) > s.opts.MaxLogBytes {
			return ErrQuota
		}
		r = Receipt{c.ID, v.HeadRev + 1, v.Epoch, actor, c.Kind}
		if _, err = t.ExecContext(ctx, "INSERT INTO document_updates VALUES(?,?,?,?)", k.WorkspaceID, k.PageID, r.Rev, c.Update); err != nil {
			return err
		}
		if _, err = t.ExecContext(ctx, "INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)", k.WorkspaceID, k.PageID, c.ID, d, c.Fingerprint, r.Rev, r.Epoch, actor, c.Kind); err != nil {
			return err
		}
		if logCount+1 >= 32 || logBytes+len(c.Update) >= 64<<10 {
			if _, err = t.ExecContext(ctx, "UPDATE documents SET snapshot=?,snapshot_rev=? WHERE workspace_id=? AND page_id=?", c.CandidateSnapshot, r.Rev, k.WorkspaceID, k.PageID); err != nil {
				return err
			}
			if _, err = t.ExecContext(ctx, "DELETE FROM document_updates WHERE workspace_id=? AND page_id=? AND rev<=?", k.WorkspaceID, k.PageID, r.Rev); err != nil {
				return err
			}
		}
		_, err = t.ExecContext(ctx, "UPDATE documents SET head_rev=?,projection=?,schema_version=? WHERE workspace_id=? AND page_id=?", r.Rev, []byte(c.Projection), c.Schema, k.WorkspaceID, k.PageID)
		return err
	})
	return
}
func (s *Store) LookupReceipt(ctx context.Context, k Key, id, fingerprint string, a Authorize) (r Receipt, found bool, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, actor string) error {
		var stored string
		var err error
		r.ID = id
		err = t.QueryRowContext(ctx, "SELECT fingerprint,rev,epoch,actor,kind FROM document_receipts WHERE workspace_id=? AND page_id=? AND id=?", k.WorkspaceID, k.PageID, id).Scan(&stored, &r.Rev, &r.Epoch, &r.Actor, &r.Kind)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if stored != fingerprint || r.Actor != actor {
			return ErrDuplicate
		}
		found = true
		return nil
	})
	return
}

// RequestDigest is stable across writer epochs, and includes payloads and all
// semantic preconditions. Lease credentials are excluded so restart replay works.
func RequestDigest(c Commit) (string, error) { return digest(c) }
