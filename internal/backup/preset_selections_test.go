package backup_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestPresetSelectionsBackupHistoryAndForkWithoutAuthority(t *testing.T) {
	db := openMigratedDB(t)
	workspace := seedWorkspace(t, db)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('preset-client','preset-client@test.invalid'),('old-selector','old-selector@test.invalid'),('inert-issuer','inert-issuer@test.invalid'),('unrelated-user','unrelated-user@test.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('preset-owner','ws_e2e_1','u_admin','OWNER','trusted'),('preset-client-member','ws_e2e_1','preset-client','MEMBER','restricted'),('inert-member','ws_e2e_1','inert-issuer','VIEWER','trusted')`,
		`INSERT INTO projects(id,workspace_id,name,slug) VALUES('preset-project','ws_e2e_1','Project','preset-project')`,
		`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('preset-routine','ws_e2e_1','Routine','preset-routine','{}','hash','active')`,
		`INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES('preset-list','preset-client-member','project','preset-project','list','u_admin','original'),('preset-read','preset-client-member','project','preset-project','read','inert-issuer','original')`,
		`INSERT INTO access_grants(id,member_id,resource_kind,routine_id,operation,created_by,created_at) VALUES('preset-view','preset-client-member','routine','preset-routine','view','u_admin','original'),('preset-results','preset-client-member','routine','preset-routine','results','u_admin','original')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range access.PresetCatalog() {
		managed, _ := json.Marshal(d.ManagedOperations)
		var project, routine any
		if d.Kind == "project" {
			project = "preset-project"
		} else {
			routine = "preset-routine"
		}
		if _, err := db.Exec(`INSERT INTO access_preset_selections(id,workspace_id,member_id,resource_kind,project_id,routine_id,preset_id,preset_version,level,definition_hash,managed_operations,selected_by,selected_at) VALUES(?,'ws_e2e_1','preset-client-member',?,?,?,?,1,'view',?,?,'old-selector','original')`, d.ID, d.Kind, project, routine, d.ID, d.Hash(), string(managed)); err != nil {
			t.Fatal(err)
		}
	}
	dump, err := backup.DumpWorkspace(t.Context(), db, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["access_preset_selections"]) != 2 {
		t.Fatal("explicit choice history lost")
	}
	found := false
	for _, user := range dump.Tables["users"] {
		if user["id"] == "old-selector" {
			found = true
		}
		if user["id"] == "unrelated-user" {
			t.Fatal("unrelated global user exported")
		}
	}
	if !found {
		t.Fatal("referenced selector removed from roster was not exported")
	}
	target := openMigratedDB(t)
	if err := backup.RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	store := access.Store{DB: target}
	if err := store.Check(t.Context(), "preset-client", workspace, access.Right{Kind: "project", ID: "preset-project", Operation: "read"}); !errors.Is(err, access.ErrIssuerDenied) {
		t.Fatalf("informational choice revived inert issuer: %v", err)
	}
	var issuer, created string
	if err := target.QueryRow(`SELECT created_by,created_at FROM access_grants WHERE id='preset-read'`).Scan(&issuer, &created); err != nil || issuer != "inert-issuer" || created != "original" {
		t.Fatalf("canonical provenance changed: %s %s %v", issuer, created, err)
	}
	if len(dump.Tables["access_attempts"]) != 0 {
		t.Fatal("execution authority included")
	}
	if err := backup.RemapIDs(t.Context(), db, dump); err != nil {
		t.Fatal(err)
	}
	for _, row := range dump.Tables["access_preset_selections"] {
		if row["member_id"] == "preset-client-member" || row["project_id"] == "preset-project" || row["routine_id"] == "preset-routine" {
			t.Fatal("fork retained source target")
		}
		if row["selected_by"] != "old-selector" || row["selected_at"] != "original" {
			t.Fatal("fork lost historical selector")
		}
	}
	if err := backup.RestoreDump(t.Context(), openMigratedDB(t), dump); err != nil {
		t.Fatal(err)
	}
	// Older bundles carry canonical grants but no saved choice. Never infer it.
	delete(dump.Tables, "access_preset_selections")
	legacyTarget := openMigratedDB(t)
	if err := backup.RestoreDump(t.Context(), legacyTarget, dump); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := legacyTarget.QueryRow(`SELECT COUNT(*) FROM access_preset_selections`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy restore backfilled choice: %d %v", count, err)
	}
}
