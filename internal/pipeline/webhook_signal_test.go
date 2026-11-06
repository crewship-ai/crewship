package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func signalTestWebhook() *Webhook {
	return &Webhook{ID: "wh1", WorkspaceID: "ws", TargetPipelineID: "pipe1",
		SignalEventType: "ev", SignalCorrelationPath: "order.id"}
}

func seedSignalRuns(t *testing.T, s *SQLSignalWaitStore, runs ...string) {
	t.Helper()
	s.db.SetMaxOpenConns(1)
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if _, err := s.db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at) VALUES (?, 'ws', 'pipe1', 's', 'waiting', '2026-01-01T00:00:00Z')`, r); err != nil {
			t.Fatal(err)
		}
		armFiltered(t, s, "ws", r, "ev", "", "A-1")
	}
}

// The receipt is keyed on the signed body, not on the unsigned delivery id.
func TestDeliverWebhookSignal_BodyIdentity(t *testing.T) {
	ctx := context.Background()
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	seedSignalRuns(t, s, "r1")
	wh := signalTestWebhook()
	body := []byte(`{"order":{"id":"A-1"}}`)

	first, err := DeliverWebhookSignal(ctx, db, wh, "d1", body)
	if err != nil || first.Duplicate || len(first.RunIDs) != 1 {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	// Same signed bytes, new delivery id: a captured request replayed against
	// a later wait must not wake it.
	seedSignalRuns(t, s, "r2")
	replay, err := DeliverWebhookSignal(ctx, db, wh, "d-new", body)
	if err != nil || !replay.Duplicate {
		t.Fatalf("replay with a new id = %+v err=%v, want duplicate", replay, err)
	}
	if st, _ := waitStatus(t, db, "r2"); st != "pending" {
		t.Fatalf("replayed body woke a later wait: %s", st)
	}
	// Same id, different body: conflict, nothing delivered.
	_, err = DeliverWebhookSignal(ctx, db, wh, "d1", []byte(`{"order":{"id":"A-1"},"x":1}`))
	if !errors.Is(err, ErrSignalDeliveryConflict) {
		t.Fatalf("same id different body err = %v, want conflict", err)
	}
	if st, _ := waitStatus(t, db, "r2"); st != "pending" {
		t.Fatalf("conflicting delivery woke a wait: %s", st)
	}
}

// A claim failure part way leaves the receipt incomplete; the retry of the same
// body claims only what is still pending.
func TestDeliverWebhookSignal_PartialClaimRetryFinishes(t *testing.T) {
	ctx := context.Background()
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	seedSignalRuns(t, s, "r1", "r2")
	if _, err := db.Exec(`CREATE TRIGGER fail_r2 BEFORE UPDATE ON pipeline_signal_waits WHEN NEW.run_id = 'r2' AND NEW.status = 'delivered'
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	wh := signalTestWebhook()
	body := []byte(`{"order":{"id":"A-1"}}`)

	res, err := DeliverWebhookSignal(ctx, db, wh, "d1", body)
	if err == nil {
		t.Fatalf("partial claim must surface an error, got %+v", res)
	}
	if len(res.RunIDs) != 1 || res.RunIDs[0] != "r1" {
		t.Fatalf("claimed runs = %v, want [r1] returned for resumption", res.RunIDs)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_r2`); err != nil {
		t.Fatal(err)
	}
	retry, err := DeliverWebhookSignal(ctx, db, wh, "d1-retry", body)
	if err != nil || retry.Duplicate {
		t.Fatalf("retry = %+v err=%v, want it to finish the remainder", retry, err)
	}
	if len(retry.RunIDs) != 1 || retry.RunIDs[0] != "r2" {
		t.Fatalf("retry claimed %v, want only [r2] (r1 must not be woken twice)", retry.RunIDs)
	}
	if st, _ := waitStatus(t, db, "r2"); st != "delivered" {
		t.Fatalf("r2 status %q, want delivered", st)
	}
	again, err := DeliverWebhookSignal(ctx, db, wh, "d1", body)
	if err != nil || !again.Duplicate || again.Outcome != "delivered" {
		t.Fatalf("after completion = %+v err=%v, want duplicate delivered", again, err)
	}
}

// Replay protection here is a receipt, not a time limit: the signature covers
// the body only. Inside retention a replay is a duplicate; once the receipt has
// expired the same signed bytes are accepted as new work again.
func TestDeliverWebhookSignal_ReplayBoundaryIsRetention(t *testing.T) {
	ctx := context.Background()
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	seedSignalRuns(t, s, "r1")
	wh := signalTestWebhook()
	body := []byte(`{"order":{"id":"A-1"}}`)
	if _, err := DeliverWebhookSignal(ctx, db, wh, "d1", body); err != nil {
		t.Fatal(err)
	}
	seedSignalRuns(t, s, "r2")
	inside, err := DeliverWebhookSignal(ctx, db, wh, "d2", body)
	if err != nil || !inside.Duplicate {
		t.Fatalf("replay inside retention = %+v err=%v, want duplicate", inside, err)
	}
	if st, _ := waitStatus(t, db, "r2"); st != "pending" {
		t.Fatalf("replay inside retention woke a wait: %s", st)
	}
	// Age the receipt past retention.
	old := tsformat.Format(time.Now().UTC().Add(-SignalDeliveryRetention - time.Hour))
	if _, err := db.Exec(`UPDATE pipeline_signal_deliveries SET received_at = ? WHERE webhook_id = ?`, old, wh.ID); err != nil {
		t.Fatal(err)
	}
	after, err := DeliverWebhookSignal(ctx, db, wh, "d3", body)
	if err != nil || after.Duplicate || len(after.RunIDs) != 1 || after.RunIDs[0] != "r2" {
		t.Fatalf("replay after expiry = %+v err=%v, want it accepted as new work (documented boundary)", after, err)
	}
}

// A crash between claiming waits and saving the receipt rolls both back, so the
// retry after restart finishes the work and wakes every wait exactly once.
func TestDeliverWebhookSignal_CrashBeforeReceiptRollsBackClaims(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "full claim"
		if partial {
			name = "partial claim"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openSignalWaitTestDB(t)
			s := NewSQLSignalWaitStore(db)
			seedSignalRuns(t, s, "r1", "r2")
			if partial {
				if _, err := db.Exec(`CREATE TRIGGER fail_r2 BEFORE UPDATE ON pipeline_signal_waits WHEN NEW.run_id = 'r2' AND NEW.status = 'delivered'
					BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
					t.Fatal(err)
				}
			}
			wh := signalTestWebhook()
			body := []byte(`{"order":{"id":"A-1"}}`)

			signalBeforeReceipt = func() error { return errors.New("simulated crash") }
			res, err := DeliverWebhookSignal(ctx, db, wh, "d1", body)
			signalBeforeReceipt = nil
			if err == nil || len(res.RunIDs) != 0 {
				t.Fatalf("crash must surface an error and wake nothing in memory: %+v err=%v", res, err)
			}
			for _, r := range []string{"r1", "r2"} {
				if st, _ := waitStatus(t, db, r); st != "pending" {
					t.Fatalf("%s is %q after a crash before the receipt; claims must roll back", r, st)
				}
			}
			var n int
			_ = db.QueryRow(`SELECT COUNT(*) FROM pipeline_signal_deliveries`).Scan(&n)
			if n != 0 {
				t.Fatalf("receipt survived a rolled-back delivery")
			}

			// Restart: the fault is gone, the sender retries the same body.
			if partial {
				if _, err := db.Exec(`DROP TRIGGER fail_r2`); err != nil {
					t.Fatal(err)
				}
			}
			retry, err := DeliverWebhookSignal(ctx, db, wh, "d1", body)
			if err != nil || retry.Duplicate || len(retry.RunIDs) != 2 {
				t.Fatalf("retry = %+v err=%v, want both waits woken", retry, err)
			}
			again, _ := DeliverWebhookSignal(ctx, db, wh, "d1", body)
			if !again.Duplicate {
				t.Fatalf("third attempt = %+v, want duplicate (no double wake)", again)
			}
		})
	}
}

