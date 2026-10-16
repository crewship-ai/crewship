package database

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const artifactBoardFirstMigration = 20261014114745

// This snapshot was captured with the real registry/modernc driver before the
// 33 unreleased feature migrations were consolidated. Comparing complete DDL
// tokens protects CHECK/UNIQUE/FK constraints, index expressions/predicates and
// trigger bodies, which column-only PRAGMAs would miss. Shipped main migrations
// remain the baseline; the migration ledger is intentionally a different shape.
func TestArtifactBoardConsolidatedMigrationsPreserveSchemaAndMainGrants(t *testing.T) {
	var expected struct {
		Objects map[string]string `json:"objects"`
	}
	raw, err := os.ReadFile("testdata/artifact_board_schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(t.TempDir(), "main-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = ensureMigrationsTable(t.Context(), db.DB); err != nil {
		t.Fatal(err)
	}
	var baseline, feature []migration
	for _, m := range migrations {
		if m.version < artifactBoardFirstMigration {
			baseline = append(baseline, m)
		} else if m.name == "page_comment_grants" || m.name == "page_artifacts" || m.name == "page_boards" {
			feature = append(feature, m)
		}
	}
	if len(feature) != 3 {
		t.Fatalf("expected three consolidated migrations, found %d", len(feature))
	}
	if err = applyRegistry(t.Context(), db.DB, baseline, logger); err != nil {
		t.Fatal("fresh shipped-main baseline", err)
	}
	before := artifactBoardSchema(t, db.DB)
	if _, err = db.Exec(`INSERT INTO users(id,email) VALUES('owner','owner@example.invalid'),('reader','reader@example.invalid');
INSERT INTO workspaces(id,name,slug) VALUES('workspace','Workspace','workspace');
INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('page','workspace','page','Page','owner','{}');
INSERT INTO page_grants(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at) VALUES
 ('page','user','reader','read',NULL,'owner','2026-10-01T00:00:00Z'),
 ('page','user','reader','write',NULL,'owner','2026-10-02T00:00:00Z'),
 ('page','user','reader','produce','["panel-one"]','owner','2026-10-03T00:00:00Z'),
 ('page','workspace','','read',NULL,'owner','2026-10-04T00:00:00Z'),
 ('page','agent','named-agent','write',NULL,'owner','2026-10-05T00:00:00Z');`); err != nil {
		t.Fatal("seed existing main grants", err)
	}
	grantRows := func() string {
		t.Helper()
		var rows string
		if err := db.QueryRow(`SELECT json_group_array(json_array(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at)) FROM (SELECT * FROM page_grants ORDER BY page_id,subject_type,subject_id,level)`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	grantsBefore := grantRows()
	const comment = `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('page',?,?,'comment','owner')`
	if _, err = db.Exec(comment, "user", "reader"); err == nil {
		t.Fatal("main unexpectedly accepted the new comment level")
	}
	if err = applyRegistry(t.Context(), db.DB, feature, logger); err != nil {
		t.Fatal("consolidated feature upgrade", err)
	}
	if after := grantRows(); after != grantsBefore {
		t.Fatalf("existing main grant values changed: before=%s after=%s", grantsBefore, after)
	}
	after := artifactBoardSchema(t, db.DB)
	changed := map[string]string{}
	for key, value := range after {
		if before[key] != value {
			changed[key] = value
		}
	}
	for key := range before {
		if _, ok := after[key]; !ok {
			t.Errorf("shipped schema object disappeared: %s", key)
		}
	}
	for key, want := range expected.Objects {
		if got := changed[key]; got != want {
			t.Errorf("schema definition differs for %s: got=%q want=%q", key, got, want)
		}
	}
	for key := range changed {
		if _, ok := expected.Objects[key]; !ok {
			t.Errorf("unexpected schema change: %s", key)
		}
	}
	for _, subject := range []struct{ kind, id string }{{"user", "reader"}, {"agent", "named-agent"}, {"workspace", ""}} {
		if _, err = db.Exec(comment, subject.kind, subject.id); err != nil {
			t.Fatalf("new explicit %s comment grant rejected: %v", subject.kind, err)
		}
	}
	for _, rejected := range []string{
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id) VALUES('page','workspace','','produce','["panel-one"]','owner')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id) VALUES('page','user','other','write','["panel-one"]','owner')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('page','user','other','admin','owner')`,
	} {
		if _, err = db.Exec(rejected); err == nil {
			t.Fatal("grant CHECK constraint weakened", rejected)
		}
	}
	grantsAfter := grantRows()
	if err = Migrate(t.Context(), db.DB, logger); err != nil {
		t.Fatal("migration replay", err)
	}
	if grantRows() != grantsAfter {
		t.Fatal("migration replay changed grant rows")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("consolidated upgrade left a foreign-key violation")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("matched %d original final-schema objects; existing main grants survived and new comment constraints remain enforced", len(changed))
}

var artifactBoardSQLTokens = regexp.MustCompile(`(?s)'(?:[^']|'')*'|"(?:""|[^"])*"|--[^\n]*(?:\n|$)|/\*.*?\*/|[A-Za-z_][A-Za-z_0-9]*|[^\s]`)

func artifactBoardSchema(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	objects := map[string]string{}
	for rows.Next() {
		var kind, name, definition string
		if err = rows.Scan(&kind, &name, &definition); err != nil {
			t.Fatal(err)
		}
		var tokens []string
		for _, token := range artifactBoardSQLTokens.FindAllString(definition, -1) {
			if strings.HasPrefix(token, "--") || strings.HasPrefix(token, "/*") {
				continue
			}
			if strings.HasPrefix(token, `"`) {
				token = strings.ReplaceAll(token[1:len(token)-1], `""`, `"`)
			}
			if !strings.HasPrefix(token, "'") {
				token = strings.ToLower(token)
			}
			tokens = append(tokens, token)
		}
		objects[kind+" "+name] = strings.Join(tokens, " ")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return objects
}
