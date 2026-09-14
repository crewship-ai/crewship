package main

import (
	"context"
	"fmt"
	"time"
)

// scenarioFencing answers protocol step 3: "Zjistit, co River sám odmítne a kde
// Crewship musí přidat fencing generation."
//
// River v0.47.0's completion statement is, verbatim from
// riverdriver/riversqlite/internal/dbsqlc/river_job.sql:595-623
// (JobSetStateIfRunning):
//
//	UPDATE river_job SET … WHERE id = @id AND state = 'running'
//
// The predicate is (id, state). There is no attempt or generation term. This
// scenario walks a job through lose-lease → rescue → re-claim and then applies
// that exact predicate on behalf of the OLD attempt, to show whether the stale
// worker can overwrite the newer one — Crewship invariant I5.
func scenarioFencing(t testingT, syncMode string) scenarioResult {
	ctx := context.Background()
	e := newEnv(t, 5, syncMode)
	defer e.close()
	e.migrateRiver(t)

	res := scenarioResult{Scenario: "fencing", Detail: map[string]any{}}

	worker := &mockWorker{}
	client := e.client(t, worker, 1)

	// Enqueue one job through the real client so the row is shaped exactly as
	// River writes it.
	out := acceptOnce(ctx, e.pool, client, delivery{
		ID: "dlv_fence", WorkspaceID: "ws1", EndpointID: "ep1",
		SourceDeliveryID: "src-fence", BodySHA256: "aa", WorkID: "work_fence",
	})
	if out.err != nil {
		return failed(res, out.err)
	}

	var jobID int64
	if err := e.pool.QueryRowContext(ctx, `SELECT id FROM river_job LIMIT 1`).Scan(&jobID); err != nil {
		return failed(res, err)
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	// Attempt 1 claims the job.
	if _, err := e.pool.ExecContext(ctx,
		`UPDATE river_job SET state='running', attempt=1, attempted_at=? WHERE id=?`, now, jobID); err != nil {
		return failed(res, err)
	}
	// Attempt 1 loses its lease; the rescuer returns the job to the queue.
	if _, err := e.pool.ExecContext(ctx,
		`UPDATE river_job SET state='available' WHERE id=?`, jobID); err != nil {
		return failed(res, err)
	}
	// Attempt 2 claims it. This is the run whose result must survive.
	if _, err := e.pool.ExecContext(ctx,
		`UPDATE river_job SET state='running', attempt=2, attempted_at=? WHERE id=?`, now, jobID); err != nil {
		return failed(res, err)
	}

	// The stale attempt-1 worker now finishes and reports success, using the
	// driver's own predicate.
	riverRes, err := e.pool.ExecContext(ctx,
		`UPDATE river_job SET state='completed', finalized_at=? WHERE id=? AND state='running'`, now, jobID)
	if err != nil {
		return failed(res, err)
	}
	riverRows, _ := riverRes.RowsAffected()
	res.Detail["river_predicate_rows_affected"] = riverRows
	res.Detail["river_accepts_stale_completion"] = riverRows == 1

	var state string
	var attempt int
	if err := e.pool.QueryRowContext(ctx, `SELECT state, attempt FROM river_job WHERE id=?`, jobID).Scan(&state, &attempt); err != nil {
		return failed(res, err)
	}
	res.Detail["state_after_stale_completion"] = state
	res.Detail["attempt_after_stale_completion"] = attempt

	// Now the same write with the fencing term Crewship has to add.
	if _, err := e.pool.ExecContext(ctx,
		`UPDATE river_job SET state='running', finalized_at=NULL WHERE id=?`, jobID); err != nil {
		return failed(res, err)
	}
	fencedRes, err := e.pool.ExecContext(ctx,
		`UPDATE river_job SET state='completed', finalized_at=? WHERE id=? AND state='running' AND attempt=?`,
		now, jobID, 1)
	if err != nil {
		return failed(res, err)
	}
	fencedRows, _ := fencedRes.RowsAffected()
	res.Detail["fenced_predicate_rows_affected"] = fencedRows
	res.Detail["fencing_term_rejects_stale_completion"] = fencedRows == 0

	// The scenario "passes" when it has established both facts; the finding is
	// that River alone does not fence, so the queue library cannot be treated
	// as satisfying I5.
	res.Pass = riverRows == 1 && fencedRows == 0
	if riverRows == 1 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"River's JobSetStateIfRunning predicate (id, state='running') accepted a completion from attempt 1 while attempt %d was the live one — invariant I5 needs a Crewship-side generation term, whichever queue is chosen", attempt))
	}
	return res
}
