package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (g *folderRig) postCred(t *testing.T, role, body string) (int, credentialResponse) {
	t.Helper()
	rr := httptest.NewRecorder()
	g.cred.Create(rr, g.req("POST", "/api/v1/credentials", role, body))
	var out credentialResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func (g *folderRig) patchCred(t *testing.T, role, id, body string) (int, credentialResponse) {
	t.Helper()
	rr := httptest.NewRecorder()
	r := g.req("PATCH", "/api/v1/credentials/"+id, role, body)
	r.SetPathValue("credentialId", id)
	g.cred.Update(rr, r)
	var out credentialResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestCredentialCreate_FolderAndTierRules(t *testing.T) {
	g := newFolderRig(t)
	plain := g.mustRoot(t, "Plain")
	_, locked := g.create(t, "OWNER", `{"name":"Locked","min_security_level":3,"default_security_level":3}`)

	tests := []struct {
		name      string
		role      string
		body      string
		want      int
		wantFold  string
		wantLevel int
	}{
		{"no folder", "OWNER", `{"name":"a","value":"v","type":"SECRET","provider":"GITHUB"}`, 201, "", 1},
		{"in folder", "MANAGER", `{"name":"b","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"` + plain + `"}`, 201, plain, 1},
		{"unknown folder", "OWNER", `{"name":"c","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"nope"}`, 400, "", 0},
		{"folder default tier applied", "ADMIN", `{"name":"d","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"` + locked.ID + `"}`, 201, locked.ID, 3},
		{"below folder floor", "ADMIN", `{"name":"e","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"` + locked.ID + `","security_level":2}`, 400, "", 0},
		{"at folder floor", "ADMIN", `{"name":"f","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"` + locked.ID + `","security_level":4}`, 201, locked.ID, 4},
		{"manager cannot file into a policy folder", "MANAGER", `{"name":"g","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"` + locked.ID + `","security_level":3}`, 403, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, c := g.postCred(t, tc.role, tc.body)
			if code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
			if code != 201 {
				return
			}
			got := ""
			if c.FolderID != nil {
				got = *c.FolderID
			}
			if got != tc.wantFold {
				t.Errorf("folder_id = %q, want %q", got, tc.wantFold)
			}
			if c.SecurityLevel != tc.wantLevel {
				t.Errorf("security_level = %d, want %d", c.SecurityLevel, tc.wantLevel)
			}
		})
	}
}

func TestCredentialUpdate_MoveBetweenFolders(t *testing.T) {
	g := newFolderRig(t)
	plain := g.mustRoot(t, "Plain")
	other := g.mustRoot(t, "Other")
	_, sealed := g.create(t, "OWNER", `{"name":"Sealed","reveal_policy":"nobody"}`)
	g.seedCred(t, "c1", "tok", plain)

	if code, c := g.patchCred(t, "MANAGER", "c1", `{"folder_id":"`+other+`"}`); code != 200 || c.FolderID == nil || *c.FolderID != other {
		t.Fatalf("move = %d %+v", code, c.FolderID)
	}
	if code, _ := g.patchCred(t, "MANAGER", "c1", `{"folder_id":"missing"}`); code != 400 {
		t.Fatalf("move to unknown folder = %d, want 400", code)
	}
	// Moving INTO a sealed folder or OUT of one changes who can reveal it.
	if code, _ := g.patchCred(t, "MANAGER", "c1", `{"folder_id":"`+sealed.ID+`"}`); code != 403 {
		t.Fatalf("manager move into policy folder = %d, want 403", code)
	}
	if code, _ := g.patchCred(t, "ADMIN", "c1", `{"folder_id":"`+sealed.ID+`"}`); code != 200 {
		t.Fatalf("admin move into policy folder = %d, want 200", code)
	}
	if code, _ := g.patchCred(t, "MANAGER", "c1", `{"folder_id":null}`); code != 403 {
		t.Fatalf("manager move out of policy folder = %d, want 403", code)
	}
	if code, c := g.patchCred(t, "ADMIN", "c1", `{"folder_id":null}`); code != 200 || c.FolderID != nil {
		t.Fatalf("admin unfile = %d %v", code, c.FolderID)
	}

	// Each move is on the credential's timeline, without any value.
	var n int
	if err := g.cred.db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id='c1' AND event_type='MOVED'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("MOVED audit events = %d, want 3", n)
	}
}

