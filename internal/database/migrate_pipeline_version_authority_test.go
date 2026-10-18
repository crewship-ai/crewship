package database

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// Equal content is insufficient to identify the authority of a publication.
// This schema preparation alone exposes no publication or execution endpoint.
func TestPipelineVersionSchemaSeparatesAuthorityIdentity(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db := migrationBoundaryDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, migrations[len(migrations)-1].version, logger); err != nil {
		t.Fatal(err)
	}
	var found int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pipeline_versions') WHERE name='authority_fingerprint' AND "notnull"=1`).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatalf("immutable executable authority discriminator missing: got %d, want 1", found)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('pipeline_versions') WHERE "table"='execution_origins' AND "from"='chat_origin_id' AND "to"='id' AND on_delete='NO ACTION'`).Scan(&found); err != nil || found != 1 {
		t.Fatalf("version origin FK missing: %d %v", found, err)
	}
	for _, query := range []string{
		`PRAGMA foreign_keys=ON`,
		`INSERT INTO workspaces(id,name,slug) VALUES('authority-ws','Authority fixture','authority-ws')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('authority-pipeline','authority-ws','authority-pipeline','Fixture','{}','same-content')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for index, fingerprint := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		if _, err := db.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint) VALUES(?,'authority-pipeline',?,'{}','same-content','user','fixture',?)`, fingerprint, index+1, fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint) VALUES('authority-duplicate','authority-pipeline',3,'{}','same-content','user','fixture',?)`, strings.Repeat("a", 64)); err == nil {
		t.Fatal("duplicate executable identity accepted")
	}
	for _, fingerprint := range []any{nil, "", "publisher-a", strings.Repeat("G", 64)} {
		if _, err := db.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint) VALUES('invalid-authority','authority-pipeline',3,'{}','other-content','user','fixture',?)`, fingerprint); err == nil {
			t.Fatalf("invalid fingerprint accepted: %v", fingerprint)
		}
	}
	if _, err := db.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id) VALUES('duplicate-version','authority-pipeline',1,'{}','other-content','user','fixture')`); err == nil {
		t.Fatal("duplicate integer version accepted")
	}
	if _, err := db.Exec(`UPDATE pipeline_versions SET authority_fingerprint=? WHERE version=1`, strings.Repeat("c", 64)); err == nil {
		t.Fatal("immutable authority was rewritten")
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign keys: %d %v", violations, err)
	}
}

func TestPipelineVersionAuthorityUpgradePreservesHistoryAndPins(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db := migrationBoundaryDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var target migration
	for _, candidate := range migrations {
		if candidate.name == "pipeline_version_authority_identity" {
			target = candidate
		}
	}
	if target.version == 0 {
		t.Fatal("authority identity migration missing")
	}
	if err := applyMigrationsUpTo(t.Context(), db, target.version-1, logger); err != nil {
		t.Fatal(err)
	}
	// Rebuilding this child table is safe only while no live table references it.
	// Check the actual pre-migration schema, including every shipped table.
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tableNames []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tableNames = append(tableNames, name)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	if err := tables.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range tableNames {
		var inbound int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list(?) WHERE lower("table")='pipeline_versions'`, name).Scan(&inbound); err != nil {
			t.Fatal(err)
		}
		if inbound != 0 {
			t.Fatalf("authority migration must account for incoming version FK from %s", name)
		}
	}
	for _, query := range []string{
		`PRAGMA foreign_keys=ON`,
		`INSERT INTO workspaces(id,name,slug) VALUES('upgrade-authority-ws','Fixture','upgrade-authority-ws')`,
		`INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('upgrade-origin','upgrade-authority-ws','root','chat','trigger','{}','2026-10-16T01:02:03.123456789Z')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,head_version,chat_origin_id) VALUES('upgrade-authority-p','upgrade-authority-ws','fixture','Fixture','{"a":1}','content-a',1,'upgrade-origin')`,
		`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,parent_version,change_summary,created_at,chat_origin_id) VALUES('old-a','upgrade-authority-p',1,'{"a":1}','content-a','user','historical-author',NULL,'A','2026-10-16T01:02:03.123456789Z','upgrade-origin'),('old-b','upgrade-authority-p',2,'{"b":2}','content-b','system','historical-system',1,'B','2026-10-16T02:03:04.234567890Z',NULL)`,
		`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,target_pipeline_version,cron_expr,enabled,chat_origin_id) VALUES('upgrade-authority-s','upgrade-authority-ws','Pinned','upgrade-authority-p',2,'0 8 * * *',0,'upgrade-origin')`,
		`INSERT INTO pending_runs(id,workspace_id,pipeline_id,pipeline_slug,pinned_version,status,fire_at,chat_origin_id) VALUES('upgrade-authority-pending','upgrade-authority-ws','upgrade-authority-p','fixture',1,'pending','2026-10-16T01:02:03Z','upgrade-origin')`,
		`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,pipeline_version,status,started_at,chat_origin_id) VALUES('upgrade-authority-wait','upgrade-authority-ws','upgrade-authority-p','fixture',2,'waiting','2026-10-16T01:02:03Z','upgrade-origin'),('upgrade-authority-interrupted','upgrade-authority-ws','upgrade-authority-p','fixture',1,'interrupted','2026-10-16T01:02:03Z','upgrade-origin')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	// Capture every existing field, not a reconstruction of the migration copy.
	queries := []string{
		`SELECT id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,parent_version,change_summary,created_at,chat_origin_id FROM pipeline_versions ORDER BY id`,
		`SELECT * FROM pipelines ORDER BY id`, `SELECT * FROM pipeline_schedules ORDER BY id`,
		`SELECT * FROM pending_runs ORDER BY id`, `SELECT * FROM pipeline_runs ORDER BY id`, `SELECT * FROM execution_origins ORDER BY id`,
	}
	snapshot := func(query string) string {
		t.Helper()
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var records [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for index := range values {
				dest[index] = &values[index]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			records = append(records, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	before := make([]string, len(queries))
	for index, query := range queries {
		before[index] = snapshot(query)
	}
	// An invalid historical row must fail copying atomically, retaining the old
	// table and every integer pin; no partial new table may survive the rollback.
	for _, query := range []string{`PRAGMA ignore_check_constraints=ON`, `UPDATE pipeline_versions SET author_type='invalid' WHERE id='old-a'`, `PRAGMA ignore_check_constraints=OFF`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), target.sql); err == nil {
		t.Fatal("corrupt historical row was silently accepted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='pipeline_versions_authority'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("failed upgrade leaked new table: %d %v", leaked, err)
	}
	if _, err := db.Exec(`UPDATE pipeline_versions SET author_type='user' WHERE id='old-a'`); err != nil {
		t.Fatal(err)
	}
	for index, query := range queries {
		if got := snapshot(query); got != before[index] {
			t.Fatalf("failed migration altered history: %s", query)
		}
	}
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), target.sql); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for index, query := range queries {
		if got := snapshot(query); got != before[index] {
			t.Fatalf("upgrade altered history or pins: %s", query)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE authority_fingerprint='legacy-trusted:v1'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy authority: %d %v", count, err)
	}
	for _, name := range []string{"idx_pipeline_versions_pipeline", "idx_pipeline_versions_chat_origin"} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s: %d %v", name, count, err)
		}
	}
	if _, err := db.Exec(`DELETE FROM execution_origins WHERE id='upgrade-origin'`); err == nil {
		t.Fatal("upgrade lost historical origin FK")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("upgrade foreign keys: %d %v", count, err)
	}
	if _, err := db.Exec(`DELETE FROM pipelines WHERE id='upgrade-authority-p'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("pipeline deletion lost version cascade: %d %v", count, err)
	}
}
