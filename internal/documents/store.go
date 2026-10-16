// Package documents persists validated document states. Callers must validate Yjs
// payloads and projections before passing them here; this package never merges CRDTs.
package documents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotFound  = errors.New("document not found")
	ErrConflict  = errors.New("document revision conflict")
	ErrFence     = errors.New("document writer fenced")
	ErrQuota     = errors.New("document quota exceeded")
	ErrSchema    = errors.New("unsupported document schema")
	ErrDuplicate = errors.New("idempotency key payload mismatch")
)

type Key struct {
	WorkspaceID string
	PageID      string
}

// Authorize checks current page authority in the same transaction as the operation.
// The returned actor is the sole source of persisted audit identity.
type Authorize func(context.Context, *sql.Tx) (string, error)
type Options struct {
	MaxDocumentBytes, MaxLogBytes, MaxHistoryBytes, MaxCommits, MaxDocuments int
	LeaseDuration                                                            time.Duration
}
type Store struct {
	db   *sql.DB
	opts Options
}
type Lease struct {
	Owner, Writer string
	Epoch         int64
	ExpiresAt     time.Time
	Generation    int64
}
type Update struct {
	Rev     int64
	Payload []byte
}
type State struct {
	Schema                                  int
	HeadRev, Epoch, SnapshotRev, Generation int64
	Snapshot                                []byte
	Projection                              json.RawMessage
	Updates                                 []Update
}

// MutationGuard enforces application-specific immutable request bounds inside the writer transaction.
type MutationGuard func(context.Context, *sql.Tx, string, State) error
type Commit struct {
	Guard                     MutationGuard
	Lease                     Lease
	ID, Fingerprint           string
	IfRev                     int64
	IfGeneration              *int64
	Schema                    int
	Update, CandidateSnapshot []byte
	Projection                json.RawMessage
	IfHashes                  map[string]string
	Kind                      string
}
type Receipt struct {
	ID          string
	Rev, Epoch  int64
	Actor, Kind string
}
type Checkpoint struct {
	ID         string
	Rev        int64
	Schema     int
	Snapshot   []byte
	Projection json.RawMessage
	Actor      string
}

func New(db *sql.DB, o Options) *Store {
	if o.MaxDocumentBytes <= 0 {
		o.MaxDocumentBytes = 1 << 20
	}
	if o.MaxLogBytes <= 0 {
		o.MaxLogBytes = 256 << 10
	}
	if o.MaxHistoryBytes <= 0 {
		o.MaxHistoryBytes = 16 << 20
	}
	if o.MaxCommits <= 0 {
		o.MaxCommits = 10000
	}
	if o.MaxDocuments <= 0 {
		o.MaxDocuments = 128
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = 30 * time.Second
	}
	return &Store{db, o}
}
func (s *Store) tx(ctx context.Context, k Key, a Authorize, fn func(*sql.Tx, string) error) error {
	if k.WorkspaceID == "" || k.PageID == "" || a == nil {
		return errors.New("document key and authority required")
	}
	t, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	// A write before authorization establishes SQLite's writer fence, including
	// reads and receipt replays, preventing authority changes between check/use.
	if _, e = t.ExecContext(ctx, "UPDATE document_writer_fence SET seq=seq WHERE id=1"); e != nil {
		return e
	}
	actor, e := a(ctx, t)
	if e != nil {
		return e
	}
	if actor == "" {
		return errors.New("empty authorized actor")
	}
	if e = fn(t, actor); e != nil {
		return e
	}
	return t.Commit()
}
func state(ctx context.Context, t *sql.Tx, k Key) (v State, e error) {
	e = t.QueryRowContext(ctx, "SELECT schema_version,head_rev,epoch,snapshot_rev,snapshot,projection,generation FROM documents WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&v.Schema, &v.HeadRev, &v.Epoch, &v.SnapshotRev, &v.Snapshot, &v.Projection, &v.Generation)
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrNotFound
	}
	return
}
func (s *Store) Read(ctx context.Context, k Key, a Authorize) (v State, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, _ string) error {
		var err error
		v, err = state(ctx, t, k)
		if err != nil {
			return err
		}
		r, err := t.QueryContext(ctx, "SELECT rev,payload FROM document_updates WHERE workspace_id=? AND page_id=? ORDER BY rev", k.WorkspaceID, k.PageID)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var u Update
			if err = r.Scan(&u.Rev, &u.Payload); err != nil {
				return err
			}
			expected := v.SnapshotRev + int64(len(v.Updates)) + 1
			if u.Rev != expected {
				return errors.New("document replay log is not contiguous")
			}
			v.Updates = append(v.Updates, u)
		}
		if err := r.Err(); err != nil {
			return err
		}
		if v.SnapshotRev+int64(len(v.Updates)) != v.HeadRev {
			return errors.New("document replay log does not cover durable head")
		}
		return nil
	})
	return
}
func receipt(ctx context.Context, t *sql.Tx, k Key, id string) (r Receipt, d string, e error) {
	r.ID = id
	e = t.QueryRowContext(ctx, "SELECT digest,rev,epoch,actor,kind FROM document_receipts WHERE workspace_id=? AND page_id=? AND id=?", k.WorkspaceID, k.PageID, id).Scan(&d, &r.Rev, &r.Epoch, &r.Actor, &r.Kind)
	return
}
