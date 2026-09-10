package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Retention for the delivery ledger, from §6.
//
// Two clocks, and they are not the same clock:
//
//   - dedup keys and receipts live at least 30 days from acceptance AND for the
//     whole non-terminal life of the work. Whichever is longer wins, so a work
//     item that has been queued for six weeks still deduplicates a re-delivery.
//   - raw bodies live at least until the work is terminal, and then for their
//     own retention, 7 days by default.
//
// The rule that does the real work in both is the second half: a non-terminal
// work item's payload is NEVER removed. Not when its nominal expiry passes, not
// under disk pressure, not to make room for new acceptance — §6 says refuse the
// new work instead. Every statement below therefore carries the terminal-state
// predicate in the WHERE clause itself rather than trusting the candidate scan
// that selected the row, because between the scan and the write the work may
// have been resurrected (needs_reconciliation resolves back to queued) and the
// scan's answer is already stale.
//
// # Expiry is a tombstone, not a NULL
//
// After a raw body expires a manual replay is unavailable, and §6 requires the
// UI to explain that rather than offer a button that cannot work. A bare
// `raw_body = NULL` cannot carry that explanation: it is indistinguishable from
// a delivery that never had a payload stored at all.
//
// So the sweep does not just clear the blob, it STAMPS raw_body_expires_at with
// the deadline it enforced. That makes the pair a tombstone with a defined
// reading, which PayloadStatus decodes:
//
//	raw_body NOT NULL                             -> replay available
//	raw_body NULL, raw_body_expires_at NOT NULL   -> had one, expired then
//	raw_body NULL, raw_body_expires_at NULL       -> never stored one
//
// This uses only columns the ledger migration already has; it needs no schema
// change, and it means the answer survives in the row rather than living in a
// log line nobody kept.
type RetentionPolicy struct {
	// DedupWindow is the minimum life of a dedup key and its receipt, measured
	// from acceptance. §6's default is 30 days. It is a FLOOR: a delivery whose
	// stored dedup_expires_at is further out keeps the later of the two.
	DedupWindow time.Duration

	// RawBodyAfterTerminal is how long a payload survives its work reaching a
	// terminal state. §6's default is 7 days. Also a floor, for the same reason:
	// a delivery carrying an explicit raw_body_expires_at can push the deadline
	// out, never pull it in, because §6 states the 7 days as the default
	// retention and the terminal state as the minimum.
	RawBodyAfterTerminal time.Duration

	// Batch bounds one write transaction. Small on purpose: the sweeper competes
	// for the single SQLite writer with the acceptance path, which has a 2 s
	// budget it must answer inside (§5). Walking happens outside any
	// transaction; only the batched writes take the lock, and only briefly.
	Batch int
}

// DefaultRawBodyRetention is §6's: after the work is terminal, the payload lives
// a further 7 days by default.
const DefaultRawBodyRetention = 7 * 24 * time.Hour

// DefaultRetentionBatch is how many rows one sweep transaction touches.
const DefaultRetentionBatch = 200

// DefaultRetentionPolicy returns §6's numbers.
func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{
		DedupWindow:          DefaultDedupWindow,
		RawBodyAfterTerminal: DefaultRawBodyRetention,
		Batch:                DefaultRetentionBatch,
	}
}

// normalized fills zero values with the defaults and refuses a negative window,
// which would otherwise mean "delete everything immediately".
func (p RetentionPolicy) normalized() RetentionPolicy {
	if p.DedupWindow <= 0 {
		p.DedupWindow = DefaultDedupWindow
	}
	if p.RawBodyAfterTerminal <= 0 {
		p.RawBodyAfterTerminal = DefaultRawBodyRetention
	}
	if p.Batch <= 0 {
		p.Batch = DefaultRetentionBatch
	}
	return p
}

// sqlTerminal is the terminal state set, derived from State.Terminal() rather
// than typed out again. work.go explains why that matters: the first version of
// this package wrote a state list inline in four queries and one of them
// disagreed with the other three.
var sqlTerminal = stateList(func(s State) bool { return s.Terminal() })

