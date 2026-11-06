package database

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// #5069: a grant whose issuer has no standing when the migration runs is
// retired; a live grant is left alone, whichever arm of standing carries it.
func TestPageGrantRetirementMigrationRetiresOnlyGrantsWithoutStanding(t *testing.T) {
	const version = 20261106225704
	db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, version-1, logger); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('r-ws','R','r-ws'),('r-else','E','r-else')`)
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('r-crew','r-ws','C','r-crew')`)
	for _, u := range []string{"admin", "owner", "crewmate", "plain", "gone", "suspended", "guest"} {
		mustExec(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, "r-"+u, u+"@example.test")
	}
	mustExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES
	 ('m1','r-ws','r-admin','ADMIN','trusted'),('m2','r-ws','r-owner','MEMBER','trusted'),
	 ('m3','r-ws','r-crewmate','MEMBER','trusted'),('m4','r-ws','r-plain','VIEWER','trusted'),
	 ('m5','r-ws','r-suspended','ADMIN','trusted'),('m6','r-ws','r-guest','MEMBER','trusted'),
	 ('m7','r-else','r-guest','MEMBER','restricted')`)
	mustExec(t, db, `UPDATE users SET suspended_at='2026-01-01' WHERE id='r-suspended'`)
	mustExec(t, db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('c1','r-crew','r-crewmate'),('c2','r-crew','r-guest')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('r-user-page','r-ws','u','U','r-owner','{}')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES('r-crew-page','r-ws','c','C','r-crew','{}')`)
	for _, g := range [][2]string{
		{"r-user-page", "r-admin"}, {"r-user-page", "r-owner"}, {"r-crew-page", "r-crewmate"},
		{"r-user-page", "r-crewmate"}, {"r-user-page", "r-plain"}, {"r-user-page", "r-gone"},
		{"r-user-page", "r-suspended"}, {"r-crew-page", "r-guest"},
	} {
		mustExec(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'read',?)`, g[0], g[1], g[1])
	}
	migration, err := findMigration(version)
	if err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='r-ws'`).Scan(&before); err != nil {
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
	if err := db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='r-ws'`).Scan(&after); err != nil || after != before {
		t.Fatalf("migration moved the authority epoch %d -> %d: %v", before, after, err)
	}
	rows, err := db.Query(`SELECT page_id,granted_by_user_id FROM page_grants WHERE retired_at IS NOT NULL ORDER BY 1,2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var retired []string
	for rows.Next() {
		var page, issuer string
		if err := rows.Scan(&page, &issuer); err != nil {
			t.Fatal(err)
		}
		retired = append(retired, page+"/"+issuer)
	}
	want := "r-crew-page/r-guest r-user-page/r-crewmate r-user-page/r-gone r-user-page/r-plain r-user-page/r-suspended"
	if got := strings.Join(retired, " "); got != want {
		t.Fatalf("retired = %q\nwant      %q", got, want)
	}
	var total int
	if err := db.QueryRow(`SELECT count(*) FROM page_grants`).Scan(&total); err != nil || total != 8 {
		t.Fatalf("grants kept = %d, %v", total, err)
	}
	// Retired at migration means retired for good: the issuer's return is no
	// new operation.
	mustExec(t, db, `UPDATE users SET suspended_at=NULL WHERE id='r-suspended'`)
	var live int
	if err := db.QueryRow(`SELECT count(*) FROM page_grants WHERE granted_by_user_id='r-suspended' AND retired_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("a grant retired by the migration returned: %d %v", live, err)
	}
}
