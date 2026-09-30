package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// The legacy /api/v1/admin/backups* routes follow the instance-admin rules:
// an instance admin ("boss", who belongs to ws-old only) downloads, verifies
// and restores ws-new's backups by naming the workspace, without being a
// member. A workspace ADMIN still reaches only their own workspace, and every
// route stays scoped to the workspace named.
func TestLegacyBackupRoutesFollowInstanceAdminRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newInstanceFixture(t)
	const pass = "instance-admin-legacy-pass-1"
	ws := "?workspace_id=ws-new"

	rr := f.do(f.boss, "POST", "/api/v1/admin/backups"+ws, `{"scope":"workspace","passphrase":"`+pass+`"}`)
	wantCode(t, rr, http.StatusCreated, "instance admin creates a backup of a workspace they are not in")
	var created struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil || created.Path == "" {
		t.Fatalf("create = %s", rr.Body.String())
	}
	p := "&path=" + url.QueryEscape(created.Path)

	rr = f.do(f.boss, "GET", "/api/v1/admin/backups"+ws, "")
	wantCode(t, rr, http.StatusOK, "list")
	var list struct {
		Data []struct {
			Path string `json:"path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list.Data) != 1 || list.Data[0].Path != created.Path {
		t.Fatalf("list = %s", rr.Body.String())
	}
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/backups/status"+ws, ""), http.StatusOK, "status")
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/backups/inspect"+ws+p, ""), http.StatusOK, "inspect")
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/backups/verify"+ws+p, ""), http.StatusOK, "verify")
	rr = f.do(f.boss, "GET", "/api/v1/admin/backups/download"+ws+p, "")
	wantCode(t, rr, http.StatusOK, "download")
	if rr.Body.Len() == 0 {
		t.Fatal("download returned no bytes")
	}
	rr = f.do(f.boss, "POST", "/api/v1/admin/backups/restore"+ws, `{"path":`+jsonString(created.Path)+`,"passphrase":"`+pass+`","dry_run":true}`)
	wantCode(t, rr, http.StatusOK, "restore dry run")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/backups/rotate"+ws, `{"keep_last":5}`), http.StatusOK, "rotate")
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/backups/metrics", ""), http.StatusOK, "metrics need no workspace")
	if f.role("ws-new", "boss") != "" {
		t.Fatal("reaching ws-new's backups made the instance admin a member")
	}

	// Without a workspace the per-workspace routes ask for one.
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/backups", ""), http.StatusBadRequest, "list without a workspace")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/backups", `{"scope":"workspace"}`), http.StatusBadRequest, "create without a workspace")

	// Scoping holds: ws-new's bundle is not ws-old's.
	wantCode(t, f.do(f.boss, "GET", "/api/v1/admin/backups/download?workspace_id=ws-old"+p, ""), http.StatusNotFound, "download through the wrong workspace")

	// A workspace ADMIN of ws-old is not an instance admin: ws-new is out of reach.
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/backups" + ws, ""},
		{"GET", "/api/v1/admin/backups/download" + ws + p, ""},
		{"POST", "/api/v1/admin/backups" + ws, `{"scope":"workspace"}`},
		{"POST", "/api/v1/admin/backups/restore" + ws, `{"path":` + jsonString(created.Path) + `,"dry_run":true}`},
		{"DELETE", "/api/v1/admin/backups" + ws + p, ""},
		{"GET", "/api/v1/admin/backups/metrics", ""},
	} {
		if code := f.do(f.wsAdmin, c.method, c.path, c.body).Code; code != http.StatusForbidden && code != http.StatusBadRequest {
			t.Errorf("workspace ADMIN %s %s = %d, want 403", c.method, c.path, code)
		}
	}
	// … while their own workspace still works.
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/admin/backups?workspace_id=ws-old", ""), http.StatusOK, "workspace ADMIN lists their own")

	wantCode(t, f.do(f.boss, "DELETE", "/api/v1/admin/backups"+ws+p, ""), http.StatusNoContent, "delete")
}
