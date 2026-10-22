package appengine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type Store struct {
	db               *sql.DB
	options          Options
	workersMu        sync.Mutex
	workers          int
	workspaceWorkers map[string]int
	artifactWorkers  map[Key]int
}

func New(db *sql.DB, o Options) *Store {
	if o.MaxWorkers <= 0 {
		o.MaxWorkers = 4
	}
	if o.MaxWorkspaceWorkers <= 0 {
		o.MaxWorkspaceWorkers = 2
	}
	if o.MaxRevisions <= 0 {
		o.MaxRevisions = 1000
	}
	if o.MaxHistoryBytes <= 0 {
		o.MaxHistoryBytes = 64 << 20
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = 30 * time.Second
	}
	return &Store{db: db, options: o, workspaceWorkers: make(map[string]int), artifactWorkers: make(map[Key]int)}
}
func (s *Store) tx(ctx context.Context, k Key, a Authorize, fn func(*sql.Tx, Principal) error) error {
	if !s.options.Enabled {
		return ErrDisabled
	}
	if a == nil || !bounded(k.WorkspaceID) || !bounded(k.PageID) {
		return ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	var synchronous int
	if err = tx.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if synchronous < 2 {
		return fmt.Errorf("%w: node durability requires FULL synchronous storage", ErrInvalid)
	}
	p, err := a(ctx, tx)
	if err != nil {
		return err
	}
	if !bounded(p.AuthorityID) || !bounded(p.MandateID) || len(p.Targets) == 0 {
		return ErrForbidden
	}
	// Every attached format is checked in the same writer-fenced transaction.
	// Reciprocal human initialization uses its existing authority checks rather
	// than triggers on another format's tables.
	occupied, err := hasOtherContent(ctx, tx, k.PageID)
	if err != nil {
		return err
	}
	if occupied {
		return ErrForbidden
	}
	if err = fn(tx, p); err != nil {
		return err
	}
	current, err := a(ctx, tx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p, current) {
		return ErrForbidden
	}
	return tx.Commit()
}
func readState(ctx context.Context, tx *sql.Tx, k Key) (State, error) {
	var s State
	var data []byte
	err := tx.QueryRowContext(ctx, `SELECT schema_version,head_rev,generation,snapshot FROM gridcraft_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&s.SchemaVersion, &s.Rev, &s.Generation, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if s.SchemaVersion != SchemaVersion || len(data) > MaxSnapshotBytes || !json.Valid(data) {
		return State{}, ErrInvalid
	}
	s.Snapshot = json.RawMessage(data)
	return s, nil
}
func (s *Store) Read(ctx context.Context, k Key, a Authorize) (v State, err error) {
	err = s.tx(ctx, k, a, func(tx *sql.Tx, _ Principal) error { var e error; v, e = readState(ctx, tx, k); return e })
	return
}

type reservation struct {
	principal Principal
	state     State
	binding   Binding
	token     int64
	until     time.Time
	receipt   *Receipt
}

func (s *Store) reserve(ctx context.Context, k Key, r Request, a Authorize, e Engine, digest string) (res reservation, err error) {
	err = s.tx(ctx, k, a, func(tx *sql.Tx, p Principal) error {
		if p.MandateID != r.MandateID {
			return ErrForbidden
		}
		res.principal = p
		var oldHash, state string
		var lease int64
		var receipt []byte
		err := tx.QueryRowContext(ctx, `SELECT payload_hash,state,token,lease_until,receipt FROM gridcraft_operations WHERE workspace_id=? AND page_id=? AND authority_id=? AND mutation_id=?`, k.WorkspaceID, k.PageID, p.AuthorityID, r.MutationID).Scan(&oldHash, &state, &res.token, &lease, &receipt)
		now := time.Now().UTC()
		if err == nil {
			if digest != oldHash {
				return ErrConflict
			}
			if state == "applied" {
				var value Receipt
				if err = json.Unmarshal(receipt, &value); err != nil {
					return err
				}
				if value.OperationKey.ArtifactID != k.PageID || value.OperationKey.AuthorityID != p.AuthorityID || value.OperationKey.MutationID != r.MutationID {
					return ErrConflict
				}
				res.receipt = &value
				return nil
			}
			if state == "in_progress" && lease > now.UnixMilli() {
				return ErrInProgress
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !permits(p, commandTargets(r.Command)) {
			return ErrEffects
		}
		v, err := readState(ctx, tx, k)
		if errors.Is(err, ErrNotFound) && r.BaseRev == 0 {
			v = State{SchemaVersion: SchemaVersion}
			if err := eligibleNewWorkbook(ctx, tx, k); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err = admitBase(ctx, tx, k, r, v, writerOf(p, r)); err != nil {
			return err
		}
		var total, workspace, artifact int
		if err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(workspace_id=?),0),coalesce(sum(workspace_id=? AND page_id=?),0) FROM gridcraft_operations WHERE state='in_progress' AND lease_until>?`, k.WorkspaceID, k.WorkspaceID, k.PageID, now.UnixMilli()).Scan(&total, &workspace, &artifact); err != nil {
			return err
		}
		if total >= s.options.MaxWorkers || workspace >= s.options.MaxWorkspaceWorkers || artifact != 0 {
			return ErrCapacity
		}
		res.token++
		res.until = now.Add(s.options.LeaseDuration)
		_, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_operations(workspace_id,page_id,authority_id,mutation_id,payload_hash,state,token,lease_until)
VALUES(?,?,?,?,?,'in_progress',?,?)
ON CONFLICT(workspace_id,page_id,authority_id,mutation_id) DO UPDATE SET
state='in_progress',
token = excluded.token,
lease_until = excluded.lease_until`, k.WorkspaceID, k.PageID, p.AuthorityID, r.MutationID, digest, res.token, res.until.UnixMilli())
		if err != nil {
			return err
		}
		commandBytes, err := json.Marshal(r.Command)
		if err != nil {
			return err
		}
		res.state = v
		res.binding = Binding{k, r.BaseRev, r.Generation, hash(commandBytes), r.MandateID, e.Identity()}
		return nil
	})
	return
}

func (s *Store) Execute(ctx context.Context, k Key, r Request, a Authorize, e Engine) (Receipt, error) {
	if !s.options.Enabled {
		return Receipt{}, ErrDisabled
	}
	if err := r.validate(k); err != nil {
		return Receipt{}, err
	}
	if e == nil || e.Identity() == "" {
		return Receipt{}, ErrInvalid
	}
	digest, err := payloadHash(k, r)
	if err != nil {
		return Receipt{}, err
	}
	res, err := s.reserve(ctx, k, r, a, e, digest)
	if err != nil {
		return Receipt{}, err
	}
	if res.receipt != nil {
		return *res.receipt, nil
	}
	// Cleanup is fenced to THIS attempt. A cancelled request must not retain a live
	// reservation until expiry or delete a newer attempt that already took over.
	defer s.release(k, r, res)
	if !s.admitLiveWorker(k) {
		return Receipt{}, ErrCapacity
	}
	defer s.releaseLiveWorker(k)
	candidateCtx, cancel := context.WithDeadline(ctx, res.until)
	defer cancel()
	c, err := e.Prepare(candidateCtx, res.binding, res.state, r.Command)
	if err != nil {
		return Receipt{}, err
	}
	if err = candidateCtx.Err(); err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	err = s.tx(ctx, k, a, func(tx *sql.Tx, p Principal) error {
		if !reflect.DeepEqual(p, res.principal) {
			return ErrForbidden
		}
		var token, lease int64
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT token,lease_until,state FROM gridcraft_operations WHERE workspace_id=? AND page_id=? AND authority_id=? AND mutation_id=?`, k.WorkspaceID, k.PageID, p.AuthorityID, r.MutationID).Scan(&token, &lease, &status); err != nil {
			return err
		}
		if token != res.token || status != "in_progress" || lease <= time.Now().UnixMilli() {
			return ErrFence
		}
		v, err := readState(ctx, tx, k)
		if errors.Is(err, ErrNotFound) && r.BaseRev == 0 {
			v = State{SchemaVersion: SchemaVersion}
		} else if err != nil {
			return err
		}
		// The candidate was computed on the head seen at reservation. If the
		// head moved since, the client retries with the same mutation ID.
		if v.Rev != res.state.Rev || v.Generation != res.state.Generation {
			return &PreconditionError{HeadRev: v.Rev}
		}
		if c.Binding != res.binding || hash(c.Snapshot) != c.ResultHash || len(c.Snapshot) == 0 || len(c.Snapshot) > MaxSnapshotBytes || !json.Valid(c.Snapshot) {
			return ErrCandidate
		}
		if !permits(p, c.Effects) {
			return ErrEffects
		}
		// The imported blob is inline and therefore reaches node durability in the
		// same FULL-synchronous SQLite transaction as all references and receipts.
		kind, command, err := recordCommand(r.Command)
		if err != nil {
			return err
		}
		rev := v.Rev + 1
		// History keeps a full snapshot only at imports and every
		// CheckpointInterval-th revision; every revision keeps its command.
		checkpoint := kind == "snapshot" || rev%CheckpointInterval == 0
		var checkpoints int
		var historyBytes int64
		if err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(snapshot)),0)+(SELECT coalesce(sum(length(command)),0) FROM gridcraft_audit WHERE page_id=?) FROM gridcraft_revisions WHERE page_id=?`, k.PageID, k.PageID).Scan(&checkpoints, &historyBytes); err != nil {
			return err
		}
		historyBytes += int64(len(command))
		if checkpoint {
			historyBytes += int64(len(c.Snapshot))
		}
		if (checkpoint && checkpoints >= s.options.MaxRevisions) || historyBytes > s.options.MaxHistoryBytes {
			return ErrQuota
		}
		generation := v.Generation
		if generation == 0 {
			generation = 1
		}
		receipt = Receipt{OperationKey: OperationKey{k.PageID, p.AuthorityID, r.MutationID}, Outcome: "applied", Rev: rev, PayloadHash: digest, IssuedBy: "host", ValidatedBy: res.binding.Engine, Effects: append([]string(nil), c.Effects...), Durability: "node", At: tsformat.Format(time.Now().UTC())}
		receiptBytes, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES(?,?,?,?,?,?) ON CONFLICT(page_id) DO UPDATE SET head_rev=excluded.head_rev,snapshot=excluded.snapshot`, k.PageID, k.WorkspaceID, SchemaVersion, rev, generation, []byte(c.Snapshot)); err != nil {
			return err
		}
		if checkpoint {
			if _, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_revisions(page_id,rev,snapshot) VALUES(?,?,?)`, k.PageID, rev, []byte(c.Snapshot)); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_audit(page_id,rev,authority_id,mutation_id,mandate_id,base_rev,receipt,kind,writer,command) VALUES(?,?,?,?,?,?,?,?,?,?)`, k.PageID, rev, p.AuthorityID, r.MutationID, p.MandateID, r.BaseRev, receiptBytes, kind, writerOf(p, r), command); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_outbox(page_id,rev) VALUES(?,?)`, k.PageID, rev); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE gridcraft_operations SET state='applied',receipt=?,lease_until=0 WHERE workspace_id=? AND page_id=? AND authority_id=? AND mutation_id=? AND token=?`, receiptBytes, k.WorkspaceID, k.PageID, p.AuthorityID, r.MutationID, res.token)
		return err
	})
	return receipt, err
}