// SweepResult reports what one sweep actually did. Counts, not just an error:
// "retention ran" is not evidence that retention worked, and an operator asking
// why the database is growing needs to see a zero here.
type SweepResult struct {
	// RawBodiesExpired is how many payloads were dropped and tombstoned.
	RawBodiesExpired int
	// RawBodyBytesFreed is the sum of body_bytes for those payloads.
	RawBodyBytesFreed int64
	// DeliveriesRemoved is how many delivery rows — dedup key and receipt
	// together — passed both windows and were deleted.
	DeliveriesRemoved int
	// RawBodiesRetained counts candidates that were examined and deliberately
	// kept: still inside a window, or still non-terminal. It is the number that
	// distinguishes "nothing was due" from "the predicate is wrong".
	RawBodiesRetained int
	// Batches is how many write transactions were taken.
	Batches int
}

// Sweep enforces the retention policy once and reports what it did.
//
// It is safe to run concurrently with acceptance. The walk is a plain read
// outside any transaction — WAL readers do not block the writer — and each
// batch of writes is its own short transaction. The sweeper never holds the
// write lock while scanning, which is the thing that would push the acceptance
// path past its 2 s budget.
//
// Order matters: payloads are expired before delivery rows are deleted, so a
// row that crosses both thresholds in the same sweep is counted once as a
// removal rather than half-counted as both.
func (s *Store) Sweep(ctx context.Context, p RetentionPolicy) (SweepResult, error) {
	p = p.normalized()
	now := s.now().UTC()

	var res SweepResult
	if err := s.sweepRawBodies(ctx, p, now, &res); err != nil {
		return res, err
	}
	if err := s.sweepDedup(ctx, p, now, &res); err != nil {
		return res, err
	}
	return res, nil
}

// rawBodyCandidate is one row the walk found, with everything the deadline
// arithmetic needs. The blob itself is never selected: it is the large column,
// and the decision does not depend on its contents.
type rawBodyCandidate struct {
	id        string
	bodyBytes int64
	// terminalRef is when the retention clock starts: the work's terminal_at,
	// or — for a delivery the filter ignored, which has no work at all — the
	// time it was received.
	terminalRef time.Time
	// explicitExpiry is the delivery's own raw_body_expires_at, if it carries
	// one. Zero means it does not.
	explicitExpiry time.Time
}

// sweepRawBodies drops payloads whose work is terminal and whose retention has
// run out.
func (s *Store) sweepRawBodies(ctx context.Context, p RetentionPolicy, now time.Time, res *SweepResult) error {
	cutoff := now.Add(-p.RawBodyAfterTerminal)
	after := ""
	for {
		batch, err := s.walkRawBodyCandidates(ctx, p.Batch, after)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		after = batch[len(batch)-1].id

		var due []rawBodyCandidate
		var deadlines []time.Time
		for _, c := range batch {
			deadline := c.terminalRef.Add(p.RawBodyAfterTerminal)
			// An explicit expiry can only push the deadline out. §6 makes the
			// 7 days a default retention and the terminal state a minimum, so a
			// row asking for a shorter life does not get one.
			if c.explicitExpiry.After(deadline) {
				deadline = c.explicitExpiry
			}
			if deadline.After(now) {
				res.RawBodiesRetained++
				continue
			}
			due = append(due, c)
			deadlines = append(deadlines, deadline)
		}
		if len(due) > 0 {
			if err := s.expireRawBodyBatch(ctx, due, deadlines, cutoff, now, res); err != nil {
				return err
			}
		}
		if len(batch) < p.Batch {
			return nil
		}
	}
}

