package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func failed(res scenarioResult, err error) scenarioResult {
	res.Pass = false
	res.Error = err.Error()
	return res
}

// holdWriteLock opens a second, independent connection to the same file and
// keeps an IMMEDIATE transaction open for d. This is protocol step 5's
// "držet write transaction druhým klientem" — the case where the acceptance
// path must either commit inside its budget or fail retryably, never answer a
// false 202.
func holdWriteLock(ctx context.Context, dbPath, syncMode string, d time.Duration) (release func(), err error) {
	holder, err := sql.Open("sqlite", crewshipDSN(dbPath, syncMode))
	if err != nil {
		return nil, err
	}
	holder.SetMaxOpenConns(1)
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		holder.Close()
		return nil, err
	}
	// _txlock=immediate already took the write lock at BEGIN; this write makes
	// that explicit and survives any future DSN change.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO deliveries (id, workspace_id, endpoint_id, source_delivery_id, body_sha256, work_id, received_at)
		 VALUES ('dlv_lockholder','ws_lock','ep_lock','src-lockholder','ff','work_lockholder',?)`,
		time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		holder.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(d):
		case <-done:
		}
		_ = tx.Rollback()
		holder.Close()
	}()
	return func() { close(done) }, nil
}

// scenarioContention measures the acceptance path under a deliberately held
// write lock, and answers the question the spike calls out explicitly: does
// context cancellation actually interrupt the 30s busy wait, or does the
// request sit there past its budget?
func scenarioContention(t testingT, poolSize int, syncMode string, ops int, hold time.Duration) scenarioResult {
	ctx := context.Background()
	e := newEnv(t, poolSize, syncMode)
	defer e.close()
	e.migrateRiver(t)

	res := scenarioResult{Scenario: "contention", Detail: map[string]any{
		"pool_size": poolSize, "synchronous": syncMode, "ops": ops, "lock_held": hold.String(),
	}}

	worker := &mockWorker{}
	client := e.client(t, worker, 1)

	// --- baseline: uncontended acceptance ----------------------------------
	var base []time.Duration
	for i := 0; i < ops; i++ {
		start := time.Now()
		out := acceptOnce(ctx, e.pool, client, delivery{
			ID: fmt.Sprintf("dlv_base_%d", i), WorkspaceID: "ws1", EndpointID: "ep1",
			SourceDeliveryID: fmt.Sprintf("src-base-%d", i), BodySHA256: "dd",
			WorkID: fmt.Sprintf("work_base_%d", i),
		})
		if out.err != nil {
			return failed(res, fmt.Errorf("baseline accept %d: %w", i, out.err))
		}
		base = append(base, time.Since(start))
	}
	res.Detail["baseline_p50_ms"] = percentile(base, 0.50).Milliseconds()
	res.Detail["baseline_p95_ms"] = percentile(base, 0.95).Milliseconds()
	res.Detail["baseline_p99_ms"] = percentile(base, 0.99).Milliseconds()

	// --- cancellation: does ctx cut the busy wait short? -------------------
	release, err := holdWriteLock(ctx, e.dbPath, syncMode, hold)
	if err != nil {
		return failed(res, fmt.Errorf("hold write lock: %w", err))
	}

	budget := 500 * time.Millisecond
	cctx, cancel := context.WithTimeout(ctx, budget)
	start := time.Now()
	out := acceptOnce(cctx, e.pool, client, delivery{
		ID: "dlv_cancel", WorkspaceID: "ws1", EndpointID: "ep1",
		SourceDeliveryID: "src-cancel", BodySHA256: "ee", WorkID: "work_cancel",
	})
	cancelLatency := time.Since(start)
	cancel()
	res.Detail["cancel_budget_ms"] = budget.Milliseconds()
	res.Detail["cancel_returned_after_ms"] = cancelLatency.Milliseconds()
	res.Detail["cancel_error"] = errString(out.err)
	// The budget is 500ms and the lock is held for `hold`. Returning near the
	// budget means cancellation works; returning near `hold` (or near the 30s
	// busy_timeout) means the driver ignored it and the HTTP handler would blow
	// its acceptance budget.
	cancelHonoured := out.err != nil && cancelLatency < hold/2
	res.Detail["cancellation_interrupts_busy_wait"] = cancelHonoured
	// A cancelled acceptance that still committed would be the worst outcome:
	// a client that got an error while the work exists.
	var leaked int
	_ = e.pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM deliveries WHERE id = 'dlv_cancel'`).Scan(&leaked)
	res.Detail["cancelled_op_left_row"] = leaked

	// The cancel probe above blocked until the holder released, so take a fresh
	// lock for the budgeted loop rather than measuring an uncontended path.
	release()
	release, err = holdWriteLock(ctx, e.dbPath, syncMode, hold)
	if err != nil {
		return failed(res, fmt.Errorf("re-hold write lock: %w", err))
	}

	// --- under-lock acceptance with a 2s budget ----------------------------
	var busy, timedOut, committed int
	var contended []time.Duration
	for i := 0; i < ops; i++ {
		bctx, bcancel := context.WithTimeout(ctx, 2*time.Second)
		s := time.Now()
		o := acceptOnce(bctx, e.pool, client, delivery{
			ID: fmt.Sprintf("dlv_lock_%d", i), WorkspaceID: "ws1", EndpointID: "ep1",
			SourceDeliveryID: fmt.Sprintf("src-lock-%d", i), BodySHA256: "ff",
			WorkID: fmt.Sprintf("work_lock_%d", i),
		})
		contended = append(contended, time.Since(s))
		bcancel()
		switch {
		case o.err == nil:
			committed++
		case isBusy(o.err):
			busy++
		default:
			timedOut++
		}
	}
	release()
	res.Detail["contended_rejected_within_budget"] = timedOut + busy
	res.Detail["contended_p95_ms"] = percentile(contended, 0.95).Milliseconds()
	res.Detail["contended_p99_ms"] = percentile(contended, 0.99).Milliseconds()
	res.Detail["contended_committed"] = committed
	res.Detail["contended_sqlite_busy"] = busy
	res.Detail["contended_other_error"] = timedOut

	// After the lock is released the path must work again, with no residue.
	post := acceptOnce(ctx, e.pool, client, delivery{
		ID: "dlv_post", WorkspaceID: "ws1", EndpointID: "ep1",
		SourceDeliveryID: "src-post", BodySHA256: "aa", WorkID: "work_post",
	})
	res.Detail["recovered_after_release"] = post.err == nil
	if post.err != nil {
		res.Notes = append(res.Notes, "post-release accept failed: "+post.err.Error())
	}

	// A false 202 is a rejected op whose row exists anyway. Only that, and a
	// broken recovery, fail the scenario: slow-under-lock is information, not
	// a failure.
	res.Pass = leaked == 0 && post.err == nil
	if !cancelHonoured {
		res.Notes = append(res.Notes, "context cancellation did NOT interrupt the SQLite busy wait — the acceptance handler needs its own budget guard")
	}
	return res
}

