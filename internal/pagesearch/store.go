package pagesearch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const MaxWorkspaceEncodedBytes int64 = 64 << 20

var (
	ErrNotFound      = errors.New("native search projection unavailable")
	ErrSourceChanged = errors.New("native search source changed; rebuild required")
)

// Guard is a compiled per-call authority/source reader. It MUST recheck the
// exact caller credential, live Page reach and current canonical source in this
// transaction. Previous candidate authorization or a managed response is not
// sufficient. No public request can supply a Guard or a projection.
type Guard func(context.Context, *sql.Tx) (SourceTag, error)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Load rechecks both authority and source in the caller's final read snapshot.
// The caller keeps that snapshot through matching and response materialization.
func (s *Store) Load(ctx context.Context, tx *sql.Tx, expected SourceTag, guard Guard) (Projection, error) {
	var projection Projection
	if tx == nil || guard == nil {
		return projection, ErrInvalid
	}
	current, err := guard(ctx, tx)
	if err != nil {
		return projection, err
	}
	if !expected.Current(current) {
		return projection, ErrSourceChanged
	}
	var cached SourceTag
	cached.WorkspaceID, cached.PageID = expected.WorkspaceID, expected.PageID
	var version, size int
	var raw []byte
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT source_type,source_schema,source_rev,source_generation,source_hash,adapter_version,
 length(encoded),encoded_sha256,CASE WHEN length(encoded)<=? THEN encoded ELSE NULL END
 FROM page_search_projections WHERE workspace_id=? AND page_id=?`, MaxProjectionBytes, expected.WorkspaceID, expected.PageID).Scan(
		&cached.Type, &cached.Schema, &cached.Revision, &cached.Generation, &cached.Hash, &version, &size, &hash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return projection, ErrNotFound
	}
	if err != nil {
		return projection, err
	}
	if !expected.Current(cached) {
		return projection, ErrSourceChanged
	}
	if version != AdapterVersion || size != len(raw) || size < 1 || size > MaxProjectionBytes {
		return projection, ErrInvalid
	}
	projection, err = Decode(raw, hash)
	if err != nil {
		return projection, err
	}
	if !projection.Source.Current(expected) {
		return projection, ErrInvalid
	}
	return projection, nil
}

// Publish serializes the final live authority/source CAS and encoded-byte quota
// under SQLite's writer fence. It does not hold a transaction over managed IPC.
func (s *Store) Publish(ctx context.Context, projection Projection, guard Guard) error {
	return s.publishBudget(ctx, projection, guard, MaxWorkspaceEncodedBytes)
}

// The smaller-budget path is used by bounded real-DB quota tests. Production
// calls only Publish's fixed ceiling; this path cannot raise that ceiling.
func (s *Store) publishBudget(ctx context.Context, projection Projection, guard Guard, budget int64) error {
	if s.db == nil || guard == nil {
		return ErrInvalid
	}
	if budget < 1 || budget > MaxWorkspaceEncodedBytes {
		return ErrBudget
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, hash, err := projection.Encode()
	if err != nil {
		return err
	}
	if int64(len(raw)) > budget {
		return ErrBudget
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE page_search_writer_fence SET seq=seq WHERE id=1`)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return fmt.Errorf("native search writer fence unavailable")
	}
	current, err := guard(ctx, tx)
	if err != nil {
		return err
	}
	if !projection.Source.Current(current) {
		return ErrSourceChanged
	}
	var used int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(encoded)),0) FROM page_search_projections WHERE workspace_id=? AND page_id<>?`, projection.Source.WorkspaceID, projection.Source.PageID).Scan(&used); err != nil {
		return err
	}
	if used < 0 || used > budget || int64(len(raw)) > budget-used {
		return ErrBudget
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO page_search_projections
 (workspace_id,page_id,adapter_version,source_type,source_schema,source_rev,source_generation,source_hash,encoded,encoded_sha256)
 VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,page_id) DO UPDATE SET
 adapter_version=excluded.adapter_version,source_type=excluded.source_type,source_schema=excluded.source_schema,
 source_rev=excluded.source_rev,source_generation=excluded.source_generation,source_hash=excluded.source_hash,
 encoded=excluded.encoded,encoded_sha256=excluded.encoded_sha256`, projection.Source.WorkspaceID, projection.Source.PageID,
		AdapterVersion, projection.Source.Type, projection.Source.Schema, projection.Source.Revision, projection.Source.Generation, projection.Source.Hash, raw, hash)
	if err != nil {
		return err
	}
	return tx.Commit()
}
