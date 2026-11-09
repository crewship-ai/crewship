package database

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// R71 §0.14/1: only the artifact's owner delegates Edit (write) or data Edit
// (produce) to an agent. The migration retires every live agent write/produce
// row its page owner did not issue, records why, and audits it. Human grants,
// agent read grants and owner-issued delegations are left alone.
func TestAgentDelegationOwnerOnlyMigrationRetiresNonOwnerRows(t *testing.T) {
	const version = 20261109132621
	db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, version-1, logger); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('d-ws','D','d-ws')`)
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('d-crew','d-ws','C','d-crew')`)
	for _, u := range []string{"admin", "owner", "crewmate"} {
		mustExec(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, "d-"+u, u+"@example.test")
	}
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES
	 ('dm1','d-ws','d-admin','ADMIN','trusted'),('dm2','d-ws','d-owner','MEMBER','trusted'),('dm3','d-ws','d-crewmate','MEMBER','trusted')`)
	mustExec(t, db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('dc1','d-crew','d-crewmate')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('d-user-page','d-ws','u','U','d-owner','{}')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES('d-crew-page','d-ws','c','C','d-crew','{}')`)
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug,deleted_at) VALUES('d-gone','d-ws','G','d-gone','2026-11-01')`)
	mustExec(t, db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('dc2','d-gone','d-crewmate')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES('d-gone-page','d-ws','g','G','d-gone','{}')`)
	// A crew of another workspace owns nothing here.
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('x-ws','X','x-ws')`)
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('x-crew','x-ws','X','x-crew')`)
	mustExec(t, db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('dc3','x-crew','d-crewmate')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES('d-foreign-page','d-ws','f','F','x-crew','{}')`)
	for _, g := range [][5]string{
		{"d-user-page", "agent", "a1", "write", "d-admin"},
		{"d-user-page", "agent", "a2", "produce", "d-admin"},
		{"d-user-page", "agent", "a3", "write", "d-owner"},
		{"d-user-page", "agent", "a4", "produce", "d-owner"},
		{"d-user-page", "agent", "a5", "read", "d-admin"},
		{"d-user-page", "user", "d-crewmate", "write", "d-admin"},
		{"d-crew-page", "agent", "a6", "produce", "d-crewmate"},
		{"d-crew-page", "agent", "a7", "write", "d-admin"},
		{"d-gone-page", "agent", "a8", "write", "d-crewmate"},
		{"d-foreign-page", "agent", "a9", "produce", "d-crewmate"},
	} {
		mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,?,?,?,?)`, g[0], g[1], g[2], g[3], g[4])
	}
	migration, err := findMigration(version)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), migration.sql); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT page_id||'/'||subject_id FROM page_grants WHERE retired_at IS NOT NULL ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var retired []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		retired = append(retired, s)
	}
	if got, want := strings.Join(retired, " "), "d-crew-page/a7 d-foreign-page/a9 d-gone-page/a8 d-user-page/a1 d-user-page/a2"; got != want {
		t.Fatalf("retired = %q\nwant      %q", got, want)
	}
	var audits, reasons int
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='page.agent_grant.retired_not_owner' AND workspace_id='d-ws' AND user_id IS NULL
		AND json_extract(metadata,'$.granted_by_user_id') IN ('d-admin','d-crewmate')`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM page_grant_retired_issuances WHERE reason='agent_delegation_not_owner'`).Scan(&reasons); err != nil {
		t.Fatal(err)
	}
	if audits != 5 || reasons != 5 {
		t.Fatalf("audits = %d, reasons = %d, want 5 and 5", audits, reasons)
	}
	// Judged once, retired for good: the issuer owning the page later is no
	// new grant.
	mustExec(t, db, `UPDATE pages SET owner_user_id='d-admin' WHERE id='d-user-page'`)
	var live int
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE subject_id IN ('a1','a2') AND retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("ownership revived a retired delegation: %d %v", live, err)
	}
	// The transfer itself ended the previous owner's delegations.
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE subject_id IN ('a3','a4') AND retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("the previous owner's delegations survived the transfer: %d %v", live, err)
	}
	// An owner-issued reissue clears the marker as a new operation.
	mustExec(t, db, `UPDATE page_grants SET granted_by_user_id='d-admin',granted_at='2026-11-09T00:00:00Z',retired_at=NULL WHERE subject_id='a1'`)
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE subject_id='a1' AND retired_at IS NULL`).Scan(&live); err != nil || live != 1 {
		t.Fatalf("the owner's reissue stayed retired: %d %v", live, err)
	}
	// Deleting the owning crew ends its members' delegations.
	mustExec(t, db, `UPDATE crews SET deleted_at='2026-11-09' WHERE id='d-crew'`)
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE subject_id='a6' AND retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("a deleted crew's delegation survived: %d %v", live, err)
	}
	// Anyone else clearing the marker is undone in the same statement.
	mustExec(t, db, `UPDATE page_grants SET retired_at=NULL WHERE subject_id='a7'`)
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE subject_id='a7' AND retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("a non-owner row was revived by clearing its marker: %d %v", live, err)
	}
}
