package main

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/riverqueue/river"
)

func runtimeVersion() string { return runtime.Version() }

// scenarioTx answers protocol step 1: delivery and River enqueue in ONE
// existing *sql.Tx. Rollback must leave neither row; commit must create both;
// and no worker may claim the job before the commit.
func scenarioTx(t testingT, poolSize int, syncMode string) scenarioResult {
	ctx := context.Background()
	e := newEnv(t, poolSize, syncMode)
	defer e.close()
	mig := e.migrateRiver(t)

	res := scenarioResult{Scenario: "tx", Detail: map[string]any{}}
	res.Detail["river_migrations_applied"] = len(mig.Versions)

	// An independent handle, so "what is durably in the file" is read outside
	// the transaction that wrote it.
	observer, err := sql.Open("sqlite", crewshipDSN(e.dbPath, syncMode))
	must(t, err)
	observer.SetMaxOpenConns(1)
	defer observer.Close()

	worker := &mockWorker{started: make(chan string, 8)}
	client := e.client(t, worker, 2)
	// Start before any transaction is opened. With River's recommended
	// single-connection pool, starting the client while a *sql.Tx is open
	// deadlocks on the pool — that is measured separately by scenarioPool, and
	// it must not silently hang this scenario.
	startCtx, startCancel := context.WithTimeout(ctx, 20*time.Second)
	defer startCancel()
	if err := client.Start(startCtx); err != nil {
		return failed(res, fmt.Errorf("client start: %w", err))
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(stopCtx)
	}()

	// --- rollback leaves nothing -------------------------------------------
	tx, err := e.pool.BeginTx(ctx, nil)
	must(t, err)
	if _, err := acceptDelivery(ctx, tx, client, delivery{
		ID: "dlv_rollback", WorkspaceID: "ws1", EndpointID: "ep1",
		SourceDeliveryID: "src-rollback", BodySHA256: "aa", WorkID: "work_rollback",
	}); err != nil {
		return failed(res, fmt.Errorf("accept (rollback case): %w", err))
	}
	if err := tx.Rollback(); err != nil {
		return failed(res, fmt.Errorf("rollback: %w", err))
	}
	dc, err := e.deliveryCount(ctx, observer)
	must(t, err)
	jc, err := e.jobCount(ctx, observer)
	must(t, err)
	res.Detail["after_rollback_deliveries"] = dc
	res.Detail["after_rollback_jobs"] = jc
	orphanFree := dc == 0 && jc == 0

	// --- job invisible before commit ---------------------------------------
	tx2, err := e.pool.BeginTx(ctx, nil)
	must(t, err)
	if _, err := acceptDelivery(ctx, tx2, client, delivery{
		ID: "dlv_commit", WorkspaceID: "ws1", EndpointID: "ep1",
		SourceDeliveryID: "src-commit", BodySHA256: "bb", WorkID: "work_commit", SleepMS: 20,
	}); err != nil {
		_ = tx2.Rollback()
		return failed(res, fmt.Errorf("accept (commit case): %w", err))
	}
	preJobs, err := e.jobCount(ctx, observer)
	must(t, err)
	res.Detail["jobs_visible_before_commit"] = preJobs
	invisibleBeforeCommit := preJobs == 0

	// The client is already running (started before any transaction was opened —
	// see below). A worker that could claim uncommitted work would be a
	// correctness failure, not a race we are allowed to lose.
	time.Sleep(300 * time.Millisecond)
	claimedEarly := worker.count() > 0
	res.Detail["worked_before_commit"] = worker.count()

	if err := tx2.Commit(); err != nil {
		_ = client.Stop(ctx)
		return failed(res, fmt.Errorf("commit: %w", err))
	}

	deadline := time.Now().Add(15 * time.Second)
	for worker.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	workedAfterCommit := worker.count()

	dc2, err := e.deliveryCount(ctx, observer)
	must(t, err)
	jc2, err := e.jobCount(ctx, observer)
	must(t, err)
	res.Detail["after_commit_deliveries"] = dc2
	res.Detail["after_commit_jobs"] = jc2
	res.Detail["worked_after_commit"] = workedAfterCommit

	res.Pass = orphanFree && invisibleBeforeCommit && !claimedEarly && dc2 == 1 && jc2 == 1 && workedAfterCommit == 1
	if !res.Pass {
		res.Notes = append(res.Notes, "one of: orphan after rollback, job visible or claimed before commit, or work not executed after commit")
	}
	return res
}

