package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestRetiredCredentialErasureUpgrade(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := MigrateSkipping(ctx, db.DB, logger, "retired_credential_erasure"); err != nil {
		t.Fatal(err)
	}
	seedCredentialForFields(t, db.DB, "retired")
	seedCredentialForFields(t, db.DB, "live")
	execMigrationFixture(t, db, `UPDATE credentials SET encrypted_value='old-cipher',account_email='old@example.test',encrypted_refresh_token='refresh',deleted_at='2026-01-01' WHERE id='retired'`)
	execMigrationFixture(t, db, `UPDATE credentials SET encrypted_value='live-cipher' WHERE id='live'`)
	execMigrationFixture(t, db, `INSERT INTO credential_fields(credential_id,key,encrypted_value,is_secret) VALUES('retired','secret','field-cipher',1)`)
	execMigrationFixture(t, db, `INSERT INTO credential_fields(credential_id,key,value,is_secret) VALUES('live','region','eu',0)`)
	execMigrationFixture(t, db, `INSERT INTO credential_audit(id,credential_id,event_type,workspace_id,metadata_json) VALUES('retired-history','retired','REVOKE','ws-cf','{"preserve":true}')`)
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatal(err)
	}
	var erased bool
	var live string
	var oldFields, liveFields, history int
	if err := db.QueryRow(`SELECT encrypted_value='' AND account_email IS NULL AND encrypted_refresh_token IS NULL AND deleted_at='2026-01-01' FROM credentials WHERE id='retired'`).Scan(&erased); err != nil || !erased {
		t.Fatalf("old tombstone material survived upgrade: %v", err)
	}
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id='live' AND deleted_at IS NULL`).Scan(&live); err != nil || live != "live-cipher" {
		t.Fatalf("upgrade changed active credential: %v", err)
	}
	for _, q := range []struct {
		sql  string
		dest *int
	}{
		{`SELECT count(*) FROM credential_fields WHERE credential_id='retired'`, &oldFields},
		{`SELECT count(*) FROM credential_fields WHERE credential_id='live' AND value='eu'`, &liveFields},
		{`SELECT count(*) FROM credential_audit WHERE id='retired-history' AND credential_id='retired' AND metadata_json='{"preserve":true}'`, &history},
	} {
		if err := db.QueryRow(q.sql).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	if oldFields != 0 || liveFields != 1 || history != 1 {
		t.Fatalf("upgrade data boundaries: oldfields=%d livefields=%d history=%d", oldFields, liveFields, history)
	}
	// Even recursive trigger mode terminates once fields and material are gone.
	execMigrationFixture(t, db, `PRAGMA recursive_triggers=ON`)
	execMigrationFixture(t, db, `UPDATE credentials SET encrypted_value='attempted-reintroduction' WHERE id='retired'`)
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id='retired'`).Scan(&live); err != nil || live != "" {
		t.Fatalf("recursive scrub did not terminate safely: %v", err)
	}
}

func TestRetiredCredentialUpgradeRefusesIncompleteErasure(t *testing.T) {
	for _, fault := range []string{"field-ignore", "material-ignore"} {
		t.Run(fault, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err := MigrateSkipping(ctx, db.DB, logger, "retired_credential_erasure"); err != nil {
				t.Fatal(err)
			}
			seedCredentialForFields(t, db.DB, "old-retired")
			execMigrationFixture(t, db, `UPDATE credentials SET deleted_at='2026-01-01',encrypted_value='old-cipher' WHERE id='old-retired'`)
			execMigrationFixture(t, db, `INSERT INTO credential_fields(credential_id,key,encrypted_value,is_secret) VALUES('old-retired','secret','old-field',1)`)
			var healthy int
			if err := db.QueryRow(`SELECT count(*) FROM credentials c JOIN credential_fields f ON f.credential_id=c.id WHERE c.id='old-retired' AND c.encrypted_value='old-cipher' AND f.encrypted_value='old-field'`).Scan(&healthy); err != nil || healthy != 1 {
				t.Fatalf("historical fixture calibration: %d %v", healthy, err)
			}
			if fault == "field-ignore" {
				execMigrationFixture(t, db, `CREATE TRIGGER old_fault BEFORE DELETE ON credential_fields BEGIN SELECT RAISE(IGNORE); END`)
			} else {
				execMigrationFixture(t, db, `CREATE TRIGGER old_fault BEFORE UPDATE OF encrypted_value ON credentials BEGIN SELECT RAISE(IGNORE); END`)
			}
			if err := Migrate(ctx, db.DB, logger); err == nil {
				t.Error("upgrade acknowledged incomplete historical erasure")
			}
			var recorded int
			if err := db.QueryRow(`SELECT count(*) FROM _migrations WHERE name='retired_credential_erasure'`).Scan(&recorded); err != nil {
				t.Fatal(err)
			}
			if recorded != 0 {
				t.Error("incomplete erasure was recorded in migration ledger")
			}
		})
	}
}
