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
	// windowScanned, when set by tests, observes how many audit rows a rebase
	// window scan read and whether it ran inside the writer transaction.
	windowScanned func(inWriter bool, rows int64)
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
	err := tx.QueryRowContext(ctx, `SELECT schema_version,head_rev,generation,snapshot,coalesce((SELECT max(rev) FROM gridcraft_audit a WHERE a.page_id=gridcraft_heads.page_id AND kind='snapshot'),0) FROM gridcraft_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&s.SchemaVersion, &s.Rev, &s.Generation, &data, &s.StructureRev)
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
	pre := s.prescan(ctx, k, r, a)
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
		if err = s.admitBase(ctx, tx, k, r, v, p, pre); err != nil {
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
	// The recorded command is bounded before any reservation or engine run.
	if _, _, err := recordCommand(r.Command); err != nil {
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
		return Receipt{}, s.interrupted(ctx, k, a, err)
	}
	if err = candidateCtx.Err(); err != nil {
		return Receipt{}, s.interrupted(ctx, k, a, err)
	}
	// Default-off test hook (build tag crewship_testbarrier). It only delays the
	// commit below; every check in that transaction still runs afterwards.
	if err = testBarrier(ctx, barrierPreCommit, k, res.principal.AuthorityID, r.MutationID); err != nil {
		return Receipt{}, s.interrupted(ctx, k, a, err)
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
		if err := checkReadSet(ctx, tx, k, r, v, p); err != nil {
			return err
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
		fits := func() (bool, error) {
			var checkpoints int
			var historyBytes int64
			if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(snapshot)),0)+(SELECT coalesce(sum(length(command)),0) FROM gridcraft_audit WHERE page_id=?) FROM gridcraft_revisions WHERE page_id=?`, k.PageID, k.PageID).Scan(&checkpoints, &historyBytes); err != nil {
				return false, err
			}
			historyBytes += int64(len(command))
			if checkpoint {
				historyBytes += int64(len(c.Snapshot))
			}
			return !(checkpoint && checkpoints >= s.options.MaxRevisions) && historyBytes <= s.options.MaxHistoryBytes, nil
		}
		ok, err := fits()
		if err != nil {
			return err
		}
		if !ok {
			// Confirmed history is retained until an explicit retention action.
			// A write must never buy capacity by discarding earlier revisions.
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
		// The audit keeps the receipt and adds the stored result digest and any
		// verified recalculation outside the mandated effects.
		auditBytes, err := json.Marshal(auditRecord{Receipt: receipt, ResultSHA256: c.ResultHash, Derived: c.Derived})
		if err != nil {
			return err
		}
		// Only the first revision creates the head, so the personal-Page insert
		// trigger keeps guarding creation. Later revisions update in place: an
		// upsert would run that insert trigger again and refuse every write
		// after the owner explicitly shared the Page.
		if v.Rev == 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES(?,?,?,?,?,?)`, k.PageID, k.WorkspaceID, SchemaVersion, rev, generation, []byte(c.Snapshot))
		} else {
			var updated sql.Result
			if updated, err = tx.ExecContext(ctx, `UPDATE gridcraft_heads SET head_rev=?,snapshot=? WHERE workspace_id=? AND page_id=? AND head_rev=?`, rev, []byte(c.Snapshot), k.WorkspaceID, k.PageID, v.Rev); err == nil {
				if n, e := updated.RowsAffected(); e != nil || n != 1 {
					err = errors.Join(e, ErrPrecondition)
				}
			}
		}
		if err != nil {
			return err
		}
		if checkpoint {
			if _, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_revisions(page_id,rev,snapshot) VALUES(?,?,?)`, k.PageID, rev, []byte(c.Snapshot)); err != nil {
				return err
			}
		}
		// The result hash lets a revision rebuilt from a checkpoint and the
		// recorded commands be verified, not just assumed, identical. The receipt
		// column keeps the audit form with any verified derived recalculation.
		if _, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_audit(page_id,rev,authority_id,mutation_id,mandate_id,base_rev,receipt,kind,writer,command,result_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, k.PageID, rev, p.AuthorityID, r.MutationID, p.MandateID, r.BaseRev, auditBytes, kind, writerOf(p, r), command, c.ResultHash); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO gridcraft_outbox(page_id,rev) VALUES(?,?)`, k.PageID, rev); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE gridcraft_operations SET state='applied',receipt=?,lease_until=0 WHERE workspace_id=? AND page_id=? AND authority_id=? AND mutation_id=? AND token=?`, receiptBytes, k.WorkspaceID, k.PageID, p.AuthorityID, r.MutationID, res.token)
		return err
	})
	if err != nil {
		return Receipt{}, s.interrupted(ctx, k, a, err)
	}
	if err = testBarrier(ctx, barrierPostCommit, k, res.principal.AuthorityID, r.MutationID); err != nil {
		// Committed; only the acknowledgement is lost. A retry returns the receipt.
		return Receipt{}, err
	}
	return receipt, nil
}

