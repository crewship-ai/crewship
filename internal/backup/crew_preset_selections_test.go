package backup_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestCrewPresetSelectionsBackupRestoreAndFork(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('crew-client','crew-client@test.invalid'),('crew-old-selector','crew-old-selector@test.invalid'),('crew-unrelated','crew-unrelated@test.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('crew-owner','ws_e2e_1','u_admin','OWNER','trusted'),('crew-client-member','ws_e2e_1','crew-client','VIEWER','restricted')`,
		`INSERT INTO access_grants(id,member_id,resource_kind,crew_id,operation,created_by,created_at) VALUES('crew-view','crew-client-member','crew','c_alpha','view','u_admin','original')`,
	} {
		if _, err := source.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range access.PresetCatalog() {
		if d.Kind != "crew" {
			continue
		}
		managed, _ := json.Marshal(d.ManagedOperations)
		if _, err := source.Exec(`INSERT INTO crew_access_preset_selections(id,workspace_id,member_id,resource_kind,crew_id,preset_id,preset_version,level,definition_hash,managed_operations,selected_by,selected_at) VALUES('crew-choice',?,'crew-client-member','crew','c_alpha',?,?,'view',?,?,'crew-old-selector','original')`, ws, d.ID, d.Version, d.Hash(), string(managed)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.Exec(`INSERT INTO crew_access_preset_selections SELECT 'retired-choice',workspace_id,member_id,resource_kind,'c_beta',preset_id,preset_version,'none',definition_hash,managed_operations,selected_by,selected_at FROM crew_access_preset_selections`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`UPDATE crews SET deleted_at='retired' WHERE id='c_beta'`); err != nil {
		t.Fatal(err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	const passphrase = "synthetic-crew-preset-roundtrip-passphrase"
	created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase})
	if err != nil {
		t.Fatalf("CreateBackup must include crew preset metadata: %v", err)
	}
	target := openMigratedDB(t)
	if _, err := backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := backup.DumpWorkspace(t.Context(), target, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["crew_access_preset_selections"]) != 2 || !reflect.DeepEqual(dump.Tables["crew_access_preset_selections"], restored.Tables["crew_access_preset_selections"]) {
		t.Fatal("crew choice fields/history lost in sealed bundle roundtrip")
	}
	if !reflect.DeepEqual(dump.Tables["access_grants"], restored.Tables["access_grants"]) {
		t.Fatal("canonical grant provenance changed")
	}
	for _, row := range dump.Tables["users"] {
		if row["id"] == "crew-unrelated" {
			t.Fatal("exported unrelated global user")
		}
	}
	if err := backup.RemapIDs(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	restoredStore := access.Store{DB: target}
	if err := restoredStore.Check(t.Context(), "crew-client", ws, access.Right{Kind: "crew", ID: "c_beta", Operation: "view"}); err == nil {
		t.Fatal("historical None label granted retired crew authority")
	}
	choice := dump.Tables["crew_access_preset_selections"][0]
	if choice["workspace_id"] == ws || choice["member_id"] == "crew-client-member" || choice["crew_id"] == "c_alpha" || choice["selected_by"] != "crew-old-selector" || choice["selected_at"] != "original" {
		t.Fatalf("fork lost scope/provenance: %+v", choice)
	}
	if err := backup.RestoreDump(t.Context(), openMigratedDB(t), dump); err != nil {
		t.Fatal(err)
	}
	delete(dump.Tables, "crew_access_preset_selections")
	legacy := openMigratedDB(t)
	if err := backup.RestoreDump(t.Context(), legacy, dump); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := legacy.QueryRow(`SELECT count(*) FROM crew_access_preset_selections`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy restore inferred metadata: %d %v", count, err)
	}
}
