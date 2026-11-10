package database

import (
	"database/sql"
	"io"
	"log/slog"
	"reflect"
	"testing"
)

const proposeGrantMigration = 20261110185446

// An agent Propose grant is additive: every existing row keeps its level, issuer,
// marker and fences; Propose names agents only and never carries a panel list.
func TestPageProposeGrantMigrationIsAdditiveAndAgentOnly(t *testing.T) {
	db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, proposeGrantMigration-1, logger); err != nil {
		t.Fatal(err)
	}
	beforeSchema := commentGrantDependentSchema(t, db)
	mustExec(t, db, `INSERT INTO users(id,email,full_name) VALUES('propose-owner','propose-owner@example.test','Owner'),('propose-user','propose-user@example.test','Person')`)
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('propose-ws','Propose','propose-ws')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('propose-page','propose-ws','propose-page','Page','propose-owner','{}')`)
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('propose-owner-member','propose-ws','propose-owner','OWNER','trusted'),('propose-admin-member','propose-ws','propose-user','ADMIN','trusted')`)
	mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES
		('propose-page','user','propose-user','read','propose-owner','2026-11-01 10:00:00'),
		('propose-page','user','propose-user','comment','propose-owner','2026-11-01 10:00:01'),
		('propose-page','agent','propose-agent','write','propose-owner','2026-11-01 10:00:02'),
		('propose-page','workspace','','read','propose-owner','2026-11-01 10:00:04')`)
	mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at,retired_at) VALUES
		('propose-page','agent','producer-agent','produce','["p1"]','propose-owner','2026-11-01 10:00:03','2026-11-02 10:00:00')`)
	if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('propose-page','agent','propose-agent','propose','propose-owner')`); err == nil {
		t.Fatal("propose grant accepted before the migration")
	}
	var liveOwnerRows int
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE granted_by_user_id='propose-owner' AND retired_at IS NULL`).Scan(&liveOwnerRows); err != nil || liveOwnerRows != 4 {
		t.Fatalf("fixture needs four live owner grants: %d %v", liveOwnerRows, err)
	}
	var beforeAudit int
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs`).Scan(&beforeAudit); err != nil {
		t.Fatal(err)
	}
	beforeRows := proposeGrantRows(t, db)
	var epochBefore, epochAfter int64
	if err := db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='propose-ws'`).Scan(&epochBefore); err != nil {
		t.Fatal(err)
	}
	migration, err := findMigration(proposeGrantMigration)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = migration.fn(t.Context(), tx, logger); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if afterSchema := commentGrantDependentSchema(t, db); !reflect.DeepEqual(beforeSchema, afterSchema) {
		t.Fatalf("authority trigger/view definitions changed: before=%v after=%v", beforeSchema, afterSchema)
	}
	if afterRows := proposeGrantRows(t, db); !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatalf("existing grants changed: before=%v after=%v", beforeRows, afterRows)
	}
	if err := db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='propose-ws'`).Scan(&epochAfter); err != nil || epochAfter != epochBefore {
		t.Fatalf("migration changed authority epoch %d -> %d: %v", epochBefore, epochAfter, err)
	}
	var afterAudit int
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs`).Scan(&afterAudit); err != nil || afterAudit != beforeAudit {
		t.Fatalf("rebuild emitted audit mutations: %d -> %d %v", beforeAudit, afterAudit, err)
	}
	// The newer shipped owner-only protections must still fire after rebuild,
	// even for a workspace ADMIN who may otherwise issue ordinary grants.
	for _, g := range []struct{ kind, id, level string }{{"agent", "non-owner-editor", "write"}, {"crew", "non-owner-producer", "produce"}} {
		mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('propose-page',?,?,?,'propose-user','2026-11-10T00:00:00Z')`, g.kind, g.id, g.level)
		var retired bool
		if err := db.QueryRow(`SELECT retired_at IS NOT NULL FROM page_grants WHERE subject_type=? AND subject_id=? AND level=?`, g.kind, g.id, g.level).Scan(&retired); err != nil || !retired {
			t.Fatalf("nonowner %s/%s not retired: %v %v", g.kind, g.level, retired, err)
		}
		var reason string
		if err := db.QueryRow(`SELECT reason FROM page_grant_retired_issuances WHERE subject_type=? AND subject_id=? AND level=?`, g.kind, g.id, g.level).Scan(&reason); err != nil || reason != "agent_delegation_not_owner" {
			t.Fatalf("owner-only retirement ledger lost: %q %v", reason, err)
		}
		var audits int
		if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='page.agent_grant.retired_not_owner' AND json_extract(metadata,'$.subject_id')=?`, g.id).Scan(&audits); err != nil || audits != 1 {
			t.Fatalf("owner-only retirement audit count %d %v", audits, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('propose-page','agent','propose-agent','propose','propose-owner')`); err != nil {
		t.Fatalf("agent propose grant refused: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('propose-page','user','propose-user','propose','propose-owner')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('propose-page','crew','some-crew','propose','propose-owner')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('propose-page','workspace','','propose','propose-owner')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('propose-page','agent','other-agent','comment','propose-owner')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('propose-page','agent','other-agent','suggest','propose-owner')`,
		`UPDATE page_grants SET subject_type='user' WHERE level='propose'`,
		`UPDATE page_grants SET panel_ids='["p1"]' WHERE level='propose'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("invalid grant accepted: %s", query)
		}
	}
	mustExec(t, db, `INSERT INTO mooring_page_grant_generations(workspace_id,page_id,principal_kind,principal_id) VALUES('propose-ws','propose-page','agent','propose-agent')`)
	var before, after int64
	if err := db.QueryRow(`SELECT revision FROM mooring_page_grant_generations WHERE page_id='propose-page' AND principal_kind='agent' AND principal_id='propose-agent'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `DELETE FROM page_grants WHERE level='propose'`)
	if err := db.QueryRow(`SELECT revision FROM mooring_page_grant_generations WHERE page_id='propose-page' AND principal_kind='agent' AND principal_id='propose-agent'`).Scan(&after); err != nil || after <= before {
		t.Fatalf("propose grant revoke lost the per-Page fence: %d -> %d (%v)", before, after, err)
	}
	mustExec(t, db, `DELETE FROM pages WHERE id='propose-page'`)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM page_grants`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Page FK cascade lost: %d %v", count, err)
	}
}

func proposeGrantRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT rowid||'|'||page_id||'|'||subject_type||'|'||subject_id||'|'||level||'|'||COALESCE(panel_ids,'-')||'|'||granted_by_user_id||'|'||granted_at||'|'||COALESCE(retired_at,'-') FROM page_grants ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil || len(out) != 5 {
		t.Fatalf("grant rows: %v %v", out, err)
	}
	return out
}
