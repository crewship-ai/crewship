package backup_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestProviderPoolCreatorOnly_RoundTrip(t *testing.T) {
	ctx := t.Context()
	source := testutil.MigratedSQLDB(t)
	_, err := source.ExecContext(ctx, `
		INSERT INTO users(id,email) VALUES ('pool-only-user','pool-only@example.test'), ('foreign-pool-user','foreign-pool@example.test');
		INSERT INTO workspaces(id,name,slug) VALUES ('pool-ws','Pool','pool-ws'), ('foreign-pool-ws','Foreign','foreign-pool-ws');
		INSERT INTO provider_login_pools(id,workspace_id,name,provider,mode,created_by) VALUES
		  ('pool','pool-ws','Pool','OPENAI','subscription','pool-only-user'),
		  ('foreign-pool','foreign-pool-ws','Foreign','OPENAI','subscription','foreign-pool-user');
	`)
	if err != nil {
		t.Fatal(err)
	}
	// A pool can survive membership removal or removal of its last credential.
	// Its creator is global and has no crew/chat/skill/workspace membership.
	dump, err := backup.DumpWorkspace(ctx, source, "pool-ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["users"]) != 1 || dump.Tables["users"][0]["id"] != "pool-only-user" {
		t.Fatalf("pool-only creator missing or foreign creator leaked: %+v", dump.Tables["users"])
	}
	target := testutil.MigratedSQLDB(t)
	if err := backup.RestoreDump(ctx, target, dump); err != nil {
		t.Fatalf("fresh-target restore: %v", err)
	}
	var creator string
	if err := target.QueryRowContext(ctx, `SELECT created_by FROM provider_login_pools WHERE id = 'pool'`).Scan(&creator); err != nil {
		t.Fatal(err)
	}
	if creator != "pool-only-user" {
		t.Fatalf("restored creator = %q", creator)
	}
	var count int
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = 'foreign-pool-user'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("foreign workspace creator restored")
	}
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("restore left dangling foreign keys")
	}
}

func TestProviderPoolCreatorScope_OlderSchemaKeepsExistingUsers(t *testing.T) {
	ctx := t.Context()
	source := testutil.MigratedSQLDB(t)
	_, err := source.ExecContext(ctx, `
		DROP TRIGGER trg_provider_login_pool_member_workspace_insert;
		DROP TRIGGER trg_provider_login_pool_member_workspace_update;
		DROP TRIGGER trg_provider_login_pool_workspace_update;
		DROP TRIGGER trg_provider_login_pool_credential_workspace_update;
		DROP TABLE provider_login_availability;
		DROP TABLE provider_login_pool_members;
		DROP TABLE provider_login_pools;
		INSERT INTO users(id,email) VALUES ('legacy-pool-user','legacy-pool@example.test');
		INSERT INTO workspaces(id,name,slug) VALUES ('legacy-pool-ws','Legacy','legacy-pool-ws');
		INSERT INTO crews(id,workspace_id,name,slug) VALUES ('legacy-pool-crew','legacy-pool-ws','Legacy','legacy-pool-crew');
		INSERT INTO crew_members(id,crew_id,user_id) VALUES ('legacy-pool-member','legacy-pool-crew','legacy-pool-user');
	`)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, source, "legacy-pool-ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["users"]) != 1 || dump.Tables["users"][0]["id"] != "legacy-pool-user" {
		t.Fatalf("optional pool scope dropped legacy users: %+v", dump.Tables["users"])
	}
}
