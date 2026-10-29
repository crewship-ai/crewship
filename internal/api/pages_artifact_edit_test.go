package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func TestArtifactProjectHumanEditLatestCASAndRevocation(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Project",`+strings.TrimPrefix(artifactProjectBody(0, "Shared one"), "{")); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	editor := "human-project-editor"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'human-project-editor@example.com','Editor')`, editor)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, editor)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'write',?,datetime('now'))`, page, editor, owner)
	if w := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Private two")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	got := call(editor, "GET", "three-screens/artifact", "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), "Private two") || !strings.Contains(got.Body.String(), `"can_write":true`) {
		t.Fatalf("editor latest %d %s", got.Code, got.Body)
	}
	if w := call(editor, "POST", "three-screens/artifact/versions", artifactProjectBody(2, "Editor three")); w.Code != 200 {
		t.Fatal("editor save", w.Code, w.Body)
	}
	var actor string
	if err := h.db.QueryRow(`SELECT actor FROM artifact_versions WHERE page_id=? AND version=3`, page).Scan(&actor); err != nil || actor != editor {
		t.Fatal("actor", actor, err)
	}
	if w := call(editor, "POST", "three-screens/artifact/versions", artifactProjectBody(2, "Stale")); w.Code != 409 {
		t.Fatal("CAS", w.Code, w.Body)
	}
	for _, suffix := range []string{"restore", "share"} {
		if w := call(editor, "POST", "three-screens/artifact/"+suffix, `{"base_version":3,"version":1}`); w.Code != 403 {
			t.Fatal(suffix, w.Code, w.Body)
		}
	}
	for _, level := range []string{"comment", "read"} {
		mustExec(t, h.db, `UPDATE page_grants SET level=? WHERE page_id=? AND subject_id=?`, level, page, editor)
		got = call(editor, "GET", "three-screens/artifact", "")
		var snap artifactProjectSnapshotResponse
		err := json.Unmarshal(got.Body.Bytes(), &snap)
		if got.Code != 200 || err != nil || snap.CanWrite || !strings.Contains(got.Body.String(), "Shared one") || strings.Contains(got.Body.String(), "Editor three") {
			t.Fatalf("downgrade %s %d %s %v", level, got.Code, got.Body, err)
		}
		if w := call(editor, "POST", "three-screens/artifact/versions", artifactProjectBody(3, "Denied")); w.Code != 403 {
			t.Fatal("denied", w.Code, w.Body)
		}
	}
	var latest, versions int
	if err := h.db.QueryRow(`SELECT latest_version,(SELECT count(*) FROM artifact_versions WHERE page_id=?) FROM artifact_version_heads WHERE page_id=?`, page, page).Scan(&latest, &versions); err != nil || latest != 3 || versions != 3 {
		t.Fatal("denied mutated history", latest, versions, err)
	}
}

func TestArtifactProjectHumanEditFinalFenceAndABA(t *testing.T) {
	for _, change := range []string{"downgrade", "issuer suspension", "editor suspension", "aba"} {
		t.Run(change, func(t *testing.T) {
			h, page, owner, editor, call := commentGrantFixture(t, "write")
			var mutation string
			switch change {
			case "downgrade":
				mutation = `UPDATE page_grants SET level='read' WHERE page_id='` + page + `';`
			case "issuer suspension":
				mutation = `UPDATE users SET suspended_at='' WHERE id='` + owner + `';`
			case "editor suspension":
				mutation = `UPDATE users SET suspended_at='' WHERE id='` + editor + `';`
			case "aba":
				mutation = `DELETE FROM page_grants WHERE page_id='` + page + `'; INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('` + page + `','user','` + editor + `','write','` + owner + `');`
			}
			var beforeBlobs int
			if err := h.db.QueryRow(`SELECT count(*) FROM artifact_version_blobs WHERE page_id=?`, page).Scan(&beforeBlobs); err != nil {
				t.Fatal(err)
			}
			mustExec(t, h.db, `CREATE TRIGGER edit_revoke_during_save AFTER INSERT ON artifact_versions BEGIN `+mutation+` END`)
			got := call(editor, "POST", "three-screens/artifact/versions", artifactProjectBody(2, "Must rollback"))
			if got.Code != 403 && got.Code != 404 {
				t.Fatalf("late %s %d %s", change, got.Code, got.Body)
			}
			var latest, count, blobs int
			if err := h.db.QueryRow(`SELECT latest_version,(SELECT count(*) FROM artifact_versions WHERE page_id=?),(SELECT count(*) FROM artifact_version_blobs WHERE page_id=?) FROM artifact_version_heads WHERE page_id=?`, page, page, page).Scan(&latest, &count, &blobs); err != nil || latest != 2 || count != 2 || blobs != beforeBlobs {
				t.Fatal("partial commit", latest, count, blobs, beforeBlobs, err)
			}
		})
	}
}

