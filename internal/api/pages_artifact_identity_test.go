package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactProjectIdentityErasurePreservesHistoryAndWorkspaceScope(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Retain content"), "{")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	w := call(owner, "POST", "three-screens/artifact/comments", `{"version":1,"path":"index.html","selector":"#hero","quote":"Retain content","text":"Keep history","send_to_agent":false}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var comment artifactProjectComment
	if json.Unmarshal(w.Body.Bytes(), &comment) != nil {
		t.Fatal("comment response")
	}
	var page string
	if h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page) != nil {
		t.Fatal("page")
	}
	mustExec(t, h.db, `INSERT INTO artifact_project_origins(page_id,workspace_id,human_user_id,message_id,created_at) VALUES(?,?,?,'synthetic-source',datetime('now'))`, page, ws, owner)
	mustExec(t, h.db, `INSERT INTO artifact_project_comment_replies(id,comment_id,version,text,author,created_at) VALUES('erasure-reply',?,1,'Question',?,datetime('now'))`, comment.ID, owner)
	mustExec(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('artifact-other-ws','Other','artifact-other-ws')`)
	mustExec(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('artifact-other-page','artifact-other-ws','other','Other',?,'{}')`, owner)
	mustExec(t, h.db, `INSERT INTO artifact_versions(workspace_id,page_id,version,manifest_json,actor,created_at) VALUES('artifact-other-ws','artifact-other-page',1,'{"entry_point":"index.html","files":{}}',?,datetime('now'))`, owner)
	eraser := NewAdminGDPRHandler(h.db, newTestLogger(), t.TempDir())
	_, err := eraser.eraseSubjectIdentity(t.Context(), "artifact-erasure-action", owner, ws, owner)
	if err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{
		`SELECT count(*) FROM artifact_versions WHERE workspace_id=? AND actor=?`,
		`SELECT count(*) FROM artifact_project_comments WHERE workspace_id=? AND author=?`,
		`SELECT count(*) FROM artifact_project_origins WHERE workspace_id=? AND human_user_id=?`,
		`SELECT count(*) FROM artifact_project_comment_replies WHERE comment_id IN(SELECT id FROM artifact_project_comments WHERE workspace_id=?) AND author=?`,
	} {
		var n int
		if err = h.db.QueryRow(query, ws, owner).Scan(&n); err != nil || n != 0 {
			t.Fatal("identity remains", n, err)
		}
	}
	var author string
	if err = h.db.QueryRow(`SELECT actor FROM artifact_versions WHERE workspace_id='artifact-other-ws'`).Scan(&author); err != nil || author != owner {
		t.Fatal("foreign workspace changed", author, err)
	}
	var n int
	if err = h.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE workspace_id=?`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatal("history lost", n, err)
	}
}

func TestArtifactProjectSharedPinDoesNotExposePrivateHistoryThroughBackups(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Shared one"), "{")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+body); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Private two")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES('artifact-backup-admin','artifact-backup-admin@example.test','Admin')`)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('artifact-backup-member',?,'artifact-backup-admin','ADMIN')`, ws)
	backup := &BackupHandler{db: h.db, logger: newTestLogger()}
	r := pagesRequest(t, "GET", "/api/v1/admin/backups/status", ws, "artifact-backup-admin", "ADMIN", "")
	w := httptest.NewRecorder()
	if backup.authorizeWorkspaceBackups(w, r) || w.Code != 403 {
		t.Fatalf("shared pin leaked private versions through workspace ADMIN backup: %d %s", w.Code, w.Body)
	}
	r = pagesRequest(t, "GET", "/api/v1/admin/backups/status", ws, owner, "OWNER", "")
	w = httptest.NewRecorder()
	if !backup.authorizeWorkspaceBackups(w, r) {
		t.Fatalf("workspace custodian denied backup: %d %s", w.Code, w.Body)
	}
}
