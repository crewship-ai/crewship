package documents

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

func token() (string, error) {
	var b [32]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}
func (s *Store) Acquire(ctx context.Context, k Key, owner string, a Authorize) (l Lease, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, _ string) error {
		if owner == "" || len(owner) > 256 {
			return errors.New("owner required")
		}
		v, err := state(ctx, t, k)
		if err != nil {
			return err
		}
		if !supportedSchema(v.Schema) {
			return ErrSchema
		}
		var writer string
		var until int64
		if err = t.QueryRowContext(ctx, "SELECT writer,lease_until FROM documents WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&writer, &until); err != nil {
			return err
		}
		if writer != "" && until > time.Now().UnixMilli() {
			return ErrFence
		}
		w, err := token()
		if err != nil {
			return err
		}
		l = Lease{owner, w, v.Epoch + 1, time.Now().Add(s.opts.LeaseDuration), v.Generation}
		_, err = t.ExecContext(ctx, "UPDATE documents SET epoch=?,writer=?,owner=?,lease_until=? WHERE workspace_id=? AND page_id=?", l.Epoch, l.Writer, l.Owner, l.ExpiresAt.UnixMilli(), k.WorkspaceID, k.PageID)
		return err
	})
	return
}
func fence(ctx context.Context, t *sql.Tx, k Key, l Lease) error {
	var w, o string
	var ep, until int64
	if err := t.QueryRowContext(ctx, "SELECT writer,owner,epoch,lease_until FROM documents WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&w, &o, &ep, &until); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if w == "" || w != l.Writer || o != l.Owner || ep != l.Epoch || until <= time.Now().UnixMilli() {
		return ErrFence
	}
	return nil
}
func (s *Store) Renew(ctx context.Context, k Key, l Lease, a Authorize) (out Lease, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, _ string) error {
		if err := fence(ctx, t, k, l); err != nil {
			return err
		}
		v, err := state(ctx, t, k)
		if err != nil {
			return err
		}
		if !supportedSchema(v.Schema) {
			return ErrSchema
		}
		out = l
		out.Generation = v.Generation
		out.ExpiresAt = time.Now().Add(s.opts.LeaseDuration)
		_, err = t.ExecContext(ctx, "UPDATE documents SET lease_until=? WHERE workspace_id=? AND page_id=?", out.ExpiresAt.UnixMilli(), k.WorkspaceID, k.PageID)
		return err
	})
	return
}
func (s *Store) Release(ctx context.Context, k Key, l Lease, a Authorize) error {
	return s.tx(ctx, k, a, func(t *sql.Tx, _ string) error {
		if e := fence(ctx, t, k, l); e != nil {
			return e
		}
		_, e := t.ExecContext(ctx, "UPDATE documents SET writer='',owner='',lease_until=0 WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID)
		return e
	})
}

// InvalidateLeases is a boot-only service operation; it fences every previous
// process without resurrecting a token from snapshots or checkpoints.
func (s *Store) InvalidateLeases(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, "UPDATE documents SET epoch=epoch+1,generation=generation+1,writer='',owner='',lease_until=0")
	return e
}
