package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestPresetMetadataGETAndManualPUTForgery(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('choice-user','choice@test.invalid')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('choice-member',?,'choice-user','MEMBER','restricted')`, ws)
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('choice-project',?,'Choice','choice-project')`, ws)
	store := access.Store{DB: db}
	m, err := store.Membership(t.Context(), "choice-user", ws)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Replace(t.Context(), owner, "choice-user", ws, "restricted", m, []access.Right{{Kind: "project", ID: "choice-project", Operation: "list"}, {Kind: "project", ID: "choice-project", Operation: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := access.ResolvePreset("project-levels", 1)
	managed, _ := json.Marshal(definition.ManagedOperations)
	execOrFatal(t, db, `INSERT INTO access_preset_selections(id,workspace_id,member_id,resource_kind,project_id,preset_id,preset_version,level,definition_hash,managed_operations,selected_by,selected_at) VALUES('legitimate',?,'choice-member','project','choice-project','project-levels',1,'view',?,?,?,'original')`, ws, definition.Hash(), string(managed), owner)
	credential := "crewship_cli_preset-metadata-synthetic"
	execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes) VALUES('choice-token',?,'Synthetic metadata',?,'["workspace:admin"]')`, owner, sha256Hex(credential))
	router, err := NewRouter(db, "preset-metadata-synthetic-secret32chars", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	path := "/api/v1/workspaces/" + ws + "/members/choice-member/access"
	do := func(method, body string) map[string]json.RawMessage {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("X-Workspace-ID", ws)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s:%d %s", method, w.Code, w.Body.String())
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := do("GET", "")
	var choices []access.PresetSelection
	if err := json.Unmarshal(before["preset_selections"], &choices); err != nil || len(choices) != 1 || choices[0].ID != "legitimate" {
		t.Fatalf("GET lacks canonical informational metadata:%v %v", before, err)
	}
	before["preset_selections"] = json.RawMessage(`[{"id":"forged","kind":"project","resource_id":"other-project","preset_id":"project-levels","version":1,"level":"manage","definition_hash":"forged","selected_by":"forged-actor","selected_at":"future","state":"selected"}]`)
	body, _ := json.Marshal(before)
	after := do("PUT", string(body))
	choices = nil
	if err := json.Unmarshal(after["preset_selections"], &choices); err != nil || len(choices) != 1 || choices[0].ID != "legitimate" || choices[0].Level != "view" || choices[0].SelectedBy == nil || *choices[0].SelectedBy != owner {
		t.Fatalf("PUT trusted forged informational metadata:%v %v", after, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM access_preset_selections`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("forged choice persisted:%d %v", count, err)
	}
	// Canonical managed-grant removal invalidates the choice; submitted stale
	// metadata cannot restore it and unselected bits do not fabricate a preset.
	after["rights"] = json.RawMessage(`[{"kind":"project","id":"choice-project","operation":"list"}]`)
	body, _ = json.Marshal(after)
	result := do("PUT", string(body))
	if string(result["preset_selections"]) != "[]" {
		t.Fatalf("stale choice restored after manual edit:%v", result)
	}
}
