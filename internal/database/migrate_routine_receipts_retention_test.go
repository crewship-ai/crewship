package database

import (
	"testing"
	"time"
)

// Receipts written before the retention migration have no known age. They are
// dated from the migration and get the full window from there — never expired
// early, because the safe error for a dedup key is to live too long.
func TestRoutineReceiptRetentionMigration_BackfillsAgeAndWindow(t *testing.T) {
	db := migrateChainSetup(t)
	for _, query := range []string{
		`DROP INDEX idx_routine_webhook_receipts_list`,
		`DROP INDEX idx_routine_webhook_receipts_expiry`,
		`ALTER TABLE routine_webhook_receipts DROP COLUMN received_at`,
		`ALTER TABLE routine_webhook_receipts DROP COLUMN dedup_expires_at`,
		`ALTER TABLE routine_webhook_receipts DROP COLUMN body_bytes`,
		`ALTER TABLE routine_webhook_receipts DROP COLUMN profile`,
		`INSERT INTO routine_webhook_receipts (id, workspace_id, endpoint_id, source_delivery_id, body_sha256, run_id)
		 VALUES ('old-rcpt', 'ws', 'wh', 'src-1', 'sha', 'run-1')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	migration, err := migrationFS.ReadFile("migrations/20260913155203_routine_webhook_receipts_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Second)
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var receivedAt, expiresAt, profile string
	var bodyBytes int64
	if err := db.QueryRow(`SELECT received_at, dedup_expires_at, body_bytes, profile FROM routine_webhook_receipts WHERE id = 'old-rcpt'`).
		Scan(&receivedAt, &expiresAt, &bodyBytes, &profile); err != nil {
		t.Fatal(err)
	}
	received, err := time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil {
		t.Fatalf("received_at %q is not RFC 3339: %v", receivedAt, err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		t.Fatalf("dedup_expires_at %q is not RFC 3339: %v", expiresAt, err)
	}
	if received.Before(before) || received.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("received_at %s is not the migration's own time", receivedAt)
	}
	if window := expires.Sub(received); window < 30*24*time.Hour-time.Second || window > 30*24*time.Hour+time.Second {
		t.Fatalf("dedup window = %s, want 30 days from the backfilled age", window)
	}
	if bodyBytes != 0 || profile != "" {
		t.Fatalf("legacy receipt invented body_bytes=%d profile=%q", bodyBytes, profile)
	}
	// The second UPDATE is guarded by the same emptiness the first one clears,
	// so a row the migration dated must also have been given its window.
	var undated int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routine_webhook_receipts WHERE received_at = '' OR dedup_expires_at = ''`).Scan(&undated); err != nil {
		t.Fatal(err)
	}
	if undated != 0 {
		t.Fatalf("%d receipts left without an age or a window", undated)
	}
}
