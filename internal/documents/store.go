// Package documents persists validated document states. Callers must validate Yjs
// payloads and projections before passing them here; this package never merges CRDTs.
package documents

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
type Commit struct {
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
func (s *Store) valid(schema int, snap []byte, p json.RawMessage) error {
	if schema != 1 && schema != 2 {
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
		if err := t.QueryRowContext(ctx, "SELECT count(*) FROM documents").Scan(&n); err != nil {
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
	})
	return
}
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
		if v.Schema != 1 && v.Schema != 2 {
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
		if v.Schema != 1 && v.Schema != 2 {
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
func digest(c Commit) (string, error) { // Include every semantic payload and precondition; never trust client digest alone.
	b, e := json.Marshal(struct {
		ID, Fingerprint  string
		IfRev            int64
		Schema           int
		Update, Snapshot []byte
		Projection       json.RawMessage
		Hashes           map[string]string
		Kind             string
	}{c.ID, c.Fingerprint, c.IfRev, c.Schema, c.Update, c.CandidateSnapshot, c.Projection, c.IfHashes, c.Kind})
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func receipt(ctx context.Context, t *sql.Tx, k Key, id string) (r Receipt, d string, e error) {
	r.ID = id
	e = t.QueryRowContext(ctx, "SELECT digest,rev,epoch,actor,kind FROM document_receipts WHERE workspace_id=? AND page_id=? AND id=?", k.WorkspaceID, k.PageID, id).Scan(&d, &r.Rev, &r.Epoch, &r.Actor, &r.Kind)
	return
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
		if (v.Schema != 1 && v.Schema != 2) || c.Schema < v.Schema {
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
		var count, logCount, logBytes int
		if err = t.QueryRowContext(ctx, "SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&count); err != nil {
			return err
		}
		if count >= s.opts.MaxCommits {
			return ErrQuota
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
func (s *Store) Checkpoint(ctx context.Context, k Key, l Lease, id string, snapshot []byte, ifRev int64, a Authorize) (Checkpoint, error) {
	return s.CheckpointRequest(ctx, k, l, id, "internal-checkpoint", snapshot, ifRev, a)
}

// CheckpointRequest retains the original semantic fingerprint for service retries.
func (s *Store) CheckpointRequest(ctx context.Context, k Key, l Lease, id, fingerprint string, snapshot []byte, ifRev int64, a Authorize) (Checkpoint, error) {
	return s.CheckpointRequestAtGeneration(ctx, k, l, id, fingerprint, snapshot, ifRev, nil, a)
}
func (s *Store) CheckpointRequestAtGeneration(ctx context.Context, k Key, l Lease, id, fingerprint string, snapshot []byte, ifRev int64, generation *int64, a Authorize) (c Checkpoint, e error) {
	e = s.tx(ctx, k, a, func(t *sql.Tx, actor string) error {
		if id == "" || len(id) > 256 || len(fingerprint) > 256 {
			return errors.New("checkpoint id required")
		}

		raw, _ := json.Marshal(struct {
			ID, Fingerprint string
			Snapshot        []byte
			IfRev           int64
		}{id, fingerprint, snapshot, ifRev})
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
			if current.Schema != 1 {
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
		if err = s.valid(v.Schema, snapshot, v.Projection); err != nil {
			return err
		}
		var count int
		if err = t.QueryRowContext(ctx, "SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&count); err != nil {
			return err
		}
		if count >= s.opts.MaxCommits {
			return ErrQuota
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
		}{id, fingerprint, ifRev})
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
		if v.Schema != 1 && v.Schema != 2 {
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
		var count int
		if err = t.QueryRowContext(ctx, "SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=?", k.WorkspaceID, k.PageID).Scan(&count); err != nil {
			return err
		}
		if count >= s.opts.MaxCommits {
			return ErrQuota
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

// LookupReceipt authorizes even a replay and compares the original semantic
// request fingerprint supplied by the trusted document service. Commit also
// checks its independently computed full payload digest to prevent ID reuse.
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
