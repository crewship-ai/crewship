package database

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeckNativeMigrationRegistryVersionMatchesConstant(t *testing.T) {
	var found int
	for _, m := range migrations {
		if m.name == "deck_native_family" {
			found++
			if m.version != deckNativeMigrationVersion || m.fnNoTx == nil {
				t.Fatalf("Deck registry binding differs from its migration: version=%d expected=%d nontransactional=%t", m.version, deckNativeMigrationVersion, m.fnNoTx != nil)
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one Deck migration, found %d", found)
	}
}

func TestDeckNativeMigrationPreservesExistingFamiliesAndDashboardChildren(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "native-families.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = ensureMigrationsTable(t.Context(), db.DB); err != nil {
		t.Fatal(err)
	}
	var baseline []migration
	for _, m := range migrations {
		if m.version != deckNativeMigrationVersion {
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
	exec(`INSERT INTO users(id,email,full_name) VALUES('native-user','native@example.test','Native')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('native-ws','Native','native-upgrade')`)
	for _, p := range []string{"project", "wordcraft", "gridcraft", "deckcraft", "invalid"} {
		exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,'native-ws',?,?,'native-user','{}',1)`, p, p, p)
	}
	for _, app := range []string{"artifact_project", "wordcraft", "gridcraft"} {
		p := app
		if app == "artifact_project" {
			p = "project"
		}
		exec(`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('native-ws',?,?,2,1)`, p, app)
		exec(`INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,message,actor,created_at) VALUES('native-ws',?,1,'{"entry_point":"document.json","files":{}}','original receipt','native-user','2026-10-28T00:00:00Z')`, p)
		exec(`INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES('native-ws',?,'original-hash',x'00ff80')`, p)
		if app != "artifact_project" {
			exec(`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,'native-ws',?,?,?,?)`, p, app, strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 64))
		}
	}
	exec(`INSERT INTO artifact_data_collections(workspace_id,page_id,name) VALUES('native-ws','project','records')`)
	exec(`INSERT INTO artifact_data_documents(workspace_id,page_id,collection,id,version,value_json,updated_by,updated_at) VALUES('native-ws','project','records','kept',7,'{"preserve":true}','native-user','2026-10-28T00:00:00Z')`)
	queries := []string{
		`SELECT * FROM artifact_version_heads ORDER BY rowid`,
		`SELECT * FROM artifact_versions ORDER BY rowid`,
		`SELECT * FROM artifact_version_blobs ORDER BY rowid`,
		`SELECT * FROM craft_native_documents ORDER BY rowid`,
		`SELECT * FROM artifact_data_collections ORDER BY rowid`,
		`SELECT * FROM artifact_data_documents ORDER BY rowid`,
		`SELECT type,name,tbl_name,rootpage,sql FROM sqlite_master WHERE type IN ('index','trigger') ORDER BY name`,
		`SELECT name,rootpage FROM sqlite_master WHERE type='table' ORDER BY name`,
	}
	before := make([]string, len(queries))
	for i, q := range queries {
		before[i] = deckMigrationRows(t, db.DB, q)
	}
	if err = migrationDeckNativeKinds(t.Context(), db.DB, logger); err != nil {
		t.Fatal(err)
	}
	for i, q := range queries {
		if got := deckMigrationRows(t, db.DB, q); got != before[i] {
			t.Fatalf("migration changed existing data/schema objects: %s", q)
		}
	}
	// Both closed families must widen, while all old immutable boundaries stay.
	exec(`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version) VALUES('native-ws','deckcraft','deckcraft',1)`)
	exec(`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('deckcraft','native-ws','deckcraft',?,?,?)`, strings.Repeat("d", 40), strings.Repeat("e", 64), strings.Repeat("f", 64))
	for _, q := range []string{
		`UPDATE artifact_version_heads SET content_type='deckcraft' WHERE page_id='wordcraft'`,
		`UPDATE craft_native_documents SET application_id='deckcraft' WHERE page_id='wordcraft'`,
		`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version) VALUES('native-ws','invalid','unknown',1)`,
		`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) SELECT 'invalid',workspace_id,'unknown',engine_revision,engine_sha256,editor_bundle FROM craft_native_documents WHERE page_id='wordcraft'`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err == nil {
			t.Fatalf("closed/immutable boundary accepted: %s", q)
		}
	}
	if err = migrationDeckNativeKinds(t.Context(), db.DB, logger); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	for _, pragma := range []struct {
		query string
		want  int
	}{{"PRAGMA foreign_keys", 1}, {"PRAGMA writable_schema", 0}} {
		var got int
		if err := db.QueryRowContext(t.Context(), pragma.query).Scan(&got); err != nil || got != pragma.want {
			t.Fatalf("%s: %d %v", pragma.query, got, err)
		}
	}
}

func deckMigrationRows(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	r, err := db.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cols, err := r.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out [][]any
	for r.Next() {
		row := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range row {
			dest[i] = &row[i]
		}
		if err := r.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Design widens the head family before Deck is deployed. Exercise that exact
// closed CHECK as a schema compatibility fixture; the full registry migration
// and dashboard child preservation are covered separately above.
func TestDeckNativeMigrationPreservesDesignFamily(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "design-before-deck.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE craft_native_documents (page_id TEXT PRIMARY KEY, application_id TEXT NOT NULL CHECK(application_id IN ('wordcraft','gridcraft')))`,
		`CREATE TABLE artifact_version_heads (page_id TEXT PRIMARY KEY, content_type TEXT NOT NULL CHECK(content_type IN ('artifact_project','wordcraft','gridcraft','design')), latest_version INTEGER NOT NULL)`,
		`INSERT INTO artifact_version_heads VALUES ('existing-design','design',7)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	before := deckMigrationRows(t, db.DB, `SELECT * FROM artifact_version_heads`)
	if err := migrationDeckNativeKinds(t.Context(), db.DB, nil); err != nil {
		t.Fatal(err)
	}
	if got := deckMigrationRows(t, db.DB, `SELECT * FROM artifact_version_heads`); got != before {
		t.Fatal("changed existing Design head")
	}
	for _, q := range []string{
		`INSERT INTO artifact_version_heads VALUES ('new-deck','deckcraft',1)`,
		`INSERT INTO artifact_version_heads VALUES ('new-design','design',1)`,
		`INSERT INTO craft_native_documents VALUES ('new-deck','deckcraft')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO artifact_version_heads VALUES ('unknown','unknown',1)`); err == nil {
		t.Fatal("opened closed family constraint")
	}
	if err := migrationDeckNativeKinds(t.Context(), db.DB, nil); err != nil {
		t.Fatalf("replay: %v", err)
	}
}
