package backup

import (
	"context"
	"database/sql"
	"testing"
)

func seedPageGrantRestore(t *testing.T, db *sql.DB, suffix string) (workspace, user string) {
	t.Helper()
	workspace, _ = seedCovWorkspace(t, db, suffix)
	user = "u_cov_" + suffix
	for _, stmt := range []string{
		`INSERT INTO users(id,email) VALUES('grant-admin-` + suffix + `','admin-` + suffix + `@cov.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('grant-owner-` + suffix + `','` + workspace + `','` + user + `','OWNER')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('grant-admin-` + suffix + `','` + workspace + `','grant-admin-` + suffix + `','ADMIN')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('grant-page-` + suffix + `','` + workspace + `','grant-page','Page','` + user + `','{}')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('grant-page-` + suffix + `','user','someone','read','grant-admin-` + suffix + `','2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return workspace, user
}

func pageGrantRetired(t *testing.T, db *sql.DB, page string) bool {
	t.Helper()
	var retired sql.NullString
	if err := db.QueryRow(`SELECT retired_at FROM page_grants WHERE page_id=?`, page).Scan(&retired); err != nil {
		t.Fatal(err)
	}
	return retired.Valid
}

// A bundle carries the marker, and a replacing restore of an earlier bundle
// cannot be the way back for a grant retired on this target (#5069).
func TestRestoreKeepsPageGrantRetirement(t *testing.T) {
	ctx := context.Background()
	t.Run("the marker travels with the bundle", func(t *testing.T) {
		source := openMigratedDBCov(t)
		workspace, _ := seedPageGrantRestore(t, source, "gr-carry")
		if _, err := source.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='grant-admin-gr-carry'`); err != nil {
			t.Fatal(err)
		}
		if _, err := source.Exec(`UPDATE workspace_members SET role='ADMIN' WHERE id='grant-admin-gr-carry'`); err != nil {
			t.Fatal(err)
		}
		dump, err := DumpWorkspace(ctx, source, workspace)
		if err != nil {
			t.Fatal(err)
		}
		target := openMigratedDBCov(t)
		if err := RestoreDump(ctx, target, dump); err != nil {
			t.Fatal(err)
		}
		if !pageGrantRetired(t, target, "grant-page-gr-carry") {
			t.Fatal("a retired grant arrived live")
		}
	})
	t.Run("an earlier bundle does not undo a retirement here", func(t *testing.T) {
		db := openMigratedDBCov(t)
		workspace, _ := seedPageGrantRestore(t, db, "gr-earlier")
		dump, err := DumpWorkspace(ctx, db, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='grant-admin-gr-earlier'`); err != nil {
			t.Fatal(err)
		}
		if !pageGrantRetired(t, db, "grant-page-gr-earlier") {
			t.Fatal("calibration: the demotion did not retire the grant")
		}
		slug := dump.Tables["workspaces"][0]["slug"].(string)
		hooks := &RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
			_, err := ReplaceWorkspaceContents(ctx, tx, workspace, slug)
			return err
		}}
		if _, err := RestoreDumpTxHooks(ctx, db, dump, hooks); err != nil {
			t.Fatal(err)
		}
		if !pageGrantRetired(t, db, "grant-page-gr-earlier") {
			t.Fatal("restoring an earlier bundle revived a retired grant")
		}
	})
	t.Run("a bundle from before retirement is judged on arrival", func(t *testing.T) {
		source := openMigratedDBCov(t)
		workspace, _ := seedPageGrantRestore(t, source, "gr-legacy")
		if _, err := source.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='grant-admin-gr-legacy'`); err != nil {
			t.Fatal(err)
		}
		dump, err := DumpWorkspace(ctx, source, workspace)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range dump.Tables["page_grants"] {
			delete(row, "retired_at")
		}
		target := openMigratedDBCov(t)
		if err := RestoreDump(ctx, target, dump); err != nil {
			t.Fatal(err)
		}
		if !pageGrantRetired(t, target, "grant-page-gr-legacy") {
			t.Fatal("a legacy inert grant arrived live and would return with its issuer")
		}
	})
	t.Run("a live grant stays live", func(t *testing.T) {
		source := openMigratedDBCov(t)
		workspace, _ := seedPageGrantRestore(t, source, "gr-live")
		dump, err := DumpWorkspace(ctx, source, workspace)
		if err != nil {
			t.Fatal(err)
		}
		target := openMigratedDBCov(t)
		if err := RestoreDump(ctx, target, dump); err != nil {
			t.Fatal(err)
		}
		if pageGrantRetired(t, target, "grant-page-gr-live") {
			t.Fatal("a restore retired a live grant")
		}
	})
}