func TestArtifactProjectEditorTypedLatestProjection(t *testing.T) {
	t.Run("slides", func(t *testing.T) {
		h, ws, _, editor, page := slidesAPIFixture(t)
		mustExec(t, h.db, `UPDATE page_grants SET level='write' WHERE page_id=? AND subject_id=?`, page, editor)
		for _, version := range []string{"2", "1"} {
			w := httptest.NewRecorder()
			h.GetArtifactSlides(w, slidesAPIRequest(t, h, ws, editor, page, "slides?version="+version))
			want := 200
			if version == "1" {
				want = 404
			}
			if w.Code != want {
				t.Fatal("editor typed selection", version, w.Code, w.Body)
			}
		}
		mustExec(t, h.db, `UPDATE page_grants SET level='comment' WHERE page_id=? AND subject_id=?`, page, editor)
		w := httptest.NewRecorder()
		h.GetArtifactSlides(w, slidesAPIRequest(t, h, ws, editor, page, "slides?version=2"))
		if w.Code != 404 {
			t.Fatal("downgrade leaked latest", w.Code, w.Body)
		}
	})
	t.Run("dashboard", func(t *testing.T) {
		h, page, _, editor, call := commentGrantFixture(t, "write")
		t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
		var ws string
		if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE id=?`, page).Scan(&ws); err != nil {
			t.Fatal(err)
		}
		var owner string
		if err := h.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id=?`, page).Scan(&owner); err != nil {
			t.Fatal(err)
		}
		if w := call(owner, "POST", "three-screens/artifact/versions", dashboardBody(2, "sales")); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		w := dashboardRequest(t, h, ws, editor, "GET", "sales", "?version=3", "")
		if w.Code != 200 {
			t.Fatal("editor dashboard latest", w.Code, w.Body)
		}
		mustExec(t, h.db, `UPDATE page_grants SET level='read' WHERE page_id=? AND subject_id=?`, page, editor)
		w = dashboardRequest(t, h, ws, editor, "GET", "sales", "?version=3", "")
		if w.Code != 409 && w.Code != 404 {
			t.Fatal("dashboard downgrade", w.Code, w.Body)
		}
	})
}

func TestArtifactProjectHumanEditGrantSources(t *testing.T) {
	for _, kind := range []string{"user", "crew", "workspace", "folder"} {
		t.Run(kind, func(t *testing.T) {
			h, page, owner, editor, call := commentGrantFixture(t, "write")
			var ws, crew string
			if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE id=?`, page).Scan(&ws); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT id FROM crews WHERE workspace_id=? LIMIT 1`, ws).Scan(&crew); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "crew":
				mustExec(t, h.db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES(?,?,?)`, generateCUID(), crew, editor)
				mustExec(t, h.db, `UPDATE page_grants SET subject_type='crew',subject_id=? WHERE page_id=?`, crew, page)
			case "workspace":
				mustExec(t, h.db, `UPDATE page_grants SET subject_type='workspace',subject_id='' WHERE page_id=?`, page)
			case "folder":
				mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=?`, page)
				mustExec(t, h.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('edit-folder',?,'edit-folder','Folder',?,datetime('now'),datetime('now'))`, ws, crew)
				mustExec(t, h.db, `UPDATE pages SET folder_id='edit-folder' WHERE id=?`, page)
				mustExec(t, h.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('edit-folder','user',?,1,1,datetime('now'))`, editor)
			}
			if w := call(editor, "POST", "three-screens/artifact/versions", artifactProjectBody(2, "Editor saves")); w.Code != 200 {
				t.Fatal(kind, w.Code, w.Body)
			}
			// Even a live Edit grant cannot widen the separate private-audience seal.
			mustExec(t, h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
			if w := call(editor, "GET", "three-screens/artifact", ""); w.Code != 404 {
				t.Fatal("private seal", w.Code, w.Body)
			}
			if w := call(editor, "POST", "three-screens/artifact/versions", artifactProjectBody(3, "Denied")); w.Code != 404 {
				t.Fatal("private write", w.Code, w.Body)
			}
			_ = owner
		})
	}
}

