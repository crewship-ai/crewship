package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SQLWaitpointStore is the production WaitpointStore backed by the
// pipeline_waitpoints table (v79 migration). Maintains an in-memory
// channel registry keyed by token so a CompleteApproval call wakes
// the goroutine waiting on WaitFor.
//
// Persistence model:
//   - CreateApproval inserts a row with status=pending, token, prompt,
//     timeout_at; returns the token to the caller (the wait step
//     embeds it in journal so the inbox UI can render the approval
//     card)
//   - WaitFor blocks on a channel keyed by token. Either:
//     (a) CompleteApproval fires (HTTP endpoint POSTed by the inbox
//     UI) → status = approved/denied → channel signal → return
//     (b) timeout_at passes (background sweeper marks status =
//     timed_out + signals channel)
//     (c) ctx cancellation (run cancelled) → channel listener exits
//   - Recovery scan on startup re-attaches goroutines to pending
//     waitpoints whose runs are still running — without it, a
//     restart leaves approvals orphaned forever
//
// Sweeper runs in a separate goroutine, fired every 30 s. Cheap
// query against idx_pipeline_waitpoints_workspace_pending; no need
// to schedule per-waitpoint timers.
type SQLWaitpointStore struct {
	db *sql.DB

	mu        sync.Mutex
	listeners map[string]chan waitDecision
	sweeperWg sync.WaitGroup
	stopCh    chan struct{}
}

// waitDecision is the value passed through the channel when an
// approval/denial fires. approved=true → return true; otherwise
// false. We don't surface the decision_payload through the
// goroutine path in MVP — Phase 2 may pipe it through.
type waitDecision struct {
	approved bool
}

// NewSQLWaitpointStore constructs a store and starts the timeout
// sweeper. Caller must call Close() to stop the sweeper goroutine
// at process shutdown.
func NewSQLWaitpointStore(db *sql.DB) *SQLWaitpointStore {
	s := &SQLWaitpointStore{
		db:        db,
		listeners: make(map[string]chan waitDecision),
		stopCh:    make(chan struct{}),
	}
	s.sweeperWg.Add(1)
	go s.sweeper()
	return s
}

// RecoverPending eagerly sweeps any waitpoints whose timeout already
// elapsed (the regular sweeper would catch them within 30 s, but at
// boot we want fast cleanup) and reports how many pending entries
// remain. Pending waitpoints from before this process started are
// "stranded" — the goroutine that called WaitFor is gone with the
// previous lifetime. Approving them via the inbox still updates the
// DB (the row is real), but the parent run cannot resume because its
// in-memory state is lost. This method does NOT auto-mark them as
// abandoned so an operator can still see + decide on them; the count
// flows to the boot log so abnormal accumulation is visible.
//
// Returns (timedOutCount, pendingCount, err). The server's main.go
// calls this once before declaring readiness.
func (s *SQLWaitpointStore) RecoverPending(ctx context.Context) (timedOut int, pending int, err error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_waitpoints
SET status = 'timed_out', decided_at = ?
WHERE status = 'pending' AND timeout_at <= ?`, now, now)
	if err != nil {
		return 0, 0, fmt.Errorf("waitpoints: recover sweep: %w", err)
	}
	n, _ := res.RowsAffected()
	timedOut = int(n)

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipeline_waitpoints WHERE status = 'pending'`,
	).Scan(&pending); err != nil {
		return timedOut, 0, fmt.Errorf("waitpoints: recover count: %w", err)
	}
	return timedOut, pending, nil
}

// Close stops the timeout sweeper. Safe to call multiple times.
func (s *SQLWaitpointStore) Close() {
	s.mu.Lock()
	select {
	case <-s.stopCh:
		s.mu.Unlock()
		return
	default:
		close(s.stopCh)
	}
	s.mu.Unlock()
	s.sweeperWg.Wait()
}

// CreateApproval mints a token, persists the waitpoint row, and
// returns the token. Default timeout is 24h if req.TimeoutSec is 0.
func (s *SQLWaitpointStore) CreateApproval(ctx context.Context, req WaitpointApprovalRequest) (string, error) {
	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 24 * 3600
	}
	token := generateWaitpointToken()
	timeoutAt := time.Now().Add(time.Duration(timeoutSec) * time.Second).UTC().Format(time.RFC3339Nano)

	_, err := s.db.ExecContext(ctx, `
INSERT INTO pipeline_waitpoints (
    token, workspace_id, pipeline_run_id, step_id, kind, prompt,
    invoking_crew_id, status, timeout_at
) VALUES (?, ?, ?, ?, 'approval', ?, ?, 'pending', ?)`,
		token, req.WorkspaceID, req.PipelineRunID, req.StepID,
		nullableStr(req.Prompt), nullableStr(req.InvokingCrewID), timeoutAt,
	)
	if err != nil {
		return "", fmt.Errorf("waitpoints: insert: %w", err)
	}
	// Pre-create the listener channel so a fast CompleteApproval
	// (somehow racing the WaitFor) doesn't get lost. Buffered 1
	// so a complete-then-wait still delivers.
	s.mu.Lock()
	s.listeners[token] = make(chan waitDecision, 1)
	s.mu.Unlock()
	return token, nil
}

