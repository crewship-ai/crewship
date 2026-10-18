package backup_test

import (
	"github.com/crewship-ai/crewship/internal/backup"
	"testing"
)

// Policy is durable configuration; restoring it must remap both the workspace
// and its allowed crews without carrying any live run authority.
func TestArtifactCreationPolicyEncryptedFork(t *testing.T) {
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)
	if _, err := db.Exec(`INSERT INTO workspace_artifact_creation_policy(workspace_id, enabled, version, updated_by_user_id, updated_at) VALUES (?,1,7,'u_admin','2026-10-16T12:00:00Z')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES (?,'c_alpha')`, ws); err != nil {
		t.Fatal(err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	const passphrase = "synthetic-policy-fork-passphrase"
	created, err := backup.CreateBackup(t.Context(), db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: created.Path, Passphrase: passphrase, AsWorkspace: "policy-fork", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	if restored.RestoredWorkspaceID == "" || restored.RestoredWorkspaceID == ws {
		t.Fatal("workspace was not forked")
	}
	for _, id := range []string{ws, restored.RestoredWorkspaceID} {
		var enabled, version int
		var editor, updated string
		if err := db.QueryRow(`SELECT enabled,version,updated_by_user_id,updated_at FROM workspace_artifact_creation_policy WHERE workspace_id=?`, id).Scan(&enabled, &version, &editor, &updated); err != nil {
			t.Fatalf("policy missing after fork: %v", err)
		}
		if enabled != 1 || version != 7 || editor != "u_admin" || updated != "2026-10-16T12:00:00Z" {
			t.Fatalf("policy changed: %d/%d/%s/%s", enabled, version, editor, updated)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM workspace_artifact_creation_crews p JOIN crews c ON c.id=p.crew_id AND c.workspace_id=p.workspace_id WHERE p.workspace_id=? AND c.slug='alpha'`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("allowlist was lost or remapped incorrectly: %d", count)
		}
	}
}