// scenarioDup answers protocol step 1's second half: concurrent duplicate
// deliveries must produce one delivery row, one job, and one stable receipt.
func scenarioDup(t testingT, poolSize int, syncMode string, n int) scenarioResult {
	ctx := context.Background()
	e := newEnv(t, poolSize, syncMode)
	defer e.close()
	e.migrateRiver(t)

	res := scenarioResult{Scenario: "dup", Detail: map[string]any{"concurrency": n}}

	worker := &mockWorker{}
	client := e.client(t, worker, 1)

	outcomes := make([]acceptOutcome, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			outcomes[i] = acceptOnce(ctx, e.pool, client, delivery{
				ID:               fmt.Sprintf("dlv_attempt_%d", i),
				WorkspaceID:      "ws1",
				EndpointID:       "ep1",
				SourceDeliveryID: "src-same",
				BodySHA256:       "cc",
				WorkID:           fmt.Sprintf("work_attempt_%d", i),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	var accepted, duplicates, errs int
	seen := map[string]int{}
	for _, o := range outcomes {
		switch {
		case o.err != nil:
			errs++
			res.Notes = append(res.Notes, "accept error: "+o.err.Error())
		case o.dup:
			duplicates++
			seen[o.workID]++
		default:
			accepted++
			seen[o.workID]++
		}
	}
	dc, err := e.deliveryCount(ctx, e.pool)
	must(t, err)
	jc, err := e.jobCount(ctx, e.pool)
	must(t, err)

	res.Detail["accepted_new"] = accepted
	res.Detail["reported_duplicate"] = duplicates
	res.Detail["errors"] = errs
	res.Detail["distinct_receipts"] = len(seen)
	res.Detail["delivery_rows"] = dc
	res.Detail["river_jobs"] = jc

	res.Pass = errs == 0 && accepted == 1 && dc == 1 && jc == 1 && len(seen) == 1
	if !res.Pass {
		res.Notes = append(res.Notes, "duplicate deliveries did not collapse to exactly one work item with one stable receipt")
	}
	return res
}

// acceptOutcome is what one acceptance attempt reports back to its caller.
type acceptOutcome struct {
	workID string
	dup    bool
	err    error
}

// acceptOnce is the acceptance path a webhook handler would run: one immediate
// transaction, unique-violation means "already accepted, return the original
// receipt" rather than "make a second work item".
func acceptOnce(ctx context.Context, pool *sql.DB, client *river.Client[*sql.Tx], d delivery) acceptOutcome {
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return acceptOutcome{err: err}
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := acceptDelivery(ctx, tx, client, d); err != nil {
		if isUniqueViolation(err) {
			// Someone else already accepted this delivery. Roll back and read
			// the receipt they wrote; a retry must never mint a second work id.
			_ = tx.Rollback()
			var workID string
			qerr := pool.QueryRowContext(ctx,
				`SELECT work_id FROM deliveries WHERE workspace_id = ? AND endpoint_id = ? AND source_delivery_id = ?`,
				d.WorkspaceID, d.EndpointID, d.SourceDeliveryID).Scan(&workID)
			if qerr != nil {
				return acceptOutcome{err: qerr}
			}
			return acceptOutcome{workID: workID, dup: true}
		}
		return acceptOutcome{err: err}
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			var workID string
			qerr := pool.QueryRowContext(ctx,
				`SELECT work_id FROM deliveries WHERE workspace_id = ? AND endpoint_id = ? AND source_delivery_id = ?`,
				d.WorkspaceID, d.EndpointID, d.SourceDeliveryID).Scan(&workID)
			if qerr != nil {
				return acceptOutcome{err: qerr}
			}
			return acceptOutcome{workID: workID, dup: true}
		}
		return acceptOutcome{err: err}
	}
	return acceptOutcome{workID: d.WorkID}
}

func isUniqueViolation(err error) bool {
	return err != nil && (contains(err.Error(), "UNIQUE constraint failed") || contains(err.Error(), "constraint failed"))
}
