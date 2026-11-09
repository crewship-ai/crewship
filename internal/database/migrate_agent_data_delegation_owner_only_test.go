package database

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// #5260: crew produce grants and agent producer designations the page's owner
// did not make are retired by the migration, audited, and stay retired. The
// designator of a stored agent producer is read from page_versions.
func TestAgentDataDelegationOwnerOnlyMigration(t *testing.T) {
	const version = 20261109152536
	db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, version-1, logger); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('p-ws','P','p-ws')`)
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('p-crew','p-ws','C','p-crew')`)
	for _, u := range []string{"admin", "owner"} {
		mustExec(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, "p-"+u, u+"@example.test")
	}
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES
	 ('pm1','p-ws','p-admin','ADMIN','trusted'),('pm2','p-ws','p-owner','MEMBER','trusted')`)
	mustExec(t, db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('p-agent','p-ws','p-crew','W','writer')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('p-page','p-ws','p','P','p-owner','{}')`)
	panel := func(id, producer string) string {
		return `{"id":"` + id + `","producer":"` + producer + `"}`
	}
	spec := func(panels ...string) string {
		return `{"spec":{"panels":[` + strings.Join(panels, ",") + `]}}`
	}
	// v1 owner: a is agent/writer, b a script. v2 admin: b becomes
	// agent/writer. v3 owner: an edit that keeps both. v4 admin: adds c.
	mustExec(t, db, `INSERT INTO page_versions(page_id,seq,spec_json,author_user_id) VALUES
	 ('p-page',1,?,'p-owner'),('p-page',2,?,'p-admin'),('p-page',3,?,'p-owner'),('p-page',4,?,'p-admin')`,
		spec(panel("a", "agent/writer"), panel("b", "script/x.sh")),
		spec(panel("a", "agent/writer"), panel("b", "agent/writer")),
		spec(panel("a", "agent/writer"), panel("b", "agent/writer")),
		spec(panel("a", "agent/writer"), panel("b", "agent/writer"), panel("c", "agent/writer")))
	for _, id := range []string{"a", "b", "c"} {
		mustExec(t, db, `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds) VALUES(?,'p-page',?,'status.v1','p-crew','agent','writer',30)`, "pp-"+id, id)
	}
	mustExec(t, db, `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds) VALUES('pp-d','p-page','d','status.v1','p-crew','routine','nightly',30)`)
	// A page whose only version does not parse: its agent producer is nobody's.
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('p-bad','p-ws','b','B','p-owner','{}')`)
	mustExec(t, db, `INSERT INTO page_versions(page_id,seq,spec_json,author_user_id) VALUES('p-bad',1,'not json','p-owner')`)
	mustExec(t, db, `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds) VALUES('pp-bad','p-bad','x','status.v1','p-crew','agent','writer',30)`)
	mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES
	 ('p-page','crew','p-crew','produce','p-admin'),('p-page','crew','p-crew','read','p-admin')`)
	mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES
	 ('p-page','crew','p-crew','write','p-admin','2026-11-01T00:00:00Z')`)

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

	designations := map[string]string{}
	rows, err := db.Query(`SELECT panel_id, COALESCE(producer_designated_by_user_id,'-'), producer_retired_at IS NOT NULL FROM page_panels WHERE page_id='p-page' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, by string
		var retired bool
		if err := rows.Scan(&id, &by, &retired); err != nil {
			t.Fatal(err)
		}
		state := "live"
		if retired {
			state = "retired"
		}
		designations[id] = by + "/" + state
	}
	rows.Close()
	want := map[string]string{"a": "p-owner/live", "b": "p-admin/retired", "c": "p-admin/retired", "d": "-/live"}
	for id, w := range want {
		if designations[id] != w {
			t.Errorf("panel %s = %q, want %q", id, designations[id], w)
		}
	}
	var crewProduceRetired, crewOthersRetired, audits, grantAudits int
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE subject_type='crew' AND level='produce' AND retired_at IS NOT NULL`).Scan(&crewProduceRetired); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE subject_type='crew' AND level<>'produce' AND retired_at IS NOT NULL`).Scan(&crewOthersRetired); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='page.agent_producer.retired_not_owner' AND workspace_id='p-ws'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='page.agent_grant.retired_not_owner' AND json_extract(metadata,'$.subject_type')='crew'`).Scan(&grantAudits); err != nil {
		t.Fatal(err)
	}
	var bad int
	if err := db.QueryRow(`SELECT count(*) FROM page_panels WHERE page_id='p-bad' AND producer_retired_at IS NOT NULL AND producer_designated_by_user_id IS NULL`).Scan(&bad); err != nil || bad != 1 {
		t.Fatalf("an unparseable history kept an agent producer live: %d %v", bad, err)
	}
	if crewProduceRetired != 1 || crewOthersRetired != 0 || audits != 3 || grantAudits != 1 {
		t.Fatalf("crew produce retired=%d, other crew levels retired=%d, producer audits=%d, crew grant audits=%d; want 1,0,3,1",
			crewProduceRetired, crewOthersRetired, audits, grantAudits)
	}

	// For good: the designator owning the page later revives nothing.
	mustExec(t, db, `UPDATE pages SET owner_user_id='p-admin' WHERE id='p-page'`)
	var live int
	if err := db.QueryRow(`SELECT count(*) FROM page_panels WHERE panel_id IN ('b','c') AND producer_retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("ownership revived a retired agent producer: %d %v", live, err)
	}
	// The transfer ended the previous owner's designation of a.
	if err := db.QueryRow(`SELECT count(*) FROM page_panels WHERE panel_id='a' AND producer_retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("the previous owner's agent producer survived the transfer: %d %v", live, err)
	}
	// Clearing the marker by hand under a non-owner designator is undone.
	mustExec(t, db, `UPDATE page_panels SET producer_retired_at=NULL WHERE panel_id='a'`)
	if err := db.QueryRow(`SELECT count(*) FROM page_panels WHERE panel_id='a' AND producer_retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("a non-owner designation was revived by clearing its marker: %d %v", live, err)
	}
	// The then-owner designating anew is live.
	mustExec(t, db, `UPDATE page_panels SET producer_designated_by_user_id='p-admin',producer_retired_at=NULL WHERE panel_id='b'`)
	if err := db.QueryRow(`SELECT count(*) FROM page_panels WHERE panel_id='b' AND producer_retired_at IS NULL`).Scan(&live); err != nil || live != 1 {
		t.Fatalf("the owner's new designation stayed retired: %d %v", live, err)
	}
	// Deleting the designating user leaves the designation nobody's.
	mustExec(t, db, `INSERT INTO users(id,email) VALUES('p-gone','gone@example.test')`)
	mustExec(t, db, `UPDATE pages SET owner_user_id='p-gone' WHERE id='p-page'`)
	mustExec(t, db, `UPDATE page_panels SET producer_designated_by_user_id='p-gone',producer_retired_at=NULL WHERE panel_id='c'`)
	mustExec(t, db, `UPDATE pages SET owner_user_id='p-owner' WHERE id='p-page'`)
	if err := db.QueryRow(`SELECT count(*) FROM page_panels WHERE panel_id='c' AND producer_retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("a transfer away from the designator left the designation live: %d %v", live, err)
	}
}
