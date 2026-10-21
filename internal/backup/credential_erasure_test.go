package backup

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestCredentialErasureHistoricalRestore(t *testing.T) {
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "fork"}[fork], func(t *testing.T) {
			ctx := context.Background()
			source := testutil.MigratedSQLDB(t)
			for _, q := range []string{
				`INSERT INTO users(id,email) VALUES('erase-u','fixture@example.test')`,
				`INSERT INTO workspaces(id,name,slug) VALUES('erase-w','Erase','erase')`,
				`INSERT INTO credentials(id,workspace_id,name,encrypted_value,encrypted_refresh_token,oauth_client_secret_enc,oauth_refresh_token_enc,username,account_email,created_by,deleted_at) VALUES('erase-old','erase-w','OLD','old-cipher','refresh','client','oauth-refresh','login','person@example.test','erase-u','2026-01-01')`,
				`INSERT INTO credentials(id,workspace_id,name,encrypted_value,created_by) VALUES('erase-live','erase-w','LIVE','live-cipher','erase-u')`,
				`INSERT INTO credential_fields(credential_id,key,encrypted_value,is_secret) VALUES('erase-old','secret','custom-cipher',1)`,
				`INSERT INTO credential_audit(id,credential_id,workspace_id,event_type,metadata_json) VALUES('erase-a','erase-old','erase-w','REVOKE','{"original":true}')`,
			} {
				if _, err := source.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			dump, err := DumpWorkspace(ctx, source, "erase-w")
			if err != nil {
				t.Fatal(err)
			}
			// This synthetic historical wire must contain the old secret, regardless of
			// whether a newer database also scrubs direct tombstone inserts.
			for _, row := range dump.Tables["credentials"] {
				if row["id"] == "erase-old" {
					row["encrypted_value"] = "old-cipher"
					row["encrypted_refresh_token"] = "refresh"
					row["oauth_client_secret_enc"] = "client"
					row["oauth_refresh_token_enc"] = "oauth-refresh"
					row["username"] = "login"
					row["account_email"] = "person@example.test"
				}
			}
			dump.Tables["credential_fields"] = []map[string]any{{"credential_id": "erase-old", "key": "secret", "encrypted_value": "custom-cipher", "is_secret": 1}}
			target := testutil.MigratedSQLDB(t)
			if _, err := target.Exec(`INSERT INTO users(id,email) VALUES('erase-u','fixture@example.test')`); err != nil {
				t.Fatal(err)
			}
			oldID, liveID, auditID := "erase-old", "erase-live", "erase-a"
			if fork {
				if err := RemapIDs(ctx, target, dump); err != nil {
					t.Fatal(err)
				}
				for _, row := range dump.Tables["credentials"] {
					if row["name"] == "OLD" {
						oldID = row["id"].(string)
					}
					if row["name"] == "LIVE" {
						liveID = row["id"].(string)
					}
				}
				auditID = dump.Tables["credential_audit"][0]["id"].(string)
			}
			if err := RestoreDump(ctx, target, dump); err != nil {
				t.Fatal(err)
			}
			var erased bool
			var live, metadata string
			var fields int
			if err := target.QueryRow(`SELECT deleted_at IS NOT NULL AND encrypted_value='' AND COALESCE(encrypted_refresh_token,'')='' AND COALESCE(oauth_client_secret_enc,'')='' AND COALESCE(oauth_refresh_token_enc,'')='' AND COALESCE(username,'')='' AND COALESCE(account_email,'')='' FROM credentials WHERE id=?`, oldID).Scan(&erased); err != nil {
				t.Fatal(err)
			}
			if !erased {
				t.Error("historical restore resurrected retired secret or personal material")
			}
			if err := target.QueryRow(`SELECT encrypted_value FROM credentials WHERE id=? AND deleted_at IS NULL`, liveID).Scan(&live); err != nil || live != "live-cipher" {
				t.Fatalf("active credential changed: %v", err)
			}
			if err := target.QueryRow(`SELECT metadata_json FROM credential_audit WHERE id=? AND credential_id=?`, auditID, oldID).Scan(&metadata); err != nil || metadata != `{"original":true}` {
				t.Fatalf("historical identity/audit changed: %v", err)
			}
			if err := target.QueryRow(`SELECT count(*) FROM credential_fields WHERE credential_id=?`, oldID).Scan(&fields); err != nil || fields != 0 {
				t.Errorf("historical restore retained custom fields: %d %v", fields, err)
			}
			rows, err := target.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() {
				t.Fatal("restore FK violation")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
