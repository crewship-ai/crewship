package api

import (
	"github.com/crewship-ai/crewship/internal/backup"
	"testing"
)

func TestArtifactCreationPolicyEncryptedForkKeepsWorkspaceAndCrewScope(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	enableArtifactPolicyForTest(t, h, ws, user, "crew-lookout")
	const pass = "artifact-policy-encrypted-backup-password"
	made, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}})
	if err != nil {
		t.Fatal(err)
	}
	db := setupTestDB(t)
	result, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: made.Path, Passphrase: pass, AsWorkspace: "policy-fork", Actor: backup.Actor{UserID: user, Role: "ADMIN"}})
	if err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var workspace, crew string
	if err := db.QueryRow(`SELECT p.enabled,a.workspace_id,a.crew_id FROM workspace_artifact_creation_policy p JOIN workspace_artifact_creation_crews a ON a.workspace_id=p.workspace_id WHERE p.workspace_id=?`, result.RestoredWorkspaceID).Scan(&enabled, &workspace, &crew); err != nil {
		t.Fatal(err)
	}
	if !enabled || workspace == ws || crew == "crew-lookout" {
		t.Fatalf("policy identities not forked: %t %q %q", enabled, workspace, crew)
	}
	var crewWS string
	if err := db.QueryRow(`SELECT workspace_id FROM crews WHERE id=?`, crew).Scan(&crewWS); err != nil || crewWS != workspace {
		t.Fatalf("allowlist escaped scope %q %v", crewWS, err)
	}
	var authority int
	_ = db.QueryRow(`SELECT count(*) FROM artifact_creation_authorizations`).Scan(&authority)
	if authority != 0 {
		t.Fatal("policy restore granted live run authority")
	}
}
