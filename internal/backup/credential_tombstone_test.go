package backup

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	sqlite "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestCredentialTombstoneAuditBackupRoundtrip(t *testing.T) {
	ctx := context.Background()
	source := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('history-w','History','history')`,
		`INSERT INTO users(id,email) VALUES('history-u','history@example.test')`,
		`INSERT INTO credentials(id,workspace_id,name,encrypted_value,created_by,deleted_at) VALUES('history-c','history-w','TOKEN [retired:history-c]','unchanged-cipher','history-u','2026-01-01T00:00:00Z')`,
		`INSERT INTO credential_audit(id,credential_id,event_type,workspace_id,metadata_json,occurred_at) VALUES('history-a','history-c','REVOKE','history-w','{"original":true}','2026-01-01T00:00:00Z')`,
	} {
		if _, err := source.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	dump, err := DumpWorkspace(ctx, source, "history-w")
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.MigratedSQLDB(t)
	// created_by is instance-global; ordinary workspace restore requires its user.
	if _, err := target.Exec(`INSERT INTO users(id,email) VALUES('history-u','history@example.test')`); err != nil {
		t.Fatal(err)
	}
	if err := RestoreDump(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	var cipher, deleted, metadata string
	if err := target.QueryRow(`SELECT c.encrypted_value,c.deleted_at,a.metadata_json FROM credentials c JOIN credential_audit a ON a.credential_id=c.id WHERE c.id='history-c' AND a.id='history-a'`).Scan(&cipher, &deleted, &metadata); err != nil {
		t.Fatal(err)
	}
	if cipher != "unchanged-cipher" || deleted != "2026-01-01T00:00:00Z" || metadata != `{"original":true}` {
		t.Fatal("restore changed tombstone/history")
	}
	active, err := ExportEncryptedCredentials(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range active {
		if c.ID == "history-c" {
			t.Fatal("instance active-secret export resurrected tombstone")
		}
	}
	rows, err := target.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("restored foreign key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialNullAuditReplaceRollsBack(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct-replace", true: "restore-replace"}[restore], func(t *testing.T) {
			ctx := context.Background()
			target := testutil.MigratedSQLDB(t)
			for _, q := range []string{
				`INSERT INTO users(id,email) VALUES('replace-u','replace@example.test')`,
				`INSERT INTO workspaces(id,name,slug) VALUES('target-w','Target','shared-slug')`,
				`INSERT INTO credentials(id,workspace_id,name,encrypted_value,created_by) VALUES('outside-history-c','target-w','TOKEN','original-cipher','replace-u')`,
				`INSERT INTO credential_audit(id,credential_id,event_type,workspace_id,metadata_json) VALUES('outside-history','outside-history-c','READ',NULL,'{"preserve":true}')`,
			} {
				admissionGuardExec(t, target, q)
			}
			snapshot := func() [][]any {
				t.Helper()
				names, err := target.Query(`SELECT name FROM main.sqlite_master WHERE type='table' ORDER BY name`)
				if err != nil {
					t.Fatal(err)
				}
				var tables []string
				for names.Next() {
					var n string
					if err := names.Scan(&n); err != nil {
						t.Fatal(err)
					}
					tables = append(tables, n)
				}
				if err := errors.Join(names.Err(), names.Close()); err != nil {
					t.Fatal(err)
				}
				var all [][]any
				for _, table := range tables {
					rows, err := target.Query(`SELECT * FROM main.` + quoteIdent(table))
					if err != nil {
						t.Fatal(err)
					}
					cols, err := rows.Columns()
					if err != nil {
						t.Fatal(err)
					}
					for rows.Next() {
						v := make([]any, len(cols)+1)
						v[0] = table
						p := make([]any, len(cols))
						for i := range p {
							p[i] = &v[i+1]
						}
						if err := rows.Scan(p...); err != nil {
							t.Fatal(err)
						}
						for i, x := range v {
							if b, ok := x.([]byte); ok {
								c := make([]byte, len(b))
								copy(c, b)
								v[i] = c
							}
						}
						all = append(all, v)
					}
					if err := errors.Join(rows.Err(), rows.Close()); err != nil {
						t.Fatal(err)
					}
				}
				return all
			}
			var dump *DBDump
			if restore {
				source := testutil.MigratedSQLDB(t)
				admissionGuardExec(t, source, `INSERT INTO workspaces(id,name,slug) VALUES('incoming-w','Incoming','shared-slug')`)
				var err error
				dump, err = DumpWorkspace(ctx, source, "incoming-w")
				if err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot()
			var err error
			called := false
			if restore {
				_, err = RestoreDumpTxHooks(ctx, target, dump, &RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
					called = true
					_, err := ReplaceWorkspaceContents(ctx, tx, "incoming-w", "shared-slug")
					return err
				}})
			} else {
				var tx *sql.Tx
				tx, err = target.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				called = true
				_, err = ReplaceWorkspaceContents(ctx, tx, "incoming-w", "shared-slug")
				if rollbackErr := tx.Rollback(); rollbackErr != nil {
					t.Fatal(rollbackErr)
				}
			}
			var dbErr *sqlite.Error
			refused := errors.As(err, &dbErr) && dbErr.Code() == 787
			if restore {
				// Restore's existing precommit foreign_key_check reports the orphan
				// before SQLite reaches COMMIT; it is not a driver constraint error.
				refused = err != nil && strings.HasPrefix(err.Error(), "backup: deferred FK violations after restore inserts: credential_audit.rowid=") && strings.HasSuffix(err.Error(), " → credentials")
			}
			if !called || !refused {
				t.Fatalf("expected FK refusal after actual replace, got called=%v err=%v", called, err)
			}
			if !reflect.DeepEqual(before, snapshot()) {
				t.Fatal("failed replace changed database rows or erased out-of-scope NULL history")
			}
		})
	}
}
