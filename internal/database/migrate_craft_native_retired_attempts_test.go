package database

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// Land the actual preceding schema with existing native document data, then
// run the real registry upgrade. New attempt fences must not rewrite history,
// compatibility pins or the live lease that an in-flight request carries.
func TestNativeRetiredAttemptUpgradePreservesExistingDocument(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "native-attempt-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	version := 0
	for _, m := range migrations {
		if m.name == "craft_native_retired_attempts" {
			version = m.version
		}
	}
	if version == 0 {
		t.Fatal("native attempt migration not registered")
	}
	if err = applyMigrationsUpTo(t.Context(), db, version-1, logger); err != nil {
		t.Fatal(err)
	}
	var before int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='craft_native_retired_attempts'`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("upgrade fixture already contains attempt table: count=%d err=%v", before, err)
	}
	for _, q := range []string{
		`PRAGMA foreign_keys=ON`,
		`INSERT INTO users(id,email) VALUES('native-owner','native-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('native-ws','Native','native-upgrade')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('native-page','native-ws','native-page','Native','native-owner','{}')`,
		`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-page','native-ws','wordcraft','` + strings.Repeat("a", 40) + `','` + strings.Repeat("b", 64) + `','` + strings.Repeat("c", 64) + `')`,
		`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('native-ws','native-page','wordcraft',2,1)`,
		`INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES('native-ws','native-page','old-hash',X'007b7dff'),('native-ws','native-page','new-hash',X'7b2276223a327d')`,
		`INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,message,actor,created_at) VALUES('native-ws','native-page',1,'{}','first receipt','native-owner','2026-11-01T00:00:00Z'),('native-ws','native-page',2,'{}','second receipt','native-owner','2026-11-01T01:00:00Z')`,
		`INSERT INTO mooring_edit_leases(page_id,workspace_id,actor_id,session_id,generation,boot_id,expires_ns) VALUES('native-page','native-ws','native-owner','session-current',7,'boot-current',999999999999)`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	assertPreserved := func() {
		t.Helper()
		for _, q := range []string{
			`SELECT count(*) FROM craft_native_documents WHERE page_id='native-page' AND application_id='wordcraft' AND engine_revision='` + strings.Repeat("a", 40) + `' AND engine_sha256='` + strings.Repeat("b", 64) + `' AND editor_bundle='` + strings.Repeat("c", 64) + `'`,
			`SELECT count(*) FROM artifact_version_heads WHERE page_id='native-page' AND latest_version=2 AND shared_version=1 AND content_type='wordcraft'`,
			`SELECT count(*) FROM artifact_version_blobs WHERE page_id='native-page' AND ((hash='old-hash' AND hex(content)='007B7DFF') OR (hash='new-hash' AND hex(content)='7B2276223A327D')) HAVING count(*)=2`,
			`SELECT count(*) FROM artifact_versions WHERE page_id='native-page' AND ((version=1 AND message='first receipt') OR (version=2 AND message='second receipt')) HAVING count(*)=2`,
			`SELECT count(*) FROM mooring_edit_leases WHERE page_id='native-page' AND session_id='session-current' AND generation=7 AND boot_id='boot-current' AND expires_ns=999999999999`,
		} {
			var n int
			if err := db.QueryRow(q).Scan(&n); err != nil || n < 1 {
				t.Fatalf("native upgrade changed existing data: count=%d err=%v query=%s", n, err, q)
			}
		}
	}
	if err = Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	assertPreserved()
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM craft_native_retired_attempts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("upgrade created attempt authority: rows=%d err=%v", n, err)
	}
	if _, err = db.Exec(`INSERT INTO craft_native_retired_attempts(page_id,workspace_id,session_id,attempt_id,generation,base_version) VALUES('native-page','native-ws','session-current','attempt-old',7,2)`); err != nil {
		t.Fatal(err)
	}
	// An idempotent rerun keeps the durable runtime fence and the existing data.
	if err = Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	assertPreserved()
	if err = db.QueryRow(`SELECT count(*) FROM craft_native_retired_attempts WHERE page_id='native-page' AND attempt_id='attempt-old' AND generation=7 AND base_version=2`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rerun lost attempt fence: rows=%d err=%v", n, err)
	}
	if _, err = db.Exec(`DELETE FROM pages WHERE id='native-page'`); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM craft_native_retired_attempts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("page deletion retained attempt rows: rows=%d err=%v", n, err)
	}
}
