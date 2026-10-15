package runauthority

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditExecutionOriginsCurrentRevokedUnknownAndBounded(t *testing.T) {
	db, receipt := authorityFixture(t)
	putEntry(t, db, "audit-source", "audit-root", "run.started", "orchestrator", map[string]any{"chat_id": "chat", "human_authority": receipt, "trigger_message_id": "trigger"})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := CaptureOrigin(t.Context(), tx, "audit-root", "agent", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('audit-invalid','ws','invalid','invalid','invalid','{}','2026-10-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, origin string }{{"audit-current", origin.ID}, {"audit-invalid", "audit-invalid"}, {"audit-null", ""}} {
		if _, err = db.Exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,chat_origin_id) VALUES(?,'ws','chat','agent','agent','SECRET_TASK','PENDING',NULLIF(?,''))`, row.id, row.origin); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('audit-crew','ws','SECRET_CREW','audit')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO recurring_issues(id,workspace_id,crew_id,title,cron_expression,enabled) VALUES('audit-recurring','ws','audit-crew','SECRET_TITLE','0 9 * * *',1)`); err != nil {
		t.Fatal(err)
	}
	report, err := AuditExecutionOrigins(t.Context(), db, 100)
	if err != nil {
		t.Fatal(err)
	}
	classifications := map[string]string{}
	for _, record := range report.Records {
		classifications[record.ID] = record.Classification
	}
	if classifications["audit-current"] != "receipt_current" || classifications["audit-invalid"] != "blocked" || classifications["audit-null"] != "needs_review" || classifications["audit-recurring"] != "blocked" {
		t.Fatalf("%+v", report)
	}
	raw, _ := json.Marshal(report)
	for _, secret := range []string{"SECRET_TASK", "SECRET_CREW", "SECRET_TITLE", "human_authority", "member_revision"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	bounded, err := AuditExecutionOrigins(t.Context(), db, 1)
	if err != nil || len(bounded.Records) != 1 || !bounded.Truncated {
		t.Fatalf("bounded %+v %v", bounded, err)
	}
	var total int64
	for _, count := range bounded.Counts {
		total += count.Candidates
	}
	if total != 4 {
		t.Fatalf("candidate total %d", total)
	}
	if _, err = db.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='member'`); err != nil {
		t.Fatal(err)
	}
	report, err = AuditExecutionOrigins(t.Context(), db, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range report.Records {
		if record.ID == "audit-current" && (record.Classification != "blocked" || record.Reason != "requester_authority_stale") {
			t.Fatalf("revoked receipt %+v", record)
		}
	}
	// Auditing cannot rewrite the origin or settle/disable the affected work.
	var status, stored string
	if err = db.QueryRow(`SELECT status,chat_origin_id FROM assignments WHERE id='audit-current'`).Scan(&status, &stored); err != nil || status != "PENDING" || stored != origin.ID {
		t.Fatalf("audit mutated work %s %s %v", status, stored, err)
	}
}

func TestAuditExecutionOriginsPreMigrationReadOnly(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE assignments(id TEXT,workspace_id TEXT,status TEXT)`,
		`INSERT INTO assignments VALUES('old-assignment','old-ws','QUEUED')`,
		`CREATE TABLE recurring_issues(id TEXT,workspace_id TEXT,enabled INTEGER)`,
		`INSERT INTO recurring_issues VALUES('old-template','old-ws',1)`,
		`PRAGMA query_only=ON`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	report, err := AuditExecutionOrigins(t.Context(), db, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !report.ReadOnly || report.Truncated || len(report.MissingTables) != 3 || len(report.Records) != 2 {
		t.Fatalf("%+v", report)
	}
	if report.Records[0].Classification != "needs_review" || report.Records[0].Reason != "pre_migration_origin_unknown" || report.Records[1].Classification != "blocked" {
		t.Fatalf("%+v", report.Records)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('assignments') WHERE name='chat_origin_id'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit migrated database %d %v", n, err)
	}
}
