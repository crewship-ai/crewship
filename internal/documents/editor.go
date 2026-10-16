package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type Comment struct {
	ID        string          `json:"id"`
	Body      string          `json:"body"`
	Anchor    json.RawMessage `json:"anchor"`
	Actor     string          `json:"actor"`
	Rev       int64           `json:"rev"`
	Version   int64           `json:"version"`
	Resolved  bool            `json:"resolved"`
	CreatedAt string          `json:"created_at"`
}

func (s *Store) Comments(ctx context.Context, k Key, a Authorize) (out []Comment, err error) {
	out = []Comment{}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, _ string) error {
		if _, e := state(ctx, tx, k); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,body,anchor_json,actor,rev,version,resolved,created_at FROM document_comments WHERE workspace_id=? AND page_id=? ORDER BY created_at,id LIMIT 1000`, k.WorkspaceID, k.PageID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var c Comment
			var raw string
			if e = rows.Scan(&c.ID, &c.Body, &raw, &c.Actor, &c.Rev, &c.Version, &c.Resolved, &c.CreatedAt); e != nil {
				return e
			}
			c.Anchor = json.RawMessage(raw)
			out = append(out, c)
		}
		return rows.Err()
	})
	return
}

// Comment mutations use the SQL writer fence, generation/revision checks,
// live comment authority and retry receipts. They do not acquire an editor lease.
// Anchors are validated by the trusted schema service.
func (s *Store) Comment(ctx context.Context, k Key, id, fingerprint, body string, anchor json.RawMessage, ifRev int64, generation *int64, a Authorize) (r Receipt, err error) {
	err = s.tx(ctx, k, a, func(tx *sql.Tx, actor string) error {
		previous, _, e := receipt(ctx, tx, k, id)
		if e == nil {
			var fp string
			if e = tx.QueryRowContext(ctx, `SELECT fingerprint FROM document_receipts WHERE workspace_id=? AND page_id=? AND id=?`, k.WorkspaceID, k.PageID, id).Scan(&fp); e != nil {
				return e
			}
			if previous.Actor != actor || fp != fingerprint || previous.Kind != "comment" {
				return ErrDuplicate
			}
			r = previous
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if id == "" || len(id) > 128 || len(fingerprint) > 256 || len(body) == 0 || len(body) > 10000 || len(anchor) > 8192 || !json.Valid(anchor) {
			return errors.New("invalid bounded comment")
		}
		v, e := state(ctx, tx, k)
		if e != nil {
			return e
		}
		if v.HeadRev != ifRev || generation == nil || *generation != v.Generation {
			return ErrConflict
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM document_comments WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&n); e != nil {
			return e
		}
		if n >= 1000 {
			return ErrQuota
		}
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&n); e != nil {
			return e
		}
		if n >= s.opts.MaxCommits {
			return ErrQuota
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO document_comments(workspace_id,page_id,id,body,anchor_json,actor,rev) VALUES(?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, id, body, string(anchor), actor, v.HeadRev); e != nil {
			return e
		}
		r = Receipt{id, v.HeadRev, v.Epoch, actor, "comment"}
		_, e = tx.ExecContext(ctx, `INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, id, fingerprint, fingerprint, r.Rev, r.Epoch, actor, r.Kind)
		return e
	})
	return
}

func (s *Store) Checkpoints(ctx context.Context, k Key, a Authorize) (out []map[string]any, err error) {
	out = []map[string]any{}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, _ string) error {
		if _, e := state(ctx, tx, k); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,rev,schema_version,actor,created_at FROM document_checkpoints WHERE workspace_id=? AND page_id=? ORDER BY rev DESC,id DESC LIMIT 1000`, k.WorkspaceID, k.PageID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, actor, created string
			var rev int64
			var schema int
			if e = rows.Scan(&id, &rev, &schema, &actor, &created); e != nil {
				return e
			}
			out = append(out, map[string]any{"id": id, "rev": rev, "schema": schema, "actor": actor, "created_at": created})
		}
		return rows.Err()
	})
	return
}

func (s *Store) ResolveComment(ctx context.Context, k Key, id, fingerprint, comment string, version, ifRev int64, resolved bool, generation *int64, a Authorize) (r Receipt, err error) {
	err = s.tx(ctx, k, a, func(tx *sql.Tx, actor string) error {
		previous, _, e := receipt(ctx, tx, k, id)
		if e == nil {
			var fp string
			if e = tx.QueryRowContext(ctx, `SELECT fingerprint FROM document_receipts WHERE workspace_id=? AND page_id=? AND id=?`, k.WorkspaceID, k.PageID, id).Scan(&fp); e != nil {
				return e
			}
			if previous.Actor != actor || fp != fingerprint || previous.Kind != "resolve" {
				return ErrDuplicate
			}
			r = previous
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if id == "" || len(id) > 128 || len(fingerprint) > 256 {
			return errors.New("invalid request identity")
		}
		v, e := state(ctx, tx, k)
		if e != nil {
			return e
		}
		if v.HeadRev != ifRev || generation == nil || *generation != v.Generation {
			return ErrConflict
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&n); e != nil {
			return e
		}
		if n >= s.opts.MaxCommits {
			return ErrQuota
		}
		result, e := tx.ExecContext(ctx, `UPDATE document_comments SET resolved=?,version=version+1 WHERE workspace_id=? AND page_id=? AND id=? AND version=?`, resolved, k.WorkspaceID, k.PageID, comment, version)
		if e != nil {
			return e
		}
		n64, _ := result.RowsAffected()
		if n64 != 1 {
			return ErrConflict
		}
		r = Receipt{id, v.HeadRev, v.Epoch, actor, "resolve"}
		_, e = tx.ExecContext(ctx, `INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,?,?,?,?)`, k.WorkspaceID, k.PageID, id, fingerprint, fingerprint, r.Rev, r.Epoch, actor, r.Kind)
		return e
	})
	return
}

// RecoverEngine is a service-only, idempotent boot barrier. The Node engine
// invokes it only after obtaining its exclusive configured private listener.
func (s *Store) RecoverEngine(ctx context.Context, boot string) error {
	if len(boot) < 16 || len(boot) > 128 {
		return errors.New("invalid engine boot identity")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE document_writer_fence SET seq=seq WHERE id=1`); e != nil {
		return e
	}
	var previous string
	if e = tx.QueryRowContext(ctx, `SELECT boot_id FROM document_writer_fence WHERE id=1`).Scan(&previous); e != nil {
		return e
	}
	if previous == boot {
		return tx.Commit()
	}
	if _, e = tx.ExecContext(ctx, `UPDATE documents SET epoch=epoch+1,generation=generation+1,writer='',owner='',lease_until=0`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE document_writer_fence SET boot_id=? WHERE id=1`, boot); e != nil {
		return e
	}
	return tx.Commit()
}
