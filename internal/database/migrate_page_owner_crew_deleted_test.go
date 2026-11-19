package database

import (
	"io"
	"log/slog"
	"testing"
)

// #5259: a live grant whose issuer held standing only as a member of a
// deleted (or foreign) owning crew is retired once by the migration; grants
// on a live crew's page, and grants whose standing never came from the crew,
// are left alone. After it, deleting an owning crew retires its members'
// grants in the same transaction.
func TestPageOwnerCrewDeletedMigrationRetiresOnlyDeadCrewStanding(t *testing.T) {
	db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, 20261119163552, logger); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('w','W','w'),('w2','W2','w2')`)
	mustExec(t, db, `INSERT INTO users(id,email,full_name) VALUES('admin','a@x.test','A'),('dead','d@x.test','D'),('live','l@x.test','L'),('foreign','f@x.test','F'),('reader','r@x.test','R')`)
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m1','w','admin','ADMIN'),('m2','w','dead','MEMBER'),('m3','w','live','MEMBER'),('m4','w','foreign','MEMBER'),('m5','w','reader','MEMBER')`)
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug,deleted_at) VALUES('c-dead','w','Dead','dead','2026-11-01T00:00:00Z'),('c-live','w','Live','live',NULL),('c-foreign','w2','Foreign','foreign',NULL),('c-later','w','Later','later',NULL)`)
	mustExec(t, db, `INSERT INTO crew_members(id,crew_id,user_id,role) VALUES('cm1','c-dead','dead','MEMBER'),('cm2','c-live','live','MEMBER'),('cm3','c-foreign','foreign','MEMBER'),('cm4','c-later','live','MEMBER')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES('p-dead','w','p-dead','P','c-dead','{}'),('p-live','w','p-live','P','c-live','{}'),('p-foreign','w','p-foreign','P','c-foreign','{}'),('p-later','w','p-later','P','c-later','{}')`)
	mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES
		('p-dead','user','reader','read','dead'),
		('p-dead','user','live','read','admin'),
		('p-live','user','reader','read','live'),
		('p-foreign','user','reader','read','foreign'),
		('p-later','user','reader','read','live')`)

	m, err := findMigration(20261119163553)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(m.sql); err != nil {
		t.Fatal(err)
	}
	retired := func(page, issuer string) bool {
		t.Helper()
		var r bool
		if err := db.QueryRow(`SELECT retired_at IS NOT NULL FROM page_grants WHERE page_id=? AND granted_by_user_id=?`, page, issuer).Scan(&r); err != nil {
			t.Fatalf("%s by %s: %v", page, issuer, err)
		}
		return r
	}
	for _, tc := range []struct {
		page, issuer string
		want         bool
	}{
		{"p-dead", "dead", true},       // standing only through the deleted crew
		{"p-dead", "admin", false},     // standing from the ADMIN role
		{"p-live", "live", false},      // live crew
		{"p-foreign", "foreign", true}, // owning crew belongs to another workspace
		{"p-later", "live", false},     // live until its crew is deleted below
	} {
		if got := retired(tc.page, tc.issuer); got != tc.want {
			t.Errorf("after migration %s by %s retired = %v, want %v", tc.page, tc.issuer, got, tc.want)
		}
	}

	mustExec(t, db, `UPDATE crews SET deleted_at='2026-11-19T00:00:00Z' WHERE id='c-later'`)
	if !retired("p-later", "live") {
		t.Error("deleting the owning crew left its member's grant live")
	}
	if retired("p-live", "live") {
		t.Error("deleting one crew retired a grant on another crew's page by the same person")
	}
}