func TestArtifactProjectTwoHumanEditorsSameBase(t *testing.T) {
	h, page, owner, editor, call := commentGrantFixture(t, "write")
	var ws string
	if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE id=?`, page).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	other := "other-human-editor"
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,'other-human-editor@example.test')`, other)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, other)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, page, other, owner)
	// Mint both credentials before parallel requests; the fixture token map then only reads.
	for _, user := range []string{editor, other} {
		if w := call(user, "GET", "three-screens/artifact", ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	result := make(chan int, 2)
	for _, user := range []string{editor, other} {
		go func(user string) {
			result <- call(user, "POST", "three-screens/artifact/versions", artifactProjectBody(2, user)).Code
		}(user)
	}
	first, second := <-result, <-result
	if !(first == 200 && second == 409 || first == 409 && second == 200) {
		t.Fatal("same-base editors", first, second)
	}
	var latest, count int
	if err := h.db.QueryRow(`SELECT latest_version,(SELECT count(*) FROM artifact_versions WHERE page_id=?) FROM artifact_version_heads WHERE page_id=?`, page, page).Scan(&latest, &count); err != nil || latest != 3 || count != 3 {
		t.Fatal("CAS partial data", latest, count, err)
	}
}

func TestArtifactProjectEditorCannotAnswerOwnerAgentTask(t *testing.T) {
	h, page, owner, editor, call := commentGrantFixture(t, "write")
	w := call(owner, "POST", "three-screens/artifact/comments", `{"version":2,"path":"index.html","selector":"#hero","quote":"Private two","text":"Owner task","send_to_agent":true}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var comment artifactProjectComment
	if err := json.Unmarshal(w.Body.Bytes(), &comment); err != nil {
		t.Fatal(err)
	}
	var ws string
	if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE id=?`, page).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"text":"Forged completed task","version":2,"needs_clarification":true}`))
	req.SetPathValue("slug", "three-screens")
	req.SetPathValue("commentId", comment.ID)
	req.Header.Set("Authorization", "Bearer "+mintTokenFor(t, h.db, editor, "editor-task-reply"))
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: editor}), ws, "MEMBER")
	req = req.WithContext(context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken))
	reply := httptest.NewRecorder()
	h.ReplyArtifactProjectComment(reply, req)
	if reply.Code != 403 {
		t.Fatal("editor answered owner task", reply.Code, reply.Body)
	}
	var replies int
	var status string
	if err := h.db.QueryRow(`SELECT status,(SELECT count(*) FROM artifact_project_comment_replies WHERE comment_id=?) FROM artifact_project_comment_tasks WHERE comment_id=?`, comment.ID, comment.ID).Scan(&status, &replies); err != nil || status != "pending" || replies != 0 {
		t.Fatal("task changed", status, replies, err)
	}
}

func TestArtifactProjectDashboardEditorReadRejectsABA(t *testing.T) {
	h, page, owner, editor, call := commentGrantFixture(t, "write")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	var ws string
	if err := h.db.QueryRow(`SELECT workspace_id FROM pages WHERE id=?`, page).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	if w := call(owner, "POST", "three-screens/artifact/versions", dashboardBody(2, "sales")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	req := slidesAPIRequest(t, h, ws, editor, page, "data/sales?version=3")
	authorize, _ := h.artifactDataAuthority(req, artifacts.VersionKey{WorkspaceID: ws, PageID: page}, "sales", 3, false)
	tx, err := h.db.BeginTx(req.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = authorize(req.Context(), tx); err != nil {
		t.Fatal("calibration", err)
	}
	if _, err = tx.Exec(`DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, editor); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, page, editor, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = authorize(req.Context(), tx); err == nil {
		t.Fatal("old data read revived after revoke/regrant")
	}
}