// walkRawBodyCandidates reads the next page of stored payloads whose work is
// already terminal, keyed on id so a page whose rows are all retained does not
// make the walk loop forever on the same prefix.
//
// This runs outside a transaction on purpose. It is the long part of the sweep,
// and holding the writer across it is exactly the stall §5's acceptance budget
// cannot absorb.
func (s *Store) walkRawBodyCandidates(ctx context.Context, limit int, after string) ([]rawBodyCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.body_bytes, d.raw_body_expires_at,
		       COALESCE(w.terminal_at, w.updated_at, d.received_at)
		FROM webhook_deliveries d
		LEFT JOIN work_items w ON w.id = d.work_id
		WHERE d.raw_body IS NOT NULL
		  AND d.id > ?
		  AND (d.work_id IS NULL OR w.state IN (`+sqlTerminal+`))
		ORDER BY d.id
		LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("work: walk raw body candidates: %w", err)
	}
	defer rows.Close()

	var out []rawBodyCandidate
	for rows.Next() {
		var c rawBodyCandidate
		var explicit sql.NullString
		var ref string
		if err := rows.Scan(&c.id, &c.bodyBytes, &explicit, &ref); err != nil {
			return nil, fmt.Errorf("work: scan raw body candidate: %w", err)
		}
		c.terminalRef = parseTime(ref)
		if explicit.Valid {
			c.explicitExpiry = parseTime(explicit.String)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("work: walk raw body candidates: %w", err)
	}
	return out, nil
}

// expireRawBodyBatch clears one batch of payloads inside one short transaction.
//
// Each statement re-states the whole guard. The candidate list is advisory: it
// was read outside a transaction and the work behind a row may have moved since
// — needs_reconciliation resolving back to queued is a real edge in the state
// machine, and a payload that becomes non-terminal again must survive. The
// terminal predicate below, not the scan, is what makes that true.
func (s *Store) expireRawBodyBatch(ctx context.Context, due []rawBodyCandidate, deadlines []time.Time, cutoff, now time.Time, res *SweepResult) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("work: begin raw body expiry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for i, c := range due {
		out, err := tx.ExecContext(ctx, `
			UPDATE webhook_deliveries
			SET raw_body = NULL, raw_body_expires_at = ?
			WHERE id = ?
			  AND raw_body IS NOT NULL
			  AND (
			        (work_id IS NULL AND received_at <= ?)
			     OR EXISTS (
			            SELECT 1 FROM work_items w
			            WHERE w.id = webhook_deliveries.work_id
			              AND w.state IN (`+sqlTerminal+`)
			              AND COALESCE(w.terminal_at, w.updated_at) <= ?)
			  )
			  AND (raw_body_expires_at IS NULL OR raw_body_expires_at <= ?)`,
			tsformat.Format(deadlines[i]), c.id,
			tsformat.Format(cutoff), tsformat.Format(cutoff), tsformat.Format(now))
		if err != nil {
			return fmt.Errorf("work: expire raw body: %w", err)
		}
		n, err := out.RowsAffected()
		if err != nil {
			return fmt.Errorf("work: expire raw body rows: %w", err)
		}
		if n == 0 {
			// The guard refused it. That is the correct outcome, not an error:
			// the work is no longer terminal, or another sweeper got there
			// first.
			res.RawBodiesRetained++
			continue
		}
		res.RawBodiesExpired += int(n)
		res.RawBodyBytesFreed += c.bodyBytes
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("work: commit raw body expiry: %w", err)
	}
	res.Batches++
	return nil
}

// sweepDedup deletes delivery rows — dedup key and receipt together — that have
// outlived both halves of §6's rule: the 30 days from acceptance, and the
// non-terminal life of the work they produced.
func (s *Store) sweepDedup(ctx context.Context, p RetentionPolicy, now time.Time, res *SweepResult) error {
	// The floor from acceptance, and the row's own stored expiry. Both must have
	// passed; the later one governs.
	accepted := tsformat.Format(now.Add(-p.DedupWindow))
	nowText := tsformat.Format(now)

	after := ""
	for {
		ids, err := s.walkDedupCandidates(ctx, p.Batch, after, accepted, nowText)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		after = ids[len(ids)-1]

		n, err := s.deleteDedupBatch(ctx, ids, accepted, nowText)
		if err != nil {
			return err
		}
		res.DeliveriesRemoved += n
		res.Batches++
		if len(ids) < p.Batch {
			return nil
		}
	}
}

