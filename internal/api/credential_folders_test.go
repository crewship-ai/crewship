package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type credFolderRig struct {
	h    *CredentialFolderHandler
	cred *CredentialHandler
	ws   string
	uid  string
}

func newCredFolderRig(t *testing.T) *credFolderRig {
	t.Helper()
	ch, db := newCredHandler(t)
	uid := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, uid)
	return &credFolderRig{h: NewCredentialFolderHandler(db, ch.logger), cred: ch, ws: ws, uid: uid}
}

func (g *credFolderRig) req(method, path, role, body string) *http.Request {
	var rd *bytes.Buffer
	if body != "" {
		rd = bytes.NewBufferString(body)
	} else {
		rd = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, rd)
	ctx := withUser(req.Context(), &AuthUser{ID: g.uid})
	ctx = withWorkspace(ctx, g.ws, role)
	return req.WithContext(ctx)
}

func (g *credFolderRig) create(t *testing.T, role, body string) (int, credentialFolderResponse) {
	t.Helper()
	rr := httptest.NewRecorder()
	g.h.Create(rr, g.req("POST", "/api/v1/credential-folders", role, body))
	var out credentialFolderResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestCredentialFolders_CreateRules(t *testing.T) {
	g := newCredFolderRig(t)
	_, prod := g.create(t, "OWNER", `{"name":"Production"}`)
	if prod.ID == "" {
		t.Fatal("seed folder not created")
	}
	_, child := g.create(t, "OWNER", `{"name":"Payments","parent_id":"`+prod.ID+`"}`)

	tests := []struct {
		name string
		role string
		body string
		want int
	}{
		{"viewer cannot create", "VIEWER", `{"name":"X"}`, http.StatusForbidden},
		{"member cannot create", "MEMBER", `{"name":"X"}`, http.StatusForbidden},
		{"manager creates plain folder", "MANAGER", `{"name":"Staging"}`, http.StatusCreated},
		{"empty name", "OWNER", `{"name":"  "}`, http.StatusBadRequest},
		{"duplicate root name, case-insensitive", "OWNER", `{"name":"production"}`, http.StatusConflict},
		{"same name under another parent is fine", "OWNER", `{"name":"Payments","parent_id":"` + g.mustRoot(t, "Ops") + `"}`, http.StatusCreated},
		{"duplicate under same parent", "OWNER", `{"name":"payments","parent_id":"` + prod.ID + `"}`, http.StatusConflict},
		{"third level refused", "OWNER", `{"name":"Deep","parent_id":"` + child.ID + `"}`, http.StatusBadRequest},
		{"unknown parent", "OWNER", `{"name":"Lost","parent_id":"nope"}`, http.StatusBadRequest},
		{"manager cannot set reveal policy", "MANAGER", `{"name":"Sealed","reveal_policy":"nobody"}`, http.StatusForbidden},
		{"manager cannot set tier floor", "MANAGER", `{"name":"Floor","min_security_level":3}`, http.StatusForbidden},
		{"admin sets policy", "ADMIN", `{"name":"Locked","reveal_policy":"admins","min_security_level":3,"default_security_level":3,"remind_after_days":90}`, http.StatusCreated},
		{"unknown reveal policy", "OWNER", `{"name":"Bad","reveal_policy":"everyone"}`, http.StatusBadRequest},
		{"default below floor", "OWNER", `{"name":"Bad2","min_security_level":3,"default_security_level":2}`, http.StatusBadRequest},
		{"negative reminder", "OWNER", `{"name":"Bad3","remind_after_days":-1}`, http.StatusBadRequest},
		{"tier out of range", "OWNER", `{"name":"Bad4","min_security_level":5}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := g.create(t, tc.role, tc.body)
			if code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}
}

func (g *credFolderRig) mustRoot(t *testing.T, name string) string {
	t.Helper()
	code, f := g.create(t, "OWNER", `{"name":"`+name+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("root %s: %d", name, code)
	}
	return f.ID
}

func TestCredentialFolders_WorkspaceIsolation(t *testing.T) {
	g := newCredFolderRig(t)
	id := g.mustRoot(t, "Mine")

	// A second workspace must neither list nor patch nor delete it.
	db := g.cred.db
	otherUser, otherWS := "u-other", "ws-other"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'other@example.com', 'O')`, otherUser); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatal(err)
	}
	other := &credFolderRig{h: g.h, cred: g.cred, ws: otherWS, uid: otherUser}

	rr := httptest.NewRecorder()
	other.h.List(rr, other.req("GET", "/api/v1/credential-folders", "OWNER", ""))
	if strings.Contains(rr.Body.String(), "Mine") {
		t.Fatalf("folder leaked to another workspace: %s", rr.Body.String())
	}
	rr = httptest.NewRecorder()
	r := other.req("PATCH", "/api/v1/credential-folders/"+id, "OWNER", `{"name":"Stolen"}`)
	r.SetPathValue("folderId", id)
	other.h.Update(rr, r)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace patch = %d, want 404", rr.Code)
	}
	rr = httptest.NewRecorder()
	r = other.req("DELETE", "/api/v1/credential-folders/"+id, "OWNER", "")
	r.SetPathValue("folderId", id)
	other.h.Delete(rr, r)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete = %d, want 404", rr.Code)
	}
}

func TestCredentialFolders_UpdateAndDelete(t *testing.T) {
	g := newCredFolderRig(t)
	id := g.mustRoot(t, "Prod")
	child := func() string {
		_, c := g.create(t, "OWNER", `{"name":"Pay","parent_id":"`+id+`"}`)
		return c.ID
	}()

	patch := func(role, fid, body string) int {
		rr := httptest.NewRecorder()
		r := g.req("PATCH", "/api/v1/credential-folders/"+fid, role, body)
		r.SetPathValue("folderId", fid)
		g.h.Update(rr, r)
		return rr.Code
	}
	if c := patch("MANAGER", id, `{"name":"Production"}`); c != http.StatusOK {
		t.Fatalf("manager rename = %d", c)
	}
	if c := patch("MANAGER", id, `{"reveal_policy":"nobody"}`); c != http.StatusForbidden {
		t.Fatalf("manager reveal policy = %d, want 403", c)
	}
	if c := patch("OWNER", id, `{"reveal_policy":"admins"}`); c != http.StatusOK {
		t.Fatalf("owner reveal policy = %d", c)
	}
	// Loosening needs manage rights as well: a MANAGER cannot undo an admin's lock.
	if c := patch("MANAGER", id, `{"reveal_policy":"inherit"}`); c != http.StatusForbidden {
		t.Fatalf("manager loosening = %d, want 403", c)
	}
	if c := patch("OWNER", id, `{"parent_id":"`+child+`"}`); c != http.StatusBadRequest {
		t.Fatalf("cycle = %d, want 400", c)
	}
	if c := patch("OWNER", child, `{"parent_id":null}`); c != http.StatusOK {
		t.Fatalf("promote to root = %d", c)
	}

	del := func(role, fid string) int {
		rr := httptest.NewRecorder()
		r := g.req("DELETE", "/api/v1/credential-folders/"+fid, role, "")
		r.SetPathValue("folderId", fid)
		g.h.Delete(rr, r)
		return rr.Code
	}
	if c := del("MANAGER", id); c != http.StatusForbidden {
		t.Fatalf("manager delete = %d, want 403", c)
	}
	// Non-empty folder: refused, nothing silently unfiled.
	g.seedCred(t, "c1", "tok", id)
	if c := del("OWNER", id); c != http.StatusConflict {
		t.Fatalf("delete non-empty = %d, want 409", c)
	}
	if _, err := g.cred.db.Exec(`UPDATE credentials SET folder_id = NULL WHERE id='c1'`); err != nil {
		t.Fatal(err)
	}
	if c := del("OWNER", id); c != http.StatusNoContent {
		t.Fatalf("delete empty = %d, want 204", c)
	}
}

func (g *credFolderRig) seedCred(t *testing.T, id, name, folderID string) {
	t.Helper()
	var f any
	if folderID != "" {
		f = folderID
	}
	if _, err := g.cred.db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, folder_id)
		VALUES (?, ?, ?, 'x', 'API_KEY', 'GITHUB', 'WORKSPACE', 'ACTIVE', ?, ?)`, id, g.ws, name, g.uid, f); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

func TestCredentialFolders_ListCounts(t *testing.T) {
	g := newCredFolderRig(t)
	id := g.mustRoot(t, "Prod")
	_, c := g.create(t, "OWNER", `{"name":"Pay","parent_id":"`+id+`"}`)
	g.seedCred(t, "a", "A", id)
	g.seedCred(t, "b", "B", c.ID)
	g.seedCred(t, "c", "C", "")

	rr := httptest.NewRecorder()
	g.h.List(rr, g.req("GET", "/api/v1/credential-folders", "OWNER", ""))
	var list []credentialFolderResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, f := range list {
		got[f.Name] = f.CredentialCount
	}
	if got["Prod"] != 1 || got["Pay"] != 1 {
		t.Fatalf("counts = %v, want Prod=1 (own only), Pay=1", got)
	}
}