// WaitFor blocks until the waitpoint resolves or ctx is cancelled.
// Returns approved=true on approval, false on denial / timeout /
// cancellation.
//
// Order of operations is critical for avoiding the lost-wakeup race:
// the listener channel is registered BEFORE the decided-state DB
// check, so CompleteApproval racing with us either delivers to the
// channel or we observe its DB write — never both miss. Without the
// pre-registration, CompleteApproval firing between checkDecided and
// listener-insert would signal to a nonexistent channel and the
// goroutine would park forever.
func (s *SQLWaitpointStore) WaitFor(ctx context.Context, token string) (bool, error) {
	s.mu.Lock()
	ch, ok := s.listeners[token]
	if !ok {
		// Pre-register the listener while holding the mutex so any
		// CompleteApproval that arrives after this point has a
		// channel to deliver to. The DB re-check below covers the
		// case where CompleteApproval already fired before we
		// pre-registered.
		ch = make(chan waitDecision, 1)
		s.listeners[token] = ch
	}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.listeners, token)
		s.mu.Unlock()
	}()

	// Now that the listener is registered, re-check DB state. If
	// CompleteApproval ran before our listener was in place, the
	// signal hit `default` (no listener) — but the DB write is
	// committed, so checkDecided observes it. If CompleteApproval
	// runs AFTER listener registration, it delivers to the channel
	// directly. Either way, no lost wake-up.
	decided, approved, err := s.checkDecided(ctx, token)
	if err != nil {
		return false, err
	}
	if decided {
		return approved, nil
	}

	select {
	case d := <-ch:
		return d.approved, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// CompleteApproval marks the waitpoint approved/denied and signals
// any waiting goroutine. Called by the public HTTP endpoint when
// an operator clicks approve/deny in the inbox.
//
// Returns ErrAlreadyDecided if the waitpoint already has a final
// status — protects against double-decide races.
func (s *SQLWaitpointStore) CompleteApproval(ctx context.Context, token string, approved bool, deciderUserID, payload string) error {
	status := "approved"
	if !approved {
		status = "denied"
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_waitpoints
SET status = ?, decided_at = datetime('now','subsec'), decided_by_user_id = ?, decision_payload = ?
WHERE token = ? AND status = 'pending'`,
		status, nullableStr(deciderUserID), nullableStr(payload), token,
	)
	if err != nil {
		return fmt.Errorf("waitpoints: update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAlreadyDecided
	}
	s.mu.Lock()
	if ch, ok := s.listeners[token]; ok {
		select {
		case ch <- waitDecision{approved: approved}:
		default:
			// Buffered channel full — already delivered. Safe
			// to drop; the decision is in the DB.
		}
	}
	s.mu.Unlock()
	return nil
}

// ErrAlreadyDecided is returned by CompleteApproval when the
// waitpoint isn't in pending state.
var ErrAlreadyDecided = errors.New("waitpoint: already decided or expired")

// checkDecided reads the current status from DB. Used by WaitFor
// when the in-memory listener is gone (post-restart recovery).
func (s *SQLWaitpointStore) checkDecided(ctx context.Context, token string) (decided bool, approved bool, err error) {
	var status string
	err = s.db.QueryRowContext(ctx,
		`SELECT status FROM pipeline_waitpoints WHERE token = ?`, token,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, fmt.Errorf("waitpoint %q not found", token)
	}
	if err != nil {
		return false, false, err
	}
	switch status {
	case "pending":
		return false, false, nil
	case "approved":
		return true, true, nil
	case "denied", "timed_out", "cancelled":
		return true, false, nil
	}
	return false, false, fmt.Errorf("waitpoint %q unknown status %q", token, status)
}

// sweeper marks expired pending waitpoints as timed_out and signals
// any in-memory listeners. Runs every 30s; cheap thanks to the
// partial index on (status='pending', timeout_at).
func (s *SQLWaitpointStore) sweeper() {
	defer s.sweeperWg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.sweepOnce()
		}
	}
}

func (s *SQLWaitpointStore) sweepOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
SELECT token FROM pipeline_waitpoints
WHERE status = 'pending' AND timeout_at <= datetime('now','subsec')
LIMIT 200`)
	if err != nil {
		return
	}
	defer rows.Close()
	var expired []string
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err == nil {
			expired = append(expired, tok)
		}
	}
	_ = rows.Err()
	for _, tok := range expired {
		_, _ = s.db.ExecContext(ctx, `
UPDATE pipeline_waitpoints
SET status = 'timed_out', decided_at = datetime('now','subsec')
WHERE token = ? AND status = 'pending'`, tok)
		s.mu.Lock()
		if ch, ok := s.listeners[tok]; ok {
			select {
			case ch <- waitDecision{approved: false}:
			default:
			}
		}
		s.mu.Unlock()
	}
}

// generateWaitpointToken returns a 32-hex-char random token. We
// don't bother with a prefix — these are URL fragments and tokens,
// not entity IDs. Random bytes from crypto/rand.
func generateWaitpointToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-derived bytes if rand fails (extremely
		// unlikely; better than panicking and breaking the run).
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (i * 8))
		}
	}
	return hex.EncodeToString(b)
}

// nullableStr converts an empty string to SQL NULL via NullString.
// Mirrors the helper in store.go but kept local to avoid pulling
// store.go's transitive deps into a Phase 2 file.
func nullableStr(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}
