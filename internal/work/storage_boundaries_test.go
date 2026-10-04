package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAcceptorRollsBackCallbackAndDeferredCommitFailures(t *testing.T) {
	_, db, _ := newTestStore(t)
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	acc, err := OpenAcceptor(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	if acc.Budget() != DefaultAcceptanceBudget {
		t.Fatalf("default acceptance budget = %s", acc.Budget())
	}
	sentinel := errors.New("domain write refused")
	err = acc.Do(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := acc.Store().AcceptTx(ctx, tx, backgroundReq("agent")); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("callback error lost: %v", err)
	}
	if countRows(t, db, "work_items") != 0 || countRows(t, db, "work_events") != 0 {
		t.Fatal("callback failure committed work")
	}
	if _, err := db.Exec(`CREATE TABLE acceptance_parent (id INTEGER PRIMARY KEY);
CREATE TABLE acceptance_child (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES acceptance_parent(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	err = acc.Do(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO acceptance_child VALUES (1,99)`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "commit acceptance") || errors.Is(err, ErrAcceptanceBudget) {
		t.Fatalf("commit failure acknowledged or mislabeled: %v", err)
	}
	if countRows(t, db, "acceptance_child") != 0 {
		t.Fatal("failed commit left a domain row")
	}
	if err := acc.Do(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO acceptance_parent VALUES (99)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO acceptance_child VALUES (1,99)`)
		return err
	}); err != nil {
		t.Fatalf("failed commit poisoned the next acceptance: %v", err)
	}
	if countRows(t, db, "acceptance_child") != 1 {
		t.Fatal("retry did not persist the valid domain row")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := acc.Do(ctx, func(context.Context, *sql.Tx) error { t.Error("cancelled acceptance called domain writer"); return nil }); !errors.Is(err, ErrAcceptanceBudget) {
		t.Fatalf("cancelled acceptance: %v", err)
	}
	if err := acc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := acc.Do(t.Context(), func(context.Context, *sql.Tx) error { t.Error("closed handle called domain writer"); return nil }); err == nil || errors.Is(err, ErrAcceptanceBudget) {
		t.Fatalf("closed acceptance handle: %v", err)
	}
	if other, err := OpenAcceptor(filepath.Join(path, "not-a-directory"), time.Second); err == nil {
		_ = other.Close()
		t.Fatal("invalid catalog path opened")
	}
}

func TestDiskGuardUsesTheCatalogFilesystem(t *testing.T) {
	dir := t.TempDir()
	g := NewDiskGuard(dir)
	if g.Path != dir || g.MinFree != DefaultMinFreeDiskBytes {
		t.Fatalf("wrong catalog guard: %#v", g)
	}
	g.MinFree = 1 << 62
	if err := g.Check(); !errors.Is(err, ErrDiskPressure) {
		t.Fatalf("impossible space demand was accepted: %v", err)
	}
}

func TestMissingLedgerTablesRefuseAuthorityAndCanRecover(t *testing.T) {
	s, db, _ := newTestStore(t)
	r := accept(t, s, db, backgroundReq("agent"))
	c, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, table := range []string{"work_items", "work_attempts"} {
		t.Run(table, func(t *testing.T) {
			before := ledgerSnapshot(t, db)
			if _, err := db.Exec("ALTER TABLE " + table + " RENAME TO unavailable_ledger_table"); err != nil {
				t.Fatal(err)
			}
			operations := map[string]func() error{
				"transition": func() error {
					return s.Transition(ctx, TransitionRequest{WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation, To: StateSucceeded})
				},
				"start intent":     func() error { return s.MarkStarting(ctx, r.WorkID, c.RunID, c.Generation, "runtime") },
				"running":          func() error { return s.StartRunning(ctx, r.WorkID, c.RunID, c.Generation, "runtime") },
				"creation request": func() error { return s.MarkRuntimeRequested(ctx, r.WorkID, c.RunID, c.Generation) },
				"cancel":           func() error { _, err := s.RequestCancel(ctx, r.WorkID, "operator", "stop"); return err },
				"defer": func() error {
					return s.Defer(ctx, r.WorkID, c.RunID, c.Generation, s.now().Add(time.Minute), "waiting")
				},
			}
			if table == "work_items" {
				operations["get"] = func() error { _, err := s.Get(ctx, r.WorkID); return err }
				operations["claim"] = func() error { _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker"}); return err }
				operations["usage"] = func() error { _, err := s.WorkspaceUsage(ctx, "ws1", IngressLimits{}); return err }
				operations["resolve"] = func() error { return s.Resolve(ctx, r.WorkID, c.Generation, StateFailed, "operator", "confirmed") }
			}
			for name, run := range operations {
				t.Run(name, func(t *testing.T) {
					if err := run(); err == nil || !strings.Contains(err.Error(), "no such table") {
						t.Fatalf("unavailable authority was not reported: %v", err)
					}
				})
			}
			if _, err := db.Exec("ALTER TABLE unavailable_ledger_table RENAME TO " + table); err != nil {
				t.Fatal(err)
			}
			if got := ledgerSnapshot(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("missing authority caused partial mutation")
			}
		})
	}
	if err := s.MarkStarting(ctx, r.WorkID, c.RunID, c.Generation, "runtime"); err != nil {
		t.Fatalf("restored ledger cannot proceed: %v", err)
	}
}

func TestRetentionFailedBatchDoesNotReportRolledBackDeletion(t *testing.T) {
	s, db, clock := newTestStore(t)
	var ids []string
	for _, id := range []string{"first", "second"} {
		d := retDelivery(id, "endpoint", []byte("retained input"))
		d.FilterDecision = FilterIgnored
		r := acceptDelivery(t, s, db, d, retWork())
		ids = append(ids, r.DeliveryID)
	}
	clock.Advance(8 * 24 * time.Hour)
	if _, err := db.Exec(`CREATE TRIGGER fail_second_expiry BEFORE UPDATE ON webhook_deliveries WHEN old.id = '` + ids[1] + `' BEGIN SELECT RAISE(ABORT,'second expiry refused'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.Sweep(t.Context(), RetentionPolicy{Batch: 2})
	if err == nil || !strings.Contains(err.Error(), "second expiry refused") {
		t.Fatalf("failed batch was accepted: %#v %v", result, err)
	}
	for _, id := range ids {
		if got := rawBodyOf(t, db, id); string(got) != "retained input" {
			t.Fatalf("failed batch removed payload %s", id)
		}
	}
	if result.RawBodiesExpired != 0 || result.RawBodyBytesFreed != 0 || result.Batches != 0 {
		t.Fatalf("rolled-back payloads reported as removed: %#v", result)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_second_expiry`); err != nil {
		t.Fatal(err)
	}
	result, err = s.Sweep(t.Context(), RetentionPolicy{Batch: 2})
	if err != nil || result.RawBodiesExpired != 2 || result.RawBodyBytesFreed != int64(2*len("retained input")) {
		t.Fatalf("expiry retry: %#v %v", result, err)
	}
}

func TestRetentionKeepsCommittedBatchAccountingAfterLaterFailure(t *testing.T) {
	s, db, clock := newTestStore(t)
	var ids []string
	for _, source := range []string{"first", "second", "third", "fourth"} {
		d := retDelivery(source, "endpoint", []byte("payload"))
		d.FilterDecision = FilterIgnored
		ids = append(ids, acceptDelivery(t, s, db, d, retWork()).DeliveryID)
	}
	clock.Advance(8 * 24 * time.Hour)
	if _, err := db.Exec(`CREATE TRIGGER fail_later_expiry BEFORE UPDATE ON webhook_deliveries WHEN old.id = '` + ids[3] + `' BEGIN SELECT RAISE(ABORT,'later expiry refused'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.Sweep(t.Context(), RetentionPolicy{Batch: 2})
	if err == nil || !strings.Contains(err.Error(), "later expiry refused") {
		t.Fatalf("expected failure: %#v %v", result, err)
	}
	if result.RawBodiesExpired != 2 || result.RawBodyBytesFreed != 14 || result.Batches != 1 {
		t.Fatalf("committed batch accounting lost or rollback counted: %#v", result)
	}
	for i, id := range ids {
		payload := rawBodyOf(t, db, id)
		if i < 2 && payload != nil {
			t.Fatalf("committed expiry missing for %s", id)
		}
		if i >= 2 && string(payload) != "payload" {
			t.Fatalf("failed batch partially removed %s", id)
		}
	}
}

func TestCancelWithoutOpenAttemptRequiresReconciliationOnce(t *testing.T) {
	s, db, _ := newTestStore(t)
	r := accept(t, s, db, backgroundReq("agent"))
	c, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	startRuntime(t, s, r.WorkID, c, "runtime-may-still-exist")
	if _, err := db.Exec(`DELETE FROM work_attempts WHERE run_id=?`, c.RunID); err != nil {
		t.Fatal(err)
	}
	first, err := s.RequestCancel(t.Context(), r.WorkID, "operator", "stop requested")
	if err != nil || first.Outcome != CancelOutcomeRequested || first.State != StateNeedsReconciliation {
		t.Fatalf("missing attempt was treated as stopped: %#v %v", first, err)
	}
	before := ledgerSnapshot(t, db)
	second, err := s.RequestCancel(t.Context(), r.WorkID, "operator", "stop requested again")
	if err != nil || second.Outcome != CancelOutcomeRequested || second.State != StateNeedsReconciliation {
		t.Fatalf("repeated request: %#v %v", second, err)
	}
	if !reflect.DeepEqual(ledgerSnapshot(t, db), before) {
		t.Fatal("repeated reconciliation request changed history")
	}
	if _, err := s.CancelRequested(t.Context(), c.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invented cancel status for missing attempt: %v", err)
	}
}

func TestResolutionFailureDoesNotReleaseUnreconciledCapacity(t *testing.T) {
	for _, table := range []string{"work_items", "work_events", "work_attempts"} {
		t.Run(table, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			r := accept(t, s, db, backgroundReq("agent"))
			c, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
			if err != nil {
				t.Fatal(err)
			}
			startRuntime(t, s, r.WorkID, c, "runtime")
			if err := s.Transition(t.Context(), TransitionRequest{WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation, To: StateNeedsReconciliation, Reason: "lost worker"}); err != nil {
				t.Fatal(err)
			}
			before := ledgerSnapshot(t, db)
			mutation := "UPDATE"
			if table == "work_events" {
				mutation = "INSERT"
			}
			failLedgerWrite(t, db, mutation, table)
			if err := s.Resolve(t.Context(), r.WorkID, c.Generation, StateFailed, "operator", "runtime stopped"); err == nil || !strings.Contains(err.Error(), "ledger storage unavailable") {
				t.Fatalf("failed resolution accepted: %v", err)
			}
			if !reflect.DeepEqual(ledgerSnapshot(t, db), before) {
				t.Fatal("failed resolution changed authority or history")
			}
			clearLedgerFailure(t, db)
			if err := s.Resolve(t.Context(), r.WorkID, c.Generation, StateFailed, "operator", "runtime stopped"); err != nil {
				t.Fatal(err)
			}
			item, err := s.Get(t.Context(), r.WorkID)
			if err != nil || item.State != StateFailed {
				t.Fatalf("resolution retry: %#v %v", item, err)
			}
		})
	}
}

func TestRecoveryFailureLeavesAttemptsAndGenerationsIntact(t *testing.T) {
	for _, table := range []string{"work_items", "work_events", "work_attempts"} {
		t.Run(table, func(t *testing.T) {
			s, db, clock := newTestStore(t)
			r := accept(t, s, db, backgroundReq("agent"))
			if _, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"}); err != nil {
				t.Fatal(err)
			}
			clock.Advance(LeaseDuration + time.Second)
			before := ledgerSnapshot(t, db)
			mutation := "UPDATE"
			if table == "work_events" {
				mutation = "INSERT"
			}
			failLedgerWrite(t, db, mutation, table)
			if _, err := s.RecoverExpiredLeases(t.Context()); err == nil || !strings.Contains(err.Error(), "ledger storage unavailable") {
				t.Fatalf("failed recovery accepted: %v", err)
			}
			if !reflect.DeepEqual(ledgerSnapshot(t, db), before) {
				t.Fatal("failed recovery partially ended attempt")
			}
			clearLedgerFailure(t, db)
			out, err := s.RecoverExpiredLeases(t.Context())
			if err != nil || !reflect.DeepEqual(out.Requeued, []string{r.WorkID}) {
				t.Fatalf("recovery retry: %#v %v", out, err)
			}
		})
	}
}

func TestClosedLedgerRefusesMutationsAndAuthoritativeReads(t *testing.T) {
	s, db, clock := newTestStore(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	operations := map[string]func() error{
		"claim": func() error {
			got, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker"})
			if got != nil {
				t.Error("failed claim returned work")
			}
			return err
		},
		"get": func() error { _, err := s.Get(ctx, "work"); return err },
		"transition": func() error {
			return s.Transition(ctx, TransitionRequest{WorkID: "work", RunID: "run", Generation: 1, To: StateSucceeded})
		},
		"start intent":     func() error { return s.MarkStarting(ctx, "work", "run", 1, "runtime") },
		"running":          func() error { return s.StartRunning(ctx, "work", "run", 1, "runtime") },
		"creation request": func() error { return s.MarkRuntimeRequested(ctx, "work", "run", 1) },
		"heartbeat":        func() error { return s.Heartbeat(ctx, "run", 1) },
		"recovery":         func() error { _, err := s.RecoverExpiredLeases(ctx); return err },
		"history":          func() error { _, err := s.History(ctx, "work"); return err },
		"cancel":           func() error { _, err := s.RequestCancel(ctx, "work", "operator", "stop"); return err },
		"cancel status":    func() error { _, err := s.CancelRequested(ctx, "run"); return err },
		"resolve":          func() error { return s.Resolve(ctx, "work", 1, StateFailed, "operator", "confirmed") },
		"defer":            func() error { return s.Defer(ctx, "work", "run", 1, clock.Now().Add(time.Minute), "approval pending") },
		"stage result":     func() error { return s.StageRunResult(ctx, "work", "run", 1, RunResult{}) },
		"projection": func() error {
			p, _, err := s.RunProjection(ctx, "run")
			if p.Ready {
				t.Error("unreadable result was ready")
			}
			return err
		},
		"pending projections": func() error { _, err := s.PendingRunProjections(ctx, 0); return err },
		"workspace usage":     func() error { _, err := s.WorkspaceUsage(ctx, "ws1", IngressLimits{}); return err },
		"usage report":        func() error { _, err := s.UsageReport(ctx, IngressLimits{}); return err },
	}
	for name, run := range operations {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("unavailable database reported success")
			}
		})
	}
}

func TestAcceptanceValidationDoesNotCreateLedgerRows(t *testing.T) {
	s, db, _ := newTestStore(t)
	for _, tc := range []struct {
		name   string
		change func(*AcceptRequest)
	}{
		{"blank workspace", func(r *AcceptRequest) { r.WorkspaceID = "  " }},
		{"missing source", func(r *AcceptRequest) { r.Source = "" }},
		{"unknown class", func(r *AcceptRequest) { r.Class = "unrecognized" }},
		{"unbound replay reason", func(r *AcceptRequest) { r.ReplayReason = "retry" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := backgroundReq("agent")
			tc.change(&r)
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			got, acceptErr := s.AcceptTx(t.Context(), tx, r)
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if acceptErr == nil || got.WorkID != "" {
				t.Fatalf("invalid request acknowledged: %#v %v", got, acceptErr)
			}
		})
	}
	if countRows(t, db, "work_items") != 0 || countRows(t, db, "work_events") != 0 {
		t.Fatal("invalid acceptance wrote rows")
	}
}

func TestRunResultValidationPreservesDurableOutput(t *testing.T) {
	s, db, _ := newTestStore(t)
	r := accept(t, s, db, backgroundReq("agent"))
	c, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []RunResult{{Metadata: map[string]any{"invalid": make(chan int)}}, {Metadata: map[string]any{"oversized": strings.Repeat("x", 512*1024)}}} {
		if err := s.StageRunResult(t.Context(), r.WorkID, c.RunID, c.Generation, result); err == nil {
			t.Fatal("unserializable or oversized result accepted")
		}
	}
	if err := s.StageRunResult(t.Context(), "", c.RunID, c.Generation, RunResult{}); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unbound output: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.StageRunResult(ctx, r.WorkID, c.RunID, c.Generation, RunResult{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled output write: %v", err)
	}
	var stored sql.NullString
	if err := db.QueryRow(`SELECT run_result_json FROM work_attempts WHERE run_id=?`, c.RunID).Scan(&stored); err != nil || stored.Valid {
		t.Fatalf("invalid result persisted: %v", err)
	}
	startRuntime(t, s, r.WorkID, c, "runtime")
	if err := s.Transition(t.Context(), TransitionRequest{WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation, To: StateSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_attempts SET run_result_json='broken JSON' WHERE run_id=?`, c.RunID); err != nil {
		t.Fatal(err)
	}
	if _, owned, err := s.RunProjection(t.Context(), c.RunID); err == nil || !owned || !strings.Contains(err.Error(), "decode run result") {
		t.Fatalf("corrupt owned output treated as unowned: owned=%v err=%v", owned, err)
	}
}

func ledgerSnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	out := map[string][][]any{}
	for _, q := range []string{"SELECT * FROM work_items ORDER BY id", "SELECT * FROM work_attempts ORDER BY run_id", "SELECT * FROM work_events ORDER BY work_id, seq"} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			out[q] = append(out[q], values)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func failLedgerWrite(t *testing.T, db *sql.DB, operation, table string) {
	t.Helper()
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_ledger_write BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'ledger storage unavailable'); END`, operation, table)); err != nil {
		t.Fatal(err)
	}
}

func clearLedgerFailure(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP TRIGGER fail_ledger_write`); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceStorageFailureLeavesNoReceiptOrPartialLedger(t *testing.T) {
	for _, table := range []string{"work_items", "work_events"} {
		t.Run(table, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			before := ledgerSnapshot(t, db)
			failLedgerWrite(t, db, "INSERT", table)
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			receipt, acceptErr := s.AcceptTx(t.Context(), tx, backgroundReq("agent"))
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if acceptErr == nil || receipt.WorkID != "" || !strings.Contains(acceptErr.Error(), "ledger storage unavailable") {
				t.Fatalf("failed acceptance acknowledged: %#v %v", receipt, acceptErr)
			}
			if got := ledgerSnapshot(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("failed acceptance left partial ledger")
			}
			clearLedgerFailure(t, db)
			if receipt := accept(t, s, db, backgroundReq("agent")); receipt.WorkID == "" {
				t.Fatal("retry did not accept")
			}
		})
	}
}

func TestClaimStorageFailureRollsBackGenerationAttemptAndHistory(t *testing.T) {
	for _, stage := range []struct{ operation, table string }{{"UPDATE", "work_items"}, {"INSERT", "work_attempts"}, {"INSERT", "work_events"}} {
		t.Run(stage.table, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			r := accept(t, s, db, backgroundReq("agent"))
			before := ledgerSnapshot(t, db)
			failLedgerWrite(t, db, stage.operation, stage.table)
			claimed, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
			if err == nil || claimed != nil || !strings.Contains(err.Error(), "ledger storage unavailable") {
				t.Fatalf("failed claim acknowledged: %#v %v", claimed, err)
			}
			if got := ledgerSnapshot(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("failed claim consumed generation or capacity")
			}
			clearLedgerFailure(t, db)
			claimed, err = s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
			if err != nil || claimed == nil || claimed.Generation != 1 {
				t.Fatalf("retry claim: %#v %v", claimed, err)
			}
			item, err := s.Get(t.Context(), r.WorkID)
			if err != nil || item.Attempts != 1 {
				t.Fatalf("failed claim spent attempt budget: %#v %v", item, err)
			}
		})
	}
}

func TestWorkerMutationsRollbackOnStorageFailure(t *testing.T) {
	type operation struct {
		name    string
		prepare func(*testing.T, *Store, string, *Claimed)
		run     func(*Store, string, *Claimed) error
		tables  []string
	}
	for _, op := range []operation{
		{"start intent", nil, func(s *Store, id string, c *Claimed) error {
			return s.MarkStarting(t.Context(), id, c.RunID, c.Generation, "owned-runtime")
		}, []string{"work_attempts"}},
		{"creation request", func(t *testing.T, s *Store, id string, c *Claimed) {
			if err := s.MarkStarting(t.Context(), id, c.RunID, c.Generation, "owned-runtime"); err != nil {
				t.Fatal(err)
			}
		}, func(s *Store, id string, c *Claimed) error {
			return s.MarkRuntimeRequested(t.Context(), id, c.RunID, c.Generation)
		}, []string{"work_attempts"}},
		{"completion", func(t *testing.T, s *Store, id string, c *Claimed) { startRuntime(t, s, id, c, "owned-runtime") }, func(s *Store, id string, c *Claimed) error {
			return s.Transition(t.Context(), TransitionRequest{WorkID: id, RunID: c.RunID, Generation: c.Generation, To: StateSucceeded, Reason: "confirmed result"})
		}, []string{"work_items", "work_attempts", "work_events"}},
		{"retry", func(t *testing.T, s *Store, id string, c *Claimed) { startRuntime(t, s, id, c, "owned-runtime") }, func(s *Store, id string, c *Claimed) error {
			return s.Transition(t.Context(), TransitionRequest{WorkID: id, RunID: c.RunID, Generation: c.Generation, To: StateRetryWait, Reason: "retryable result"})
		}, []string{"work_items", "work_attempts", "work_events"}},
		{"cancel request", func(t *testing.T, s *Store, id string, c *Claimed) { startRuntime(t, s, id, c, "owned-runtime") }, func(s *Store, id string, _ *Claimed) error {
			_, err := s.RequestCancel(t.Context(), id, "operator", "stop requested")
			return err
		}, []string{"work_attempts", "work_events"}},
		{"deferral", nil, func(s *Store, id string, c *Claimed) error {
			return s.Defer(t.Context(), id, c.RunID, c.Generation, s.now().Add(time.Minute), "awaiting approval")
		}, []string{"work_items", "work_attempts", "work_events"}},
	} {
		for _, table := range op.tables {
			t.Run(op.name+"/"+table, func(t *testing.T) {
				s, db, _ := newTestStore(t)
				r := accept(t, s, db, backgroundReq("agent"))
				c, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
				if err != nil {
					t.Fatal(err)
				}
				if op.prepare != nil {
					op.prepare(t, s, r.WorkID, c)
				}
				before := ledgerSnapshot(t, db)
				mutation := "UPDATE"
				if table == "work_events" {
					mutation = "INSERT"
				}
				failLedgerWrite(t, db, mutation, table)
				if err := op.run(s, r.WorkID, c); err == nil || !strings.Contains(err.Error(), "ledger storage unavailable") {
					t.Fatalf("failed %s acknowledged: %v", op.name, err)
				}
				if got := ledgerSnapshot(t, db); !reflect.DeepEqual(got, before) {
					t.Fatalf("failed %s left partial state", op.name)
				}
				clearLedgerFailure(t, db)
				if err := op.run(s, r.WorkID, c); err != nil {
					t.Fatalf("retry %s: %v", op.name, err)
				}
			})
		}
	}
}

func TestDeliveryWriteFailureRollsBackWorkAndOriginalReceipt(t *testing.T) {
	s, db, _ := newTestStore(t)
	original := retDelivery("original", "endpoint", []byte("original payload"))
	original.ID = "existing-delivery"
	receipt := acceptDelivery(t, s, db, original, retWork())
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprint(collision), func(t *testing.T) {
			before := ledgerSnapshot(t, db)
			incoming := retDelivery("new-source", "endpoint", []byte("new payload"))
			if collision {
				incoming.ID = original.ID
			} else {
				failLedgerWrite(t, db, "INSERT", "webhook_deliveries")
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			got, acceptErr := s.AcceptDeliveryTx(t.Context(), tx, incoming, retWork())
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if acceptErr == nil || got.WorkID != "" || got.DeliveryID != "" {
				t.Fatalf("failed delivery acknowledged: %#v %v", got, acceptErr)
			}
			if collision && !errors.Is(acceptErr, ErrDuplicateDelivery) {
				t.Fatalf("collision classification: %v", acceptErr)
			}
			if !collision && !strings.Contains(acceptErr.Error(), "ledger storage unavailable") {
				t.Fatalf("write failure lost: %v", acceptErr)
			}
			if !reflect.DeepEqual(before, ledgerSnapshot(t, db)) || countRows(t, db, "webhook_deliveries") != 1 {
				t.Fatal("failed delivery left orphan work or changed original")
			}
			if string(rawBodyOf(t, db, receipt.DeliveryID)) != "original payload" {
				t.Fatal("collision overwrote original payload")
			}
			if !collision {
				clearLedgerFailure(t, db)
			}
		})
	}
}

func TestConfiguredIngressRefusesNewWorkButPreservesDuplicateReceipts(t *testing.T) {
	base, db, _ := newTestStore(t)
	s := base.WithIngressLimits(IngressLimits{EndpointNonTerminal: 1})
	original := retDelivery("original", "endpoint", []byte("original payload"))
	receipt := acceptDelivery(t, s, db, original, retWork())
	duplicate := acceptDelivery(t, s, db, original, retWork())
	if !duplicate.Duplicate || duplicate.WorkID != receipt.WorkID || duplicate.DeliveryID != receipt.DeliveryID {
		t.Fatalf("full endpoint lost existing receipt: %#v", duplicate)
	}
	before := ledgerSnapshot(t, db)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, acceptErr := s.AcceptDeliveryTx(t.Context(), tx, retDelivery("new", "endpoint", []byte("new payload")), retWork())
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(acceptErr, ErrEndpointFull) || got.WorkID != "" {
		t.Fatalf("configured limit bypassed: %#v %v", got, acceptErr)
	}
	if !reflect.DeepEqual(before, ledgerSnapshot(t, db)) || countRows(t, db, "webhook_deliveries") != 1 {
		t.Fatal("refusal mutated existing work")
	}
	// A cloned store's configuration must not silently tighten the original store.
	if got := acceptDelivery(t, base, db, retDelivery("base-arrival", "endpoint", []byte("new payload")), retWork()); got.WorkID == "" {
		t.Fatal("configured clone changed original limits")
	}
}

func TestQueuedCancellationFailureDoesNotPublishTerminalState(t *testing.T) {
	for _, table := range []string{"work_items", "work_events"} {
		t.Run(table, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			receipt := accept(t, s, db, backgroundReq("agent"))
			before := ledgerSnapshot(t, db)
			operation := "UPDATE"
			if table == "work_events" {
				operation = "INSERT"
			}
			failLedgerWrite(t, db, operation, table)
			if _, err := s.RequestCancel(t.Context(), receipt.WorkID, "operator", "stop"); err == nil || !strings.Contains(err.Error(), "ledger storage unavailable") {
				t.Fatalf("failed cancellation acknowledged: %v", err)
			}
			if !reflect.DeepEqual(before, ledgerSnapshot(t, db)) {
				t.Fatal("failed cancellation partially changed work")
			}
			clearLedgerFailure(t, db)
			result, err := s.RequestCancel(t.Context(), receipt.WorkID, "operator", "stop")
			if err != nil || result.Outcome != CancelOutcomeCancelled || result.State != StateCancelled {
				t.Fatalf("cancellation retry: %#v %v", result, err)
			}
		})
	}
}

func TestDeliveryValidationAndLookupNeverInventReceipts(t *testing.T) {
	s, db, _ := newTestStore(t)
	for _, change := range []func(*Delivery){
		func(d *Delivery) { d.WorkspaceID = " " }, func(d *Delivery) { d.EndpointID = "" },
		func(d *Delivery) { d.SourceDeliveryID = " " }, func(d *Delivery) { d.FilterDecision = "unknown" },
	} {
		d := retDelivery("invalid", "endpoint", nil)
		change(&d)
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		result, acceptErr := s.AcceptDeliveryTx(t.Context(), tx, d, retWork())
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if acceptErr == nil || result != (Receipt{}) {
			t.Fatalf("invalid arrival acknowledged: %#v %v", result, acceptErr)
		}
	}
	if countRows(t, db, "webhook_deliveries") != 0 || countRows(t, db, "work_items") != 0 {
		t.Fatal("invalid delivery persisted")
	}
	if got, err := s.LookupDelivery(t.Context(), "ws1", "endpoint", "missing"); !errors.Is(err, ErrNotFound) || got != nil {
		t.Fatalf("invented missing receipt: %#v %v", got, err)
	}
	if _, err := s.PayloadStatus(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invented payload: %v", err)
	}
	d := retDelivery("ignored", "endpoint", []byte("ignored payload"))
	d.FilterDecision = FilterIgnored
	original := acceptDelivery(t, s, db, d, retWork())
	got, err := s.LookupDelivery(t.Context(), "ws1", "endpoint", "ignored", d.BodySHA256)
	if err != nil || got.DeliveryID != original.DeliveryID || got.WorkID != "" || got.State != "" {
		t.Fatalf("ignored receipt became work: %#v %v", got, err)
	}
	if got, err := s.LookupDelivery(t.Context(), "ws1", "endpoint", "ignored", "different-body"); !errors.Is(err, ErrDeliveryConflict) || got != nil {
		t.Fatalf("conflicting body acknowledged: %#v %v", got, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupDelivery(t.Context(), "ws1", "endpoint", "ignored"); err == nil {
		t.Fatal("closed ledger returned receipt")
	}
	if _, err := s.PayloadStatus(t.Context(), original.DeliveryID); err == nil {
		t.Fatal("closed ledger returned payload")
	}
}

func TestInvalidWorkerDecisionsPreserveTheLedger(t *testing.T) {
	s, db, _ := newTestStore(t)
	receipt := accept(t, s, db, backgroundReq("agent"))
	if c, err := s.Claim(t.Context(), ClaimOptions{}); err == nil || c != nil {
		t.Fatalf("anonymous worker claimed: %#v %v", c, err)
	}
	c, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	before := ledgerSnapshot(t, db)
	for name, run := range map[string]func() error{
		"unknown transition": func() error {
			return s.Transition(t.Context(), TransitionRequest{WorkID: receipt.WorkID, RunID: c.RunID, Generation: c.Generation, To: "unknown"})
		},
		"stopped failure without reconciliation": func() error {
			return s.Transition(t.Context(), TransitionRequest{WorkID: receipt.WorkID, RunID: c.RunID, Generation: c.Generation, To: StateFailed, StoppedRunFailed: true})
		},
		"missing deferral reason": func() error {
			return s.Defer(t.Context(), receipt.WorkID, c.RunID, c.Generation, s.now().Add(time.Minute), "")
		},
		"resolution without actor":  func() error { return s.Resolve(t.Context(), receipt.WorkID, c.Generation, StateFailed, "", "observed") },
		"resolution without reason": func() error { return s.Resolve(t.Context(), receipt.WorkID, c.Generation, StateFailed, "operator", "") },
		"resolution of stale generation": func() error {
			return s.Resolve(t.Context(), receipt.WorkID, c.Generation+1, StateFailed, "operator", "observed")
		},
		"resolution of active work": func() error {
			return s.Resolve(t.Context(), receipt.WorkID, c.Generation, StateFailed, "operator", "observed")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("invalid decision accepted")
			}
			if !reflect.DeepEqual(before, ledgerSnapshot(t, db)) {
				t.Fatal("invalid decision changed durable state")
			}
		})
	}
	startRuntime(t, s, receipt.WorkID, c, "runtime")
	if err := s.Transition(t.Context(), TransitionRequest{WorkID: receipt.WorkID, RunID: c.RunID, Generation: c.Generation, To: StateNeedsReconciliation}); err != nil {
		t.Fatal(err)
	}
	before = ledgerSnapshot(t, db)
	if err := s.Resolve(t.Context(), receipt.WorkID, c.Generation, StateRunning, "operator", "observed"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("illegal resolution edge: %v", err)
	}
	if !reflect.DeepEqual(before, ledgerSnapshot(t, db)) {
		t.Fatal("illegal resolution changed state")
	}
	if err := s.Resolve(t.Context(), receipt.WorkID, c.Generation, StateFailed, "operator", "runtime confirmed stopped"); err != nil {
		t.Fatal(err)
	}
	before = ledgerSnapshot(t, db)
	if err := s.Defer(t.Context(), receipt.WorkID, c.RunID, c.Generation, s.now().Add(time.Minute), "wait"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("terminal deferral: %v", err)
	}
	if !reflect.DeepEqual(before, ledgerSnapshot(t, db)) {
		t.Fatal("terminal work deferred")
	}
}

func TestUnavailableDeliveryLedgerRefusesAcceptanceAndRetention(t *testing.T) {
	s, db, _ := newTestStore(t)
	if _, err := db.Exec(`ALTER TABLE webhook_deliveries RENAME TO unavailable_deliveries`); err != nil {
		t.Fatal(err)
	}
	before := ledgerSnapshot(t, db)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, acceptErr := s.AcceptDeliveryTx(t.Context(), tx, retDelivery("new", "endpoint", []byte("body")), retWork())
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if acceptErr == nil || receipt != (Receipt{}) {
		t.Fatalf("missing ledger accepted: %#v %v", receipt, acceptErr)
	}
	for _, req := range []IngressRequest{{WorkspaceID: "ws1", EndpointID: "endpoint", SourceDeliveryID: "new"}, {WorkspaceID: "ws1", EndpointID: "endpoint"}} {
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ingressErr := s.CheckIngressTx(t.Context(), tx, IngressLimits{}, req)
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if ingressErr == nil {
			t.Fatal("unreadable capacity admitted work")
		}
	}
	if res, err := s.Sweep(t.Context(), RetentionPolicy{}); err == nil || res != (SweepResult{}) {
		t.Fatalf("unavailable retention reported success: %#v %v", res, err)
	}
	if !reflect.DeepEqual(before, ledgerSnapshot(t, db)) {
		t.Fatal("unavailable delivery ledger mutated work")
	}
	if _, err := db.Exec(`ALTER TABLE unavailable_deliveries RENAME TO webhook_deliveries`); err != nil {
		t.Fatal(err)
	}
	if receipt := acceptDelivery(t, s, db, retDelivery("new", "endpoint", []byte("body")), retWork()); receipt.WorkID == "" {
		t.Fatal("restored ledger refused arrival")
	}
}

func TestDedupDeletionFailureRetainsRowsAndRetriesAtomically(t *testing.T) {
	s, db, clock := newTestStore(t)
	for _, id := range []string{"first", "second"} {
		d := retDelivery(id, "endpoint", nil)
		d.FilterDecision = FilterIgnored
		acceptDelivery(t, s, db, d, retWork())
	}
	clock.Advance(31 * 24 * time.Hour)
	failLedgerWrite(t, db, "DELETE", "webhook_deliveries")
	result, err := s.Sweep(t.Context(), RetentionPolicy{Batch: 2})
	if err == nil || !strings.Contains(err.Error(), "ledger storage unavailable") || result.DeliveriesRemoved != 0 || countRows(t, db, "webhook_deliveries") != 2 {
		t.Fatalf("failed dedup deletion: %#v %v", result, err)
	}
	clearLedgerFailure(t, db)
	result, err = s.Sweep(t.Context(), RetentionPolicy{Batch: 2})
	if err != nil || result.DeliveriesRemoved != 2 || countRows(t, db, "webhook_deliveries") != 0 {
		t.Fatalf("dedup retry: %#v %v", result, err)
	}
}
