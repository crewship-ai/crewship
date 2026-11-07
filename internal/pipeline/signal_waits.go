package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// SignalWaitStore persists wait(event) step arm/delivery state
// (pipeline_signal_waits, migration v154) so a signal survives a
// process restart (#1409). The in-memory SignalRegistry alone loses
// any signal delivered while nothing is registered to receive it —
// because the process is down, or because the run simply hasn't
// reached the wait step yet. This store is the durable source of
// truth; SignalRegistry stays as the fast in-process wake-up path for
// the common case where the waiting goroutine is live.
type SignalWaitStore interface {
	// Arm records that (runID, stepID) is now waiting on eventType, in
	// workspaceID. Idempotent: re-arming the same (run, step) — e.g. a
	// resume that re-executes the wait step before the original arm's
	// transaction is even visible — is a no-op, not an error.
	Arm(ctx context.Context, workspaceID, runID, stepID, eventType string) error

	// ArmWithTimeout preserves the first arm's deadline, including on resume.
	ArmWithTimeout(ctx context.Context, workspaceID, runID, stepID, eventType string, timeout time.Duration) (time.Time, error)

	// ArmMatching is ArmWithTimeout plus the wait's rendered event_filter and
	// correlation key, stored with the row in the same statement so no delivery
	// can see the wait without them. The first arm wins on resume.
	ArmMatching(ctx context.Context, workspaceID, runID, stepID, eventType, filter, correlationKey string, timeout time.Duration) (time.Time, error)

	// DeliverCorrelated persists payload against pending waits in workspaceID
	// of runs of pipelineID whose event type and correlation key match, and
	// returns the claimed run ids. An empty key never matches.
	DeliverCorrelated(ctx context.Context, workspaceID, pipelineID, eventType, correlationKey, payload string) (runIDs []string, err error)

	Status(ctx context.Context, runID, stepID string) (string, error)
	Expired(ctx context.Context, runID, stepID string) (bool, error)

	// Resolve expires only pending rows; a committed delivery always wins.
	Resolve(ctx context.Context, runID, stepID string) (status string, err error)

	// ReplayConsumed recovers the consume-to-step-output crash window. Only a
	// resume executing an unrestored step may replay; live consumer claims stay
	// exactly once through ConsumeDelivered.
	ReplayConsumed(ctx context.Context, runID, stepID string) (string, error)

	// Deliver persists payload against the oldest PENDING wait matching
	// (runID, eventType) and marks it delivered. Returns armed=false
	// when no pending row matches — the caller's signal (wrong
	// event_type, run never armed one, or it was already delivered)
	// should be treated as "no run waiting on that event", same
	// contract the in-memory registry offered.
	Deliver(ctx context.Context, runID, eventType, payload string) (armed bool, err error)

	// DeliverTopic persists payload against EVERY pending wait in
	// workspaceID matching eventType, and returns the ids of the runs it
	// claimed so the caller can un-park each one.
	//
	// This is the run_id-less door Deliver cannot be. An internal event
	// source ("a mission changed status") knows a workspace and an event
	// type; it never knows which runs are parked on them, and asking it
	// to find out would make every producer re-implement this lookup.
	//
	// An empty result is NOT an error: an event nobody waits on is the
	// normal case for a producer that emits regardless of listeners.
	// Callers that need "did anyone hear me" read len(runIDs).
	DeliverTopic(ctx context.Context, workspaceID, eventType, payload string) (runIDs []string, err error)

	// ConsumeDelivered returns the payload for a delivered-but-not-yet-
	// consumed wait at (runID, stepID) and marks it consumed in the same
	// call (so two concurrent consumers — a live goroutine racing a
	// resume — can't both claim it). ok=false means nothing has been
	// delivered yet; the caller should fall back to blocking/registering
	// for a live signal.
	ConsumeDelivered(ctx context.Context, runID, stepID string) (payload string, ok bool, err error)
}

// SQLSignalWaitStore is the production SignalWaitStore backed by
// pipeline_signal_waits.
type SQLSignalWaitStore struct {
	db *sql.DB
}

// NewSQLSignalWaitStore returns a store backed by the given DB handle.
// The handle must already have the event-deadline migration (#2903).
func NewSQLSignalWaitStore(db *sql.DB) *SQLSignalWaitStore {
	return &SQLSignalWaitStore{db: db}
}

func (s *SQLSignalWaitStore) Arm(ctx context.Context, workspaceID, runID, stepID, eventType string) error {
	_, err := s.ArmWithTimeout(ctx, workspaceID, runID, stepID, eventType, time.Hour)
	return err
}

// ArmWithTimeout is idempotent even across retries/restarts. For pre-deadline
// schema rows, recover from the original created_at rather than from now.
func (s *SQLSignalWaitStore) ArmWithTimeout(ctx context.Context, workspaceID, runID, stepID, eventType string, timeout time.Duration) (time.Time, error) {
	return s.ArmMatching(ctx, workspaceID, runID, stepID, eventType, "", "", timeout)
}

