package backup_test

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestResourceAuthorityBackupPreservesGrantsWithoutAttempts(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	for _, query := range []string{
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('access-owner','ws_e2e_1','u_admin','OWNER')`,
		`INSERT INTO users(id,email) VALUES('access-client','access-client@backup.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('access-member','ws_e2e_1','access-client','MEMBER')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('access-chat','ws_e2e_1','a_alice','access-client','private')`,
	} {
		if _, err := source.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	store := access.Store{DB: source}
	member, err := store.Membership(t.Context(), "access-client", workspace)
	if err != nil {
		t.Fatal(err)
	}
	right := access.Right{Kind: "agent", ID: "a_alice", Operation: "run"}
	if _, err = store.Replace(t.Context(), "u_admin", "access-client", workspace, "restricted", member, []access.Right{right}); err != nil {
		t.Fatal(err)
	}
	handle, _, err := store.Admit(t.Context(), "access-client", workspace, "a_alice", "access-chat", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["access_grants"]) != 1 || len(dump.Tables["access_attempts"]) != 0 {
		t.Fatal("grant/attempt backup classification is wrong")
	}
	target := openMigratedDB(t)
	if err = backup.RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	restored := access.Store{DB: target}
	if err = restored.Check(t.Context(), "access-client", workspace, right); err != nil {
		t.Fatalf("grant lost: %v", err)
	}
	if err = restored.Check(t.Context(), "access-client", workspace, access.Right{Kind: "agent", ID: "a_bob", Operation: "run"}); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("foreign grant widened: %v", err)
	}
	if _, err = restored.Resolve(t.Context(), handle); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("attempt restored: %v", err)
	}
	// Forking must remap the typed agent FK rather than point to the original.
	if err = backup.RemapIDs(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	if dump.Tables["access_grants"][0]["agent_id"] == "a_alice" {
		t.Fatal("fork retained source resource ID")
	}
	if err = backup.RestoreDump(t.Context(), openMigratedDB(t), dump); err != nil {
		t.Fatal(err)
	}
}