func TestCredentialUpdate_TierFloorOfFolder(t *testing.T) {
	g := newFolderRig(t)
	_, locked := g.create(t, "OWNER", `{"name":"Locked","min_security_level":3}`)
	g.seedCred(t, "c1", "tok", locked.ID)
	if code, _ := g.patchCred(t, "ADMIN", "c1", `{"security_level":2}`); code != 400 {
		t.Fatalf("lower below floor = %d, want 400", code)
	}
	if code, _ := g.patchCred(t, "ADMIN", "c1", `{"security_level":4}`); code != 200 {
		t.Fatalf("raise = %d, want 200", code)
	}
}

func TestCredentialList_FolderFilterAndReplaceDue(t *testing.T) {
	g := newFolderRig(t)
	_, f := g.create(t, "OWNER", `{"name":"Rotating","remind_after_days":30}`)
	g.seedCred(t, "old", "old-key", f.ID)
	g.seedCred(t, "fresh", "fresh-key", f.ID)
	g.seedCred(t, "free", "free-key", "")
	db := g.cred.db
	db.Exec(`UPDATE credentials SET created_at = datetime('now','-90 days') WHERE id='old'`)
	// A rotation resets the clock.
	db.Exec(`UPDATE credentials SET created_at = datetime('now','-90 days') WHERE id='fresh'`)
	db.Exec(`INSERT INTO credential_audit (id, credential_id, event_type, occurred_at) VALUES ('a1','fresh','ROTATE', datetime('now','-2 days'))`)

	list := func(q string) []credentialResponse {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/credentials?"+q, bytes.NewReader(nil))
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: g.uid}), g.ws, "OWNER"))
		g.cred.List(rr, req)
		var out []credentialResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, rr.Body.String())
		}
		return out
	}
	if got := list("folder_id=" + f.ID); len(got) != 2 {
		t.Fatalf("folder filter = %d rows, want 2", len(got))
	}
	if got := list("folder_id=none"); len(got) != 1 || got[0].ID != "free" {
		t.Fatalf("unfiled filter = %+v", got)
	}
	due := map[string]bool{}
	for _, c := range list("") {
		due[c.ID] = c.ReplaceDue
	}
	if !due["old"] || due["fresh"] || due["free"] {
		t.Fatalf("replace_due = %v, want only old", due)
	}
}

