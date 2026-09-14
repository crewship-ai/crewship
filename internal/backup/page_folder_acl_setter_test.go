package backup_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A folder's sharing outlives the manager who set it (page_folder_acl.set_by_user_id
// is audit, ON DELETE SET NULL), so the setter may well be referenced by
// nothing else in the workspace — an admin in no crew who never chatted.
// The users scope must still carry them, or the ACL row's FK fails on
// restore and the folder comes back unshared.
func TestPageFolderACLSetterOnly_RoundTrip(t *testing.T) {
	ctx := t.Context()
	source := testutil.MigratedSQLDB(t)
	_, err := source.ExecContext(ctx, `
		INSERT INTO users(id,email) VALUES ('acl-setter','acl-setter@example.test'), ('foreign-setter','foreign-setter@example.test');
		INSERT INTO workspaces(id,name,slug) VALUES ('acl-ws','ACL','acl-ws'), ('foreign-acl-ws','Foreign','foreign-acl-ws');
		INSERT INTO crews(id,workspace_id,name,slug) VALUES ('acl-crew','acl-ws','Engine','engine'), ('foreign-crew','foreign-acl-ws','Foreign','foreign');
		INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES
		  ('acl-folder','acl-ws','engine-ops','Engine ops','acl-crew','2026-09-14T00:00:00Z','2026-09-14T00:00:00Z'),
		  ('foreign-folder','foreign-acl-ws','foreign-ops','Foreign ops','foreign-crew','2026-09-14T00:00:00Z','2026-09-14T00:00:00Z');
		INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_write,set_by_user_id,set_at) VALUES
		  ('acl-folder','workspace','',0,'acl-setter','2026-09-14T00:00:00Z'),
		  ('foreign-folder','workspace','',0,'foreign-setter','2026-09-14T00:00:00Z');
	`)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, source, "acl-ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["users"]) != 1 || dump.Tables["users"][0]["id"] != "acl-setter" {
		t.Fatalf("ACL setter missing or foreign setter leaked: %+v", dump.Tables["users"])
	}
	target := testutil.MigratedSQLDB(t)
	if err := backup.RestoreDump(ctx, target, dump); err != nil {
		t.Fatalf("fresh-target restore: %v", err)
	}
	var setter string
	if err := target.QueryRowContext(ctx, `SELECT set_by_user_id FROM page_folder_acl WHERE folder_id = 'acl-folder'`).Scan(&setter); err != nil {
		t.Fatalf("restored ACL row: %v", err)
	}
	if setter != "acl-setter" {
		t.Fatalf("restored setter = %q", setter)
	}
	var count int
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = 'foreign-setter'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("foreign workspace setter restored")
	}
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("restore left dangling foreign keys")
	}
}

// A database from before folders had an ACL dumps as it always did: the
// probe skips the absent table instead of failing the users scope.
func TestPageFolderACLSetterScope_OlderSchemaKeepsExistingUsers(t *testing.T) {
	ctx := t.Context()
	source := testutil.MigratedSQLDB(t)
	_, err := source.ExecContext(ctx, `
		DROP TRIGGER trg_page_folder_acl_user_deleted;
		DROP TABLE page_folder_acl;
		INSERT INTO users(id,email) VALUES ('legacy-acl-user','legacy-acl@example.test');
		INSERT INTO workspaces(id,name,slug) VALUES ('legacy-acl-ws','Legacy','legacy-acl-ws');
		INSERT INTO crews(id,workspace_id,name,slug) VALUES ('legacy-acl-crew','legacy-acl-ws','Legacy','legacy-acl-crew');
		INSERT INTO crew_members(id,crew_id,user_id) VALUES ('legacy-acl-member','legacy-acl-crew','legacy-acl-user');
	`)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(ctx, source, "legacy-acl-ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["users"]) != 1 || dump.Tables["users"][0]["id"] != "legacy-acl-user" {
		t.Fatalf("optional ACL setter scope dropped legacy users: %+v", dump.Tables["users"])
	}
}