func (s *Store) walkDedupCandidates(ctx context.Context, limit int, after, accepted, nowText string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id FROM webhook_deliveries d
		WHERE d.id > ?
		  AND d.dedup_expires_at <= ?
		  AND d.received_at <= ?
		  AND (d.work_id IS NULL OR EXISTS (
		        SELECT 1 FROM work_items w
		        WHERE w.id = d.work_id AND w.state IN (`+sqlTerminal+`)))
		ORDER BY d.id
		LIMIT ?`, after, nowText, accepted, limit)
	if err != nil {
		return nil, fmt.Errorf("work: walk dedup candidates: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("work: scan dedup candidate: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("work: walk dedup candidates: %w", err)
	}
	return out, nil
}

// deleteDedupBatch removes one page of expired deliveries in one short
// transaction, restating the guard so a work item that went non-terminal
// between the walk and the write keeps its dedup key.
func (s *Store) deleteDedupBatch(ctx context.Context, ids []string, accepted, nowText string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("work: begin dedup delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	args := make([]any, 0, len(ids)+2)
	args = append(args, nowText, accepted)
	placeholders := ""
	for i, id := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}

	out, err := tx.ExecContext(ctx, `
		DELETE FROM webhook_deliveries
		WHERE dedup_expires_at <= ?
		  AND received_at <= ?
		  AND id IN (`+placeholders+`)
		  AND (work_id IS NULL OR EXISTS (
		        SELECT 1 FROM work_items w
		        WHERE w.id = webhook_deliveries.work_id AND w.state IN (`+sqlTerminal+`)))`,
		args...)
	if err != nil {
		return 0, fmt.Errorf("work: delete expired deliveries: %w", err)
	}
	n, err := out.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("work: deleted delivery rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("work: commit dedup delete: %w", err)
	}
	return int(n), nil
}

// PayloadStatus says whether a delivery can still be replayed, and — when it
// cannot — whether that is because the payload expired or because there never
// was one. §6 requires the UI to explain an unavailable replay, and it cannot
// explain what the row does not record.
type PayloadStatus string

const (
	// PayloadAvailable means the raw body is still stored and a manual replay
	// can use it.
	PayloadAvailable PayloadStatus = "available"
	// PayloadExpired means the delivery HAD a payload and retention removed it.
	// Manual replay is unavailable. The row keeps raw_body_expires_at as the
	// tombstone, so the UI can say when, not merely that.
	PayloadExpired PayloadStatus = "expired"
	// PayloadNone means no payload was ever stored for this delivery.
	PayloadNone PayloadStatus = "none"
)

// ReplayAvailable reports whether a manual replay can still be offered.
func (p PayloadStatus) ReplayAvailable() bool { return p == PayloadAvailable }

// DeliveryPayload is the replay-availability answer for one delivery.
type DeliveryPayload struct {
	DeliveryID string
	Status     PayloadStatus
	// ExpiredAt is set only for PayloadExpired: the deadline retention actually
	// enforced.
	ExpiredAt time.Time
	// BodyBytes is what the payload measured, kept even after the blob is gone
	// so the UI can say what is no longer there.
	BodyBytes int64
}

// ReplayAvailable reports whether a manual replay can still be offered for this
// delivery. A UI that gates its replay button on anything else — the delivery
// existing, the work having finished — offers a button that cannot work.
func (d DeliveryPayload) ReplayAvailable() bool { return d.Status.ReplayAvailable() }

// PayloadStatus reports whether delivery's raw body is still replayable.
func (s *Store) PayloadStatus(ctx context.Context, deliveryID string) (DeliveryPayload, error) {
	var present int
	var expires sql.NullString
	var bytes int64
	err := s.db.QueryRowContext(ctx, `
		SELECT raw_body IS NOT NULL, raw_body_expires_at, body_bytes
		FROM webhook_deliveries WHERE id = ?`, deliveryID).Scan(&present, &expires, &bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryPayload{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPayload{}, fmt.Errorf("work: read payload status: %w", err)
	}

	out := DeliveryPayload{DeliveryID: deliveryID, BodyBytes: bytes}
	switch {
	case present == 1:
		out.Status = PayloadAvailable
	case expires.Valid:
		// The tombstone: a NULL blob beside a recorded deadline means the
		// payload existed and retention took it.
		out.Status = PayloadExpired
		out.ExpiredAt = parseTime(expires.String)
	default:
		out.Status = PayloadNone
	}
	return out, nil
}