func TestReveal_FolderPolicyOnlyTightens(t *testing.T) {
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-f", true)
	r.seedMember(t, "ws-f", "u-owner", "OWNER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedMember(t, "ws-f", "u-admin", "ADMIN", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedMember(t, "ws-f", "u-mgr", "MANAGER", []string{CapabilityChat, CapabilityCredentialReveal})
	for _, id := range []string{"cred-inherit", "cred-admins", "cred-nobody"} {
		r.seedCredential(t, "ws-f", "u-owner", id, strings.ToUpper(id), "secret-"+id, SensitivityStandard)
	}
	for id, pol := range map[string]string{"f-i": "inherit", "f-a": "admins", "f-n": "nobody"} {
		if _, err := r.db.Exec(`INSERT INTO credential_folders (id, workspace_id, name, reveal_policy, created_at, updated_at) VALUES (?, 'ws-f', ?, ?, datetime('now'), datetime('now'))`, id, id, pol); err != nil {
			t.Fatal(err)
		}
	}
	for c, f := range map[string]string{"cred-inherit": "f-i", "cred-admins": "f-a", "cred-nobody": "f-n"} {
		if _, err := r.db.Exec(`UPDATE credentials SET folder_id=? WHERE id=?`, f, c); err != nil {
			t.Fatal(err)
		}
	}
	// MANAGER reveal needs crew scope; workspace-scoped rows are in scope.
	tests := []struct {
		cred, user, role string
		want             int
	}{
		{"cred-inherit", "u-mgr", "MANAGER", http.StatusOK},
		{"cred-admins", "u-mgr", "MANAGER", http.StatusForbidden},
		{"cred-admins", "u-admin", "ADMIN", http.StatusOK},
		{"cred-admins", "u-owner", "OWNER", http.StatusOK},
		{"cred-nobody", "u-admin", "ADMIN", http.StatusForbidden},
		{"cred-nobody", "u-owner", "OWNER", http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.cred+"/"+tc.role, func(t *testing.T) {
			before := len(r.j.all())
			rec := r.doReveal(r.revealReq(tc.cred, "ws-f", tc.user, tc.role, validRevealReason))
			if rec.Code != tc.want {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.want == http.StatusForbidden {
				if bodyMentions(rec, "secret-"+tc.cred) {
					t.Fatal("refused reveal leaked the value")
				}
				if len(r.j.all()) != before {
					t.Fatal("a refused reveal must not write a chained reveal entry")
				}
			}
		})
	}
}

// A subfolder is never looser than the folder it sits in.
func TestCredentialFolders_SubfolderInheritsParentPolicy(t *testing.T) {
	g := newFolderRig(t)
	_, parent := g.create(t, "OWNER", `{"name":"Prod","reveal_policy":"admins","min_security_level":3}`)
	_, child := g.create(t, "OWNER", `{"name":"Pay","parent_id":"`+parent.ID+`"}`)

	// Tier floor and default come from the parent.
	if code, c := g.postCred(t, "ADMIN", `{"name":"k1","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"`+child.ID+`"}`); code != 201 || c.SecurityLevel != 3 {
		t.Fatalf("create in child = %d level %d, want 201 at the parent's floor L3", code, c.SecurityLevel)
	}
	if code, _ := g.postCred(t, "ADMIN", `{"name":"k2","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"`+child.ID+`","security_level":2}`); code != 400 {
		t.Fatalf("below the parent's floor = %d, want 400", code)
	}
	// Filing a manager's secret into the child is an access decision too.
	if code, _ := g.postCred(t, "MANAGER", `{"name":"k3","value":"v","type":"SECRET","provider":"GITHUB","folder_id":"`+child.ID+`","security_level":3}`); code != 403 {
		t.Fatalf("manager into child of a policy folder = %d, want 403", code)
	}

	// Reveal: the child's own policy says inherit, the parent's says admins.
	r := newRevealRig(t)
	r.seedWorkspace(t, "ws-i", true)
	r.seedMember(t, "ws-i", "u-mgr", "MANAGER", []string{CapabilityChat, CapabilityCredentialReveal})
	r.seedCredential(t, "ws-i", "u-mgr", "cred-1", "C1", "secret-c1", SensitivityStandard)
	for _, q := range []string{
		`INSERT INTO credential_folders (id, workspace_id, name, reveal_policy, created_at, updated_at) VALUES ('p','ws-i','P','admins',datetime('now'),datetime('now'))`,
		`INSERT INTO credential_folders (id, workspace_id, parent_id, name, created_at, updated_at) VALUES ('c','ws-i','p','C',datetime('now'),datetime('now'))`,
		`UPDATE credentials SET folder_id='c' WHERE id='cred-1'`,
	} {
		if _, err := r.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rec := r.doReveal(r.revealReq("cred-1", "ws-i", "u-mgr", "MANAGER", validRevealReason))
	if rec.Code != http.StatusForbidden || bodyMentions(rec, "secret-c1") {
		t.Fatalf("reveal in a child of an admins-only folder = %d %s, want 403 without the value", rec.Code, rec.Body.String())
	}
}