// interrupted classifies a failure that happened after the request context
// ended. Revoking a credential cancels its in-flight request, which engine
// transports report as unavailable; the caller must instead learn that its
// authority is gone. Authorization is re-evaluated read-only (no writer
// fence, rolled back) on a detached, bounded context purely to choose the
// error: nothing is written or granted.
func (s *Store) interrupted(ctx context.Context, k Key, a Authorize, err error) error {
	if ctx.Err() == nil {
		return err
	}
	check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	authErr := s.recheckAuthority(check, k, a)
	if errors.Is(authErr, ErrUnauthenticated) || errors.Is(authErr, ErrForbidden) || errors.Is(authErr, ErrNotFound) {
		return authErr
	}
	// Still authorized: the caller went away. Keep both causes in the chain.
	return fmt.Errorf("%w: operation interrupted: %w", ctx.Err(), err)
}

func (s *Store) recheckAuthority(ctx context.Context, k Key, a Authorize) error {
	if a == nil || !bounded(k.WorkspaceID) || !bounded(k.PageID) {
		return ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = a(ctx, tx)
	return err
}

// pruneHistory drops history that replay from checkpoint keep no longer needs:
// checkpoints before keep and the commands of revisions up to keep. Audit rows
// stay, with writer, receipt and result hash; a pruned row becomes a snapshot
// revision (unknown cells), so a window scan that reaches it requires a reread.
// This helper is never invoked by the commit path.
func pruneHistory(ctx context.Context, tx *sql.Tx, page string, keep int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM gridcraft_revisions WHERE page_id=? AND rev<?`, page, keep); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE gridcraft_audit SET kind='snapshot',command=NULL WHERE page_id=? AND rev<=? AND command IS NOT NULL`, page, keep)
	return err
}

type cellKey [2]string

// windowScan is what the audit says about a rebase window (base, to] for one
// writer and its target cells. Scans extend it revision by revision, so the
// part read before the writer transaction is reused inside it.
type windowScan struct {
	base, to   int64
	writer     string
	targets    map[cellKey]bool
	seen       int64
	unknown    bool
	latest     map[cellKey]CellConflict
	conflicted map[cellKey]bool
	order      []cellKey
}

func newWindowScan(r Request, writer string) *windowScan {
	w := &windowScan{base: r.BaseRev, to: r.BaseRev, writer: writer, targets: map[cellKey]bool{}, latest: map[cellKey]CellConflict{}, conflicted: map[cellKey]bool{}}
	for _, c := range commandCells(r.Command) {
		w.targets[cellKey{c.Sheet, c.Cell}] = true
	}
	return w
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// extend reads revisions (w.to, to]. A conflict needs another writer's write to
// a target cell; it reports the cell's latest write in the window by anyone,
// which with no snapshot revision in between is the cell's input in the head.
func (w *windowScan) extend(ctx context.Context, q queryer, page string, to int64) (int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT rev,kind,writer,authority_id,command FROM gridcraft_audit WHERE page_id=? AND rev>? AND rev<=? ORDER BY rev`, page, w.to, to)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	read := int64(0)
	for rows.Next() && !w.unknown {
		var rev int64
		var kind, other, authority string
		var command []byte
		if err = rows.Scan(&rev, &kind, &other, &authority, &command); err != nil {
			return read, err
		}
		read++
		w.seen++
		if kind != "cells" {
			w.unknown = true
			break
		}
		var written commandRecord
		if err = json.Unmarshal(command, &written); err != nil {
			return read, err
		}
		for _, c := range written.Cells {
			key := cellKey{c.Sheet, c.Cell}
			if !w.targets[key] {
				continue
			}
			w.latest[key] = CellConflict{Sheet: c.Sheet, Cell: c.Cell, Input: c.Input, Rev: rev, AuthorityID: authority}
			if other != w.writer && !w.conflicted[key] {
				w.conflicted[key] = true
				w.order = append(w.order, key)
			}
		}
	}
	if err = rows.Err(); err != nil {
		return read, err
	}
	w.to = to
	return read, nil
}

// prescan reads and parses the rebase window before the writer transaction,
// which holds the database-wide write lock. It runs only for an authorized
// principal, in a separate read transaction, and every fact it collects is
// immutable once committed; admitBase then scans only revisions committed since.
// Any failure just leaves the whole scan to admitBase.
func (s *Store) prescan(ctx context.Context, k Key, r Request, a Authorize) *windowScan {
	if !s.options.Enabled || a == nil || r.BaseRev < 1 || r.Command.Kind == "import" {
		return nil
	}
	// ReadOnly matters: the database runs with _txlock=immediate, which makes
	// every other transaction take the database-wide write lock at BEGIN.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	p, err := a(ctx, tx)
	if err != nil || p.MandateID != r.MandateID || !permits(p, commandTargets(r.Command)) || r.BaseRev < p.HistoryFloor {
		return nil
	}
	var head, generation int64
	if tx.QueryRowContext(ctx, `SELECT head_rev,generation FROM gridcraft_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&head, &generation) != nil {
		return nil
	}
	if generation != r.Generation || head <= r.BaseRev || head-r.BaseRev > MaxRebaseWindow {
		return nil
	}
	w := newWindowScan(r, writerOf(p, r))
	read, err := w.extend(ctx, tx, k.PageID, head)
	if s.windowScanned != nil {
		s.windowScanned(false, read)
	}
	if err != nil {
		return nil
	}
	return w
}