// A body that arrives before its wait is armed must not burn: the receipt is
// non-terminal and the result tells the sender to retry, so the same bytes
// delivered after the wait arms wake it instead of answering "duplicate".
func TestDeliverWebhookSignal_EarlyDeliveryDoesNotBurnBody(t *testing.T) {
	ctx := context.Background()
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	wh := signalTestWebhook()
	body := []byte(`{"order":{"id":"A-1"}}`)

	early, err := DeliverWebhookSignal(ctx, db, wh, "d1", body)
	if err != nil || early.Outcome != "ignored" || !early.Retry || early.Duplicate {
		t.Fatalf("early = %+v err=%v, want ignored with Retry and no duplicate", early, err)
	}
	// Same bytes again with nothing armed: still retryable, still not a duplicate.
	again, err := DeliverWebhookSignal(ctx, db, wh, "d1", body)
	if err != nil || !again.Retry || again.Duplicate {
		t.Fatalf("second early = %+v err=%v, want Retry and no duplicate", again, err)
	}

	seedSignalRuns(t, s, "r1")
	retry, err := DeliverWebhookSignal(ctx, db, wh, "d1", body)
	if err != nil || retry.Duplicate || retry.Retry || retry.Outcome != "delivered" || len(retry.RunIDs) != 1 || retry.RunIDs[0] != "r1" {
		t.Fatalf("retry after arm = %+v err=%v, want delivered to r1", retry, err)
	}
	if st, _ := waitStatus(t, db, "r1"); st != "delivered" {
		t.Fatalf("wait status %q, want delivered", st)
	}
	// Now it is terminal: a further replay is a duplicate.
	dup, err := DeliverWebhookSignal(ctx, db, wh, "d2", body)
	if err != nil || !dup.Duplicate || dup.Retry {
		t.Fatalf("replay after delivery = %+v err=%v, want terminal duplicate", dup, err)
	}
}
