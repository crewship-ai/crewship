package database

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Exercise an actual old schema containing every shipped artifact family and
// live dashboard data. A fresh empty database cannot detect DROP-parent cascade
// data loss when the head CHECK constraint is widened for Design.
func TestDesignMigrationPreservesExistingArtifactsAndDashboardData(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "design-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = ensureMigrationsTable(t.Context(), db.DB); err != nil {
		t.Fatal(err)
	}
	var baseline []migration
	for _, m := range migrations {
		if m.version < 20261028202000 {
			baseline = append(baseline, m)
		}
	}
	if err = applyRegistry(t.Context(), db.DB, baseline, logger); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email) VALUES('design-upgrade-user','design-upgrade@example.test')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('design-upgrade-ws','Design upgrade','design-upgrade'),('design-other-ws','Other','design-other')`)
	for i, kind := range []string{"artifact_project", "wordcraft", "gridcraft"} {
		exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,'design-upgrade-ws',?,?,'design-upgrade-user','{}')`, kind, kind, kind)
		exec(`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('design-upgrade-ws',?,?,?,?)`, kind, kind, i+2, i+1)
		exec(`INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,message,actor,created_at) VALUES('design-upgrade-ws',?,?,'{"entry_point":"index.html","files":{}}','Preserve me','design-upgrade-user','2026-10-28T20:00:00Z')`, kind, i+2)
		exec(`INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES('design-upgrade-ws',?,'original-bytes',x'00ff80')`, kind)
	}
	exec(`INSERT INTO artifact_data_collections(workspace_id,page_id,name) VALUES('design-upgrade-ws','artifact_project','orders'),('design-upgrade-ws','artifact_project','empty')`)
	exec(`INSERT INTO artifact_data_documents(workspace_id,page_id,collection,id,version,value_json,updated_by,updated_at) VALUES('design-upgrade-ws','artifact_project','orders','order-1',7,'{"total":1499,"currency":"CZK","nested":{"items":[1,2]}}','design-upgrade-user','2026-10-28T20:00:00Z')`)
	snapshot := func(table string) string {
		t.Helper()
		rows, err := db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1,2")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var all [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			all = append(all, values)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(all)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	schemaObjects := func() map[string]string {
		t.Helper()
		rows, err := db.QueryContext(t.Context(), `SELECT type,name,tbl_name FROM sqlite_schema WHERE type IN ('trigger','index') ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]string{}
		for rows.Next() {
			var kind, name, table string
			if err = rows.Scan(&kind, &name, &table); err != nil {
				t.Fatal(err)
			}
			result[name] = kind + ":" + table
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := map[string]string{}
	for _, table := range []string{"artifact_version_heads", "artifact_versions", "artifact_version_blobs", "artifact_data_collections", "artifact_data_documents"} {
		before[table] = snapshot(table)
	}
	objects := schemaObjects()
	for pass := 1; pass <= 2; pass++ {
		if err = Migrate(t.Context(), db.DB, logger); err != nil {
			t.Fatalf("migration pass %d: %v", pass, err)
		}
		after := map[string]string{}
		for table := range before {
			after[table] = snapshot(table)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("existing artifact content changed on pass %d:\nbefore %v\nafter %v", pass, before, after)
		}
		current := schemaObjects()
		for name, object := range objects {
			if current[name] != object {
				t.Errorf("lost or changed schema object %s: %q != %q", name, current[name], object)
			}
		}
	}
	foreignKeys := func() {
		t.Helper()
		rows, err := db.QueryContext(t.Context(), `PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if rows.Next() {
			var table, parent string
			var rowID, foreignKey int64
			_ = rows.Scan(&table, &rowID, &parent, &foreignKey)
			t.Fatalf("foreign key violation in %s row %d -> %s (%d)", table, rowID, parent, foreignKey)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	foreignKeys()
	var enabled int
	if err = db.QueryRow(`SELECT enabled FROM feature_flags WHERE key='artifacts_design'`).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatalf("Design feature must default off: %d %v", enabled, err)
	}
	if _, err = db.Exec(`INSERT INTO artifact_design_documents(workspace_id,page_id,state,updated_by,updated_at) VALUES('design-upgrade-ws','artifact_project',x'0000','design-upgrade-user','2026-10-28T20:00:00Z')`); err == nil || !strings.Contains(err.Error(), "family mismatch") {
		t.Fatalf("Design doc attached to another content family: %v", err)
	}
	// The recreated trigger must still reject a storage-family switch.
	if _, err = db.Exec(`UPDATE artifact_version_heads SET content_type='design' WHERE page_id='artifact_project'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("storage family became mutable: %v", err)
	}
	exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('design-new-page','design-upgrade-ws','design-new','Design','design-upgrade-user','{}')`)
	if _, err = db.Exec(`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version) VALUES('design-other-ws','design-new-page','design',1)`); err == nil || !strings.Contains(err.Error(), "workspace mismatch") {
		t.Fatalf("workspace head guard lost: %v", err)
	}
	exec(`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version) VALUES('design-upgrade-ws','design-new-page','design',1)`)
	exec(`INSERT INTO artifact_design_documents(workspace_id,page_id,state,updated_by,updated_at) VALUES('design-upgrade-ws','design-new-page',x'0000','design-upgrade-user','2026-10-28T20:00:00Z')`)
	// Preserve the existing owner-deletion restriction: retaining an artifact must
	// never silently become permission to delete its human custodian or data.
	if _, err = db.Exec(`DELETE FROM users WHERE id='design-upgrade-user'`); err == nil {
		t.Fatal("owned Page no longer restricts user deletion")
	}
	if got := snapshot("artifact_data_documents"); got != before["artifact_data_documents"] {
		t.Fatal("failed user deletion removed dashboard rows")
	}
	exec(`DELETE FROM pages WHERE id IN ('artifact_project','design-new-page')`)
	for _, table := range []string{"artifact_data_collections", "artifact_data_documents", "artifact_design_documents"} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("Page cascade lost for %s: count %d error %v", table, count, err)
		}
	}
	// Workspace deletion still owns the remaining heads, manifests and blobs.
	exec(`DELETE FROM workspaces WHERE id='design-upgrade-ws'`)
	for _, table := range []string{"pages", "artifact_version_heads", "artifact_versions", "artifact_version_blobs"} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("workspace cascade lost for %s: count %d error %v", table, count, err)
		}
	}
	exec(`DELETE FROM users WHERE id='design-upgrade-user'`)
	foreignKeys()
}