func (s *SQLSignalWaitStore) ArmMatching(ctx context.Context, workspaceID, runID, stepID, eventType, filter, correlationKey string, timeout time.Duration) (time.Time, error) {
	if timeout <= 0 {
		timeout = time.Hour
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO pipeline_signal_waits (id, workspace_id, run_id, step_id, event_type, status, created_at, timeout_at, event_filter, correlation_key)
VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)
ON CONFLICT (run_id, step_id) DO NOTHING`,
		"sigwait_"+runID+"_"+stepID, workspaceID, runID, stepID, eventType, tsformat.Format(now), tsformat.Format(now.Add(timeout)),
		nullStr(filter), nullStr(correlationKey))
	if err != nil {
		return time.Time{}, fmt.Errorf("signal_waits: arm: %w", err)
	}
	var created string
	var deadline sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT created_at, timeout_at FROM pipeline_signal_waits WHERE run_id=? AND step_id=?`, runID, stepID).Scan(&created, &deadline); err != nil {
		return time.Time{}, err
	}
	if !deadline.Valid {
		original, err := time.Parse(time.RFC3339Nano, created) // tsformat:allow: read-only parse; recovered deadline is written with tsformat.Format
		if err != nil {
			return time.Time{}, fmt.Errorf("signal_waits: original arm time: %w", err)
		}
		// COALESCE prevents competing recovery attempts from renewing it.
		if _, err := s.db.ExecContext(ctx, `UPDATE pipeline_signal_waits SET timeout_at=COALESCE(timeout_at,?) WHERE run_id=? AND step_id=?`, tsformat.Format(original.Add(timeout)), runID, stepID); err != nil {
			return time.Time{}, err
		}
		if err := s.db.QueryRowContext(ctx, `SELECT timeout_at FROM pipeline_signal_waits WHERE run_id=? AND step_id=?`, runID, stepID).Scan(&deadline); err != nil {
			return time.Time{}, err
		}
	}
	return time.Parse(time.RFC3339Nano, deadline.String) // tsformat:allow: read-only parse of stored deadline, no SQL write
}

func (s *SQLSignalWaitStore) Resolve(ctx context.Context, runID, stepID string) (string, error) {
	// The UPDATE and delivery both predicate on pending. Delivered rows never
	// expire, even if recovery cannot process their payload until much later.
	if _, err := s.db.ExecContext(ctx, `UPDATE pipeline_signal_waits SET status='timed_out'
 WHERE run_id=? AND step_id=? AND status='pending' AND timeout_at <= ?`, runID, stepID, tsformat.Format(time.Now())); err != nil {
		return "", err
	}
	return s.Status(ctx, runID, stepID)
}

