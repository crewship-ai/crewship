package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRetiredCredentialCreatorMigration(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := MigrateSkipping(ctx, db.DB, logger, "retired_credential_creator"); err != nil {
		t.Fatal(err)
	}
	seedCredentialForFields(t, db.DB, "creator-live")
	seedCredentialForFields(t, db.DB, "creator-retired")
	execMigrationFixture(t, db, `UPDATE credentials SET deleted_at='2026-01-01' WHERE id='creator-retired'`)
	execMigrationFixture(t, db, `INSERT INTO credential_audit(id,credential_id,workspace_id,event_type,metadata_json) VALUES('creator-history','creator-retired','ws-cf','REVOKE','{"preserve":true}')`)
	columns := func() []string {
		t.Helper()
		rows, err := db.Query(`PRAGMA table_info(credentials)`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var cid, notNull, pk int
			var name, kind string
			var def any
			if err := rows.Scan(&cid, &name, &kind, &notNull, &def, &pk); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return names
	}
	before := columns()
	var expected []string
	for _, name := range before {
		if name != "created_by" {
			expected = append(expected, name)
		}
	}
	expected = append(expected, "created_by")
	var indexBefore string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='idx_credential_created_by'`).Scan(&indexBefore); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(columns(), expected) {
		t.Error("migration did not preserve column names/order apart from moving created_by to the end")
	}
	var activeCreator, cipher, retiredCreator string
	if err := db.QueryRow(`SELECT created_by,encrypted_value FROM credentials WHERE id='creator-live' AND deleted_at IS NULL`).Scan(&activeCreator, &cipher); err != nil || activeCreator != "u-cf" || cipher != "enc" {
		t.Fatalf("active credential changed: %v", err)
	}
	if err := db.QueryRow(`SELECT created_by FROM credentials WHERE id='creator-retired'`).Scan(&retiredCreator); err != nil || retiredCreator != "u-cf" {
		t.Fatalf("migration rewrote historical creator before an erasure request: %v", err)
	}
	if _, err := db.Exec(`UPDATE credentials SET created_by=NULL WHERE id='creator-retired'`); err != nil {
		t.Errorf("retired creator must be nullable: %v", err)
	}
	if _, err := db.Exec(`UPDATE credentials SET created_by=NULL WHERE id='creator-live'`); err == nil {
		t.Error("live NULL creator accepted")
	}
	if _, err := db.Exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,created_by) VALUES('creatorless','ws-cf','NO_CREATOR','enc',NULL)`); err == nil {
		t.Error("new live NULL creator accepted")
	}
	// Independently prove the reactivation gate even on the pre-fix schema: a
	// legal retired NULL fixture is available only after the nullable migration.
	var creator any
	if err := db.QueryRow(`SELECT created_by FROM credentials WHERE id='creator-retired'`).Scan(&creator); err != nil {
		t.Fatal(err)
	}
	if creator == nil {
		if _, err := db.Exec(`UPDATE credentials SET deleted_at=NULL WHERE id='creator-retired'`); err == nil {
			t.Error("creatorless tombstone reactivated")
		}
	}
	var indexAfter string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='idx_credential_created_by'`).Scan(&indexAfter); err != nil || indexAfter != indexBefore {
		t.Errorf("creator index changed: %v", err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_list(credentials)`)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		var id, seq int
		var table, from, to, update, delete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &update, &delete, &match); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if from == "created_by" {
			found = table == "users" && to == "id" && update == "NO ACTION" && delete == "NO ACTION"
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if !found {
		t.Error("creator FK/action changed")
	}
	var history int
	if err := db.QueryRow(`SELECT count(*) FROM credential_audit WHERE id='creator-history' AND credential_id='creator-retired' AND metadata_json='{"preserve":true}'`).Scan(&history); err != nil || history != 1 {
		t.Errorf("audit history changed: %d %v", history, err)
	}
	rows, err = db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("native FK violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRetiredCredentialCreatorCopyMustBeAcknowledged(t *testing.T) {
	for _, fault := range []string{"copy-ignore", "copy-deletes-parent"} {
		t.Run(fault, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "copy.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err := MigrateSkipping(ctx, db.DB, logger, "retired_credential_creator"); err != nil {
				t.Fatal(err)
			}
			seedCredentialForFields(t, db.DB, "copy-live")
			seedCredentialForFields(t, db.DB, "copy-retired")
			execMigrationFixture(t, db, `UPDATE credentials SET deleted_at='2026-01-01' WHERE id='copy-retired'`)
			// The active parent is deliberately free of inbound references, making
			// native deletion legal; history on the retired parent must still survive.
			execMigrationFixture(t, db, `INSERT INTO credential_audit(id,credential_id,workspace_id,event_type) VALUES('copy-history','copy-retired','ws-cf','REVOKE')`)
			var healthy int
			if err := db.QueryRow(`SELECT count(*) FROM credentials WHERE id IN ('copy-live','copy-retired') AND created_by='u-cf'`).Scan(&healthy); err != nil || healthy != 2 {
				t.Fatalf("copy baseline: %d %v", healthy, err)
			}
			if fault == "copy-ignore" {
				execMigrationFixture(t, db, `CREATE TRIGGER copy_fault BEFORE UPDATE ON credentials WHEN OLD.id='copy-live' BEGIN SELECT RAISE(IGNORE); END`)
			} else {
				execMigrationFixture(t, db, `CREATE TRIGGER copy_fault AFTER UPDATE ON credentials WHEN NEW.id='copy-live' BEGIN DELETE FROM credentials WHERE id=NEW.id; END`)
			}
			if err := Migrate(ctx, db.DB, logger); err == nil {
				t.Error("migration acknowledged lost creator copy or parent identity")
			}
			var ledger, retained int
			if err := db.QueryRow(`SELECT count(*) FROM _migrations WHERE name='retired_credential_creator'`).Scan(&ledger); err != nil {
				t.Fatal(err)
			}
			if ledger != 0 {
				t.Error("unacknowledged creator copy recorded in migration ledger")
			}
			if err := db.QueryRow(`SELECT count(*) FROM credentials WHERE (id='copy-live' AND created_by='u-cf' AND encrypted_value='enc' AND deleted_at IS NULL) OR (id='copy-retired' AND created_by='u-cf' AND encrypted_value='' AND deleted_at='2026-01-01')`).Scan(&retained); err != nil || retained != 2 {
				t.Errorf("failed migration changed original tuples: %d %v", retained, err)
			}
			var temporary int
			if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('credentials') WHERE name='erasure_created_by'`).Scan(&temporary); err != nil || temporary != 0 {
				t.Errorf("failed migration left intermediate column: %d %v", temporary, err)
			}
		})
	}
}