// admitBase applies cell-level concurrency. A mutation whose base is behind the
// head is rebased onto it only when the generation matches, the window holds no
// import (snapshot revision), the window is at most MaxRebaseWindow revisions
// and no other writer changed one of its target cells. Imports still require
// the exact head.
func admitBase(ctx context.Context, tx *sql.Tx, k Key, r Request, v State, writer string) error {
	if v.Generation != r.Generation || r.BaseRev > v.Rev {
		return &PreconditionError{HeadRev: v.Rev}
	}
	if r.BaseRev == v.Rev {
		return nil
	}
	if r.Command.Kind == "import" {
		return &PreconditionError{HeadRev: v.Rev}
	}
	if v.Rev-r.BaseRev > MaxRebaseWindow {
		return &PreconditionError{HeadRev: v.Rev, SnapshotRequired: true}
	}
	_, record, err := recordCommand(r.Command)
	if err != nil {
		return err
	}
	var mine commandRecord
	if err = json.Unmarshal(record, &mine); err != nil {
		return err
	}
	targets := map[[2]string]bool{}
	for _, c := range mine.Cells {
		targets[[2]string{c.Sheet, c.Cell}] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT rev,kind,writer,authority_id,command FROM gridcraft_audit WHERE page_id=? AND rev>? AND rev<=? ORDER BY rev`, k.PageID, r.BaseRev, v.Rev)
	if err != nil {
		return err
	}
	defer rows.Close()
	latest := map[[2]string]CellConflict{}
	var order [][2]string
	seen := int64(0)
	for rows.Next() {
		var rev int64
		var kind, other, authority string
		var command []byte
		if err = rows.Scan(&rev, &kind, &other, &authority, &command); err != nil {
			return err
		}
		seen++
		if kind != "cells" {
			return &PreconditionError{HeadRev: v.Rev, SnapshotRequired: true}
		}
		var written commandRecord
		if err = json.Unmarshal(command, &written); err != nil {
			return err
		}
		if other == writer {
			continue
		}
		for _, c := range written.Cells {
			key := [2]string{c.Sheet, c.Cell}
			if !targets[key] {
				continue
			}
			if _, ok := latest[key]; !ok {
				order = append(order, key)
			}
			latest[key] = CellConflict{Sheet: c.Sheet, Cell: c.Cell, Input: c.Input, Rev: rev, AuthorityID: authority}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if seen != v.Rev-r.BaseRev {
		return &PreconditionError{HeadRev: v.Rev, SnapshotRequired: true}
	}
	if len(order) > 0 {
		conflict := &PreconditionError{HeadRev: v.Rev}
		for _, key := range order {
			conflict.Conflicts = append(conflict.Conflicts, latest[key])
		}
		return conflict
	}
	return nil
}

func (s *Store) release(k Key, r Request, res reservation) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// No authority is granted by cleanup; it can only release this attempt's
	// uncommitted reservation. Confirmed receipts and replay fences never expire.
	_, _ = s.db.ExecContext(ctx, `UPDATE gridcraft_operations SET state='failed',lease_until=0 WHERE workspace_id=? AND page_id=? AND authority_id=? AND mutation_id=? AND token=? AND state='in_progress'`, k.WorkspaceID, k.PageID, res.principal.AuthorityID, r.MutationID, res.token)
}

// Status authorizes again and separates the immutable receipt from delivery and
// current head. No live transport is installed by this package.
type Status struct {
	Receipt      *Receipt `json:"receipt,omitempty"`
	State        string   `json:"state"`
	HeadRev      int64    `json:"head_rev"`
	EffectInHead string   `json:"effect_in_head"`
	Delivery     string   `json:"delivery"`
}

func (s *Store) Status(ctx context.Context, k Key, mutation string, a Authorize) (out Status, err error) {
	err = s.tx(ctx, k, a, func(tx *sql.Tx, p Principal) error {
		var data []byte
		if e := tx.QueryRowContext(ctx, `SELECT state,receipt FROM gridcraft_operations WHERE workspace_id=? AND page_id=? AND authority_id=? AND mutation_id=?`, k.WorkspaceID, k.PageID, p.AuthorityID, mutation).Scan(&out.State, &data); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		if len(data) == 0 {
			return nil
		}
		var receipt Receipt
		if e := json.Unmarshal(data, &receipt); e != nil {
			return e
		}
		if receipt.OperationKey.ArtifactID != k.PageID || receipt.OperationKey.AuthorityID != p.AuthorityID || receipt.OperationKey.MutationID != mutation {
			return ErrConflict
		}
		out.Receipt = &receipt
		v, e := readState(ctx, tx, k)
		if e != nil {
			return e
		}
		out.HeadRev = v.Rev
		out.EffectInHead = "unknown"
		if v.Rev == receipt.Rev {
			out.EffectInHead = "present"
		}
		if e = tx.QueryRowContext(ctx, `SELECT state FROM gridcraft_outbox WHERE page_id=? AND rev=?`, k.PageID, receipt.Rev).Scan(&out.Delivery); e != nil {
			return fmt.Errorf("outbox: %w", e)
		}
		return nil
	})
	return
}

func eligibleNewWorkbook(ctx context.Context, tx *sql.Tx, k Key) error {
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pages WHERE workspace_id=? AND id=? AND owner_user_id IS NOT NULL AND owner_crew_id IS NULL AND private_audience=1 AND folder_id IS NULL)`, k.WorkspaceID, k.PageID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrForbidden
	}
	occupied, err := hasOtherContent(ctx, tx, k.PageID)
	if err != nil {
		return err
	}
	if occupied {
		return ErrInvalid
	}
	return nil
}