// admitBase applies cell-level concurrency. A mutation whose base is behind the
// head is rebased onto it only when the generation matches, the window holds no
// import (snapshot revision), the window is at most MaxRebaseWindow revisions
// and no other writer changed one of its target cells. Imports still require
// the exact head. Every rejection that a retry of the same packet cannot fix
// requires a fresh snapshot.
func (s *Store) admitBase(ctx context.Context, tx *sql.Tx, k Key, r Request, v State, p Principal, pre *windowScan) error {
	if err := checkReadSet(ctx, tx, k, r, v, p); err != nil {
		return err
	}
	reread := &PreconditionError{HeadRev: v.Rev, SnapshotRequired: true}
	// A base below the principal's history floor would rebase over revisions
	// the principal may not read; their conflicts would disclose that history.
	if v.Generation != r.Generation || r.BaseRev > v.Rev || r.BaseRev < p.HistoryFloor {
		return reread
	}
	writer := writerOf(p, r)
	if r.BaseRev == v.Rev {
		return nil
	}
	if r.Command.Kind == "import" || v.Rev-r.BaseRev > MaxRebaseWindow {
		return reread
	}
	w := pre
	if w == nil || w.base != r.BaseRev || w.writer != writer || w.to > v.Rev {
		w = newWindowScan(r, writer)
	} else if w.to > w.base {
		// A forced checkpoint may have pruned the prescanned rows since; the
		// edit must then reread exactly as if it had scanned them here.
		var pruned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gridcraft_audit WHERE page_id=? AND rev>? AND rev<=? AND kind!='cells')`, k.PageID, w.base, w.to).Scan(&pruned); err != nil {
			return err
		}
		if pruned {
			return reread
		}
	}
	read, err := w.extend(ctx, tx, k.PageID, v.Rev)
	if s.windowScanned != nil {
		s.windowScanned(true, read)
	}
	if err != nil {
		return err
	}
	if w.unknown || w.seen != v.Rev-r.BaseRev {
		return reread
	}
	if len(w.order) > 0 {
		conflict := &PreconditionError{HeadRev: v.Rev}
		for _, key := range w.order {
			conflict.Conflicts = append(conflict.Conflicts, w.latest[key])
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
	for _, table := range []string{"documents", "slides_heads", "page_panels", "page_project_drafts", "page_project_live", "page_artifacts", "page_boards", "mooring_documents", "artifact_version_heads"} {
		var installed, occupied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=? COLLATE NOCASE)`, table).Scan(&installed); err != nil {
			return false, err
		}
		if installed {
			query := "SELECT EXISTS(SELECT 1 FROM " + table + " WHERE page_id=?)"
			if table == "artifact_version_heads" {
				// Native history is part of its existing workbook, never an
				// alternative format. An orphaned or foreign-family head still
				// blocks initialization and every operation on another engine.
				query = `SELECT EXISTS(SELECT 1 FROM artifact_version_heads v WHERE v.page_id=? AND (v.content_type<>'gridcraft' OR NOT EXISTS(SELECT 1 FROM gridcraft_heads g WHERE g.page_id=v.page_id AND g.workspace_id=v.workspace_id)))`
			}
			if err := tx.QueryRowContext(ctx, query, pageID).Scan(&occupied); err != nil {
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

type auditRecord struct {
	Receipt
	ResultSHA256 string   `json:"result_sha256"`
	Derived      *Derived `json:"derived,omitempty"`
}