func (s *SQLSignalWaitStore) Status(ctx context.Context, runID, stepID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM pipeline_signal_waits WHERE run_id=? AND step_id=?`, runID, stepID).Scan(&status)
	return status, err
}

// claimableWait is the shared predicate for a wait a delivery may claim: still
// pending, inside its deadline, and its run not terminal.
const claimableWait = `status = 'pending'
      AND (timeout_at IS NULL OR timeout_at > strftime('%Y-%m-%dT%H:%M:%f000000Z','now'))
      AND NOT EXISTS (SELECT 1 FROM pipeline_runs r WHERE r.id = pipeline_signal_waits.run_id AND r.status IN ('completed','failed','cancelled','interrupted','dry_run'))`

// claimMatching claims up to limit pending waits selected by where/args whose
// stored event_filter accepts payload, oldest first, and returns their run ids
// (one entry per claimed row). A wait whose filter rejects the payload is left
// pending, so the signal is neither consumed by it nor lost for other waits.
// Each claim is its own guarded UPDATE, so concurrent deliveries partition the
// rows and never claim one twice.
func (s *SQLSignalWaitStore) claimMatching(ctx context.Context, where string, args []any, payload string, limit int) ([]string, error) {
	return claimMatchingOn(ctx, s.db, where, args, payload, limit)
}

// claimMatchingOn is claimMatching against a db or an open transaction, so a
// caller can commit the claims together with a receipt.
func claimMatchingOn(ctx context.Context, x sqlExecQuerier, where string, args []any, payload string, limit int) ([]string, error) {
	rows, err := x.QueryContext(ctx, `SELECT id, run_id, COALESCE(event_filter,'') FROM pipeline_signal_waits
 WHERE `+where+` AND `+claimableWait+` ORDER BY created_at ASC, id ASC LIMIT ?`, append(args, maxTopicFanout)...)
	if err != nil {
		return nil, err
	}
	type cand struct{ id, run, filter string }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.run, &c.filter); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var claimed []string
	for _, c := range cands {
		if len(claimed) >= limit {
			break
		}
		if c.filter != "" && !EventFilterMatches(c.filter, payload) {
			continue
		}
		res, err := x.ExecContext(ctx, `
UPDATE pipeline_signal_waits
SET status = 'delivered', payload = ?, delivered_at = strftime('%Y-%m-%dT%H:%M:%f000000Z','now')
WHERE id = ? AND `+claimableWait, payload, c.id)
		if err != nil {
			return claimed, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			claimed = append(claimed, c.run)
		}
	}
	return claimed, nil
}

// Deliver claims the oldest pending wait of the run that matches the event
// type and whose event_filter accepts the payload.
func (s *SQLSignalWaitStore) Deliver(ctx context.Context, runID, eventType, payload string) (bool, error) {
	claimed, err := s.claimMatching(ctx, `run_id = ? AND event_type = ?`, []any{runID, eventType}, payload, 1)
	if err != nil {
		return false, fmt.Errorf("signal_waits: deliver: %w", err)
	}
	return len(claimed) > 0, nil
}

func (s *SQLSignalWaitStore) DeliverCorrelated(ctx context.Context, workspaceID, pipelineID, eventType, correlationKey, payload string) ([]string, error) {
	return deliverCorrelatedOn(ctx, s.db, workspaceID, pipelineID, eventType, correlationKey, payload)
}

func deliverCorrelatedOn(ctx context.Context, x sqlExecQuerier, workspaceID, pipelineID, eventType, correlationKey, payload string) ([]string, error) {
	if workspaceID == "" || pipelineID == "" || eventType == "" || correlationKey == "" {
		return nil, nil
	}
	claimed, err := claimMatchingOn(ctx, x,
		`workspace_id = ? AND event_type = ? AND correlation_key = ?
 AND run_id IN (SELECT id FROM pipeline_runs WHERE workspace_id = ? AND pipeline_id = ?)`,
		[]any{workspaceID, eventType, correlationKey, workspaceID, pipelineID}, payload, maxTopicFanout)
	if err != nil {
		return dedupeStrings(claimed), fmt.Errorf("signal_waits: deliver correlated: %w", err)
	}
	return dedupeStrings(claimed), nil
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// maxTopicFanout bounds one DeliverTopic call. The loop below terminates on
// its own — every iteration flips exactly one row out of 'pending', and only
// Arm puts rows back in — so this is not the termination condition, it is a
// guard against a producer that arms faster than one delivery can drain
// (which would otherwise turn a single HTTP request into an unbounded loop).
// A workspace with 10k runs parked on one topic at the same instant is a
// pathology worth reporting rather than serving.
const maxTopicFanout = 10000

// ErrTopicFanoutTruncated reports that a topic delivery hit maxTopicFanout.
// The run ids returned alongside it WERE delivered and must still be resumed;
// the rest are untouched and a second call will claim them.
var ErrTopicFanoutTruncated = errors.New("signal_waits: topic fan-out exceeded the per-call cap")

func (s *SQLSignalWaitStore) DeliverTopic(ctx context.Context, workspaceID, eventType, payload string) ([]string, error) {
	if workspaceID == "" || eventType == "" {
		return nil, fmt.Errorf("signal_waits: deliver topic: workspace_id and event_type are required")
	}
	claimed, err := s.claimMatching(ctx, `workspace_id = ? AND event_type = ?`, []any{workspaceID, eventType}, payload, maxTopicFanout)
	// A run can hold more than one pending wait (two branches parked on the
	// same event). Each row is claimed, but the run is woken once — resuming
	// the same run twice is the double-execution this claim discipline exists
	// to prevent. Partial progress is returned with the error so the caller
	// resumes what was claimed.
	runIDs := dedupeStrings(claimed)
	if err != nil {
		return runIDs, fmt.Errorf("signal_waits: deliver topic: %w", err)
	}
	if len(claimed) >= maxTopicFanout {
		return runIDs, ErrTopicFanoutTruncated
	}
	return runIDs, nil
}

func (s *SQLSignalWaitStore) ConsumeDelivered(ctx context.Context, runID, stepID string) (string, bool, error) {
	var payload sql.NullString
	err := s.db.QueryRowContext(ctx, `UPDATE pipeline_signal_waits SET status='consumed', consumed_at=?
 WHERE run_id=? AND step_id=? AND status='delivered' RETURNING payload`, tsformat.Format(time.Now()), runID, stepID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("signal_waits: consume: %w", err)
	}
	return payload.String, true, nil
}

func (s *SQLSignalWaitStore) ReplayConsumed(ctx context.Context, runID, stepID string) (string, error) {
	var payload sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM pipeline_signal_waits WHERE run_id=? AND step_id=? AND status='consumed'`, runID, stepID).Scan(&payload)
	return payload.String, err
}

func (s *SQLSignalWaitStore) Expired(ctx context.Context, runID, stepID string) (bool, error) {
	var expired bool
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(timeout_at<=strftime('%Y-%m-%dT%H:%M:%f000000Z','now'),0) FROM pipeline_signal_waits WHERE run_id=? AND step_id=?`, runID, stepID).Scan(&expired)
	return expired, err
}
