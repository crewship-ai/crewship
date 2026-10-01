package restrictedworkflow

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/work"
)

// The domain table retains private authority and outputs. Only content-free
// scheduling identity enters the shared ledger; it owns all claims and leases.
const workflowDomain = "restricted_workflow"

func workflowDispatchConfig() dispatch.Config {
	return dispatch.Config{Owner: "restricted-workflow-dispatcher", Limits: work.SerialAgentLimits(),
		Kinds:        []work.Kind{{Source: work.SourceManual, DomainKind: workflowDomain}},
		PollInterval: 2 * time.Second, Paused: quiesce.QueuePaused}
}
func (s *Service) acceptWork(ctx context.Context, tx *sql.Tx, j job) error {
	fire, err := time.Parse(time.RFC3339Nano, j.FireAt)
	if err != nil {
		return err
	}
	expires, err := time.Parse(time.RFC3339Nano, j.Expires)
	if err != nil {
		return err
	}
	_, err = s.ledger.WithIDs(func() string { return j.ID }).AcceptTx(ctx, tx, work.AcceptRequest{
		WorkspaceID: j.Workspace, Source: work.SourceManual, DomainKind: workflowDomain, DomainID: j.ID,
		AgentID: j.Agent, SessionID: j.Chat, Class: work.ClassBackground,
		EligibleAt: fire, DeadlineAt: expires,
	})
	return err
}
func (s *Service) checkOwnership(ctx context.Context, q ownershipQuery, a dispatch.Assignment) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM work_items w JOIN work_attempts a ON a.work_id=w.id AND a.generation=w.generation
 WHERE w.id=? AND w.domain_kind=? AND w.domain_id=? AND w.generation=? AND a.run_id=?
 AND w.state IN ('starting','running') AND a.ended_at IS NULL AND a.lease_expires_at>?`,
		a.Item.ID, workflowDomain, a.Item.DomainID, a.Generation, a.RunID, tsformat.Format(time.Now())).Scan(&one)
	if err != nil {
		return work.ErrStaleGeneration
	}
	return nil
}

type ownershipQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Service) markJobRunning(ctx context.Context, a dispatch.Assignment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.checkOwnership(ctx, tx, a); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE restricted_workflow_jobs SET state='running' WHERE id=? AND state='pending'`, a.Item.DomainID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDenied
	}
	return tx.Commit()
}

type workflowAttempt struct {
	cancel           context.CancelFunc
	done             chan struct{}
	workflowID       string
	executionStarted bool
}
type workflowRuntime struct {
	service  *Service
	mu       sync.Mutex
	attempts map[string]*workflowAttempt
	err      error
}

func newWorkflowRuntime(s *Service) *workflowRuntime {
	return &workflowRuntime{service: s, attempts: map[string]*workflowAttempt{}}
}
func (r *workflowRuntime) clearError()           { r.mu.Lock(); defer r.mu.Unlock(); r.err = nil }
func (r *workflowRuntime) lastError() error      { r.mu.Lock(); defer r.mu.Unlock(); return r.err }
func (r *workflowRuntime) recordError(err error) { r.mu.Lock(); defer r.mu.Unlock(); r.err = err }
func (r *workflowRuntime) Locator(a dispatch.Assignment) string {
	return "restricted-workflow:" + a.RunID
}
func (r *workflowRuntime) Authorize(ctx context.Context, a dispatch.Assignment) (dispatch.Decision, error) {
	j, err := r.service.load(ctx, a.Item.DomainID)
	if err != nil {
		return dispatch.Decision{}, err
	}
	handle, err := encryption.Decrypt(j.Handle)
	if err != nil || j.State != "pending" || r.service.checkJob(ctx, r.service.db, j, handle) != nil {
		r.recordError(ErrDenied)
		// Refusal is before any external execution. Project only this owned job.
		if e := r.service.markJobRunning(ctx, a); e == nil {
			r.service.failOwned(j, a)
		}
		return dispatch.Refuse("private workflow authority unavailable"), nil
	}
	return dispatch.Allow(), nil
}

// workflowReconciliationRequired preserves unconfirmed cleanup or lost ownership
// independently of a terminal graph failure. Failed graphs are never retried.
type workflowReconciliationRequired struct{ error }

