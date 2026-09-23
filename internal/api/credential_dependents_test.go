package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestCredentialDependentsVisibilityAndSelection(t *testing.T) {
	rig := newFieldRig(t)
	definition := `{"dsl_version":"1.0","name":"Needs login","credentials_required":[{"type":"userpass"}],"steps":[]}`
	for _, p := range []struct {
		id, slug string
		visible  int
	}{
		{"p-visible", "visible-routine", 1}, {"p-hidden", "hidden-routine", 0},
	} {
		if _, err := rig.db.Exec(`INSERT INTO pipelines
			(id,workspace_id,slug,name,definition_json,definition_hash,dsl_version,head_version,workspace_visible,created_at,updated_at)
			VALUES (?,?,?,?,?,'hash','1.0',1,?,datetime('now'),datetime('now'))`,
			p.id, rig.wsID, p.slug, p.slug, definition, p.visible); err != nil {
			t.Fatal(err)
		}
	}
	read := func(role string) (int, struct {
		Routines          []credentialRoutineDependent `json:"routines"`
		VisibilityLimited bool                         `json:"visibility_limited"`
		RecordedUse       string                       `json:"recorded_use"`
	}) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/credentials/"+rig.credID+"/dependents", nil)
		req.SetPathValue("credentialId", rig.credID)
		req = withWorkspaceUser(req, rig.userID, rig.wsID, role)
		rec := httptest.NewRecorder()
		rig.creds.Dependents(rec, req)
		var result struct {
			Routines          []credentialRoutineDependent `json:"routines"`
			VisibilityLimited bool                         `json:"visibility_limited"`
			RecordedUse       string                       `json:"recorded_use"`
		}
		if rec.Code == 200 && json.Unmarshal(rec.Body.Bytes(), &result) != nil {
			t.Fatalf("invalid response: %s", rec.Body.String())
		}
		return rec.Code, result
	}
	if status, result := read("VIEWER"); status != 200 || len(result.Routines) != 1 || result.Routines[0].Slug != "visible-routine" || !result.VisibilityLimited {
		t.Fatalf("viewer: status=%d result=%+v", status, result)
	}
	if status, result := read("OWNER"); status != 200 || len(result.Routines) != 2 || result.Routines[0].Resolution != "would_resolve" || result.RecordedUse != "not_attributed" {
		t.Fatalf("owner: status=%d result=%+v", status, result)
	}
	if _, err := rig.db.Exec(`INSERT INTO credentials
		(id,workspace_id,name,encrypted_value,type,provider,scope,status,created_by,created_at,updated_at)
		VALUES ('cred-new',?,?,?,?, 'NONE','WORKSPACE','ACTIVE',?,datetime('now','+1 day'),datetime('now'))`,
		rig.wsID, "newer", "encrypted-unused", "USERPASS", rig.userID); err != nil {
		t.Fatal(err)
	}
	if status, result := read("OWNER"); status != 200 || result.Routines[0].Resolution != "another_credential" {
		t.Fatalf("newer candidate: status=%d result=%+v", status, result)
	}
	rig.seedCredential(t, "crew-hidden", rig.wsID, "crew-only", "CREW")
	hiddenReq := httptest.NewRequest("GET", "/api/v1/credentials/crew-hidden/dependents", nil)
	hiddenReq.SetPathValue("credentialId", "crew-hidden")
	hiddenReq = withWorkspaceUser(hiddenReq, rig.userID, rig.wsID, "VIEWER")
	hiddenRec := httptest.NewRecorder()
	rig.creds.Dependents(hiddenRec, hiddenReq)
	if hiddenRec.Code != 404 || len(hiddenRec.Body.Bytes()) == 0 {
		t.Fatalf("viewer must not enumerate hidden credential: %d %s", hiddenRec.Code, hiddenRec.Body.String())
	}
	if _, err := rig.db.Exec(`UPDATE credentials SET deleted_at=datetime('now') WHERE id=?`, rig.credID); err != nil {
		t.Fatal(err)
	}
	if status, _ := read("OWNER"); status != 404 {
		t.Fatalf("deleted credential status=%d, want 404", status)
	}
}
