package backup

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestRetiredCredentialCreatorBackupRoundtrip(t *testing.T) {
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "fork"}[fork], func(t *testing.T) {
			ctx := context.Background()
			source := testutil.MigratedSQLDB(t)
			for _, q := range []string{
				`INSERT INTO users(id,email) VALUES('creator-u','creator@example.test')`,
				`INSERT INTO workspaces(id,name,slug) VALUES('creator-w','Creator','creator')`,
				`INSERT INTO credentials(id,workspace_id,name,encrypted_value,created_by) VALUES('creator-live','creator-w','LIVE','live-cipher','creator-u')`,
				`INSERT INTO credentials(id,workspace_id,name,encrypted_value,created_by,deleted_at) VALUES('creator-retired','creator-w','RETIRED','','creator-u','2026-01-01')`,
				`INSERT INTO credential_audit(id,credential_id,workspace_id,event_type,metadata_json) VALUES('creator-audit','creator-retired','creator-w','REVOKE','{"preserve":true}')`,
			} {
				if _, err := source.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := source.Exec(`UPDATE credentials SET created_by=NULL WHERE id='creator-retired'`); err != nil {
				t.Errorf("retired creator cannot be anonymized before export: %v", err)
			}
			dump, err := DumpWorkspace(ctx, source, "creator-w")
			if err != nil {
				t.Fatal(err)
			}
			// A real nullable historical wire must be preserved by the target's named
			// columns despite the migration moving created_by to the last column.
			for _, row := range dump.Tables["credentials"] {
				if row["id"] == "creator-retired" {
					if row["created_by"] != nil {
						t.Error("dump did not preserve NULL retired creator")
					}
					// Continue the target counterexample on the old schema as well;
					// source anonymization failure above still fails the test.
					row["created_by"] = nil
				}
			}
			target := testutil.MigratedSQLDB(t)
			if _, err := target.Exec(`INSERT INTO users(id,email) VALUES('creator-u','creator@example.test')`); err != nil {
				t.Fatal(err)
			}
			retired, live, audit := "creator-retired", "creator-live", "creator-audit"
			if fork {
				if err := RemapIDs(ctx, target, dump); err != nil {
					t.Fatal(err)
				}
				for _, row := range dump.Tables["credentials"] {
					if row["name"] == "RETIRED" {
						retired = row["id"].(string)
					}
					if row["name"] == "LIVE" {
						live = row["id"].(string)
					}
				}
				audit = dump.Tables["credential_audit"][0]["id"].(string)
			}
			if err := RestoreDump(ctx, target, dump); err != nil {
				t.Fatalf("nullable retired creator restore: %v", err)
			}
			var anonymous bool
			var creator, cipher, metadata string
			if err := target.QueryRow(`SELECT created_by IS NULL AND deleted_at='2026-01-01' AND encrypted_value='' FROM credentials WHERE id=?`, retired).Scan(&anonymous); err != nil || !anonymous {
				t.Errorf("retired creator/tuple changed: %v", err)
			}
			if err := target.QueryRow(`SELECT created_by,encrypted_value FROM credentials WHERE id=? AND deleted_at IS NULL`, live).Scan(&creator, &cipher); err != nil || creator != "creator-u" || cipher != "live-cipher" {
				t.Errorf("active tuple changed: %v", err)
			}
			if err := target.QueryRow(`SELECT metadata_json FROM credential_audit WHERE id=? AND credential_id=?`, audit, retired).Scan(&metadata); err != nil || metadata != `{"preserve":true}` {
				t.Errorf("history/identity changed: %v", err)
			}
			rows, err := target.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() {
				t.Error("restore FK violation")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
