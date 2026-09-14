package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func seedRoutineReceipt(t *testing.T, db *sql.DB, id, expiresAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO routine_webhook_receipts
		(id, workspace_id, endpoint_id, source_delivery_id, body_sha256, run_id, received_at, dedup_expires_at, body_bytes, profile)
		VALUES (?, 'ws', 'wh', ?, 'sha', ?, ?, ?, 1, 'legacy-routine-hmac')`,
		id, "src-"+id, "run-"+id, tsformat.Format(time.Now().UTC()), expiresAt); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func routineReceiptIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM routine_webhook_receipts ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// The window closes strictly before `now`: a receipt expiring at the sweep's
// own instant is kept, one with no expiry is never touched.
func TestSweepRoutineWebhookReceipts_RemovesOnlyExpired(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	seedRoutineReceipt(t, db, "a-expired", tsformat.Format(now.Add(-time.Second)))
	seedRoutineReceipt(t, db, "b-boundary", tsformat.Format(now))
	seedRoutineReceipt(t, db, "c-future", tsformat.Format(now.Add(time.Second)))
	seedRoutineReceipt(t, db, "d-unknown", "")

	n, err := SweepRoutineWebhookReceipts(context.Background(), db, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted %d, want 1", n)
	}
	got := routineReceiptIDs(t, db)
	want := []string{"b-boundary", "c-future", "d-unknown"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("remaining = %v, want %v", got, want)
	}

	// Idempotent: a second sweep at the same instant removes nothing.
	if n, err := SweepRoutineWebhookReceipts(context.Background(), db, now); err != nil || n != 0 {
		t.Fatalf("second sweep = %d, %v", n, err)
	}
	// And the boundary row goes once the clock moves past it.
	if n, err := SweepRoutineWebhookReceipts(context.Background(), db, now.Add(time.Millisecond)); err != nil || n != 1 {
		t.Fatalf("sweep past the boundary = %d, %v; want 1", n, err)
	}
}

// A backlog larger than one batch is drained in one call.
func TestSweepRoutineWebhookReceipts_DrainsPastOneBatch(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	const backlog = routineReceiptSweepBatch*2 + 3
	expired := tsformat.Format(now.Add(-time.Hour))
	for i := 0; i < backlog; i++ {
		seedRoutineReceipt(t, db, fmt.Sprintf("e-%05d", i), expired)
	}
	seedRoutineReceipt(t, db, "z-live", tsformat.Format(now.Add(time.Hour)))

	n, err := SweepRoutineWebhookReceipts(context.Background(), db, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != backlog {
		t.Fatalf("deleted %d, want %d", n, backlog)
	}
	if got := routineReceiptIDs(t, db); fmt.Sprint(got) != "[z-live]" {
		t.Fatalf("remaining = %v, want only the live receipt", got)
	}
}

func TestRoutineReceiptDedupExpiry_IsThirtyDaysFromAcceptance(t *testing.T) {
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	if got := RoutineReceiptDedupExpiry(at); got.Sub(at) != 30*24*time.Hour {
		t.Fatalf("expiry = %s after acceptance, want 30 days", got.Sub(at))
	}
}

// The production loop, not only the helper: started the way cmd_start starts
// it, the sweeper's immediate first pass removes an expired receipt.
func TestStartRoutineReceiptRetentionSweeper_SweepsOnStart(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	seedRoutineReceipt(t, db, "expired-at-boot", tsformat.Format(time.Now().Add(-time.Hour)))
	seedRoutineReceipt(t, db, "live-at-boot", tsformat.Format(time.Now().Add(time.Hour)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		StartRoutineReceiptRetentionSweeper(ctx, db, nil, time.Hour)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := routineReceiptIDs(t, db); fmt.Sprint(got) == "[live-at-boot]" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if got := routineReceiptIDs(t, db); fmt.Sprint(got) != "[live-at-boot]" {
		t.Fatalf("after the sweeper's first pass receipts = %v, want only the live one", got)
	}
}
