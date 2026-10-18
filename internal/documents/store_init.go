package documents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

func (s *Store) valid(schema int, snap []byte, p json.RawMessage) error {
	if !supportedSchema(schema) {
		return ErrSchema
	}
	if len(snap) == 0 || !json.Valid(p) {
		return errors.New("snapshot and valid projection required")
	}
	if len(snap)+len(p) > s.opts.MaxDocumentBytes {
		return ErrQuota
	}
	return nil
}
func (s *Store) Init(ctx context.Context, k Key, schema int, snap []byte, p json.RawMessage, a Authorize) (State, error) {
	return s.InitRequest(ctx, k, "__init", "internal-init", schema, snap, p, a)
}
func (s *Store) InitRequest(ctx context.Context, k Key, id, fingerprint string, schema int, snap []byte, p json.RawMessage, a Authorize) (v State, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, actor string) error {
		v, e = s.InitRequestTx(ctx, t, k, id, fingerprint, schema, snap, p, actor)
		return e
	})
	return
}

// InitRequestTx initializes inside a caller-owned, writer-fenced creation
// transaction. The caller must authorize the exact current principal before
// invoking it and commit Page, document and creation allowance atomically.
// Existing human initialization keeps using InitRequest's authority wrapper.
func (s *Store) InitRequestTx(ctx context.Context, t *sql.Tx, k Key, id, fingerprint string, schema int, snap []byte, p json.RawMessage, actor string) (v State, e error) {
	if t == nil || actor == "" || k.WorkspaceID == "" || k.PageID == "" {
		return v, errors.New("document transaction, key and authorized actor required")
	}
	e = func() error {
		if id == "" || len(id) > 256 || len(fingerprint) > 256 {
			return errors.New("initialization request id required")
		}
		raw, err := json.Marshal(struct {
			ID, Fingerprint string
			Schema          int
			Snapshot        []byte
			Projection      json.RawMessage
		}{id, fingerprint, schema, snap, p})
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		initDigest := hex.EncodeToString(sum[:])
		previous, stored, lookupErr := receipt(ctx, t, k, id)
		if lookupErr == nil {
			if previous.Actor != actor || stored != initDigest || previous.Kind != "init" {
				return ErrDuplicate
			}
			v, err = state(ctx, t, k)
			return err
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}

		if _, err := state(ctx, t, k); err == nil {
			return ErrConflict
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := s.valid(schema, snap, p); err != nil {
			return err
		}
		var n int
		if err := t.QueryRowContext(ctx, "SELECT count(*) FROM documents WHERE workspace_id=?", k.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= s.opts.MaxDocuments {
			return ErrQuota
		}
		_, err = t.ExecContext(ctx, "INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES(?,?,?,?,?)", k.WorkspaceID, k.PageID, schema, snap, []byte(p))
		if err != nil {
			return err
		}
		if _, err = t.ExecContext(ctx, "INSERT INTO document_receipts(workspace_id,page_id,id,digest,fingerprint,rev,epoch,actor,kind) VALUES(?,?,?,?,?,0,0,?,'init')", k.WorkspaceID, k.PageID, id, initDigest, fingerprint, actor); err != nil {
			return err
		}
		v, err = state(ctx, t, k)
		return err
	}()
	return
}
