package backup

import (
	"context"
	"database/sql"
	"strings"
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

func replacingRestore(t *testing.T, db *sql.DB, dump *DBDump, workspace string) {
	t.Helper()
	slug := dump.Tables["workspaces"][0]["slug"].(string)
	hooks := &RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
		_, err := ReplaceWorkspaceContents(ctx, tx, workspace, slug)
		return err
	}}
	if _, err := RestoreDumpTxHooks(context.Background(), db, dump, hooks); err != nil {
		t.Fatal(err)
	}
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
		replacingRestore(t, db, dump, workspace)
		if !pageGrantRetired(t, db, "grant-page-gr-earlier") {
			t.Fatal("restoring an earlier bundle revived a retired grant")
		}
	})
	// The retirement outlives the row: a revoke deletes it and a reissue
	// overwrites it, and neither may hand the old issuance back to an earlier
	// bundle.
	for _, test := range []struct {
		name string
		then []string
	}{
		{name: "retire, revoke, older restore", then: []string{
			`DELETE FROM page_grants WHERE page_id='grant-page-SUFFIX'`,
		}},
		{name: "retire, reissue, older restore", then: []string{
			`UPDATE workspace_members SET role='ADMIN' WHERE id='grant-admin-SUFFIX'`,
			`UPDATE page_grants SET retired_at=NULL,granted_at='2026-01-01T00:00:01Z' WHERE page_id='grant-page-SUFFIX'`,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			suffix := "gr-" + strings.ReplaceAll(test.name, ", ", "-")
			suffix = strings.ReplaceAll(suffix, " ", "-")
			db := openMigratedDBCov(t)
			workspace, _ := seedPageGrantRestore(t, db, suffix)
			dump, err := DumpWorkspace(ctx, db, workspace)
			if err != nil {
				t.Fatal(err)
			}
			steps := append([]string{`UPDATE workspace_members SET role='VIEWER' WHERE id='grant-admin-SUFFIX'`}, test.then...)
			for _, step := range steps {
				if _, err := db.Exec(strings.ReplaceAll(step, "SUFFIX", suffix)); err != nil {
					t.Fatal(err)
				}
			}
			replacingRestore(t, db, dump, workspace)
			if !pageGrantRetired(t, db, "grant-page-"+suffix) {
				t.Fatal("an earlier bundle brought the old issuance back live")
			}
		})
	}
	t.Run("the memory of a retirement never travels", func(t *testing.T) {
		db := openMigratedDBCov(t)
		workspace, _ := seedPageGrantRestore(t, db, "gr-local")
		if _, err := db.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='grant-admin-gr-local'`); err != nil {
			t.Fatal(err)
		}
		dump, err := DumpWorkspace(ctx, db, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, carried := dump.Tables["page_grant_retired_issuances"]; carried {
			t.Fatal("a bundle carries the target's retirement memory")
		}
		replacingRestore(t, db, dump, workspace)
		var kept int
		if err := db.QueryRow(`SELECT count(*) FROM page_grant_retired_issuances WHERE page_id='grant-page-gr-local'`).Scan(&kept); err != nil || kept != 1 {
			t.Fatalf("a replacing restore removed the target's memory: %d %v", kept, err)
		}
	})
	t.Run("a grant that never retired survives a replacing restore", func(t *testing.T) {
		db := openMigratedDBCov(t)
		workspace, _ := seedPageGrantRestore(t, db, "gr-never")
		dump, err := DumpWorkspace(ctx, db, workspace)
		if err != nil {
			t.Fatal(err)
		}
		replacingRestore(t, db, dump, workspace)
		if pageGrantRetired(t, db, "grant-page-gr-never") {
			t.Fatal("a replacing restore retired a grant that never lost standing")
		}
	})
	t.Run("a new issuance in a later bundle is not the retired one", func(t *testing.T) {
		db := openMigratedDBCov(t)
		workspace, _ := seedPageGrantRestore(t, db, "gr-new")
		for _, step := range []string{
			`UPDATE workspace_members SET role='VIEWER' WHERE id='grant-admin-gr-new'`,
			`UPDATE workspace_members SET role='ADMIN' WHERE id='grant-admin-gr-new'`,
			`UPDATE page_grants SET retired_at=NULL,granted_at='2026-01-01T00:00:01Z' WHERE page_id='grant-page-gr-new'`,
		} {
			if _, err := db.Exec(step); err != nil {
				t.Fatal(err)
			}
		}
		dump, err := DumpWorkspace(ctx, db, workspace)
		if err != nil {
			t.Fatal(err)
		}
		replacingRestore(t, db, dump, workspace)
		if pageGrantRetired(t, db, "grant-page-gr-new") {
			t.Fatal("the retirement of the old issuance ended the new one")
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