// PageHasNoContent reports whether a Page holds neither a workbook nor any
// other content format, so it may still become a new workbook.
func PageHasNoContent(ctx context.Context, tx *sql.Tx, pageID string) (bool, error) {
	var workbook bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=?)`, pageID).Scan(&workbook); err != nil || workbook {
		return false, err
	}
	occupied, err := hasOtherContent(ctx, tx, pageID)
	return !occupied, err
}

func hasOtherContent(ctx context.Context, tx *sql.Tx, pageID string) (bool, error) {
	for _, table := range []string{"documents", "slides_heads", "page_panels", "page_project_drafts", "page_project_live", "page_artifacts", "page_boards"} {
		var installed, occupied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=? COLLATE NOCASE)`, table).Scan(&installed); err != nil {
			return false, err
		}
		if installed {
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE page_id=?)", pageID).Scan(&occupied); err != nil {
				return false, err
			}
			if occupied {
				return true, nil
			}
		}
	}
	return false, nil
}

// A lease fences commits, but its expiry does not prove the old process has
// stopped. Keep physical admission until Prepare (including cleanup) returns.
// The service owns one Store; restart reconciliation must separately qualify
// and remove orphan workers before activation in a new service incarnation.
func (s *Store) admitLiveWorker(k Key) bool {
	s.workersMu.Lock()
	defer s.workersMu.Unlock()
	if s.workers >= s.options.MaxWorkers || s.workspaceWorkers[k.WorkspaceID] >= s.options.MaxWorkspaceWorkers || s.artifactWorkers[k] != 0 {
		return false
	}
	s.workers++
	s.workspaceWorkers[k.WorkspaceID]++
	s.artifactWorkers[k]++
	return true
}
func (s *Store) releaseLiveWorker(k Key) {
	s.workersMu.Lock()
	defer s.workersMu.Unlock()
	s.workers--
	s.workspaceWorkers[k.WorkspaceID]--
	if s.workspaceWorkers[k.WorkspaceID] == 0 {
		delete(s.workspaceWorkers, k.WorkspaceID)
	}
	delete(s.artifactWorkers, k)
}