func (r *workflowRuntime) Run(ctx context.Context, a dispatch.Assignment, started func()) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	live := &workflowAttempt{cancel: cancel, done: make(chan struct{}), workflowID: a.Item.DomainID}
	r.mu.Lock()
	r.attempts[r.Locator(a)] = live
	r.mu.Unlock()
	var leave func()
	defer func() {
		if leave != nil {
			defer leave()
		}
		if live.executionStarted {
			ownershipCtx, ownershipCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			ownershipErr := r.service.checkOwnership(ownershipCtx, r.service.db, a)
			ownershipCancel()
			if ownershipErr != nil {
				err = errors.Join(err, workflowReconciliationRequired{ownershipErr})
			}
			if checker, ok := r.service.executor.(interface {
				ConfirmWorkflowStopped(context.Context, string) error
			}); ok {
				clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				stopErr := checker.ConfirmWorkflowStopped(clean, live.workflowID)
				cancel()
				if stopErr != nil {
					err = errors.Join(err, workflowReconciliationRequired{stopErr})
				}
			}
		}
		close(live.done)
		if err != nil {
			r.recordError(err)
		}
	}()
	wr, ok := quiesce.Enter(ctx)
	if !ok {
		return errors.New("private workflow writer unavailable")
	}
	leave = wr.Leave
	return r.service.executeAssignment(wr.Context(), a, func() {
		live.executionStarted = true
		started()
	})
}
func (r *workflowRuntime) Classify(_ dispatch.Assignment, err error) dispatch.Outcome {
	if err == nil {
		return dispatch.OutcomeSucceeded
	}
	var uncertain workflowReconciliationRequired
	if errors.As(err, &uncertain) {
		return dispatch.OutcomeUnclear
	}
	// Failure can retain paid requests or external effects. Terminal failure
	// releases capacity, without scheduling a retry or claiming those effects
	// never happened. Unconfirmed cleanup or lost ownership requires reconciliation.
	return dispatch.OutcomeFailed
}
func (r *workflowRuntime) Alive(ctx context.Context, locator string) (bool, error) {
	r.mu.Lock()
	live := r.attempts[locator]
	r.mu.Unlock()
	if live == nil {
		return false, errors.New("private runtime requires reconciliation")
	}
	select {
	case <-live.done:
		if !live.executionStarted {
			return false, nil
		}
		if checker, ok := r.service.executor.(interface {
			ConfirmWorkflowStopped(context.Context, string) error
		}); ok {
			if err := checker.ConfirmWorkflowStopped(ctx, live.workflowID); err != nil {
				return false, err
			}
		}
		return false, nil
	default:
		return true, nil
	}
}
func (r *workflowRuntime) Stop(ctx context.Context, locator string) (bool, error) {
	r.mu.Lock()
	live := r.attempts[locator]
	r.mu.Unlock()
	if live == nil {
		return false, errors.New("private runtime requires reconciliation")
	}
	live.cancel()
	select {
	case <-live.done:
		alive, err := r.Alive(ctx, locator)
		return !alive && err == nil, err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

var _ dispatch.Runtime = (*workflowRuntime)(nil)
var _ dispatch.Authorizer = (*workflowRuntime)(nil)

// FlushRunOutcomes projects terminal scheduling decisions without touching
// uncertain work. Reconciliation remains visible through private receipts.
func (r *workflowRuntime) FlushRunOutcomes(ctx context.Context) error {
	writer, ok := quiesce.Enter(ctx)
	if !ok {
		return nil
	}
	defer writer.Leave()
	ctx = writer.Context()
	tx, err := r.service.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,?) WHERE id IN (
 SELECT j.origin_attempt_id FROM restricted_workflow_jobs j JOIN work_items w ON w.id=j.id
 WHERE w.domain_kind=? AND w.state IN ('failed','expired','cancelled') AND j.state IN ('pending','running'))`, tsformat.Format(time.Now()), workflowDomain); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE restricted_workflow_jobs SET state=CASE (SELECT state FROM work_items WHERE id=restricted_workflow_jobs.id)
 WHEN 'cancelled' THEN 'canceled' ELSE 'failed' END,finished_at=?
 WHERE state IN ('pending','running') AND id IN (SELECT id FROM work_items WHERE domain_kind=? AND state IN ('failed','expired','cancelled'))`, tsformat.Format(time.Now()), workflowDomain); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) receiptState(ctx context.Context, j job) (string, error) {
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM work_items WHERE id=? AND domain_kind=?`, j.ID, workflowDomain).Scan(&state); err != nil {
		return "", err
	}
	if state == "needs_reconciliation" {
		return state, nil
	}
	// The domain result must not escape before the dispatch outcome is durable.
	if j.State == "completed" && state != "succeeded" {
		switch state {
		case "failed", "expired":
			return "failed", nil
		case "cancelled":
			return "canceled", nil
		}
		return "running", nil
	}
	return j.State, nil
}

func (r *workflowRuntime) Forget(locator string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if live := r.attempts[locator]; live != nil {
		select {
		case <-live.done:
			delete(r.attempts, locator)
		default:
		}
	}
}

// Wake is a post-commit latency hint. Losing it cannot lose accepted work.
func (s *Service) Wake() { s.notify() }

func (s *Service) loadReceipt(ctx context.Context, id string) (job, error) {
	j, err := s.load(ctx, id)
	if err != nil {
		return j, err
	}
	state, err := s.receiptState(ctx, j)
	if err != nil {
		return job{}, err
	}
	if state == "needs_reconciliation" {
		j.State = state
	}
	return j, nil
}
