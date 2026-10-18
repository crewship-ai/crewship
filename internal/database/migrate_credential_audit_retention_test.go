package database

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCredentialAuditRejectsParentHardDelete(t *testing.T) {
	db := openMigratedDB(t)
	seedCredentialForFields(t, db, "retained")
	if _, err := db.Exec(`INSERT INTO credential_audit(id,credential_id,workspace_id,event_type,metadata_json) VALUES('history','retained','ws-cf','READ','{"original":true}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM credentials WHERE id='retained'`); err == nil {
		t.Error("hard delete cascaded credential history")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE id='history' AND metadata_json='{"original":true}'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit not retained: %d %v", n, err)
	}
}

func TestCredentialAuditRetentionUpgrade(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := MigrateSkipping(ctx, db.DB, logger, "credential_audit_retention"); err != nil {
		t.Fatal(err)
	}
	seedCredentialForFields(t, db.DB, "upgraded")
	execMigrationFixture(t, db, `INSERT INTO credential_audit(id,credential_id,event_type,agent_id,ip_address,metadata_json,occurred_at,workspace_id) VALUES('old','upgraded','READ',NULL,'127.0.0.1','{"old":true}','2020-01-01T00:00:00Z','ws-cf')`)
	read := func() []any {
		t.Helper()
		row := make([]any, 8)
		ptr := make([]any, 8)
		for i := range row {
			ptr[i] = &row[i]
		}
		if err := db.QueryRow(`SELECT id,credential_id,event_type,agent_id,ip_address,metadata_json,occurred_at,workspace_id FROM credential_audit WHERE id='old'`).Scan(ptr...); err != nil {
			t.Fatal(err)
		}
		return row
	}
	shape := func(query string) [][]any {
		t.Helper()
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var all [][]any
		for rows.Next() {
			v := make([]any, len(cols))
			p := make([]any, len(cols))
			for i := range v {
				p[i] = &v[i]
			}
			if err := rows.Scan(p...); err != nil {
				t.Fatal(err)
			}
			all = append(all, v)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
		return all
	}
	columnsBefore := shape(`PRAGMA table_info(credential_audit)`)
	indexesBefore := map[string][][]any{}
	for _, index := range []string{"idx_credential_audit_credential", "idx_credential_audit_occurred", "idx_credential_audit_workspace_time", "idx_credential_audit_agent"} {
		indexesBefore[index] = shape(`PRAGMA index_xinfo(` + index + `)`)
	}
	indexListBefore := shape(`PRAGMA index_list(credential_audit)`)
	before := read()
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("migration changed an existing audit row")
	}
	if !reflect.DeepEqual(columnsBefore, shape(`PRAGMA table_info(credential_audit)`)) {
		t.Error("migration changed current column/type/nullability/default/PK shape")
	}
	if !reflect.DeepEqual(indexListBefore, shape(`PRAGMA index_list(credential_audit)`)) {
		t.Error("migration changed index uniqueness/origin/partial shape")
	}
	for index, before := range indexesBefore {
		if !reflect.DeepEqual(before, shape(`PRAGMA index_xinfo(`+index+`)`)) {
			t.Errorf("migration changed index definition %s", index)
		}
	}
	for _, index := range []string{"idx_credential_audit_credential", "idx_credential_audit_occurred", "idx_credential_audit_workspace_time", "idx_credential_audit_agent"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s: %d %v", index, n, err)
		}
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	// Whole-workspace erasure retains its existing cascade contract.
	if _, err := db.Exec(`DELETE FROM workspaces WHERE id='ws-cf'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE id='old'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("workspace cascade: %d %v", n, err)
	}
}

func TestCredentialAuditNullWorkspaceHardErase(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-cascade", true: "retained-history"}[modern], func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "erase.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if modern {
				err = Migrate(ctx, db.DB, logger)
			} else {
				err = MigrateSkipping(ctx, db.DB, logger, "credential_audit_retention")
			}
			if err != nil {
				t.Fatal(err)
			}
			seedCredentialForFields(t, db.DB, "null-history")
			execMigrationFixture(t, db, `INSERT INTO credential_audit(id,credential_id,event_type,workspace_id) VALUES('null-history','null-history','READ',NULL)`)
			if _, err := db.Exec(`DELETE FROM workspaces WHERE id='ws-cf'`); err != nil {
				t.Fatalf("hard workspace erasure blocked by legal NULL history: %v", err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE id='null-history'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("hard erased workspace retained audit: %d %v", n, err)
			}
		})
	}
}