// scenarioDurability measures what synchronous=FULL costs on the acceptance
// transaction, so the PRD's "FULL on every authoritative connection" is a
// measured decision rather than an assumed one.
func scenarioDurability(t testingT, ops int) scenarioResult {
	ctx := context.Background()
	res := scenarioResult{Scenario: "durability", Detail: map[string]any{"ops": ops}}

	for _, syncMode := range []string{"NORMAL", "FULL"} {
		e := newEnv(t, 1, syncMode)
		e.migrateRiver(t)
		worker := &mockWorker{}
		client := e.client(t, worker, 1)

		// Delivery row only: the cost of one durable domain write.
		var plain []time.Duration
		for i := 0; i < ops; i++ {
			s := time.Now()
			tx, err := e.pool.BeginTx(ctx, nil)
			if err != nil {
				e.close()
				return failed(res, err)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO deliveries (id, workspace_id, endpoint_id, source_delivery_id, body_sha256, work_id, received_at)
				 VALUES (?,?,?,?,?,?,?)`,
				fmt.Sprintf("p_%s_%d", syncMode, i), "ws1", "ep_plain", fmt.Sprintf("src-p-%s-%d", syncMode, i),
				"aa", fmt.Sprintf("work_p_%d", i), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				_ = tx.Rollback()
				e.close()
				return failed(res, err)
			}
			if err := tx.Commit(); err != nil {
				e.close()
				return failed(res, err)
			}
			plain = append(plain, time.Since(s))
		}

		// Delivery row + River enqueue: the real acceptance transaction.
		var full []time.Duration
		for i := 0; i < ops; i++ {
			s := time.Now()
			o := acceptOnce(ctx, e.pool, client, delivery{
				ID: fmt.Sprintf("a_%s_%d", syncMode, i), WorkspaceID: "ws1", EndpointID: "ep_acc",
				SourceDeliveryID: fmt.Sprintf("src-a-%s-%d", syncMode, i), BodySHA256: "bb",
				WorkID: fmt.Sprintf("work_a_%d", i),
			})
			if o.err != nil {
				e.close()
				return failed(res, o.err)
			}
			full = append(full, time.Since(s))
		}

		p := "sync_" + syncMode + "_"
		res.Detail[p+"delivery_only_p50_us"] = percentile(plain, 0.50).Microseconds()
		res.Detail[p+"delivery_only_p95_us"] = percentile(plain, 0.95).Microseconds()
		res.Detail[p+"acceptance_p50_us"] = percentile(full, 0.50).Microseconds()
		res.Detail[p+"acceptance_p95_us"] = percentile(full, 0.95).Microseconds()
		res.Detail[p+"acceptance_p99_us"] = percentile(full, 0.99).Microseconds()
		e.close()
	}
	res.Pass = true
	return res
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// scenarioPool answers the protocol's "odhalit opětovné získávání connection z
// poolu uvnitř již otevřené transakce". River's SQLite driver recommends
// SetMaxOpenConns(1); Crewship runs 5. With a pool of 1, any code that opens a
// *sql.Tx and then needs a second connection from the same pool deadlocks
// until the transaction ends — which is exactly the shape of an acceptance
// handler that enqueues and then reads back.
func scenarioPool(t testingT, syncMode string, poolSizes []int) scenarioResult {
	ctx := context.Background()
	res := scenarioResult{Scenario: "pool", Detail: map[string]any{"synchronous": syncMode}}
	res.Pass = true

	for _, size := range poolSizes {
		e := newEnv(t, size, syncMode)
		e.migrateRiver(t)

		tx, err := e.pool.BeginTx(ctx, nil)
		if err != nil {
			e.close()
			return failed(res, err)
		}
		// A second connection is requested from the same pool while the
		// transaction is open. database/sql blocks here when the pool is
		// exhausted; ctx is the only escape.
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		start := time.Now()
		var n int
		perr := e.pool.QueryRowContext(probeCtx, `SELECT COUNT(*) FROM river_job`).Scan(&n)
		took := time.Since(start)
		cancel()
		_ = tx.Rollback()

		key := fmt.Sprintf("pool_%d_", size)
		res.Detail[key+"second_conn_ok"] = perr == nil
		res.Detail[key+"second_conn_ms"] = took.Milliseconds()
		res.Detail[key+"second_conn_err"] = errString(perr)
		if perr != nil {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"pool=%d: a second connection inside an open transaction starved (%v) — River's recommended single-connection pool is incompatible with any acceptance path that reads back through the pool",
				size, perr))
		}
		e.close()
	}
	return res
}
