package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
)

// Upgrade an existing timestamp-20261020092200 database after historical
// feature migrations arrive through integration. Preserve issued ledger rows,
// credential values and constraints; compare fresh and upgraded named schemas.
func TestHistoricalMigrationsUpgradeExistingMain(t *testing.T) {
	const baselineMax = 20261020092200
	historical := map[int]string{
		20261018200000: "routine_folders",
		20261018200100: "routine_test_results",
		20261018200200: "routine_templates",
		20261018220000: "credential_folders",
		20261018224500: "pipeline_webhook_previous_secret",
		20261019090000: "workspace_appearance_default",
		20261019143137: "routine_folder_icon_parent",
		20261019194500: "pin_legacy_avatar_style",
	}
	var baseline []migration
	found := 0
	for _, m := range migrations {
		if name, ok := historical[m.version]; ok {
			if m.name != name {
				t.Fatalf("historical version %d has unexpected identity %q", m.version, m.name)
			}
			found++
			continue
		}
		if m.version <= baselineMax {
			baseline = append(baseline, m)
		}
	}
	if found != len(historical) {
		t.Fatalf("fixture needs exactly eight historical entries, found %d", found)
	}
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func(name string) *DB {
		t.Helper()
		db, err := Open(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	db := open("current-main-upgrade.db")
	if err := ensureMigrationsTable(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	if err := applyRegistry(ctx, db.DB, baseline, logger); err != nil {
		t.Fatal(err)
	}
	var maxApplied int
	if err := db.QueryRow(`SELECT MAX(version) FROM _migrations`).Scan(&maxApplied); err != nil || maxApplied != baselineMax {
		t.Fatalf("baseline is not current main's high-water mark: %d %v", maxApplied, err)
	}
	// Reuse the existing native fixture: explicit named columns, a real human
	// FK and fake ciphertext. No runtime/private database or credentials used.
	seedCredentialForFields(t, db.DB, "historical-live")
	seedCredentialForFields(t, db.DB, "historical-retired")
	if _, err := db.Exec(`UPDATE credentials SET deleted_at='2026-01-01',created_by=NULL WHERE id='historical-retired'`); err != nil {
		t.Fatal(err)
	}
	ledger := func() map[int][2]string {
		t.Helper()
		rows, err := db.Query(`SELECT version,name,applied_at FROM _migrations ORDER BY version`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int][2]string{}
		for rows.Next() {
			var version int
			var name, applied string
			if err := rows.Scan(&version, &name, &applied); err != nil {
				t.Fatal(err)
			}
			out[version] = [2]string{name, applied}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	credentialRows := func() string {
		t.Helper()
		var encoded string
		if err := db.QueryRow(`SELECT json_group_array(json_array(id,workspace_id,name,encrypted_value,type,provider,scope,status,created_by,deleted_at)) FROM (SELECT id,workspace_id,name,encrypted_value,type,provider,scope,status,created_by,deleted_at FROM credentials ORDER BY id)`).Scan(&encoded); err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	var creatorIndexBefore string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='idx_credential_created_by'`).Scan(&creatorIndexBefore); err != nil {
		t.Fatal(err)
	}
	beforeLedger, beforeRows := ledger(), credentialRows()
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatal(err)
	}
	after := ledger()
	bootMigrations := 0
	for _, m := range migrations {
		if !m.postDeploy {
			bootMigrations++
		}
	}
	if len(after) != bootMigrations {
		t.Fatalf("upgrade did not apply the boot registry: want=%d after=%d", bootMigrations, len(after))
	}
	for version, prior := range beforeLedger {
		if after[version] != prior {
			t.Errorf("issued main ledger identity/timestamp changed at %d", version)
		}
	}
	for version, name := range historical {
		if after[version][0] != name || after[version][1] == "" {
			t.Errorf("missing exact historical identity %d/%s", version, name)
		}
	}
	if credentialRows() != beforeRows {
		t.Error("upgrade changed existing named credential values or custodian tombstone")
	}
	var creatorIndexAfter string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='idx_credential_created_by'`).Scan(&creatorIndexAfter); err != nil || creatorIndexAfter != creatorIndexBefore {
		t.Errorf("issued creator index changed: %v", err)
	}
	for _, fk := range [][3]string{{"created_by", "users", "NO ACTION"}, {"folder_id", "credential_folders", "RESTRICT"}} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list('credentials') WHERE "from"=? AND "table"=? AND on_delete=?`, fk[0], fk[1], fk[2]).Scan(&count); err != nil || count != 1 {
			t.Errorf("credential %s FK missing/changed: %d %v", fk[0], count, err)
		}
	}
	if _, err := db.Exec(`UPDATE credentials SET created_by=NULL WHERE id='historical-live'`); err == nil {
		t.Error("upgrade weakened the issued live-creator constraint")
	}
	if credentialRows() != beforeRows {
		t.Error("rejected live-creator mutation changed a stored row")
	}
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatalf("second upgrade is not idempotent: %v", err)
	}
	if !reflect.DeepEqual(ledger(), after) || credentialRows() != beforeRows {
		t.Error("retry changed an existing ledger row or credential")
	}
	fresh := open("fresh-candidate.db")
	if err := Migrate(ctx, fresh.DB, logger); err != nil {
		t.Fatal(err)
	}
	columns := func(conn *sql.DB) []string {
		t.Helper()
		rows, err := conn.Query(`SELECT name FROM pragma_table_info('credentials') ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return names
	}
	if !reflect.DeepEqual(columns(db.DB), columns(fresh.DB)) {
		t.Error("fresh and upgraded schemas disagree on named credential columns")
	}
	for _, conn := range []*sql.DB{db.DB, fresh.DB} {
		rows, err := conn.Query(`PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		hasViolation := rows.Next()
		readErr := rows.Err()
		rows.Close()
		if hasViolation || readErr != nil {
			t.Fatalf("foreign key violation after migration: %v", readErr)
		}
	}
}
