package database

import "testing"

func TestRoutineReceiptMigration_PreservesIdentityAndParksLegacyPhantom(t *testing.T) {
	db := migrateChainSetup(t)
	for _, query := range []string{
		`DROP TABLE routine_webhook_receipts`,
		`INSERT INTO work_items (id,workspace_id,source,domain_kind,domain_id,state,eligible_at,created_at,updated_at) VALUES ('old-work','ws','webhook','pipeline_run','original-run','queued','now','now','now')`,
		`INSERT INTO webhook_deliveries (id,workspace_id,endpoint_id,endpoint_kind,profile,source_delivery_id,body_sha256,filter_decision,work_id,received_at,dedup_expires_at) VALUES ('old-delivery','ws','endpoint','routine','legacy','source-id','sha','accepted','old-work','now','future')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := migrationFS.ReadFile("migrations/20260912160150_routine_webhook_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var run, state, event string
	var generation int
	if err := db.QueryRow(`SELECT run_id FROM routine_webhook_receipts WHERE id = 'old-delivery'`).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if run != "original-run" {
		t.Fatalf("lost original run: %q", run)
	}
	if err := db.QueryRow(`SELECT state, generation FROM work_items WHERE id = 'old-work'`).Scan(&state, &generation); err != nil {
		t.Fatal(err)
	}
	if state != "needs_reconciliation" || generation != 1 {
		t.Fatalf("legacy phantom: state=%s generation=%d", state, generation)
	}
	if err := db.QueryRow(`SELECT to_state FROM work_events WHERE work_id = 'old-work'`).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if event != state {
		t.Fatalf("state %s disagrees with history %s", state, event)
	}
}
